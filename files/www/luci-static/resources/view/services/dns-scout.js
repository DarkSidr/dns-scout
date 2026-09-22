'use strict';
'require view';
'require rpc';
'require ui';
'require poll';

var status = rpc.declare({ object: 'dns-scout', method: 'status', expect: {} });
var scan = rpc.declare({ object: 'dns-scout', method: 'scan', expect: {} });
var apply = rpc.declare({ object: 'dns-scout', method: 'apply', params: ['id'], expect: {} });
var save = rpc.declare({ object: 'dns-scout', method: 'save', params: ['config'], expect: {} });
var recover = rpc.declare({ object: 'dns-scout', method: 'recover', expect: {} });
function checked(r) { if (r.error) throw new Error(r.error); return r; }
function error(e) { ui.addNotification(null, E('p', {}, e.message), 'error'); }
function field(label, input, note) {
 return E('div', { 'class': 'cbi-value' }, [E('label', { 'class': 'cbi-value-title' }, label),
  E('div', { 'class': 'cbi-value-field' }, [input, note ? E('div', { 'class': 'cbi-value-description' }, note) : ''])]);
}
function input(value, type) { return E('input', { 'class': 'cbi-input-text', type: type || 'text', value: value }); }
function check(value) { return E('input', { type: 'checkbox', checked: value ? '' : null }); }
function button(label, handler, disabled) {
 return E('button', { 'class': 'cbi-button cbi-button-action', type: 'button', disabled: disabled ? '' : null,
  click: function(event) { event.preventDefault(); return Promise.resolve().then(handler).catch(error); } }, label);
}
function time(t) { return t ? new Date(t * 1000).toLocaleString() : '—'; }
function table(headers, rows) {
 return E('div', { 'class': 'table-responsive' }, E('table', { 'class': 'table cbi-section-table' }, [
  E('tr', { 'class': 'tr table-titles' }, headers.map(function(h) { return E('th', { 'class': 'th' }, h); }))
 ].concat(rows.map(function(row) { return E('tr', { 'class': 'tr' }, row.map(function(c, i) { return E('td', { 'class': 'td', 'data-title': headers[i] }, c); })); }))));
}
return view.extend({
 load: function() { return status().then(checked); },
 render: function(data) {
  var config = data.config, latest = data, reservesDirty = false;
  var summary = E('div'), results = E('div'), bootstrapResults = E('div');
  var run = button('Запустить тест', function() { return scan().then(checked).then(refresh); });
  var best = button('Применить лучший + резерв', function() { return confirmApply(''); });
  var reserveMode = E('select', {'class':'cbi-input-select', id:'dns-scout-fallback-mode'}, [E('option',{value:'auto'},'Автоматически по рейтингу'),E('option',{value:'manual'},'Выбрать вручную')]);
  reserveMode.value = config.fallback_mode || 'auto';
  var reserveOne = E('select', {'class':'cbi-input-select', id:'dns-scout-reserve-1'}), reserveTwo = E('select', {'class':'cbi-input-select', id:'dns-scout-reserve-2'});
  function reserveOptions(c) {
   [reserveOne,reserveTwo].forEach(function(select,i) {
    select.replaceChildren(E('option',{value:''},'Не использовать'));
    c.servers.filter(function(s){return s.enabled && s.eligible;}).forEach(function(s){select.appendChild(E('option',{value:s.id},s.name+' — '+s.url));});
    select.value = (c.fallback_ids || [])[i] || '';
   });
  }
  reserveOptions(config);
  function reserveValues() {
   if(reserveMode.value==='manual' && !reserveOne.value && reserveTwo.value) throw new Error('Сначала выберите первый резервный DNS.');
   return {fallback_mode:reserveMode.value, fallback_ids:reserveMode.value==='manual' ? [reserveOne.value,reserveTwo.value].filter(Boolean) : [], fallbacks:+fallbacks.value};
  }
  function confirmApply(id) {
   if(reservesDirty) throw new Error('Сначала нажмите «Сохранить резервирование».');
   var c=latest.config;
   var reserveText=(c.fallback_mode==='manual') ? ((c.fallback_ids || []).map(function(bid){var s=c.servers.find(function(s){return s.id===bid;});return s?s.name:bid;}).join(' → ') || 'без резервных DNS') : 'автоматически по рейтингу (всего до '+c.fallbacks+' DNS)';

   ui.showModal('Применение DNS', [E('p', {}, 'Будут заменены DoH-инстансы https-dns-proxy и общие DNS-серверы dnsmasq. Правила отдельных доменов сохранятся. Резервные серверы используются в сохранённом порядке. При ошибке выполняется откат.'),
    E('p', {}, 'Резервирование: '+reserveText),
    E('p', {}, 'Перед применением сохраните настройки ниже. Возможен краткий перерыв DNS во время перезапуска.'),
    E('div', { 'class': 'right' }, [button('Отмена', function() { ui.hideModal(); }), ' ', button('Применить', function() { ui.hideModal(); return apply(id).then(checked).then(refresh); })])]);
  }
  function update(d) {
   latest=d;
   var busy = d.job && d.job.running, report = d.report;
   run.disabled = !!busy; best.disabled = !!busy || !report || !report.finished || Date.now()/1000-report.finished>3600;
   var children = [E('p', {}, (d.job.message || 'Готов к проверке') + (busy ? ' · ' + d.job.done + '/' + d.job.total : ''))];
   if (d.active) children.push(E('p', {}, 'Последнее подтверждённое применение: ' + time(d.active.verified_at) + ' · ' + d.active.servers.map(function(s) { return s.name; }).join(' → ') + '. Это история проверки, не непрерывный мониторинг.'));
   if (d.pending_recovery) children.push(E('p', {}, ['Нужен откат незавершённой операции. ', button('Восстановить настройки', function() { return recover().then(checked).then(refresh); }, busy)]));
   summary.replaceChildren.apply(summary, children);
   var rows = ((report && report.results) || []).map(function(r) {
    var current = d.config.servers.find(function(s){return s.id===r.server.id;});
    return [r.server.name, r.server.url, r.success + '/' + r.total,
     r.success ? r.median_ms.toFixed(1) + ' мс' : '—', r.success ? r.p95_ms.toFixed(1) + ' мс' : '—',
     r.error || 'OK', button('Применить', function() { return confirmApply(r.server.id); }, busy || !report.finished || r.success!==r.total || !current || !current.enabled || !current.eligible || current.url!==r.server.url || Date.now()/1000-report.finished>3600)];
   });
   results.replaceChildren(E('p', {}, 'Последний завершённый тест: ' + time(report && report.finished)), table(['Сервер', 'DoH URL', 'Успех', 'Медиана', 'p95', 'Результат / последняя ошибка', ''], rows));
   bootstrapResults.replaceChildren(table(['Bootstrap', 'Успех', 'Медиана', 'Ошибка'], ((report && report.bootstrap) || []).map(function(r) { return [r.server.name, r.success+'/'+r.total, r.success ? r.median_ms.toFixed(1)+' мс' : '—', r.error || 'OK']; })));
  }
  function refresh() { return status().then(checked).then(update); }
  var boot=input(config.bootstrap.join(', ')), domains=input(config.domains.join(', ')), samples=input(config.samples,'number'), timeout=input(config.timeout,'number'), parallel=input(config.parallel,'number'), daily=input(config.daily), auto=check(config.auto), fallbacks=input(config.fallbacks,'number');
  samples.min=2; samples.max=10; timeout.min=2;timeout.max=15;parallel.min=1;parallel.max=8;fallbacks.min=1;fallbacks.max=3;
  var editors=[], serverBody=E('tbody');
  function add(s) {
   var name=input(s.name), url=input(s.url), enabled=check(s.enabled), eligible=check(s.eligible);
   var row = { id:s.id, name:name, url:url, enabled:enabled, eligible:eligible };
   row.node=E('tr', { 'class': 'tr' }, [name,url,enabled,eligible,button('Удалить',function(){editors=editors.filter(function(x){return x!==row;});row.node.remove();})].map(function(c,i){return E('td',{'class':'td','data-title':['Название','DoH URL','Тестировать','Разрешить выбор',''][i]},c);}));
   editors.push(row);serverBody.appendChild(row.node);
  }
  config.servers.forEach(add);
  var reserveManual = E('div',{},[field('Первый резервный DNS',reserveOne),field('Второй резервный DNS',reserveTwo)]);
  var reserveAuto = field('Всего DNS в цепочке',fallbacks,'1–3: основной и резервные. Этот предел используется в автоматическом режиме.');
  function reserveVisibility(){reserveManual.style.display=reserveMode.value==='manual'?'':'none';reserveAuto.style.display=reserveMode.value==='manual'?'none':'';}
  [reserveMode,reserveOne,reserveTwo,fallbacks].forEach(function(el){el.addEventListener('change',function(){reservesDirty=true;reserveVisibility();});});
  reserveVisibility();
  var reservePanel=E('div',{'class':'cbi-section'},[E('h3',{},'Резервные DNS'),field('Режим выбора',reserveMode),reserveAuto,reserveManual,
   E('p',{},'Основной DNS выбирается кнопкой «Применить» в таблице. В ручном режиме используются только выбранные здесь резервы. Они должны пройти все тесты; совпадение с основным сервером не допускается.'),
   button('Сохранить резервирование',function(){var next=Object.assign({},latest.config,reserveValues());return save(JSON.stringify(next)).then(checked).then(function(){reservesDirty=false;ui.addNotification(null,E('p',{},'Резервирование сохранено.'));return refresh();});})]);
  var settings=E('div', { 'class': 'cbi-section' }, [E('h3', {}, 'Настройки'),
   field('Bootstrap DNS',boot,'Публичные IPv4 через запятую. Нужны только для поиска IP DoH-сервера; запросы к ним не зашифрованы. Проверяются в указанном порядке.'),
   field('Тестовые домены',domains,'Минимум два существующих домена с публичной A-записью. Запросы будут видны выбранным DNS-провайдерам.'),
   field('Запросов на сервер',samples),field('Таймаут, секунд',timeout),field('Параллельных проверок',parallel,'Для слабого роутера: 1. Каждая проба включает bootstrap, новое TLS-соединение и DNS-запрос.'),
   field('Ежедневный тест',daily,'ЧЧ:ММ по времени роутера; пусто — расписание выключено. Используется cron.'),
   field('Автовыбор после теста по расписанию',auto,'Применяются только разрешённые серверы со 100% успешных проб. При отсутствии подходящих настройки не меняются.'),
   E('h3',{},'Каталог серверов'), E('p',{},'Дополнительные адреса из gist выключены: список старый, доступность и владельцы могли измениться.'), button('Включить все для теста',function(){editors.forEach(function(r){r.enabled.checked=true;});}), ' ', button('Выключить дополнительные из gist',function(){editors.forEach(function(r){if(r.id.indexOf("gist_")===0)r.enabled.checked=false;});}), E('p',{},'«Тестировать» включает проверку. «Разрешить выбор» разрешает ручное и автоматическое применение. Проверяйте политику фильтрации и доверие к провайдеру самостоятельно.'),
   E('div',{'class':'table-responsive'},E('table',{'class':'table'},[E('thead',{},E('tr',{'class':'tr'},['Название','DoH URL','Тестировать','Разрешить выбор',''].map(function(s){return E('th',{'class':'th'},s);}))),serverBody])),
   button('Добавить сервер',function(){add({id:'custom_'+Date.now().toString(36),name:'',url:'https://',enabled:true,eligible:false});}), ' ',
   button('Сохранить настройки',function(){
    var next={bootstrap:boot.value.split(/[ ,]+/).filter(Boolean),domains:domains.value.split(/[ ,]+/).filter(Boolean),samples:+samples.value,timeout:+timeout.value,parallel:+parallel.value,daily:daily.value.trim(),auto:auto.checked,fallbacks:+fallbacks.value,
     servers:editors.map(function(r){return {id:r.id,name:r.name.value.trim(),protocol:'doh',url:r.url.value.trim(),enabled:r.enabled.checked,eligible:r.eligible.checked};})};
    Object.assign(next,reserveValues());
    return save(JSON.stringify(next)).then(checked).then(function(){reservesDirty=false;reserveOptions(next);ui.addNotification(null,E('p',{},'Настройки сохранены.'));return refresh();});
   })]);
  update(data);poll.add(function(){return refresh().catch(error);},10);
  return E('div', {}, [E('h2', {}, 'DNS Scout '+(data.version || '')),  E('p', {}, 'Проверка доступности DoH с этого роутера. Рейтинг: доля успешных ответов, затем p95 задержки. Скорость не является оценкой приватности. Результаты исчезают после перезагрузки.'),summary,E('div',{'class':'cbi-section'},[run,' ',best]),reservePanel,results,E('h3',{},'Bootstrap DNS'),bootstrapResults,settings]);
 },
 handleSaveApply:null,handleSave:null,handleReset:null
});
