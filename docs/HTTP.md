# HTTP-сервер и наблюдаемость

Go API использует стандартный `net/http.ServeMux`. Точка входа —
[cmd/api/main.go](../backend/cmd/api/main.go), маршруты —
[router.go](../backend/internal/handlers/router.go), [auth.go](../backend/internal/handlers/auth.go)
и [agents.go](../backend/internal/handlers/agents.go).
Запуск и переменные описаны в [BACKEND.md](BACKEND.md), установка стенда —
в [плане релиза 0.0.1](upgrade_cluster/release_0.0.1.md).

## Маршруты

| Метод и полный путь | Назначение | Доступность и авторизация |
| --- | --- | --- |
| `GET`, `HEAD /health/live` | Проверка ответа HTTP-процесса | Всегда, без авторизации |
| `GET`, `HEAD /health/startup` | Проверка завершения начальной настройки | Всегда, без авторизации |
| `GET`, `HEAD /health/ready` | Готовность принимать запросы и проверка соединения с БД | Всегда, без авторизации |
| `GET /api/v1/auth/csrf` | Получить CSRF-токен, при необходимости создать предварительную сессию | Только при включённом Auth; вход не требуется |
| `POST /api/v1/auth/register` | Зарегистрировать пользователя и войти | Только при включённом Auth; нужна предварительная или действующая сессия |
| `POST /api/v1/auth/login` | Войти по email и паролю | То же |
| `GET /api/v1/auth/session` | Получить локальную identity и параметры сессии | Только при включённом Auth; нужна сессия после входа |
| `POST /api/v1/auth/logout` | Отозвать сессию | Только при включённом Auth; нужна действующая сессия |
| `POST /api/v1/agents` | Создать агента | При включённом Auth; owner/admin текущей компании |
| `GET /api/v1/agents` | Список агентов | То же |
| `GET /api/v1/agents/{id}` | Получить агента и ETag | То же |
| `PATCH /api/v1/agents/{id}` | Обновить полную тройку полей | То же; If-Match обязателен |
| `DELETE /api/v1/agents/{id}` | Скрыть агента и принять заявку очистки | То же; If-Match не требуется |

Полный набор настроек Auth описан в [BACKEND.md](BACKEND.md). В development без
этих настроек Auth не подключается и возвращает 404. Неполная конфигурация
останавливает запуск. В staging/production API без Auth не запускается.
Chat, загрузка документов и восстановление пароля через Go API не реализованы. Форматы, коды, cookie,
Origin/CSRF и примеры всех Auth-запросов — в [AUTH.md](AUTH.md).

## REST-схемы

[openapi.yaml](../contracts/openapi.yaml) `1.0.0-draft.4` содержит работающие Auth/CRUD агентов и
проектируемые operations кабинета/виджета/партнёра. `x-implementation-status`
отмечает доступность; добавление схемы не регистрирует маршрут.
[health.openapi.yaml](../contracts/health.openapi.yaml) описывает probes.
[Пояснения контракта](../contracts/README.md) фиксируют required/nullability,
pagination, revisions, idempotency и открытые продуктовые решения.

В Auth 405 содержит Allow; HEAD не поддерживается и получает 405 без тела по
правилам net/http. Logout допускает предварительную сессию с её CSRF и Origin.
Session/logout не имеют искусственных 400/429 ответов; session использует
GET, logout — POST. Ошибки provider 502/503 относятся к login/register.
Фактические ограничения credential/body измеряются в байтах UTF-8 через
x-maxBytes/x-maxBodyBytes, а не maxLength; byte cases проверяются HTTP-тестом.
NoopAuthentication в тестах fixtures валидирует формат, а не tenant RBAC.

Кабинетные изменения требуют Origin и CSRF; feedback также допускает отдельную
WidgetSession без кабинетных заголовков. Незавершённый чат описан ответом
`202 ChatOperation`, конфликт payload — ответом 409. Первичная выдача visitor
session исключена из replay ответов с bearer token. Проектируемый
`GET /api/v1/widget/messages` читает историю собственного диалога из session с
limit/cursor. Чувствительный JSON требует `Cache-Control: no-store`.
Эти операции ещё не подключены к HTTP-серверу.

