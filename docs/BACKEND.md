# Backend FAQ Bot

`backend` — Go-модуль `faqbot/backend`. Он обслуживает HTTP-запросы авторизации,
проверяет пароль через Supabase и хранит локальные компании, пользователей и сессии
в PostgreSQL. Отдельная программа применяет миграции — изменения схемы БД.

Работают проверки состояния, регистрация, вход, получение и отзыв сессии.
Доступные HTTP-маршруты, проверки состояния и логи описаны в [HTTP.md](HTTP.md).
Порядок установки стенда — [релиз 0.0.1](upgrade_cluster/release_0.0.1.md).

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
Ожидается 200. Для работы авторизации сначала подготовить
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
| `AUTH_ORIGIN` | Единственный разрешённый origin страницы; API → AuthHandler | Обязательна при Auth; отсутствует | HTTPS, без credentials, пути (включая `/`), query и fragment; сравнение строк точное. Шаблон `https://<PUBLIC_HOST>`; порт включать, если его отправляет браузер |
| `AUTH_ALLOW_INSECURE_HTTP` | Разрешение loopback HTTP при проверке URL; API → AuthHandler управляет Secure cookie | Необязательна; `false` | Boolean по `strconv.ParseBool`: `true/false`, `1/0`, `t/f` и поддерживаемые варианты регистра. `true` допустимо только в development. Для стенда `false` либо не задавать |
| `TEST_DATABASE_URL` | Только SQL-интеграционные тесты, не конфигурация API/migrator | Для выполнения SQL-тестов; отсутствует → пропуск | Отдельная тестовая БД с правом создания схем/таблиц. Шаблон `postgres://<TEST_ROLE>:<URL_ENCODED_PASSWORD>@127.0.0.1:5432/<TEST_DB>?sslmode=disable`; отключение TLS только для локальной БД |

Установка хотя бы одной из четырёх Auth-переменных (`SUPABASE_URL`,
`SUPABASE_KEY`, `AUTH_ORIGIN`, `AUTH_ALLOW_INSECURE_HTTP`), даже в пустую строку
или `AUTH_ALLOW_INSECURE_HTTP=false`, включает Auth и требует полного набора:
непустых URL, ключа, origin и `DATABASE_URL`. Чтобы запускать только health в
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
DSN роли migrator: роли и SQL-права приведены в плане установки.

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
docker run --rm --env DATABASE_URL --env SUPABASE_URL --env SUPABASE_KEY --env AUTH_ORIGIN -p 127.0.0.1:8080:8080 faq-api:local
```

`127.0.0.1` внутри контейнера указывает на сам контейнер. Для сервисов на хосте
Docker Desktop используется `host.docker.internal`; для отдельных контейнеров —
имя сервиса в общей сети. HTTPS origin требует внешнего TLS-прокси, которого в
репозитории нет. Запуск только проверок состояния без авторизации:

```sh
docker run --rm -e APP_ENV=development -p 127.0.0.1:8080:8080 faq-api:local
```

## Тесты и типичные проблемы

Из `backend`:

```sh
go test -count=1 ./...
go vet ./...
golangci-lint run ./...
go test -race -count=1 ./...
```

golangci-lint устанавливается отдельно; конфигурация v2 в `.golangci.yml`.
Проверенная версия — v2.1.6. `-race` требует поддерживаемой платформы и включённого cgo; вне macOS также
нужен C-компилятор. Подробности — [документация Go](https://go.dev/doc/articles/race_detector).
SQL-тестам нужна отдельная БД с правами создания схем
и таблиц и `TEST_DATABASE_URL`; без неё они пропускаются.

Ранее зафиксированная проверка 04.11.2026: `go test -count=1 ./...`, `go vet ./...`,
`golangci-lint run ./...` и `go test -race -count=1 ./...` прошли на Go 1.24.0
с отдельной PostgreSQL 18.3. SQL-тесты выполнялись без пропусков.
Проверены запуск настроенного API, продление сессии до абсолютного срока,
параллельный вход и отзыв сессии во время проверки пароля.
Эти результаты относились к рабочему дереву с дополнительными локальными
тестами авторизации и контрактов; они не подтверждают состав поставки релиза.
Перед выпуском состав и ревизию необходимо сверить по плану установки.
Настоящий Supabase и браузерный клиент в эту проверку не входили.

При подготовке установки 04–05.11.2026 повторно прошли `go test -count=1 ./...`
с отдельной временной PostgreSQL 18.3 (743 теста/подтеста, без пропусков)
и `go vet ./...` текущего рабочего дерева. Обе программы отдельно собраны из
зафиксированной ревизии; миграции, права ролей и HTTP с заглушкой провайдера
проверены по [плану установки](upgrade_cluster/release_0.0.1.md).
Docker-образ, настоящий Supabase, HTTPS-прокси и браузер не проверялись.

- 404 на Auth: не включена конфигурация Auth; на agents/chat — маршрут ещё отсутствует.
- Сервер не стартует: проверить обязательные переменные, доступность БД, свободный порт.
- Auth возвращает 500 при работающем readiness: проверить применённую схему;
  readiness проверяет соединение, а не наличие таблиц.
- 403 на POST: точный `Origin`, cookie и актуальный `X-CSRF-Token` обязательны.
- Cookie теряется на HTTP: для локальной разработки нужно явное разрешение HTTP.
- Контейнер сразу завершился: production требует БД и полного набора Auth.

Ограничения сессий описаны в [AUTH.md](AUTH.md).
