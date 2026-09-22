const assert = require('node:assert/strict');
const fs = require('node:fs');
const vm = require('node:vm');
const path = require('node:path');

const root = path.resolve(__dirname, '..');
const source = fs.readFileSync(path.join(root, 'files/www/luci-static/resources/view/services/dns-scout.js'), 'utf8');

function element(tag, attrs = {}, children = []) {
  if (Array.isArray(attrs) || typeof attrs !== 'object') [children, attrs] = [attrs, {}];
  const node = {
    tag, children: [], style: {}, value: '',
    appendChild(child) { this.children.push(child); return child; },
    replaceChildren(...items) { this.children = items.flat(); },
    addEventListener() {}, remove() {},
  };
  for (const [key, value] of Object.entries(attrs)) {
    node[key] = ['checked', 'disabled'].includes(key) ? value != null : value;
  }
  node.replaceChildren(children);
  return node;
}

async function checkBulk(label, expected) {
  const config = JSON.parse(fs.readFileSync(path.join(root, 'files/etc/dns-scout/config.json')));
  config.servers = config.servers.slice(0, 3);
  config.servers.forEach((s, i) => { s.enabled = i === 0; s.eligible = i === 1; });
  const data = { config, job: {}, report: null };
  let saved;
  const errors = [];
  const context = {
    E: element, view: { extend: obj => obj }, poll: { add() {} },
    ui: { addNotification: (_title, message, type) => { if (type === 'error') errors.push(message); } },
    rpc: { declare: ({ method }) => async value => {
      if (method === 'status') return data;
      if (method === 'save') { saved = JSON.parse(value); data.config = saved; return { ok: true }; }
      throw Error('Unexpected RPC: ' + method);
    } },
  };
  const view = vm.runInNewContext('(function() {\n' + source + '\n})()', context);
  const tree = view.render(data);
  function find(node, text) {
    if (!node || typeof node !== 'object') return null;
    if (node.tag === 'button' && node.children.includes(text)) return node;
    for (const child of node.children || []) { const match = find(child, text); if (match) return match; }
    return null;
  }
  const click = async text => {
    const button = find(tree, text);
    assert.ok(button, 'Missing button: ' + text);
    await button.click({ preventDefault() {} });
  };
  await click(label);
  assert.equal(saved, undefined, 'Bulk selection must wait for Save');
  await click('Сохранить настройки');
  assert.deepEqual(errors, []);
  assert.ok(saved, 'Save did not send configuration');
  assert.deepEqual(saved.servers.map(s => [s.enabled, s.eligible]), expected);
}

(async () => {
  await checkBulk('Тестировать все', [[true, false], [true, true], [true, false]]);
  await checkBulk('Разрешить выбор всем', [[true, true], [false, true], [false, true]]);
  await checkBulk('Тестировать и разрешить всех', [[true, true], [true, true], [true, true]]);
  console.log('UI bulk selection and saved configuration: OK');
})().catch(error => { console.error(error); process.exitCode = 1; });