## Проверки состояния

Для health-запросов не нужны тело, cookie, Origin, CSRF или специальные заголовки.
После успешного старта `GET` возвращает 200 и `{"status":"ok"}`, `HEAD` — 200 без
тела. Ошибки `GET` имеют общий JSON-формат, описанный ниже; `HEAD` не содержит тела.

| Проверка | Что проверяется | Отрицательный ответ | Чего не подтверждает 200 |
| --- | --- | --- | --- |
| `/health/live` | Процесс обрабатывает HTTP-запрос | У проверки нет собственной ветки 503; остановленный/зависший процесс может не ответить | Готовность БД, схема, права, Supabase, Auth, прокси и браузер |
| `/health/startup` | Флаг, установленный после чтения настроек, начального подключения к БД (если задана) и открытия порта | 503 `service_starting` до установки флага | Текущую доступность БД, миграции, права таблиц, Supabase |
| `/health/ready` | Флаг ready; при заданной БД — `PingContext` с таймаутом 1s | 503 `service_not_ready` при снятом флаге или ошибке соединения | Наличие таблиц и правильность версии схемы, DML-права API, Supabase, регистрацию/вход, внешний TLS и cookie |

`health.Start()` вызывается перед `Serve`, поэтому обычный запуск начинает
отвечать сразу с установленными startup/ready. Ошибки конфигурации, начального
соединения с БД или открытия порта завершают процесс до HTTP-ответа.
Без `DATABASE_URL` в development readiness проверяет только флаг процесса.
После потери соединения с настроенной БД ready может вернуть 503, а live и
startup — 200. При остановке снимается ready; live/startup остаются успешными,
пока сервер ещё способен обслужить запрос. Затем порт закрывается.

Из любого каталога при работающем локальном API:

```sh
curl -i http://127.0.0.1:8080/health/live
curl -I http://127.0.0.1:8080/health/startup
curl -i http://127.0.0.1:8080/health/ready
```

Эти команды проверяют прямое соединение с API, без внешнего прокси.

## Ответы и ограничения HTTP

JSON-ответы имеют `Content-Type: application/json`. Формат ошибки:

```json
{"code":"not_found","message":"Ресурс не найден","request_id":"example-request-id"}
```

Ответы прикладных обработчиков получают `Cache-Control: no-store` и `X-Request-ID`.
Входной `X-Request-ID` принимается только как `[A-Za-z0-9_-]{1,80}`, иначе
создаётся случайный. Это идентификатор запроса, не доказательство прав.
Неизвестный путь возвращает 404 `not_found`. Неверный метод — 405
`method_not_allowed` с `Allow: GET, HEAD` для health или единственным разрешённым
методом Auth. Для коллекции агентов Allow — `GET, POST`, для объекта —
`GET, PATCH, DELETE`; HEAD/OPTIONS агентов также получают 405.
Стандартный ServeMux может перенаправлять пути при нормализации URL;
для вызовов следует использовать точные пути из таблицы.

Серверные таймауты: заголовки 5s, чтение 15s, запись 30s, idle 60s;
максимум заголовков 1 MiB. Ограничения JSON и частоты Auth — в [AUTH.md](AUTH.md).

## Метрики и логи

Экспортируемых метрик сейчас нет. Маршрут `/metrics`, Prometheus exporter,
счётчики HTTP-запросов и гистограммы задержек не реализованы. Поэтому имена,
типы, единицы измерения и labels метрик отсутствуют. Запрос `/metrics` получает
обычный 404. Health-проверки и строки логов не являются Prometheus-метриками.

API пишет JSON через `slog` в stdout: общие поля `time`, `level`, `msg`;
для `API started` добавляются `address` и `environment`, при завершении с ошибкой —
`error`. Реализованы события `API started`, `API shutting down`, `API stopped`.
Migrator пишет `migrations are up to date` при успехе или `migration stopped`
с `error` при сбое. Код возврата процесса при сбое — 1.

