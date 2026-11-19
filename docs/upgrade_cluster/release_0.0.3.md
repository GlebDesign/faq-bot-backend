# Release 0.0.3 — Защищённое управление агентами

Карточка описывает установку схемы 6 и защищённого CRUD агентов. Предыдущие
карточки [0.0.1](release_0.0.1.md) и [0.0.2](release_0.0.2.md) сохраняют
исторические исходные снимки и ограничения.

## Задача

Добавить управление агентами текущей компании с проверкой прав, защитой от
повторного выполнения запросов и транзакционным аудитом изменений.

## Группа разработки

- Даниил — backend: API, данные, миграции и контракт.
- Сергей — DevOps и тимлид: параметры стенда, контейнеры, Nginx и установка.

Параметры сервера, БД, сети и внешнего адреса не предоставлены. Согласованная
конфигурация стенда и результаты потребительской приёмки отсутствуют.

## Описание релиза

1. Создание, список, чтение, изменение и логическое удаление агентов.
2. Scope из действующей сессии и текущего членства компании; CRUD доступен
   owner/admin. UUID, public_id и account_id клиента не предоставляют права.
3. Строгая валидация JSON, HMAC pagination cursor, ETag/If-Match и revisions.
4. Идемпотентные mutations: повтор возвращает сохранённый результат, другой
   payload с тем же ключом отвергается; конкурентные изменения защищены CAS.
5. Общая транзакция изменения агента, обязательного аудита и replay result.
   При удалении она также сохраняет tombstone и одну pending-заявку очистки.
6. Схемы agents, scoped conversations/messages/message_sources/ai_requests,
   agent_operations и agent_cleanup_requests. Таблицы и repository replay
   не означают работающий ChatService, виджет или отправку запросов AI.

Авторизация и локальные сессии включены ранее, внутренний журнал аудита — в
0.0.2. Этот выпуск подключает аудит к CRUD. Регистрация и подготовка одного
pending-намерения trial фактически присутствуют в общем исходном снимке как
зависимости доступа; реальное начисление обращений не подключено. Миграция 6
не назначает trial существующим компаниям.

Worker и физическая очистка отсутствуют. Отказы доступа не записываются
отдельной интеграцией аудита; Auth не вызывает audit writer. Запрет работы
удалённого агента в чате не подтверждён: chat/public session/search ещё нет.
Реализованный CRUD проверен локально; полная продуктовая приёмка не завершена.

Файл с версиями сервисов: не используется. Файл с версиями ролей: не используется.
Значения owner/admin и audit_read хранятся в членстве без отдельной версии.

## Порядок установки

Обновление 0.0.2 предполагает схему 2 с dirty=false. Порядок: проверить исходную
схему и доступы; сделать и проверить backup; подготовить настройки; сохранить
и остановить прежний API; выполнить отдельный migrator выбранного образа;
выдать права API; запустить новый API; настроить прокси и проверить установку.
Дополнительный SQL для данных и seed-записи не требуются.

### Выбор исходников и образа

В образ входят `/api`, `/migrate` и все миграции 1–6; пользователь 65532:65532,
по умолчанию APP_ENV=production. Нужен PostgreSQL 15+; прежняя локальная проверка
выполнена на 18.3. API и migrator используют одну БД с разными ограниченными ролями.

Из корня репозитория проверить выбранный снимок и локальные изменения:

```sh
git rev-parse HEAD
git status --short
git diff -- backend contracts
```

База подготовки карточки — `9dc8cf9b6cc63a013174332f67c1cbb3681a4822`.
Backend и contracts побайтно совпадают с проверенным снимком из раздела Diff.
Новый HEAD вместе с локальным diff необходимо фиксировать отдельно: один HEAD
не описывает незакоммиченные файлы. Сборка из произвольной будущей dev может
содержать другие миграции и не считается поставкой этой версии.

В том же терминале из корня:

```sh
docker build -f backend/Dockerfile -t faq-api:0.0.3-local . && \
  IMAGE_ID=$(docker image inspect --format '{{.Id}}' faq-api:0.0.3-local) && \
  docker image inspect --format '{{.Id}}' "$IMAGE_ID"
```

