# Накат миграций

API не создаёт таблицы при старте. Изменения схемы применяет отдельная программа
`cmd/migrate` на golang-migrate v4. В [каталоге SQL](../backend/migrations)
находятся пары `000001_identity`, `000002_audit` и `000006_trial_bootstrap` (`up.sql`/`down.sql`).
Первая создаёт `users`, `accounts`, `account_members`, `sessions`, вторая —
`audit_events` с ограничениями и индексами. Последняя добавляет намерения пробного начисления
`trial_bootstrap_intents`. Текущая версия этого каталога — 6.
Номер 6 выбран после уже подготовленных отдельных изменений схемы 3–5.
Перед установкой согласовать полный состав SQL-файлов поставки: runner не применяет
файлы с номером ниже уже установленной версии. Если поставка включает версии 3–5,
они должны быть в каталоге до первого применения версии 6. Не добавлять их
задним числом и не переименовывать уже применённые миграции.
Текущий каталог 1/2/6 предназначен для отдельной разработки. Установку общего
выпуска остановить до включения согласованных версий 3–5; назначение номера
не заменяет их интеграцию. Для локальной проверки допустима отдельная тестовая БД.
Подготовка БД — в [DATABASE.md](DATABASE.md), обновление —
в [карточке 0.0.2](upgrade_cluster/release_0.0.2.md) только для исходного снимка
этого выпуска. Версия выпуска с подготовкой trial ещё не назначена;
описанный здесь порядок не означает разрешение установки несогласованного каталога.

## Окружение и подключение

