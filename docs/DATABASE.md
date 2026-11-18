# PostgreSQL: подключение и данные

Backend хранит пользователей, компании, членство в компаниях, сессии и события аудита в PostgreSQL.
Поиск по знаниям локального AI CLI использует отдельный SQLite, к этой БД он не подключён.
Драйвер backend — pgx через стандартный `database/sql`.

## Локальная БД

Нужен PostgreSQL с доступом по сети или Unix socket. Ранее SQL-тесты выполнялись
на PostgreSQL 18.3; минимальная поддерживаемая версия отдельно не проверялась.
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

После применения всех миграций ожидается `version=6`, `dirty=false`.
Для production следует разделить роли: migrator меняет схему, API выполняет
только нужные запросы к данным. Автоматического создания ролей в проекте нет.
Роли API достаточно `CONNECT` к БД, `USAGE` схемы, `SELECT, INSERT` для `users`,
`accounts`, `account_members` и `SELECT, INSERT, UPDATE, DELETE` для `sessions`.
Для `trial_bootstrap_intents` API нужен INSERT; SELECT для диагностики выполняется
отдельной ролью. UPDATE/DELETE/TRUNCATE и DDL для этой таблицы API не нужны.
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
| `trial_bootstrap_intents` | Одна pending-запись на новую компанию; account_id — PK/FK, created_at; без лимита и баланса |
| `schema_migrations` | Версия схемы и признак незавершённого применения |

Email, пароль и токены Supabase backend не сохраняет.
При первом успешном входе пользователь, компания и роль владельца создаются
в одной транзакции вместе с намерением trial. Повтор по тому
же идентификатору Supabase не создаёт вторую компанию. Текущий SQL выбирает первое членство
по ID компании; переключения компаний через API нет. Удалённое членство не
восстанавливается автоматически. У предварительной сессии user_id и account_id равны NULL.
Старые компании при обновлении не получают намерений автоматически.
Порядок версий и проверка состава поставки — в [MIGRATIONS.md](MIGRATIONS.md).
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
Для проверки прав журнала и регистрации дополнительно задаётся `TEST_AUDIT_ROLE`: имя заранее
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
go test -count=1 -v ./internal/migration -run '^Test(AuditSQL|Registration)RestrictedRole$'
```

Пользователю `TEST_DATABASE_URL` нужны права создания изолированных схем,
выдачи GRANT на свои схемы и таблицы и выполнения SET ROLE в тестовую роль.
Тест выдаёт только USAGE текущей схемы и SELECT/INSERT на audit_events,
переключает роль через SET LOCAL ROLE и проверяет настоящий INSERT, повтор,
SELECT и запреты UPDATE/DELETE/TRUNCATE с SQLSTATE 42501. Схема и её grants
удаляются при очистке. Глобальные роли тесты и приложение не создают.
Без `TEST_AUDIT_ROLE` обе проверки явно пропускаются. Журнал сообщает
`set TEST_AUDIT_ROLE for audit INSERT/SELECT and denied UPDATE/DELETE/TRUNCATE checks`,
регистрация — `set TEST_AUDIT_ROLE for restricted registration checks`;
остальным SQL-тестам эта переменная и CREATEROLE не нужны. Пропуск этого теста
не подтверждает проверку прав.

При неверном URL проверить адрес и порт, роль, пароль, TLS и доступность сервера.
При `dirty=true` использовать [инструкцию миграций](MIGRATIONS.md), а не менять
версию наугад.

Аудит и бизнес-изменение записываются одной транзакцией вызывающего сервиса.
Ссылка на компанию не каскадная, actor и объект сохраняются как идентификаторы
без каскадного удаления. Подробнее — [AUDIT.md](AUDIT.md).

Проверка `TestRegistrationRestrictedRole` использует ту же ограниченную NOLOGIN-роль
в отдельной схеме. Под ней выполняются bootstrap, повтор и чтение сессии;
для trial-таблицы выдан только INSERT. Отзыв INSERT вызывает ошибку 42501
и rollback user/account/owner. Право SELECT для проверки данных остаётся
у владельца тестовой схемы.
