# Backend FAQ Bot

`backend` — Go-модуль `faqbot/backend`. Он обслуживает HTTP-запросы авторизации,
проверяет пароль через Supabase и хранит локальные компании, пользователей и сессии
в PostgreSQL. Отдельная программа применяет миграции — изменения схемы БД.

Работают проверки состояния, регистрация, вход, получение и отзыв сессии.
Доступные HTTP-маршруты перечислены в [HTTP.md](HTTP.md).

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

Все последующие команды Go выполняются из `backend`. `.env.example` — справочник,
не загружаемый файл. Переменные задаются в терминале через `export` (sh/zsh/bash)
или `$env:ИМЯ = 'значение'` (PowerShell). Секреты не сохраняются в репозиторий.

## Переменные окружения

| Переменная | Назначение и обязательность | По умолчанию | Безопасный пример |
| --- | --- | --- | --- |
| `APP_ENV` | Режим: development/staging/production; необязательна | `development` | `development` |
| `HTTP_LISTEN_ADDR` | IP/localhost и порт 1..65535; необязательна | `127.0.0.1:8080` | `127.0.0.1:8081` |
| `HTTP_SHUTDOWN_TIMEOUT` | Время завершения запросов, >0 и ≤1m; необязательна | `10s` | `15s` |
| `DATABASE_URL` | Подключение PostgreSQL; обязательно для Auth, migrate, staging/production | Нет | `postgres://faq:local-only@127.0.0.1:5432/faqbot?sslmode=disable` — только локальная БД |
| `MIGRATIONS_PATH` | Каталог SQL относительно рабочего каталога; необязательна | `./migrations` | `./migrations` |
| `SUPABASE_URL` | Адрес проекта без пути/query; обязателен для Auth | Нет | `https://project.supabase.co` — заменить проект |
| `SUPABASE_KEY` | Ключ API проекта, передаваемый Supabase в `apikey`; обязателен для Auth | Нет | `<ключ тестового проекта>` — подставить через окружение |
| `AUTH_ORIGIN` | Точный адрес клиента со схемой и портом, без пути; обязателен для Auth | Нет | `https://faq.example.org` |
| `AUTH_ALLOW_INSECURE_HTTP` | Разрешить локальный HTTP; cookie без Secure только для HTTP-origin в development | `false` | `true` — только локальная разработка |
| `TEST_DATABASE_URL` | Отдельная БД интеграционных тестов, не настройка API | Нет | `postgres://faq:local-only@127.0.0.1:5432/faqbot_test?sslmode=disable` |

Установка хотя бы одной из четырёх Auth-переменных, включая
`AUTH_ALLOW_INSECURE_HTTP=false`, включает проверку полного набора Auth и требует БД.
В staging/production API требует Auth. Явные пустые значения основных настроек
отклоняются. Для Supabase и origin по умолчанию требуется HTTPS; HTTP допускается
только при явном разрешении, в development и с loopback-адресом (localhost/127.0.0.1).

## Docker

Собирать образ нужно из корня проекта:

```sh
docker build -f backend/Dockerfile -t faq-api:local .
```

Образ содержит `/api`, `/migrate`, SQL и CA-сертификаты. Основа образа — `scratch`,
без shell и curl, пользователь `65532:65532`. По умолчанию production, порт `:8080`,
`MIGRATIONS_PATH=/migrations`. Для запуска Auth переменные должны быть заранее
установлены в текущем терминале; адрес БД должен быть доступен из контейнера:

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

Проверка 04.11.2026: `go test -count=1 ./...`, `go vet ./...`,
`golangci-lint run ./...` и `go test -race -count=1 ./...` прошли на Go 1.24.0
с отдельной PostgreSQL 18.3. SQL-тесты выполнялись без пропусков.
Проверены запуск настроенного API, продление сессии до абсолютного срока,
параллельный вход и отзыв сессии во время проверки пароля.
Эти проверки выполнялись с дополнительными локальными тестами авторизации
и контрактов; дополнительные тестовые файлы и каталог `contracts` в поставку не входят.
Настоящий Supabase и браузерный клиент в эту проверку не входили.

- 404 на Auth: не включена конфигурация Auth; на agents/chat — маршрут ещё отсутствует.
- Сервер не стартует: проверить обязательные переменные, доступность БД, свободный порт.
- Auth возвращает 500 при работающем readiness: проверить применённую схему;
  readiness проверяет соединение, а не наличие таблиц.
- 403 на POST: точный `Origin`, cookie и актуальный `X-CSRF-Token` обязательны.
- Cookie теряется на HTTP: для локальной разработки нужно явное разрешение HTTP.
- Контейнер сразу завершился: production требует БД и полного набора Auth.

Ограничения сессий описаны в [AUTH.md](AUTH.md).