При ошибке остановиться, старый тег или значение IMAGE_ID не использовать.
Зафиксировать фактический ID успешной сборки и использовать его для API и
migrator. Это предложенный локальный тег, публикация в registry не подтверждена.
Базовый образ подвижный, повторная сборка может получить другой ID; прежние
результаты не являются проверкой нового образа.

### Подготовка

1. Проверить версию и dirty, фактическую схему, владельцев и историю SQL по
   [MIGRATIONS.md](../MIGRATIONS.md#проверка-перед-накатом). Поддерживаемые здесь
   исходные состояния — пустая БД без таблиц приложения либо полная схема 2
   с dirty=false. Если версия 6 ранее применялась без миграций 3–5, обычный
   повтор не исправит схему: требуется отдельный план восстановления.
2. Для существующей БД сделать [резервную копию](../MIGRATIONS.md#резервная-копия)
   и проверить восстановление в другую БД. Проверка релиза не заменяет backup
   конкретного стенда.
3. Подготовить [роли и права схемы](../MIGRATIONS.md#роли-и-права).
   Migrator владеет таблицами; API не владеет ими и не получает CREATE либо
   административных прав, в том числе через PUBLIC и членство.
4. Подготовить два защищённых env-файла вне Git: для migrator и API.
   Пути и адреса должны быть доступны с Docker-хоста/из контейнера.

Migrator env содержит `APP_ENV=production`, `DATABASE_URL` роли migrator и
при необходимости `MIGRATIONS_PATH=/migrations`. Все Auth-переменные следует
убрать из этого файла: частичный набор блокирует общий config.Load.
API env содержит `APP_ENV=production`, `DATABASE_URL` роли API, `SUPABASE_URL`,
`SUPABASE_KEY`, `AUTH_ORIGIN` и `AGENT_CURSOR_KEY`. Полные ограничения —
в [BACKEND.md](../BACKEND.md#переменные-окружения).

`AGENT_CURSOR_KEY` — отдельный секрет минимум 32 байта, одинаковый у всех реплик
и после перезапуска. Его ротация делает старые cursors недействительными.
`SUPABASE_URL` и `AUTH_ORIGIN` должны использовать HTTPS;
`AUTH_ALLOW_INSECURE_HTTP` в production не включается.
`.env` автоматически не читается. API сам не обслуживает TLS: HTTPS завершается
на внешнем прокси;
к API внутри контейнера прокси обращается по HTTP. Для PostgreSQL выбрать
TLS-параметры DSN и при необходимости [подключить CA](../MIGRATIONS.md#запуск-runner).
`127.0.0.1` внутри контейнера означает сам контейнер: для БД использовать адрес
общей Docker-сети либо доступный внешний адрес.

### Мигратор и права API

Для обновления сначала сохранить имя и ID прежнего API и штатно остановить
его, освободив порт. Пример для прежнего контейнера `faq-api`:

```sh
OLD_API_CONTAINER=faq-api
OLD_IMAGE_ID=$(docker inspect --format '{{.Image}}' "$OLD_API_CONTAINER") && \
  docker stop --timeout 75 "$OLD_API_CONTAINER"
```

Сохранить значения `OLD_API_CONTAINER` и `OLD_IMAGE_ID` вне временного терминала.
Ожидание Docker 75 секунд превышает максимально допустимый
`HTTP_SHUTDOWN_TIMEOUT=1m` на 15 секунд и даёт приложению завершить активные
запросы до принудительной остановки. Прежний контейнер оставить остановленным
для возможного отката; не удалять его.
При первой установке без прежнего API этот шаг пропускается.

Из любого каталога на Docker-хосте в том же терминале использовать `IMAGE_ID`,
полученный после успешной сборки выше. `MIGRATOR_ENV_FILE` — абсолютный путь
к env-файлу migrator. Приведённый в начале карточки исторический ID не
перезаписывает результат новой сборки:

```sh
docker run --rm --env-file "$MIGRATOR_ENV_FILE" --entrypoint /migrate "$IMAGE_ID"
```

Если БД в отдельной Docker-сети, добавить `--network` с её именем.
Runner применяет весь штатный каталог: empty→6 или 2→6 без сброса истории.
Не изменять применённые миграции и не подменять каталог ради выбора версии.
Ожидаются exit 0 и `migrations are up to date`. Проверить в psql целевой БД:

```sql
SELECT version, dirty FROM public.schema_migrations;
```

Ожидается одна строка `6, false`. Дополнительно сверить все таблицы, constraints
и владельцев по [проверке результата](../MIGRATIONS.md#проверка-результата).
Повторить ту же команду migrator: версия и dirty должны остаться прежними.
При 2→6 прежние identity и audit сохраняются; trial старым компаниям
автоматически не назначается. Новая регистрация сохраняет только pending intent,
реальное начисление trial пока не подключено.

Подключившись владельцем таблиц или администратором к этой БД, выполнить
актуальные [права API](../MIGRATIONS.md#права-api-после-миграции):

```sql
\set ON_ERROR_STOP on
\set api_role REPLACE_API_ROLE
BEGIN;
GRANT SELECT, INSERT ON public.users, public.accounts, public.account_members TO :"api_role";
GRANT SELECT, INSERT, UPDATE, DELETE ON public.sessions TO :"api_role";
REVOKE UPDATE, DELETE, TRUNCATE ON public.audit_events FROM :"api_role";
GRANT SELECT, INSERT ON public.audit_events TO :"api_role";
REVOKE UPDATE, DELETE, TRUNCATE ON public.trial_bootstrap_intents FROM :"api_role";
GRANT INSERT ON public.trial_bootstrap_intents TO :"api_role";
GRANT SELECT, INSERT, UPDATE ON public.agents, public.agent_operations TO :"api_role";
GRANT INSERT ON public.agent_cleanup_requests TO :"api_role";
COMMIT;
```

Заменить `REPLACE_API_ROLE` именем созданной роли. REVOKE не отменяет права,
полученные через другие роли или PUBLIC. API не нужны права на таблицы чата,
DDL, schema_migrations или UPDATE/DELETE/TRUNCATE журнала.

### Запуск API и внешний доступ

Задать `API_ENV_FILE` абсолютным путём к env-файлу API, сохранив `IMAGE_ID`.
Из любого каталога Docker-хоста:

```sh
API_CONTAINER=faq-api-release-0.0.3
docker run -d --name "$API_CONTAINER" --env-file "$API_ENV_FILE" \
  -p 127.0.0.1:8080:8080 "$IMAGE_ID" && \
  docker inspect --format '{{.Image}}' "$API_CONTAINER"
curl --fail-with-body --retry 20 --retry-connrefused --retry-delay 1 \
  --retry-max-time 60 --connect-timeout 2 --max-time 3 \
  http://127.0.0.1:8080/health/ready
docker logs --tail 100 "$API_CONTAINER"
```

Выведенный `.Image` должен совпасть с выбранным `IMAGE_ID`. Для нового запуска
выбрать свободное имя `API_CONTAINER`; прежний контейнер остаётся остановленным.
При необходимости добавить ту же Docker-сеть, что использует БД.
Повторы ready ограничены 60 секундами и 20 попытками, отдельный запрос — 3
секундами. HTTP 503 и отказ соединения вызывают повтор; исчерпание повторов
возвращает ненулевой exit code curl. При неуспехе не продолжать приёмку:
проверить состояние контейнера и логи. Health 200 не подтверждает схему,
DML-права, внешний HTTPS или Supabase.
Проверить регистрацию/login/session/logout через внешний HTTPS по
[AUTH.md](../AUTH.md#проверка-после-установки), включая cookie Secure,
Origin и CSRF. Настройки почты проверяются отдельно в настоящем Supabase.


На внешнем HTTPS-прокси направить без изменения URI коллекцию `/api/v1/agents`
и объекты `/api/v1/agents/{id}` на тот же API. Сохранить Cookie, Set-Cookie,
Origin, X-CSRF-Token, Idempotency-Key и If-Match; в ответах передавать ETag и
Cache-Control. Клиент и API используют общий origin из AUTH_ORIGIN.
Конфигурация стенда не предоставлена. Следующий фрагмент Nginx — пример для
существующего HTTPS server с настроенным TLS и upstream `faq_api`:

```nginx
location = /api/v1/agents {
    proxy_pass http://faq_api;
    proxy_set_header Host $host;
}
location ^~ /api/v1/agents/ {
    proxy_pass http://faq_api;
    proxy_set_header Host $host;
}
```

У `proxy_pass` нет URI-части, поэтому исходный путь сохраняется. Nginx по
умолчанию передаёт перечисленные request headers и Set-Cookie/ETag; проверить,
что общая конфигурация не переопределяет и не скрывает их. Auth-маршрут
`/api/v1/auth/` сохраняется. После адаптации примера на хосте Nginx из каталога
конфигурации выполнить `sudo nginx -t && sudo nginx -s reload`; ожидается
успешная проверка и reload. TLS-прокси не входит в Docker-образ API.

Маршруты и форматы CRUD — в [HTTP.md](../HTTP.md#crud-агентов).
Нужно проверить два tenant и два агента одного tenant: список/get/create,
PATCH с ETag/If-Match, чужой UUID 404, отсутствие клиентского выбора scope,
один результат на тот же Idempotency-Key и payload, конфликт payload 409,
устаревшую revision 412 и конкурентный CAS с одним победителем.
Ошибка обязательного аудита должна откатывать mutation и operation.

DELETE возвращает 202 Job после tombstone, audit и pending cleanup request
в одной транзакции. GET/list и новые изменения скрывают tombstone. Ранее
успешные POST/PATCH/DELETE с исходными ключом и payload возвращают сохранённый
результат после текущей проверки сессии/membership/роли; PATCH требует исходный
If-Match. Отозванная сессия или membership получает 401 и при replay.
Worker, физическая очистка и `GET /api/v1/jobs/{id}` отсутствуют; pending
не подтверждает очистку. Запрет чата удалённому агенту требует отдельной
проверки после подключения chat/public session/search.

Проверка отказа БД должна различать ready 503 и live/startup 200, затем
восстановление сессии после возвращения БД. Проверить сохранность данных и
cursor после перезапуска с прежним `AGENT_CURSOR_KEY`. Для штатной остановки
использовать `docker stop --timeout 75 "$API_CONTAINER"`: это даёт 15 секунд
запаса сверх максимально допустимого `HTTP_SHUTDOWN_TIMEOUT=1m`.


## Порядок отката релиза

1. **Приложение.** Сохранить диагностику и закрыть изменения на прокси.
   На Docker-хосте из любого каталога остановить новый API:

   ```sh
   docker stop --timeout 75 "$API_CONTAINER"
   ```

   Совместимость предыдущего API с полной схемой 6 в этой работе не проверена.
   До возврата проверить сохранённый OLD_IMAGE_ID с копией обновлённой БД и
   прежними настройками. Только после успешной проверки запустить сохранённый
   контейнер `docker start "$OLD_API_CONTAINER"`, проверить health и Auth и
   переключить upstream. При отсутствии совместимого образа API остаётся
   остановленным. Прежний smoke на схеме 2 не доказывает совместимость со схемой 6.
2. **Конфигурация.** Вернуть сохранённый upstream, env и CA только при фактическом
   изменении. Прежний API может не использовать AGENT_CURSOR_KEY; не отзывать
   общие секреты, пока остаются использующие их реплики. После изменения Nginx
   выполнить `sudo nginx -t && sudo nginx -s reload`. Отзыв новых grants выполняется
   администратором после остановки писателей; он не удаляет данные.
3. **Схема.** Обычный откат приложения сохраняет таблицы и данные. Down 6 удаляет
   trial-намерения; down 5 — replay results и cleanup requests; down 4 — requests,
   sources, messages и conversations; down 3 — агентов. Возврат 6→2 уничтожает
   эти данные и требует отдельного решения, backup и проверки восстановления.
   Уже применённые SQL не редактировать; историю version/dirty не сбрасывать.
   Встроенный runner поддерживает только Up. Порядок destructive down —
   в [MIGRATIONS.md](../MIGRATIONS.md#новые-миграции-и-откат), не как первый шаг.
   Восстановление backup теряет изменения после копии и не откатывает Supabase.
   При возврате старого приложения регистрацию нужно закрыть до проверки:
   прежний API может создать компанию без trial intent; последующий Up не
   выполняет backfill.

## Новые БС-ы

Не добавляются. Agents и AuditService — внутренние компоненты существующего
Go backend. Отдельные ChatService, worker и AI-сервис этим релизом не разворачиваются.

## Новые/изменённые эндпоинты

| № | Метод, путь | Query | Описание | Доступ (хэдеры, роли) |
| --- | --- | --- | --- | --- |
| 1 | POST `/api/v1/agents` | Не используются | Создать агента, 201 и ETag | Cookie faqbot_session, текущий owner/admin; точные Origin и X-CSRF-Token, Idempotency-Key, JSON |
| 2 | GET `/api/v1/agents` | limit 1..100, default 20; cursor | Список живых агентов компании | Та же cookie и роль; Origin отсутствует либо AUTH_ORIGIN; Sec-Fetch-Site не cross-site |
| 3 | GET `/api/v1/agents/{id}` | Не используются | Агент и ETag; чужой UUID/tombstone 404 | Те же требования GET; public_id не заменяет UUID |
| 4 | PATCH `/api/v1/agents/{id}` | Не используются | Изменить три поля; 200 и ETag | Требования POST плюс quoted If-Match; stale revision 412 |
| 5 | DELETE `/api/v1/agents/{id}` | Не используются | Tombstone и pending cleanup, 202 | Требования mutations; пустое тело; If-Match не требуется |

Sec-Fetch-Site: cross-site запрещён для всех пяти методов, включая mutations.
Auth и health маршруты сохраняются. Форматы JSON, ограничения, ошибки,
pagination и воспроизводимые curl-примеры — в [HTTP.md](../HTTP.md#crud-агентов).
Отзыв сессии или членства запрещает и replay. Новый GET/PATCH/DELETE скрывает
удалённый объект; повтор прежней успешной операции с исходным key/payload
возвращает исторический результат после повторной проверки прав.


## Новые и изменённые настройки

| Настройка | Тип | Обязательное (по умолчанию) | Пример | Описание |
| --- | --- | --- | --- | --- |
| AGENT_CURSOR_KEY | string/secret | Да при Auth, default отсутствует | `REPLACE_SHARED_SECRET_AT_LEAST_32_BYTES` | API, подпись cursor; минимум 32 байта, общий секрет всех реплик и перезапусков; ротация инвалидирует cursors |

Прежние APP_ENV, DATABASE_URL, MIGRATIONS_PATH, SUPABASE_URL/KEY, AUTH_ORIGIN,
HTTP-настройки и тестовые переменные описаны в
[BACKEND.md](../BACKEND.md#переменные-окружения). Новых env для worker/AI нет.

## Diff

Точный Git-снимок установленного 0.0.2 не предоставлен. Ревизия
`f858c8f314470033142d8b91f0dbbc5991a8f1cd` в прежней карточке — база ветки аудита,
а не полный снимок выпуска. Сам журнал уже относится к 0.0.2; изменения схемы
2→6 перечислены ниже. Полный diff с production нельзя подтвердить без его ревизии.
Подготовка этого документа выполняется на `9dc8cf9b6cc63a013174332f67c1cbb3681a4822`.
Сравнение backend/contracts между ним и проверенным ниже исходником не показало
изменений. Новая карточка пока незакоммичена и учитывается отдельно.

Исторически проверенный 18–19.11.2026 исходник:
`e3efc83a64b643ba8ea388daca0c31b1010f08b6`;
локальный тег `faq-api:release-0.0.3-check`;
ID `sha256:598f170c8ad4e0e3b5b7de13e5d4b657280e8f61848a7c8ee85d03fc3ea1ff5b`.
Опубликованный образ и релизный Git-тег не подтверждены. В этой доработке
документации сборка и runtime-тесты заново не выполнялись.

- Контракт и DTO: [openapi.yaml](../../contracts/openapi.yaml),
  [fixtures](../../contracts/fixtures), [handlers/dto](../../backend/internal/handlers/dto).
- API и логика: [handlers/agents.go](../../backend/internal/handlers/agents.go),
  [service/agents.go](../../backend/internal/service/agents.go),
  [scope.go](../../backend/internal/service/scope.go),
  [repository/agents.go](../../backend/internal/repository/agents.go).
- Аудит: [service/audit.go](../../backend/internal/service/audit.go),
  [repository/audit.go](../../backend/internal/repository/audit.go).
- Настройки/сборка: [config.go](../../backend/internal/config/config.go),
  [Dockerfile](../../backend/Dockerfile), [runner](../../backend/internal/migration/migration.go).
- SQL 3–6 перечислен в разделе Postgres; прежние миграции 1–2 сохраняются.


### Ранее выполненные проверки

Выполнены production-запуск через локальный TLS-прокси и
HTTPS-mock провайдера, сценарии Auth/trial и HTTP CRUD с SQL assertions.
Подтверждены изоляция tenant, Secure cookie, pagination/cursor, валидация,
replay/conflict, CAS, rollback аудита/cleanup, tombstone, отзыв membership/session,
сохранность после рестарта и SIGTERM, восстановление после недоступности БД.
Migrator образа прошёл empty→6, 2→6 и повтор с версией 6 clean; все 12 SQL-файлов
образа совпали с исходниками по SHA-256. При обновлении 2→6 тестовые
user/account/owner/session/audit сохранены: JSON пяти таблиц совпал побайтно,
trial backfill отсутствует. Пройдены 122 HTTP и 29 lifecycle assertions.

Из `backend` на отдельной PostgreSQL 18.3 с ограниченной ролью владельца тестовых
схем и `TEST_AUDIT_ROLE` выполнены `go test -count=1 -json ./...` и
`go test -race -count=1 -json ./...`: каждый по 1241 успешному тесту/подтесту,
без ошибок и пропусков. `go vet ./...`, `go build ./...` и
`golangci-lint run ./...` прошли; вывод `gofmt -l` пуст.
Down/up проверен SQL-тестами в одноразовых схемах; CLI `/migrate` выполняет Up.
Подтверждённых дефектов существующих функций не воспроизведено; Go и SQL не менялись.


Облачный Supabase, отсутствие исходящих писем, браузер и внешний HTTPS стенда
не подтверждены локальным TLS proxy/mock. Проверка CRUD tombstone не заменяет
проверку запрета чата. Потребительское согласование контрактов и полная
интеграционная приёмка остаются открытыми.

## Список обновляемых модулей

| Репозиторий | Модуль | Версия на проде | Новая версия | Примечания |
| --- | --- | --- | --- | --- |
| faq-bot-backend | backend: cmd/api, cmd/migrate, CRUD/аудит | Не предоставлено | 0.0.3 — обозначение карточки; точный снимок и образ в Diff | Один образ для API и migrator; публикация и установка не подтверждены |

Supabase/PostgreSQL — внешние зависимости, их обновление этой карточкой не задаётся.
Python AI и worker не разворачиваются.

## Postgres

| Репозиторий | Инстанс БД на проде | Название артефакта | Статус | Версия на проде | Предлагаемая версия | Артефакт для раскатки | Примечания |
| --- | --- | --- | --- | --- | --- | --- | --- |
| faq-bot-backend | Не предоставлено | 000003_agents | SQL в репозитории; стенд не подтверждён | Не предоставлено | Схема 3 | [up](../../backend/migrations/000003_agents.up.sql) | После 2; agents, scope/FK, public_id, revisions; down удаляет агентов |
| faq-bot-backend | Не предоставлено | 000004_conversations | То же | Не предоставлено | Схема 4 | [up](../../backend/migrations/000004_conversations.up.sql) | Scoped conversations/messages/message_sources/ai_requests; down удаляет историю и requests |
| faq-bot-backend | Не предоставлено | 000005_agent_operations | То же | Не предоставлено | Схема 5 | [up](../../backend/migrations/000005_agent_operations.up.sql) | Replay и cleanup requests; down теряет результаты/заявки |
| faq-bot-backend | Не предоставлено | 000006_trial_bootstrap | То же | Не предоставлено | Схема 6, dirty=false | [up](../../backend/migrations/000006_trial_bootstrap.up.sql) | Зависимость регистрации; pending intent, без backfill/начисления; down удаляет намерения |

Up применяет весь штатный каталог: при исходной схеме 2 — 3, 4, 5, 6;
на пустой БД сначала 1 и 2, затем 3–6. Seed не нужен. Перед установкой получить
адрес/порт и имя БД, роли и DSN, TLS/CA и сетевые параметры, внешний HTTPS-origin,
upstream Nginx, способ передачи образа/секретов, backup и процедуру восстановления.
Неизвестные параметры не заменяются значениями локального тестового окружения.
