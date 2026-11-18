# Backend FAQ Bot

`backend` — Go-модуль `faqbot/backend`. Он обслуживает HTTP-запросы авторизации,
проверяет пароль через Supabase и хранит локальные компании, пользователей и сессии
в PostgreSQL. Отдельная программа применяет миграции — изменения схемы БД.

Работают проверки состояния, регистрация, вход, получение и отзыв сессии,
а также изолированный CRUD агентов для owner/admin текущей компании.
Новая компания сохраняется вместе с owner и одним pending-намерением trial;
реальное начисление пока не подключено. Повтор проверенного subject не создаёт
новую компанию или новое намерение. Порядок проверки после установки —
в [AUTH.md](AUTH.md#проверка-после-установки).
Доступные HTTP-маршруты и проверки состояния описаны в [HTTP.md](HTTP.md).
Порядок первой установки Auth — [карточка релиза 0.0.1](upgrade_cluster/release_0.0.1.md),
обновление схемы аудита — [карточка 0.0.2](upgrade_cluster/release_0.0.2.md),
подготовка и применение схемы — [Накат миграций](MIGRATIONS.md).

## Зависимости и код

Используется Go 1.24+, стандартные `net/http`, `database/sql`, `encoding/json`,
`slog`; pgx v5 — драйвер PostgreSQL, golang-migrate v4 — миграции.
kin-openapi и jsonschema закреплены для дополнительных локальных проверок контрактов.
Точные версии заданы в [go.mod](../backend/go.mod) и `go.sum`.

Точки входа — `cmd/api/main.go` и `cmd/migrate/main.go`. HTTP-слой `internal/handlers`
проверяет запрос, `service` выполняет сценарий, `repository` обращается к БД,
`authprovider` — к Supabase. Общие типы находятся в `models`, настройки — в
`config`. Подробная [схема слоёв](ARCHITECTURE.md) и [карта чтения](PROJECT_WALKTHROUGH.md).

## Установка и запуск

Из корня проекта:

```sh
cd backend
go mod download
go run ./cmd/api
```

Это запуск без БД и авторизации при чистом окружении development.
Проверка из любого каталога: `curl -i http://127.0.0.1:8080/health/ready`.
Ожидается 200. Для Auth и агентов нужен AGENT_CURSOR_KEY: отдельный секрет минимум 32 байта,
общий для всех реплик, заданный до запуска.
Для работы авторизации сначала подготовить
[PostgreSQL](DATABASE.md), применить [миграции](MIGRATIONS.md), затем настроить
[Supabase и Auth](AUTH.md). Если `DATABASE_URL` задан, соединение проверяется до
открытия HTTP-порта. API не создаёт таблицы при старте.

Все последующие команды Go выполняются из `backend`. Переменные читаются
через `os.LookupEnv`/`os.Getenv` в [config.go](../backend/internal/config/config.go).
Ни API, ни migrator автоматически не загружают `.env` и не принимают флаг пути
к нему. [backend/.env.example](../backend/.env.example) — справочник для локальной
разработки. Секреты не сохраняются в репозиторий.

## Переменные окружения

Значения по умолчанию ниже относятся к программам, запущенным напрямую.
Dockerfile дополнительно задаёт `APP_ENV=production`, `HTTP_LISTEN_ADDR=:8080`
и `MIGRATIONS_PATH=/migrations`. Указанные URL с заменяемыми частями — примеры,
а не адреса действующего стенда.

| Переменная | Назначение и потребитель | Обязательность и значение по умолчанию | Ограничения и безопасный пример |
| --- | --- | --- | --- |
| `APP_ENV` | Режим; общий `config.Load`, API и migrator | Необязательна; `development` | Только `development`, `staging`, `production`; для стенда `staging` либо `production` |
| `HTTP_LISTEN_ADDR` | TCP-адрес API; migrator только валидирует | Необязательна; `127.0.0.1:8080` | Host — пустой, IP или `localhost`; порт 1..65535. Пример `127.0.0.1:8081`; контейнеру `:8080` |
| `HTTP_SHUTDOWN_TIMEOUT` | Срок `Server.Shutdown` API; migrator только валидирует | Необязательна; `10s` | Формат Go duration, >0 и ≤1m; пример `15s` |
| `DATABASE_URL` | DSN PostgreSQL; API (`db.Open`), migrator (`migration.Apply`) | Обязательна для Auth, migrator и любого режима вне development; иначе отсутствует | URL или DSN, принимаемый pgx. Шаблон `postgres://<API_ROLE>:<URL_ENCODED_PASSWORD>@<DB_HOST>:5432/<DB_NAME>?sslmode=verify-full`. Для migrator использовать его роль. Значения в `<…>` заменить; режим TLS согласовать с БД |
| `MIGRATIONS_PATH` | Каталог SQL для migrator; API только валидирует | Необязательна; `./migrations` | Непустой; относительный путь считается от рабочего каталога процесса. Пример `./migrations` из `backend`; существование каталога проверяет migrator |
| `SUPABASE_URL` | Базовый адрес проекта; API → `authprovider.Supabase` | Обязательна при Auth; отсутствует | HTTPS без credentials/query/fragment и пути (конечный `/` допустим). Шаблон `https://<PROJECT_REF>.supabase.co`, заменить проект |
| `SUPABASE_KEY` | Ключ в заголовке `apikey` запросов API → Supabase Auth | Обязательна при Auth; отсутствует | После удаления пробелов не может быть пустой; тип/права ключа кодом не проверяются. Пример `<KEY_FROM_SECRET_STORE>`; нужен ключ, принимаемый выбранным проектом для signup/password login |
| `AGENT_CURSOR_KEY` | Секрет подписи курсоров агентов | Обязательна при Auth | Минимум 32 байта; отдельный секрет, одинаковый у всех реплик и после перезапуска; ротация инвалидирует старые cursors |
| `AUTH_ORIGIN` | Единственный разрешённый origin страницы; API → AuthHandler | Обязательна при Auth; отсутствует | HTTPS, без credentials, пути (включая `/`), query и fragment; сравнение строк точное. Шаблон `https://<PUBLIC_HOST>`; порт включать, если его отправляет браузер |
| `AUTH_ALLOW_INSECURE_HTTP` | Разрешение loopback HTTP при проверке URL; API → AuthHandler управляет Secure cookie | Необязательна; `false` | Boolean по `strconv.ParseBool`: `true/false`, `1/0`, `t/f` и поддерживаемые варианты регистра. `true` допустимо только в development. Для стенда `false` либо не задавать |
| `TEST_DATABASE_URL` | Только SQL-интеграционные тесты, не конфигурация API/migrator | Для выполнения SQL-тестов; отсутствует → пропуск | Отдельная тестовая БД с правом создания схем/таблиц. Шаблон `postgres://<TEST_ROLE>:<URL_ENCODED_PASSWORD>@127.0.0.1:5432/<TEST_DB>?sslmode=disable`; отключение TLS только для локальной БД |
| `TEST_AUDIT_ROLE` | Только проверки ограниченной роли журнала и регистрации | Необязательна; без неё пропускаются `TestAuditSQLRestrictedRole` и `TestRegistrationRestrictedRole` | Заранее созданная отдельная NOLOGIN роль без административных прав, DDL и владения; подключение тестов должно уметь GRANT и SET ROLE. Подготовка — [DATABASE.md](DATABASE.md#sql-тесты) |

Установка хотя бы одной из четырёх Auth-переменных (`SUPABASE_URL`,
`SUPABASE_KEY`, `AUTH_ORIGIN`, `AUTH_ALLOW_INSECURE_HTTP`), даже в пустую строку
или `AUTH_ALLOW_INSECURE_HTTP=false`, включает Auth и требует полного набора:
непустых URL, ключа, origin, `DATABASE_URL` и `AGENT_CURSOR_KEY`. Чтобы запускать только health в
development, эти четыре переменные должны отсутствовать, а не быть пустыми.
В staging/production API требует Auth. Сам migrator не требует Auth, но при
заданных Auth-переменных проверяет тот же полный набор через общий `config.Load`.
Неверные HTTP-настройки поэтому могут остановить и migrator.

Явно пустые `APP_ENV`, `HTTP_LISTEN_ADDR`, `HTTP_SHUTDOWN_TIMEOUT`,
`DATABASE_URL`, `MIGRATIONS_PATH` отклоняются. Для Supabase и origin по умолчанию
требуется HTTPS. HTTP допускается только при `AUTH_ALLOW_INSECURE_HTTP=true`,
в development и с `localhost` или loopback IP, включая `127.0.0.1` и `::1`.
Это правило проверяется для каждого URL отдельно. Даже при разрешении HTTP
cookie остаётся Secure, если `AUTH_ORIGIN` использует HTTPS.

Приложение не настраивает TLS HTTP-сервера; HTTPS завершается на внешнем прокси.
`DATABASE_URL` должен задавать подходящий TLS-режим отдельно. В коде БД фиксированы
connect timeout 5s, statement timeout 5s и idle-in-transaction timeout 5s,
пул до 25 открытых/10 свободных соединений. Migrator устанавливает для своей
сессии lock timeout 5s и statement timeout 30s. Env для этих значений нет.

Настройки передаются в окружении процесса: `export ИМЯ='значение'` в sh/zsh/bash,
`$env:ИМЯ = 'значение'` в PowerShell либо средствами запуска контейнера/сервиса.
В Docker `--env ИМЯ` передаёт уже экспортированное значение текущего терминала;
`--env-file <ПУТЬ_К_ЗАЩИЩЁННОМУ_ФАЙЛУ>` загружает файл средствами Docker,
а не приложения. В таком файле используется `ИМЯ=значение` без `export`;
путь в угловых скобках заменить. Файл должен храниться вне передаваемых файлов,
с ограниченными правами, без вывода содержимого в логи. Нельзя передавать API
DSN роли migrator: роли и SQL-права приведены в [инструкции наката миграций](MIGRATIONS.md).

## Docker

Собирать образ нужно из корня проекта:

```sh
docker build -f backend/Dockerfile -t faq-api:local .
```

Образ содержит `/api`, `/migrate`, SQL и CA-сертификаты. Основа образа — `scratch`,
без shell и curl, пользователь `65532:65532`. По умолчанию production, порт `:8080`,
`MIGRATIONS_PATH=/migrations`. Для команд ниже рабочий каталог — корень проекта. Для запуска Auth переменные
должны быть заранее экспортированы в текущем терминале; адрес БД должен быть
доступен из контейнера. Для первого вызова `DATABASE_URL` должен содержать DSN
роли migrator; перед вторым вызовом заменить его DSN роли API:

```sh
docker run --rm --env DATABASE_URL --entrypoint /migrate faq-api:local
docker run --rm --env DATABASE_URL --env SUPABASE_URL --env SUPABASE_KEY --env AUTH_ORIGIN --env AGENT_CURSOR_KEY -p 127.0.0.1:8080:8080 faq-api:local
```

`127.0.0.1` внутри контейнера указывает на сам контейнер. Для сервисов на хосте
Docker Desktop используется `host.docker.internal`; для отдельных контейнеров —
имя сервиса в общей сети. HTTPS origin требует внешнего TLS-прокси, которого в
репозитории нет. Запуск только проверок состояния без авторизации:

```sh
docker run --rm -e APP_ENV=development -p 127.0.0.1:8080:8080 faq-api:local
```

## Тесты и типичные проблемы

Из каталога `backend`:

```sh
go test ./...
go vet ./...
golangci-lint run ./...
```

Для проверки конкурентного кода использовать `go test -race ./...`.
Нужны включённый cgo и C-компилятор. golangci-lint v2 устанавливается отдельно;
конфигурация находится в `.golangci.yml`.

SQL-тестам нужна отдельная БД и переменная `TEST_DATABASE_URL`.
Без неё SQL-тесты пропускаются. Для проверки INSERT/SELECT и запретов
UPDATE/DELETE/TRUNCATE журнала нужна также `TEST_AUDIT_ROLE`; без неё явно
пропускаются `TestAuditSQLRestrictedRole` и `TestRegistrationRestrictedRole`. Роли приложение не создаёт.
Подготовка и точное сообщение пропуска описаны в [DATABASE.md](DATABASE.md#sql-тесты).

- API не запускается — проверить окружение, доступность БД и свободный порт.
- Auth возвращает 404 — проверить настройки авторизации.
- Auth возвращает 500 — проверить миграции и права API в БД.
- POST возвращает 403 — проверить Origin, cookie и X-CSRF-Token.
- Cookie не сохраняется на локальном HTTP — проверить `AUTH_ALLOW_INSECURE_HTTP`.

Работа с сессиями и ограничения описаны в [AUTH.md](AUTH.md).

## Внутренний аудит

Миграция `000002_audit` добавляет журнал; миграции 3–5 — агентов, диалоги,
replay и cleanup requests; миграция 6 — подготовку trial. Текущая версия схемы — 6.
Состав и порядок применения описаны в [MIGRATIONS.md](MIGRATIONS.md).
`AuditService.WriteInTx` сохраняет событие в переданной бизнес-транзакции,
проверяет каталог, scope/actor и whitelist metadata. Повтор сравнивается по
серверному ключу, конфликт payload возвращает ошибку. Прикладной роли нужны
INSERT и SELECT для сравнения повторов, без UPDATE/DELETE/TRUNCATE.
Полный контракт и пример вызова — в [AUDIT.md](AUDIT.md).
CRUD агентов вызывает аудит в общей транзакции. Вызовы из Auth, чтение аудита
и tenant RBAC для журнала пока отсутствуют.

Проверки аудита включают unit-валидацию и SQL-сценарии: upgrade 1→2, повтор
миграций, constraints, дедупликацию, конфликт payload, две параллельные доставки
и rollback бизнес-изменения при ошибке аудита, включая конфликт повтора.
Дополнительно проверяются rollback первого заблокировавшего писателя, отмена
ожидающего INSERT с откатом бизнес-изменения и повторным использованием пула,
точные SQLSTATE/constraint/column и runner down/up с историей 2→1→2.
Проверяются границы UTC, сохранение журнала при удалении actor и запрет удаления
компании со ссылкой из аудита. SQL-тесты выполняют настоящие
миграции в отдельных схемах TEST_DATABASE_URL.

Исторические результаты, сохранённые до текущей проверки:

- 11.11.2026: полный набор и `-race` — по 146 успешных случаев без пропусков
  с отдельной PostgreSQL 18.3. Форматирование, vet, lint и сборка прошли.
- 17.11.2026: после дополнения тестов полный набор и `-race` — по 151 успешному
  случаю без пропусков с отдельной PostgreSQL 18.3 и ограниченной
  `TEST_AUDIT_ROLE`. Форматирование, vet, lint и сборка прошли. Отдельный запуск
  без `TEST_AUDIT_ROLE` подтвердил явный skip проверки прав; он не проверяет права.
- В этих прогонах сборка и запуск контейнера не проверены: Docker daemon недоступен.

Проверка аудита 17.11.2026 до расширения схемы: `go test -count=1 ./...` и
`go test -race -count=1 ./...` — каждый по 151 успешному случаю, 0 ошибок,
0 пропусков с отдельной PostgreSQL 18.3 и `TEST_AUDIT_ROLE`. Форматирование
без diff, vet, build и golangci-lint v2.1.6 прошли без замечаний.

Отдельный локальный smoke подтвердил backup и восстановление в другую БД,
upgrade 1→2 и повтор под LOGIN-ролью migrator, constraints/6 индексов,
SELECT/INSERT и реальные отказы UPDATE/DELETE/TRUNCATE под LOGIN-ролью API.
Новый API и предыдущий API из коммита
`e758d73ef9a649459cae642f009a94c80b52a779` вернули health 200 на схеме 2
и штатно завершились по SIGTERM. Оба запуска — development без Auth/Supabase.
Docker daemon повторно недоступен; Docker build/run не выполнялись.
Стенд, рабочие роли/backup, внешний HTTPS/proxy и login/session/logout через
Supabase не проверены. Эти локальные результаты не заменяют приёмку стенда;
подробности — в [карточке 0.0.2](upgrade_cluster/release_0.0.2.md#e2e-тесты).


Проверка 18.11.2026 после добавления подготовки trial: полный набор
`go test -count=1 ./...` и `go test -race -count=1 ./...` — каждый по 192
успешных случая, 0 ошибок и 0 пропусков. Использовались отдельная PostgreSQL
18.3 и ограниченная NOLOGIN-роль. Проверены повтор/гонка регистрации,
rollback owner/intent, восстановление login после provider/SQL/session ошибок,
минимальные INSERT-права на intent и отказ 42501, upgrade 2→6/repeat/down/up,
legacy identity и отсутствие backfill. Форматирование gofmt/goimports без diff,
`go vet ./...`, `go build ./...` и golangci-lint v2 прошли без замечаний.

До слияния веток объединённый каталог миграций 1–6 отдельно проверялся во
временной БД. Теперь все шесть миграций входят в текущую ветку вместе с CRUD
агентов. Результаты ниже относятся к отдельным этапам разработки; проверка
объединённого состояния указана в конце документа. Внешний Supabase, отсутствие
писем, браузер и HTTPS/proxy остаются приёмкой стенда. Шаги проверки регистрации —
в [AUTH.md](AUTH.md#проверка-после-установки).

## REST, агенты и диалоги

[Контракт REST](../contracts/README.md) версии draft.4 содержит DTO/fixtures,
тесты refs/schema/request/response и JSON parity. Работающие Auth/probes
проверяются через локальный HTTP server, включая HEAD, Allow и границы UTF-8.
Кроме CRUD агентов остальные операции проектируются и HTTP-маршрутов пока не имеют.

Миграции 3/4 добавляют агента с website/description/tombstone и scoped таблицы
чата с состояниями и terminal result. В repository появился минимальный
CreateOrRead AI request с SHA-256 payload и conflict/replay. Это не ChatService:
ChatService RBAC, отправка AI-запросов, учёт обращений, виджет, очередь и аудит
чат-операций пока не подключены. Scope, FK, revisions и ограничения sources описаны
в [DATABASE.md](DATABASE.md#агенты-и-диалоги), применение текущей версии 6 — в
[MIGRATIONS.md](MIGRATIONS.md). Политики partner signature, consent, quota и
retention требуют решений владельцев; consumer review и G01 остаются внешней
приёмкой.

Проверка пакета 17.11.2026 на отдельной PostgreSQL 18.3:
`go test -count=1 -json ./...` и `go test -race -count=1 -json ./...` —
каждый по 866 успешных тестов и подтестов, 0 ошибок и 0 пропущенных тестов.
Пять пакетов без тестовых файлов отмечены Go как `[no test files]`; SQL-тесты
выполнены с TEST_DATABASE_URL и TEST_AUDIT_ROLE. Проверены fresh/upgrade/repeat/
down-up новых миграций, scoped replay/conflict и фактическое ожидание
конкурентного INSERT. `go vet ./...`, golangci-lint v2.1.6 и проверка форматирования
прошли без замечаний. Внешние проверки, перечисленные выше, этим не подтверждаются.


18.11.2026 уточнены REST mutations/CSRF, planned 202 ChatOperation и session-scoped
visitor history, nonblank Unicode и no-store responses. Первичная visitor session
не участвует в token-response replay. Членство проверяется trigger при создании
или смене actor/scope диалога; постоянного FK на membership нет, поэтому
существующий отзыв сессии через DELETE membership сохраняет историю чата.
CreateOrRead явно фиксирует READ COMMITTED.

Итоговая проверка 18.11.2026 после исправлений: `go test -count=1 -json ./...`
и `go test -race -count=1 -json ./...` — каждый по 1182 успешных случая,
0 ошибок и 0 пропущенных тестов. Отдельный `go test -count=1 -json ./internal/migration`
проверил 100 случаев. SQL выполнен на отдельной PostgreSQL 18.3 с ограниченной
тестовой ролью; пакеты без тестовых файлов не учитываются как пропущенные тесты.
`go vet ./...`, `golangci-lint run ./...` и `golangci-lint fmt --diff`
прошли: 0 замечаний и пустой diff форматирования. Независимое review выполнено
четырьмя проверками; после исправлений повторная проверка не нашла подтверждённых
проблем. Потребительская и интеграционная приёмка остаются открытыми.

## Агенты

При включённом Auth подключены POST/GET `/api/v1/agents` и GET/PATCH/DELETE
`/api/v1/agents/{id}`. Для них нужна миграция 5 и [права API](MIGRATIONS.md#права-api-после-миграции).
Сервис берёт компанию, пользователя и текущую роль из проверенной сессии.
`audit_read` не меняет CRUD права. Public ID, bearer и клиентский account ID
кабинетного доступа не дают.

POST и PATCH требуют всю тройку `name`, `website`, `description`; PATCH
дополнительно требует quoted `If-Match`. Пустые website/description допустимы,
website хранится как текст без загрузки сайта. Технический предел JSON — 1 MiB.
GET/PATCH возвращают ETag; stale revision даёт 412. Все изменения требуют
Origin, constant-time проверки CSRF и Idempotency-Key. Повтор проверяет права
заново и возвращает сохранённый результат; конфликт payload даёт 409.

DELETE возвращает 202 Job со статусом pending после общей транзакции tombstone,
аудита и durable cleanup request. Worker очистки, widget/chat проверки и удаление
файлов пока отсутствуют. Tombstone скрыт из list/get/update; новый ключ DELETE
для него даёт 404. [HTTP.md](HTTP.md) описывает точные ограничения и повторы.

Проверка CRUD 18.11.2026: `go test -count=1 -json ./internal/handlers/... ./internal/service ./internal/migration` из backend — 1171 успешный тест и подтест,
0 ошибок и 0 пропусков. Использованы отдельная локальная PostgreSQL и
TEST_AUDIT_ROLE; HTTP-тесты выполняют настоящий router с Auth login/session,
expiry/logout, двумя tenant, конкурентным replay/CAS, rollback и cleanup retry.
Supabase в новых тестах заменён provider stub; реальный Supabase, внешний
consumer/G01, widget/chat и worker очистки этим прогоном не проверены.

Итоговая проверка CRUD 18.11.2026 после исправлений: из `backend`
`go test -count=1 -json ./...` и `go test -race -count=1 -json ./...` —
каждый по 1208 успешных тестов и подтестов, без ошибок и пропусков.
SQL выполнялся на отдельной PostgreSQL 18.3 с `TEST_DATABASE_URL` и
`TEST_AUDIT_ROLE`. `go vet ./...`, `golangci-lint run ./...` и
`golangci-lint fmt --diff` прошли: 0 замечаний, пустой diff форматирования.
Проверены upgrade 4→5, repeat и down5/up5 с сохранением прежних данных,
ошибки SQL после вставки cleanup, конкурентный DELETE, общий ключ cursor,
защита PATCH/DELETE и сохранение опубликованных snapshots.
Consumer review, облачный Supabase, стенд и интеграционные барьеры остаются
внешней приёмкой. Cleanup executor и `GET /api/v1/jobs/{id}` пока не подключены.

## Проверка объединённой ветки

18.11.2026 после объединения регистрации с подготовкой trial и актуальной dev
повторно выполнены `go test -count=1 -json ./...` и
`go test -race -count=1 -json ./...` из `backend`. Каждый прогон: 1241 успешный
тест и подтест, без ошибок и пропусков. SQL-тесты выполнялись на отдельной
временной PostgreSQL 18.3 с `TEST_DATABASE_URL` и ограниченной NOLOGIN-ролью
`TEST_AUDIT_ROLE`. Проверены общий каталог миграций 1–6, регистрация, повтор,
rollback, права, CRUD агентов и replay. Тестовые настройки Auth включают
trial-таблицу; проверки полной схемы ожидают версию 6, а down миграции 6 — версию 5.

`go vet ./...`, `go build ./...`, golangci-lint и проверка форматирования прошли
без замечаний. В пяти обновлённых документах проверены 65 локальных ссылок
на файлы: отсутствующих целей нет. Эти результаты не подтверждают Docker build/run,
внешний Supabase, отсутствие исходящих писем, браузер и HTTPS/proxy на стенде.