Логирования каждого HTTP-запроса и отдельных ошибок Auth нет; `X-Request-ID`
возвращается клиенту, но не записывается автоматически в лог запроса. Пароли,
cookie и тела запросов в эти логи не выводятся; ошибки БД обёрнуты для безопасного
вывода. Успешный лог запуска подтверждает запуск процесса, а лог мигратора —
завершение `Up` без ошибки (в том числе отсутствие новых миграций).
Они не заменяют SQL-проверку схемы и пробный вход через Supabase.

Из любого каталога для контейнера с именем `faq-api` (если оно назначено при запуске):

```sh
docker logs --tail 100 faq-api
```

При запуске `go run ./cmd/api` логи видны в терминале процесса.

## Остановка

SIGINT/SIGTERM снимает готовность и вызывает `Server.Shutdown`. На завершение
текущих запросов выделяется `HTTP_SHUTDOWN_TIMEOUT`, затем соединения закрываются
принудительно с ошибкой. Задержки для распространения readiness в оркестраторе нет.

Из корня проекта для ручной проверки:

```sh
cd backend
go build -o /tmp/faq-api ./cmd/api
/tmp/faq-api
```

В другом терминале из любого каталога послать `kill -TERM <PID>` именно
собранному процессу, заменив `<PID>` его идентификатором. `go run` использует
дополнительный процесс-обёртку. Автотест в `cmd/api/main_test.go` запускает
отдельный процесс и проверяет остановку; ему нужно открытие локального TCP-порта.

## CRUD агентов

Scope содержит копию user/account/role/audit_read из проверенной Auth session.
Предварительная, истёкшая, отозванная сессия и отсутствие членства дают 401;
роль вне owner/admin — 403. GET запрещает чужой Origin и Sec-Fetch-Site=cross-site.
POST/PATCH/DELETE требуют точный настроенный Origin и X-CSRF-Token текущей cookie.
`audit_read` не даёт прав изменения и не ограничивает owner/admin CRUD.

POST/PATCH принимают только JSON с тремя обязательными строками `name`, `website`,
`description`. Missing/null, неизвестные и повторные поля, trailing JSON,
невалидный UTF-8 и тело больше 1048576 байт дают 400 invalid_request. Имя содержит
1..100 Unicode символов и хотя бы один символ вне White_Space; пробелы сохраняются.
Website/description могут быть пустыми. Website не проверяется как URL и не загружается.
DELETE принимает пустое тело. Scope нельзя задать через тело, query или headers.
UUID чужой компании и public_id вместо UUID дают 404. GET и список скрывают
tombstone; новые PATCH/DELETE с новым Idempotency-Key для него дают 404.

Список принимает только `limit` (1..100, default 20) и `cursor`, по одному значению.
Сортировка — created_at DESC,id DESC; пустая страница содержит items=[] и
next_cursor=null. HMAC cursor привязан к компании, ресурсу и сортировке. Чужой,
изменённый, пустой и malformed cursor дают 400. Ключ подписи AGENT_CURSOR_KEY общий для реплик и перезапусков. После его ротации
нужно начать список заново.

GET/PATCH объекта возвращают quoted ETag revision. PATCH требует ровно quoted
положительное int64 в If-Match без префиксов, списков или пробелов; missing/invalid —
400, устаревшее значение — 412 revision_conflict. Изменение атомарно повышает
revision на один; knowledge/config revisions этот CRUD не меняет.

Все три mutations требуют Idempotency-Key `[A-Za-z0-9_-]{1,128}`. Scope ключа —
account+actor+operation+resource; для создания resource пуст. SHA-256 включает
точные значения трёх полей, у PATCH также If-Match revision. Серверный operation
UUID не зависит от Request-ID. Конкурентные запросы с одним ключом сохраняют одно
изменение и один audit; replay возвращает исходный JSON/ID и код ответа после
новой проверки сессии/прав. Replay PATCH проверяется до stale revision. Другой
payload с тем же ключом — 409 idempotency_conflict. Ранее успешные POST/PATCH/DELETE
с исходными ключом и payload возвращают сохранённый результат даже после
удаления агента. Для PATCH нужен и исходный If-Match. Перед replay заново
проверяются текущие сессия, membership и роль; отзыв сессии или membership даёт
401. Такой replay не восстанавливает агента и не открывает к нему новый доступ.

