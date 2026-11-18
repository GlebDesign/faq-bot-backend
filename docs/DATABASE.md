# PostgreSQL: подключение и данные

Backend хранит пользователей, компании, членство, сессии, аудит, агентов и scoped данные чата в PostgreSQL.
Поиск по знаниям локального AI CLI использует отдельный SQLite, к этой БД он не подключён.
Драйвер backend — pgx через стандартный `database/sql`.

## Локальная БД

Нужен PostgreSQL 15+ с доступом по сети или Unix socket: миграция 4 использует
`UNIQUE NULLS NOT DISTINCT`. SQL-тесты выполняются на PostgreSQL 18.3;
нижняя граница 15 определяется синтаксисом, отдельно на версии 15 не проверялась.
Для учебного запуска с установленным Docker из корня проекта:

```sh
docker run --name faq-postgres -e POSTGRES_USER=faq -e POSTGRES_PASSWORD=local-only -e POSTGRES_DB=faqbot -p 127.0.0.1:5432:5432 -v faq-postgres-data:/var/lib/postgresql -d postgres:18
docker exec faq-postgres pg_isready -U faq -d faqbot
```

Дождаться `accepting connections`. Пароль примера предназначен только локальной
БД; не использовать его на сервере. Данные сохраняются в именованном томе
`faq-postgres-data`, который переживает удаление контейнера. При повторном создании
контейнера подключать тот же том; переменные POSTGRES_* инициализируют только пустую БД.
Путь тома соответствует [официальному образу PostgreSQL 18](https://docs.docker.com/guides/postgresql/).
Повторный запуск существующего контейнера: `docker start faq-postgres` из любого каталога.
Существующую БД можно использовать вместо контейнера, указав собственный URL.

Из корня проекта перейти в `backend` и задать окружение:

```sh
cd backend
export DATABASE_URL='postgres://faq:local-only@127.0.0.1:5432/faqbot?sslmode=disable'
go run ./cmd/migrate
go run ./cmd/api
```

`sslmode=disable` подходит только для локальной БД. На стенде параметры TLS
задаются по настройкам сервера. Проверка из любого каталога с установленным `psql`
и тем же окружением:

```sh
psql "$DATABASE_URL" -c 'SELECT 1;'
psql "$DATABASE_URL" -c 'SELECT version, dirty FROM schema_migrations;'
```

После применения всех миграций ожидается `version=4`, `dirty=false`.
Для production следует разделить роли: migrator меняет схему, API выполняет
только нужные запросы к данным. Автоматического создания ролей в проекте нет.
Роли API достаточно `CONNECT` к БД, `USAGE` схемы, `SELECT, INSERT` для `users`,
`accounts`, `account_members` и `SELECT, INSERT, UPDATE, DELETE` для `sessions`.
Для `audit_events` нужны `SELECT, INSERT`: SELECT сравнивает повтор по ключу,
UPDATE/DELETE/TRUNCATE запрещены. Прав на изменение схемы и доступ к
`schema_migrations` API не требуется.
UUID создаёт Go, последовательности и расширения PostgreSQL миграция не использует.
Готовый SQL и проверка прав находятся в [инструкции наката миграций](MIGRATIONS.md).
В ней используется схема `public`: мигратор и API должны видеть одни таблицы
через одинаковый `search_path`. Прикладные запросы не содержат имён схем.

## Какие данные хранятся

| Таблица | Данные |
| --- | --- |
| `users` | Внутренний UUID и уникальный `auth_subject` Supabase, время создания |
| `accounts` | Компания с UUID и временем создания |
| `account_members` | Связь пользователя с компанией, `owner`/`admin`, `audit_read` |
| `sessions` | SHA-256 хеш токена, CSRF, user/account, сроки absolute/idle, отзыв |
| `audit_events` | Внутренний журнал с серверным ключом, scope, actor, объектом, исходом и безопасными metadata |
| `agents` | Account FK, уникальный public ID, name/website/description, статусы, revisions и tombstone |
| `conversations` | Account+agent и ровно один visitor/member; member связан с account_members |
| `messages` | Scoped вопрос, ответ и шесть состояний обработки |
| `message_sources` | Snapshot источника с composite FK к своему сообщению |
| `ai_requests` | Scoped ключ, SHA-256 значимого payload, состояние и terminal result |
| `schema_migrations` | Версия схемы и признак незавершённого применения |

Email, пароль и токены Supabase backend не сохраняет.
При первом успешном входе пользователь, компания и роль владельца создаются
в одной транзакции. Повтор по тому
же идентификатору Supabase не создаёт вторую компанию. Текущий SQL выбирает первое членство
по ID компании; переключения компаний через API нет. Удалённое членство не
восстанавливается автоматически. У предварительной сессии user_id и account_id равны NULL.
Истёкшие и отозванные сессии очищаются при выдаче следующей сессии, фонового уборщика нет.

## Соединения и транзакции

`*sql.DB` — пул соединений, безопасный для параллельных запросов. Максимум открытых
соединений 25, свободных 10; срок соединения 5m, простой 1m. Подключение и проверка связи ограничены
5s. На сервере задаются `statement_timeout=5s` и `idle_in_transaction_session_timeout=5s`.
Это константы реализации, отдельных переменных окружения для них нет.

`db.WithTx` создаёт контекст до 5s, начинает транзакцию, вызывает callback и делает
commit при успехе. При ошибке или panic выполняется rollback. Сам контекст не
останавливает произвольный Go-код: SQL должен использовать переданный context.
Сетевой вызов Supabase выполняется вне SQL-транзакции.

`db.WrapError` сохраняет причину для `errors.Is/As`, но возвращает безопасный текст
без SQL, паролей и пользовательских данных. Раскрытый `Unwrap` не логируется.

## SQL-тесты

Тестам нужна **отдельная** БД: они создают схемы и таблицы. Для локального контейнера
из любого каталога создать её один раз:

```sh
docker exec faq-postgres createdb -U faq faqbot_test
```

Из `backend`:

```sh
export TEST_DATABASE_URL='postgres://faq:local-only@127.0.0.1:5432/faqbot_test?sslmode=disable'
go test -count=1 -v ./...
```

Тесты сами готовят необходимые схемы, предварительный migrate в тестовую БД не
требуется. Без TEST_DATABASE_URL SQL-проверки пропускаются; это не проверка БД.
Для проверки прав журнала дополнительно задаётся `TEST_AUDIT_ROLE`: имя заранее
созданной отдельной роли NOLOGIN без SUPERUSER, CREATEDB, CREATEROLE, REPLICATION,
BYPASSRLS, владения таблицами и прав DDL. Роль не должна наследовать расширенные
права. В отдельной локальной тестовой БД администратор может подготовить её так
(имена роли подключения и тестовой роли заменить своими):

```sql
CREATE ROLE audit_test_writer NOLOGIN NOSUPERUSER NOCREATEDB NOCREATEROLE NOREPLICATION NOBYPASSRLS;
GRANT audit_test_writer TO test_database_user;
```

После этого из `backend`:

```sh
export TEST_AUDIT_ROLE='audit_test_writer'
go test -count=1 -v ./internal/migration -run '^TestAuditSQLRestrictedRole$'
```

Пользователю `TEST_DATABASE_URL` нужны права создания изолированных схем,
выдачи GRANT на свои схемы и таблицы и выполнения SET ROLE в тестовую роль.
Тест выдаёт только USAGE текущей схемы и SELECT/INSERT на audit_events,
переключает роль через SET LOCAL ROLE и проверяет настоящий INSERT, повтор,
SELECT и запреты UPDATE/DELETE/TRUNCATE с SQLSTATE 42501. Схема и её grants
удаляются при очистке. Глобальные роли тесты и приложение не создают.
Без `TEST_AUDIT_ROLE` явно пропускаются `TestAuditSQLRestrictedRole`
и `TestConversationMembershipTriggerRestrictedRole`;
проверка trigger членства также использует эту ограниченную роль. Для проверки
подмены таблицы ей нужен TEMP в отдельной тестовой БД; приложению TEMP не требуется.
Остальным SQL-тестам эта переменная и CREATEROLE не нужны. Пропуск проверки роли
не подтверждает права.

При неверном URL проверить адрес и порт, роль, пароль, TLS и доступность сервера.
При `dirty=true` использовать [инструкцию миграций](MIGRATIONS.md), а не менять
версию наугад.

Аудит и бизнес-изменение записываются одной транзакцией вызывающего сервиса.
Ссылка на компанию не каскадная, actor и объект сохраняются как идентификаторы
без каскадного удаления. Подробнее — [AUDIT.md](AUDIT.md).

## Агенты и диалоги

[Миграция 3](../backend/migrations/000003_agents.up.sql) добавляет агента одной
компании; публичный ID уникален глобально, не секретен и не переиспользуется
другим агентом при сохранённой tombstone записи. `website` и `description` — обычный текст, пустое
значение допустимо. `status` — draft/published/deleted. Deleted требует
`deleted_at`, остальные состояния запрещают его; timestamp не раньше создания.
Partial index исключает tombstones из обычного списка. Потребитель обязан сам
фильтровать tombstones и отклонять публичные сессии удалённого агента: миграция
не реализует HTTP CRUD, завершение сессий и очистку данных.

Настройки представлены явным `public_sources_enabled`, без произвольного JSON.
`revision` нужен для If-Match, knowledge/active_knowledge и config/published_config
разделяют редактируемую и доступную версию. Active не превышает knowledge,
published_config не превышает config; опубликованный агент требует ненулевые
active/published версии. Это локальные структурные инварианты. Механизм
монотонного увеличения revisions и управление snapshot реализуют последующие
сервисы; одно UPDATE само по себе их не обеспечивает. WidgetConfig/media имеют
REST-схемы, но SQL-хранилище полного оформления сюда не входит.

[Миграция 4](../backend/migrations/000004_conversations.up.sql) связывает каждый
диалог с account+agent. Visitor ID — доверенный серверный идентификатор, не login
и не произвольная browser history. Member ID — исторический внутренний user UUID. Trigger проверяет членство
в той же компании при INSERT и изменении member_id/account_id; `visitor_id` и `member_id` взаимоисключающие. Messages ссылаются
на полный scope диалога, sources — на scope сообщения. `document_id/version`
источника — snapshot; таблицы documents пока нет, FK принадлежности документа
здесь отсутствует. Перед выдачей источника будущий сервис обязан проверить
account/agent документа, актуальную версию, tombstone и public visibility.
Текст источника и вопроса не являются audit metadata.

Messages и AI requests допускают accepted/generating/completed/no_answer/failed/
cancelled. У messages ответ существует только в completed/no_answer. У requests
terminal result обязателен только в четырёх terminal состояниях, является JSON
object и содержит `status`, равный state. SQL не проверяет весь REST DTO результата
и не синхронизирует messages.status с ai_requests.state: это будущая транзакция
ChatService. Таблицы не реализуют автомат переходов и неизменяемость terminal
состояния. Передавать arbitrary JSON из браузера как result нельзя.

`ai_requests_scope_key` использует NULLS NOT DISTINCT для
account+agent+visitor/member+operation+key; NULL не обходит уникальность.
Widget операция требует visitor, test операция — member. Conversation/actor и
request/message соединены составными FK. Внутренний
[AIRequests.CreateOrRead](../backend/internal/repository/ai_request.go) выполняет
INSERT ON CONFLICT DO NOTHING, затем SELECT в том же scope в транзакции
READ COMMITTED. Хешируется JSON пары conversation_id+точный текст message.
Повтор с новыми candidate ID возвращает исходные operation/message ID, state и
result; другой значимый payload возвращает `ErrAIRequestConflict` через
errors.Is. Stream — транспортный hint, в этот hash не входит. Репозиторий сам
не создаёт messages; вызывающий сервис должен решать создание кандидата и
rollback лишнего сообщения при replay до подключения к HTTP.

Это основание схемы и локальная проверка replay; RBAC, session orchestration,
RPC, ledger и политики quota/retention не реализованы. SQL-тесты проверяют
чистую установку, upgrade 2→3→4, repeat, down 4→2→4 с сохранением identity/audit,
чужие связи, NULL scope, конфликт hash и конкурентный INSERT с реальной
блокировкой, подтверждённой `pg_blocking_pids`.


У conversation нет постоянного FK на account_members: отзыв членства удаляет
действующие sessions по существующему CASCADE, но сохраняет conversation,
messages и ai_requests как историю. UUID actor в старом диалоге не доказывает
текущие полномочия. Новая member conversation после отзыва запрещена, а текущее
право на чтение истории и replay должен заново проверять вызывающий сервис.
Composite request→conversation actor FK и visitor/member XOR сохраняются.

Trigger check_conversation_member берёт FOR KEY SHARE при проверке membership,
чтобы создание диалога и одновременный отзыв не обходили проверку. Он выполняется
как SECURITY DEFINER владельца миграций с search_path pg_catalog,pg_temp и
обращается только к account_members из trusted TG_TABLE_SCHEMA через quoted
qualification. У PUBLIC отозван EXECUTE; API не получает UPDATE membership ради
этого чтения. SQL regression использует роль с SELECT membership/INSERT
conversations, без UPDATE/прямого EXECUTE, и проверяет невозможность подмены
account_members через временную таблицу. Down версии 4 удаляет и trigger function.

CreateOrRead явно задаёт SET TRANSACTION ISOLATION LEVEL READ COMMITTED до
первого обращения к данным. Конкурентный replay проверяется также при session
default repeatable read. Name и message требуют символ вне полного Unicode
White_Space, совпадающего с Go strings.TrimSpace. SQL btrim с явным набором
символов и cross-engine regexp в OpenAPI отвергают пробельные строки, сохраняя
исходные пробелы внутри содержательного текста и значимый payload.
