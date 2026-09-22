# Сборка и разработка DNS Scout

[← Руководство пользователя](../README.md)

## Содержание

- [Сборка пакетов](#сборка-пакетов)
- [Проверки](#проверки)
- [Выпуск релиза](#выпуск-релиза)
- [Устройство проекта](#устройство-проекта)

## Сборка пакетов

Нужны Go 1.20+ и Python 3. Команды выполняются из корня репозитория. Для IPK:

```sh
python3 scripts/build-release.py --arch arm64 --openwrt-arch aarch64_cortex-a53
python3 scripts/build-release.py --arch mipsle --openwrt-arch mipsel_24kc
```

Результаты попадают в `dist/`. Скрипт собирает статический Linux-бинарник с `CGO_ENABLED=0`. ARMv7 использует `GOARM=7`, MIPS — softfloat. Сторонних Go-модулей нет.

Для APK v3 нужны `apk` и `fakeroot` из одного OpenWrt SDK:

```sh
export STAGING_DIR_HOST=/path/to/openwrt-sdk/staging_dir/host
"$STAGING_DIR_HOST/bin/fakeroot" python3 scripts/build-release.py \
  --arch arm64 --openwrt-arch aarch64_cortex-a53 \
  --apk "$STAGING_DIR_HOST/bin/apk"
```

Собрать все пять архитектур:

```sh
"$STAGING_DIR_HOST/bin/fakeroot" sh scripts/build-all.sh
```

Для сборки штатными средствами SDK поместите проект в `package/dns-scout`, подключите packages feed с `golang/host` и выполните:

```sh
make menuconfig  # LuCI → Applications → luci-app-dns-scout
make package/dns-scout/compile V=s
```

## Проверки

```sh
go test -race ./...
go vet ./...
go test ./... -run '^$' -fuzz FuzzDNSParser -fuzztime=10s
node --check files/www/luci-static/resources/view/services/dns-scout.js
node scripts/test-ui.cjs
sh -n install.sh scripts/build-all.sh
```

Тесты охватывают разбор DNS, повреждённые ответы, недоверенный TLS, валидацию настроек, ранжирование, выбор резервов и восстановление файлов при сбое применения. Тесты отката используют временные файлы и имитацию системных команд.

При проверке на устройстве отдельно проверяйте применение, ответы каждого прокси, резервирование при недоступности основного и сохранность конфигурации после перезагрузки. Обычная перезагрузка не проверяет аварийный откат незавершённой транзакции.

## Выпуск релиза

`VERSION` содержит версию приложения, `RELEASE` — номер сборки OpenWrt. Например, `0.2.0` и `2` образуют версию пакета `0.2.0-r2`.

1. Обновите `VERSION` и `RELEASE`.
2. Дополните [CHANGELOG](../CHANGELOG.md) и создайте `docs/releases/<версия>-r<сборка>.md`.
3. Выполните проверки и зафиксируйте изменения.
4. Создайте и отправьте тег, совпадающий с версией пакета, например `v0.2.0-r2`.

Workflow [Test and release](../.github/workflows/release.yml) запускает проверки, собирает APK/IPK и публикует пакеты вместе с `release.json` и `SHA256SUMS`. На обычный push или pull request выполняются только проверки.

## Устройство проекта

| Путь | Назначение |
| --- | --- |
| `cmd/dns-scout/` | Тестирование DoH, выбор кандидатов, применение и RPC. |
| `files/www/luci-static/resources/view/services/dns-scout.js` | Интерфейс LuCI. |
| `files/etc/dns-scout/config.json` | Начальные настройки и каталог. |
| `files/etc/init.d/dns-scout` | Восстановление и настройка cron при загрузке. |
| `scripts/` | Сборка, установочные хуки и импорт каталога. |

На роутере настройки находятся в `/etc/dns-scout/config.json`, последнее подтверждённое применение — в `/etc/dns-scout/active.json`, резервные копии транзакции — в `/etc/dns-scout/transaction/`. Результаты и прогресс хранятся в `/tmp/dns-scout/`.

Дополнительный каталог импортирован из [gist thiagozs](https://gist.github.com/thiagozs/088fd8f8129ca06df524f6711116ee8f), ревизия `6c330056c1809797650f86d3e3e1d5f32a9e4c11`. Скрипт `scripts/import-gist.py` обрабатывает предварительно скачанный Markdown; дополнительные записи по умолчанию выключены.