DELETE не требует If-Match. Он в одной SQL-транзакции создаёт tombstone,
agent.deleted audit и agent_cleanup_requests со статусом pending; 202 означает
принятую заявку, а не завершение очистки. Ошибка cleanup adapter отменяет всё и
возвращает 500; тот же ключ можно повторить после устранения ошибки. Тот же ключ
успешного DELETE возвращает исходный Job; новый ключ для tombstone — 404.
Обработчика очереди, физической очистки, widget/chat маршрутов пока нет.
`GET /api/v1/jobs/{id}` также ещё не подключён; сохранённый Job остаётся pending.
Retention replay и cleanup записей ещё не назначен. Отказы доступа не записываются
этим CRUD в audit; успешные изменения и audit подтверждаются вместе.

Строки агента с NUL и JSON с непарными UTF-16 surrogate escapes отклоняются
с 400 до SQL. Корректные surrogate pairs, literal U+FFFD и исходный Unicode
сохраняются без нормализации. DELETE проверяет фактическое пустое тело, включая chunked.

### Примеры CRUD после входа

Из любого каталога с curl сначала выполнить вход по [AUTH.md](AUTH.md#порядок-входа-и-проверка-запросами).
Задать `API_BASE_URL` без завершающего `/`, `AUTH_ORIGIN` точным внешним origin,
`COOKIE_FILE` абсолютным путём к сохранённому cookie jar и `CSRF_TOKEN` токеном
из ответа успешного login/session. Cookie jar хранить вне Git. Примеры изменяют
только специально созданного тестового агента. Ключи операций должны быть новыми
для нового сценария; при проверке replay сохранять исходные ключ и payload.

```sh
curl --fail-with-body -i -b "$COOKIE_FILE" \
  -H "Origin: $AUTH_ORIGIN" -H "X-CSRF-Token: $CSRF_TOKEN" \
  -H 'Content-Type: application/json' -H 'Idempotency-Key: smoke-create-001' \
  --data '{"name":"Проверочный агент","website":"","description":""}' \
  "$API_BASE_URL/api/v1/agents"
```

Ожидается 201, JSON агента и ETag. Сохранить `AGENT_ID` из `id` и `AGENT_ETAG`
из заголовка целиком, включая кавычки. Список и чтение:

```sh
curl --fail-with-body -i -b "$COOKIE_FILE" "$API_BASE_URL/api/v1/agents?limit=1"
curl --fail-with-body -i -b "$COOKIE_FILE" "$API_BASE_URL/api/v1/agents/$AGENT_ID"
```

Ожидается 200. Для следующей страницы взять непустой next_cursor; при null
страниц больше нет. Передавать cursor с URL-кодированием:

```sh
curl --fail-with-body -i -b "$COOKIE_FILE" --get \
  --data-urlencode 'limit=1' --data-urlencode "cursor=$NEXT_CURSOR" \
  "$API_BASE_URL/api/v1/agents"
```

Изменение и удаление:

```sh
curl --fail-with-body -i -b "$COOKIE_FILE" -X PATCH \
  -H "Origin: $AUTH_ORIGIN" -H "X-CSRF-Token: $CSRF_TOKEN" \
  -H 'Content-Type: application/json' -H 'Idempotency-Key: smoke-update-001' \
  -H "If-Match: $AGENT_ETAG" \
  --data '{"name":"Обновлённый агент","website":"","description":""}' \
  "$API_BASE_URL/api/v1/agents/$AGENT_ID"
curl --fail-with-body -i -b "$COOKIE_FILE" -X DELETE \
  -H "Origin: $AUTH_ORIGIN" -H "X-CSRF-Token: $CSRF_TOKEN" \
  -H 'Idempotency-Key: smoke-delete-001' \
  "$API_BASE_URL/api/v1/agents/$AGENT_ID"
```

PATCH возвращает 200 и новую revision/ETag, DELETE — 202 с Job pending.
GET удалённого ID с той же cookie возвращает 404; повтор DELETE с исходным
ключом — исходный Job. Для проверки stale revision повторить PATCH до удаления
с прежним ETag и новым ключом: ожидается 412. Ошибки возвращают JSON с error.code;
400 — invalid_request, 401 — unauthorized, 403 — forbidden, 404 — not_found,
409 — idempotency_conflict, 412 — revision_conflict, 500 — internal_error.

### Ответы и ошибки CRUD

JSON агента содержит `id`, `account_id`, `public_id`, `name`, `website`,
`description`, `status`, `revision`, `knowledge_revision`,
`active_knowledge_revision`, `config_revision`, `published_config_revision`
и `public_sources_enabled`. ID и revision берутся из ответа; клиент не задаёт
account_id или revision в теле. GET и POST/PATCH возвращают объект агента.
Список возвращает `{"items":[],"next_cursor":null}` для пустой страницы;
на непустой странице items содержит такие же объекты, next_cursor — строка
для следующей страницы либо null.

Пример успешного создания (UUID и public_id — синтетические):

```json
{"id":"11111111-1111-4111-8111-111111111111","account_id":"22222222-2222-4222-8222-222222222222","public_id":"EXAMPLE_PUBLIC_ID","name":"Проверочный агент","website":"","description":"","status":"draft","revision":1,"knowledge_revision":0,"active_knowledge_revision":0,"config_revision":1,"published_config_revision":0,"public_sources_enabled":false}
```

DELETE возвращает, например,
`{"id":"33333333-3333-4333-8333-333333333333","status":"pending","error_code":null}`.
ID здесь относится к заявке, а не к агенту. Ошибка имеет форму
`{"error":{"code":"not_found","message":"Ресурс не найден"}}`.

| HTTP | error.code | Причина |
| --- | --- | --- |
| 400 | invalid_request | JSON, поля, размеры, query, key или If-Match не соответствуют контракту |
| 401 | unauthorized | Нет действующей авторизованной сессии или членства |
| 403 | forbidden | Роль, Origin, CSRF или cross-site запрос запрещены |
| 404 | not_found | Чужой/неизвестный UUID, public_id вместо UUID, новый доступ к tombstone |
| 405 | method_not_allowed | Метод не поддерживается; Allow перечисляет разрешённые методы |
| 409 | idempotency_conflict | Ключ использован с другим значимым payload |
| 412 | revision_conflict | Новый PATCH использует устаревшую revision |
| 500 | internal_error | Ошибка хранения, обязательного аудита или cleanup; транзакция откатывается |

## Диалоги и сообщения в кабинете

| Метод и путь | Результат |
| --- | --- |
| POST `/api/v1/agents/{id}/conversations` | 201, созданный или ранее сохранённый тестовый диалог |
| GET `/api/v1/agents/{id}/conversations` | 200, страница диалогов текущего пользователя |
| POST `/api/v1/agents/{id}/test-chat/messages` | 202 для принятого запроса, 200 при повторе с сохранённым ответом |
| GET `/api/v1/conversations/{id}/messages` | 200, страница сообщений своего диалога |

Все маршруты используют сессию кабинета. Для POST передаются Origin,
X-CSRF-Token и Idempotency-Key. Создание диалога не принимает тело запроса.
Отправка сообщения принимает JSON с `conversation_id`, `message` и
`stream: false`. Повтор с исходным ключом и текстом возвращает ту же операцию.
При изменении текста или диалога в пределах того же ключа сервер отвечает 409.
Чужой или недоступный ресурс возвращает 404.

Страницы истории сортируются от новых записей к старым. Используются `limit`
от 1 до 100 (по умолчанию 20) и курсор `next_cursor` из предыдущей страницы.
Курсор привязан к пользователю, компании и ресурсу. Текущий API сохраняет запрос
со статусом `accepted`. Запуск генерации и учёт обращений подключаются отдельно.

### Маршруты на прокси

Правило для префикса `/api/v1/agents/` уже охватывает маршруты агента.
Для чтения сообщений в существующем HTTPS server Nginx добавить правило
без замены исходного URI:

```nginx
location ^~ /api/v1/conversations/ {
    proxy_pass http://faq_api;
    proxy_set_header Host $host;
}
```

Сохранить Cookie, Origin, X-CSRF-Token и Idempotency-Key. Перед применением
проверить конфигурацию прокси командой `nginx -t`.