Нужны доступная PostgreSQL и `DATABASE_URL` роли migrator. `MIGRATIONS_PATH`
необязателен: по умолчанию `./migrations` относительно рабочего каталога,
в Docker-образе `/migrations`. `.env` автоматически не загружается.
Все настройки и способы передачи — в [BACKEND.md](BACKEND.md#переменные-окружения).

Мигратор читает общий `config.Load`: неверные HTTP-параметры и частичный Auth
останавливают его запуск. В staging/production migrator не требует Supabase,
если все четыре Auth-переменные отсутствуют. Даже отдельная
`AUTH_ALLOW_INSECURE_HTTP=false` включает проверку полного Auth-набора.
Для подключения нужен endpoint, сохраняющий сессию: runner использует отдельное
соединение и advisory lock. Transaction/statement pooling отдельно не проверялся.

Администратор создаёт БД и роли штатным способом. Пароли в SQL не помещаются.
Примеры ниже рассчитаны на выделенную БД приложения и схему `public`.
Не использовать служебную схему `auth` Supabase. API и migrator должны видеть
одни таблицы через одинаковый `search_path`. При существующей схеме сначала
проверить историю и владельцев; не накатывать версию 1 поверх более новой версии.

## Роли и права

Обе роли имеют `LOGIN`, сетевой доступ и `CONNECT` к БД. Migrator нужны
`USAGE, CREATE` схемы и владение созданными таблицами, включая `schema_migrations`.
Суперпользователь, создание других ролей и БД приложению не требуются.

В `psql`, подключённом администратором к целевой БД, выполнить следующий SQL.
Заменить имена существующих ролей в `\set`; `DBNAME` — переменная самого psql:

```sql
\set ON_ERROR_STOP on
\set migrator_role REPLACE_MIGRATOR_ROLE
\set api_role REPLACE_API_ROLE
GRANT CONNECT ON DATABASE :"DBNAME" TO :"migrator_role", :"api_role";
GRANT USAGE, CREATE ON SCHEMA public TO :"migrator_role";
GRANT USAGE ON SCHEMA public TO :"api_role";
ALTER ROLE :"migrator_role" IN DATABASE :"DBNAME" SET search_path = public;
ALTER ROLE :"api_role" IN DATABASE :"DBNAME" SET search_path = public;
```

Проверить, что API не получает `CREATE` через PUBLIC или членство в других ролях.
Узкие GRANT не отменяют уже существующие привилегии. Если таблицы созданы раньше
другим владельцем, права и владение согласовать до миграции.

## Проверка перед накатом

Подключиться к целевой БД как migrator из любого каталога:

```sh
psql "$MIGRATOR_DATABASE_URL" -v ON_ERROR_STOP=1
```

В psql сначала выполнить:

```sql
SELECT to_regclass('public.schema_migrations') AS history;
SELECT tablename, tableowner FROM pg_tables
WHERE schemaname='public'
  AND tablename IN ('schema_migrations','users','accounts','account_members','sessions','audit_events','trial_bootstrap_intents')
ORDER BY tablename;
```

Если history равен NULL и таблиц приложения нет, это новая схема.
Если таблицы приложения уже есть без history, остановиться для сверки, не запускать
runner поверх них. Если history не NULL, отдельно выполнить:

```sql
SELECT version, dirty FROM public.schema_migrations;
```

Версии 1 и 2 с dirty=false допускают обновление до 6 после проверки состава
поставки; версия 6 с dirty=false — повторный запуск того же каталога. Версии 3–5
требуют сверки исторических файлов с согласованной поставкой.
Dirty=true или версия выше 6 требуют остановки и проверки
по разделу ошибок. Пустая history допускается только при отсутствии таблиц
приложения. Существующую схему сверить с применёнными миграциями до продолжения.

## Резервная копия

Перед изменением существующей БД сохранить резервную копию вне Git. Версия
`pg_dump` должна быть не старше сервера; сверить `pg_dump --version` и
`SHOW server_version;`. У роли копирования должен быть доступ ко всем таблицам.
Пример из корня репозитория, с заранее экспортированным `MIGRATOR_DATABASE_URL`
или DSN отдельной backup-роли:

```sh
set -e
umask 077
backup_dir=$(mktemp -d ../faqbot-before-migration.XXXXXX)
pg_dump --format=custom --file="$backup_dir/database.dump" "$MIGRATOR_DATABASE_URL"
pg_restore --list "$backup_dir/database.dump" > "$backup_dir/contents.txt"
printf 'Резервная копия: %s\n' "$backup_dir/database.dump"
```

`pg_restore --list` проверяет читаемость архива, но не заменяет пробное
восстановление в отдельную БД. Для первой установки сохранить параметры БД,
ролей и исходный конфиг прокси. `MIGRATOR_DATABASE_URL` — имя переменной примера,
приложение его не читает: runner принимает только `DATABASE_URL`.

## Запуск runner

Прямой запуск из корня репозитория, с Go 1.24+ и заранее экспортированным
`MIGRATOR_DATABASE_URL`. Подстановка URL должна содержать роль migrator,
percent-encoded пароль и согласованные параметры TLS:

```sh
cd backend
env -u SUPABASE_URL -u SUPABASE_KEY -u AUTH_ORIGIN -u AUTH_ALLOW_INSECURE_HTTP \
  APP_ENV=production DATABASE_URL="$MIGRATOR_DATABASE_URL" \
  MIGRATIONS_PATH=./migrations go run ./cmd/migrate
```

Ожидаются exit code 0 и JSON-лог `migrations are up to date`.
Runner вызывает `Up()` и применяет все ожидающие миграции из указанного каталога,
а не одну выбранную версию. В текущем каталоге на пустой БД это 000001, 000002
и 000006, на версии 1 — 000002 и 000006, на версии 2 — 000006, на чистой
версии 6 — отсутствие изменений. Дополнительные версии согласованной поставки
тоже выполняются в порядке номеров. Не подменять каталог неполным
набором SQL ради выбора одной миграции. Повторный запуск
той же команды допустим: уже применённая миграция повторно не выполняется.

Для уже собранного образа из любого каталога на Docker-хосте заранее задать
`IMAGE_ID` (ID выбранного образа) и `MIGRATOR_ENV_FILE` (абсолютный путь к
защищённому env-файлу мигратора):

```sh
docker run --rm --env-file "$MIGRATOR_ENV_FILE" \
  --entrypoint /migrate "$IMAGE_ID"
```

Образ должен быть собран заранее; опубликованный образ не предполагается.
Env-файл мигратора содержит
`APP_ENV=production`, `DATABASE_URL` роли migrator и `MIGRATIONS_PATH=/migrations`,
без Auth-переменных. Не запускать runner в каждой реплике API.
API разворачивается после успешной миграции и выдачи прав.

`127.0.0.1` внутри контейнера указывает на него самого. При отдельной Docker-сети
добавить `--network` с её именем. CA-файл можно подключить read-only аргументом
`--mount type=bind,src=/REPLACE_HOST_CA_FILE,dst=/run/db-ca.pem,readonly`
и добавить `sslrootcert=/run/db-ca.pem` в контейнерный DSN.
Файл должен быть читаем пользователем образа `65532:65532`, например иметь режим
644 для публичного CA без приватного ключа. Хостовые `psql`/`pg_dump` используют
доступные с хоста адрес и путь CA; контейнерный DSN может отличаться.

## Проверка результата

Из любого каталога с psql и `MIGRATOR_DATABASE_URL` той же БД:

```sh
psql "$MIGRATOR_DATABASE_URL" -v ON_ERROR_STOP=1
```

В открытом psql выполнить:

```sql
SELECT version, dirty FROM public.schema_migrations;
SELECT name, to_regclass('public.' || name) AS actual_table
FROM (VALUES ('users'), ('accounts'), ('account_members'), ('sessions'), ('audit_events'), ('trial_bootstrap_intents')) AS t(name);
SELECT to_regclass('public.sessions_expiry_idx') AS expiry_index;
SELECT indexname, indexdef FROM pg_indexes
WHERE schemaname='public' AND tablename='audit_events' ORDER BY indexname;
SELECT conname, contype, convalidated, pg_get_constraintdef(oid)
FROM pg_constraint WHERE conrelid='public.audit_events'::regclass
ORDER BY conname;
SELECT tablename, tableowner FROM pg_tables
WHERE schemaname='public'
  AND tablename IN ('schema_migrations','users','accounts','account_members','sessions','audit_events','trial_bootstrap_intents')
ORDER BY tablename;
```

Ожидаются одна строка `version=6, dirty=false`, шесть ненулевых `actual_table`,
индекс сессий, шесть индексов аудита (PK, unique ключ и четыре поисковых)
и владелец-migrator. Все ограничения должны быть валидны и соответствовать
[000002 up](../backend/migrations/000002_audit.up.sql): PK/unique, FK компании,
CHECK scope/account/actor/entity/outcome, корреляционных полей, source и JSONB.
SQL проверяет форму action/entity, но не весь прикладной каталог и whitelist
metadata из [AUDIT.md](AUDIT.md). На новой БД таблицы данных
пусты. После повторного runner версия и `dirty` остаются такими же.
Наличие таблиц само по себе не подтверждает историю и права API.
Для `trial_bootstrap_intents` сверить PK по account_id, FK на accounts,
NOT NULL для всех полей и CHECK единственного состояния pending с
[000006 up](../backend/migrations/000006_trial_bootstrap.up.sql).
Обновление оставляет таблицу пустой: старым компаниям trial автоматически не назначается.

## Права API после миграции

В psql, подключённом владельцем таблиц или администратором к той же БД:

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
COMMIT;
```

API не нужны DDL-права, доступ к `schema_migrations` и sequences; UUID создаёт Go.
SELECT для `accounts` можно не выдавать при строго минимальной политике:
текущий код только вставляет в неё строки. После миграции другой ролью API
не получает табличные права автоматически. SELECT на audit_events нужен для
сравнения повторов. Изменение журнала ограничено INSERT; это не запрещает
доверенной серверной роли чтение таблицы. HTTP API чтения и tenant RBAC журнала
пока отсутствуют. Проверить, что UPDATE/DELETE/TRUNCATE не унаследованы от других
ролей или PUBLIC; точечный REVOKE не отменяет унаследованные права.

Подключиться как API из любого каталога с `API_DATABASE_URL`:

```sh
psql "$API_DATABASE_URL" -v ON_ERROR_STOP=1
```

В psql проверить:

```sql
SELECT current_user, current_database(), current_schema();
SELECT
  has_schema_privilege(current_user,'public','USAGE') AS schema_usage,
  (has_table_privilege(current_user,'public.users','SELECT')
   AND has_table_privilege(current_user,'public.users','INSERT')) AS users_access,
  has_table_privilege(current_user,'public.accounts','INSERT') AS accounts_access,
  (has_table_privilege(current_user,'public.account_members','SELECT')
   AND has_table_privilege(current_user,'public.account_members','INSERT')) AS members_access,
  (has_table_privilege(current_user,'public.sessions','SELECT')
   AND has_table_privilege(current_user,'public.sessions','INSERT')
   AND has_table_privilege(current_user,'public.sessions','UPDATE')
   AND has_table_privilege(current_user,'public.sessions','DELETE')) AS sessions_access,
  (has_table_privilege(current_user,'public.audit_events','SELECT')
   AND has_table_privilege(current_user,'public.audit_events','INSERT')) AS audit_access,
  has_table_privilege(current_user,'public.trial_bootstrap_intents','INSERT') AS trial_insert,
  has_table_privilege(current_user,'public.audit_events','UPDATE') AS audit_update,
  has_table_privilege(current_user,'public.audit_events','DELETE') AS audit_delete,
  has_table_privilege(current_user,'public.audit_events','TRUNCATE') AS audit_truncate,
  has_table_privilege(current_user,'public.trial_bootstrap_intents','UPDATE') AS trial_update,
  has_table_privilege(current_user,'public.trial_bootstrap_intents','DELETE') AS trial_delete,
  has_table_privilege(current_user,'public.trial_bootstrap_intents','TRUNCATE') AS trial_truncate;
SELECT rolsuper, rolcreatedb, rolcreaterole, rolbypassrls
FROM pg_roles WHERE rolname=current_user;
SELECT has_schema_privilege(current_user,'public','CREATE') AS schema_create;
SELECT tablename FROM pg_tables
WHERE schemaname='public' AND tableowner=current_user;
```

Ожидаются роль API, целевая БД, схема `public`, семь положительных проверок
доступа, audit_update/audit_delete/audit_truncate=false и
trial_update/trial_delete/trial_truncate=false, четыре административных флага `false`, `schema_create=false` и отсутствие таблиц
во владении API. При другом результате администратор проверяет унаследованные
привилегии и роли. Эти запросы не меняют grants и не заменяют пробный вход:
RLS и дополнительные ограничения существующей БД могут влиять на запросы.

Seed-данные не требуются, в том числе для `audit_events`. Не вставлять
фиктивные события в рабочий журнал для проверки установки. При первом успешном входе/регистрации приложение
создаёт пользователя, компанию, членство `owner` с `audit_read=false` и одно
намерение trial в одной транзакции. Сессии создаёт backend; пароли и пользователей Supabase вручную
в SQL не вставлять. Повторный вход того же subject сохраняет компанию.

## Проверка данных авторизации

После входа нового тестового пользователя и logout из любого каталога подключиться
под migrator/диагностической ролью. UUID взять из identity ответа входа:

```sh
psql "$MIGRATOR_DATABASE_URL" -v ON_ERROR_STOP=1 \
  -v user_id=REPLACE_USER_UUID -v account_id=REPLACE_ACCOUNT_UUID
```

В psql выполнить:

```sql
SELECT u.id AS user_id, a.id AS account_id, m.role, m.audit_read
FROM public.users u
JOIN public.account_members m ON m.user_id=u.id
JOIN public.accounts a ON a.id=m.account_id
WHERE u.id=:'user_id'::uuid AND a.id=:'account_id'::uuid;
SELECT count(*) AS active_sessions
FROM public.sessions
WHERE user_id=:'user_id'::uuid AND account_id=:'account_id'::uuid
  AND revoked_at IS NULL AND expires_at>now() AND idle_expires_at>now();
```

Ожидается одна строка членства; для нового subject — owner и audit_read=false.
Сразу после logout в изолированном тесте без других входов active_sessions=0.
Отозванная строка может остаться до следующей выдачи сессии. Проверка HTTP 401
только после удаления cookie сама по себе не подтверждает отзыв серверной сессии.

## Если произошла ошибка

Ожидание блокировки ограничено 5s, выполнение SQL — 30s. SIGINT/SIGTERM
останавливает обработку перед следующим файлом; текущий SQL может выполняться
до таймаута. `dirty=true` означает незавершённое применение миграции.

Остановить дальнейшую установку, сохранить копию, проверить фактическую схему
и выяснить, какие команды успели выполниться. Не запускать up.sql напрямую
через psql, не менять версию наугад и не сбрасывать dirty автоматически.
После проверки можно убрать частичные изменения и повторить миграцию либо
завершить изменение вручную. Для исправления номера версии нужен внешний
CLI golang-migrate v4 с драйверами file/postgres: встроенный runner не имеет
команды `force`. Номер выбирается по реальной схеме, затем снова выполняются
runner и проверки версии/dirty.

На новой БД пустой каталог даёт пустую историю версий. На БД с применёнными
миграциями отсутствие исторических файлов считается ошибкой.

## Новые миграции и откат

Имя файла содержит номер, название и `.up.sql`/`.down.sql`. Следующая миграция
получает новый номер; уже применённые SQL-файлы не меняются. Во всех парах
используются явные `BEGIN`/`COMMIT`. Операции вне транзакций, например
`CREATE INDEX CONCURRENTLY`, требуют отдельного файла и плана восстановления.

Встроенный runner выполняет только `up`. Down версии 6 удаляет
`trial_bootstrap_intents` и теряет сохранённые намерения. Перед откатом остановить
новый API и сохранить копию. Обычный откат приложения сохраняет схему и намерения;
предыдущий API их не обрабатывает. На время отката отключить новые регистрации:
старый API создаёт компании без намерений, и последующее обновление не
добавит их для уже известных subject. Если такие регистрации всё же произошли,
нужна отдельная согласованная сверка eligibility и восстановление намерений;
автоматической процедуры восстановления сейчас нет. При повторном up потерянные намерения не
восстанавливаются: bootstrap не создаёт их для старого subject. Поэтому down/up
не является безопасным способом отката данных.
Down версии 2 удаляет только
`audit_events` с журналом; down версии 1 — четыре таблицы авторизации.
Прямое исполнение down через psql не меняет `schema_migrations` и оставит
историю несогласованной. Откат приложения
обычно сохраняет БД; для первой установки можно остановить API и убрать его
маршрут из прокси, оставив данные. Разрушительный откат схемы требует отдельного
решения, остановки всех писателей, резервной копии и внешнего golang-migrate
для `down 1` либо восстановления копии. Восстановление теряет записи после
создания копии и не откатывает пользователей Supabase.
