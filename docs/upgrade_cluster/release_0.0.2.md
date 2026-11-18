# Release 0.0.2 — Транзакционная запись аудита

Карточка описывает исходный снимок этого выпуска с последней миграцией 2.
Для её установки использовать закреплённый исходный снимок и образ выпуска.
Сборка из текущей ветки содержит дополнительные миграции и требует отдельного
согласования состава поставки по [MIGRATIONS.md](../MIGRATIONS.md).

## Задача

Добавить основу транзакционного аудита: журнал PostgreSQL и внутреннюю запись
события в одной транзакции с бизнес-изменением. Контракт — в [AUDIT.md](../AUDIT.md).

## Группа разработки

Подтверждённые в [релизе 0.0.1](release_0.0.1.md#группа-разработки) ответственные:
Даниил — backend; Сергей — DevOps и тимлид, инфраструктура и установка.
Согласования, параметры стенда и установленная версия этого выпуска не предоставлены.

## Описание релиза

1. [Миграция 000002](../../backend/migrations/000002_audit.up.sql) создаёт
   `audit_events`, ограничения, первичный и уникальный ключи, четыре поисковых индекса.
   Итоговая версия схемы БД — **2**, `dirty=false`.
2. [AuditService](../../backend/internal/service/audit.go) проверяет событие и
   передаёт его [SQL-репозиторию](../../backend/internal/repository/audit.go) в
   существующей `*sql.Tx`. Если вызывающий код возвращает ошибку аудита из
   callback `db.WithTx`, бизнес-изменение и аудит откатываются вместе.
3. Ключ SHA-256 охватывает `OperationID`, `Action`, `Scope`, `AccountID`.
   Идентичный повтор сохраняет первую строку; различия сравниваемого payload,
   включая request/trace/source/metadata, возвращают `ErrAuditConflict`.
4. Сервис проверяет каталог из 43 событий, scope, actor и whitelist metadata.
   Неизвестные поля, неверные типы и значения отклоняются; JSON ограничен 4096 байтами.
5. SQL дополнительно проверяет форму полей, JSON object, размер metadata и связь
   с компанией. API нужны `SELECT, INSERT` на журнал без владения и прав
   `UPDATE, DELETE, TRUNCATE`. SQL не повторяет весь каталог и whitelist сервиса.

Инфраструктура ещё не подключена к Auth и бизнес-API. Чтение журнала, экспорт,
HTTP tenant RBAC и RLS журнала отсутствуют. `audit.exported` в каталоге не означает
готовый экспорт. Принадлежность actor и объекта компании проверяет вызывающий
бизнес-сервис. Формат записи `AuditRecord.SchemaVersion=1` отличается от версии БД 2.
Новых зависимостей, отдельных развёртываемых сервисов и файлов версий сервисов
или ролей нет. `go.mod` описывает зависимости, а не установленный выпуск.

## Порядок установки

### Исходные данные и доступы

Инструкция рассчитана на обновление 0.0.1 → 0.0.2 в схеме `public`.
Адреса БД, сеть, имя контейнера, привязка порта, origin и опубликованный образ
не предоставлены: все `REPLACE_…` ниже заменить параметрами действующего стенда.
Команды установки не являются отчётом об их выполнении.

1. Сохранить фактический образ и команду запуска прежнего API, его защищённый
   env-файл, сеть, порт и конфигурацию прокси. У приложения нет version endpoint;
   версию определить по учёту поставки и ID работающего образа. Не считать health
   проверкой версии. Если прежний API работает в Docker, из любого каталога
   Docker-хоста с `API_CONTAINER` (имя действующего контейнера):

   ```sh
   docker inspect --format '{{.Image}}' "$API_CONTAINER"
   ```

   Ожидается immutable ID старого образа; записать его как `OLD_IMAGE_ID` в учёт
   поставки. Сохранить исходные ACL журнала, если схема 2 уже существует.
2. Подготовить отдельные LOGIN-роли migrator и API по
   [ролям и правам](../MIGRATIONS.md#роли-и-права). Migrator владеет таблицами и
   историей, имеет `CONNECT`, `USAGE, CREATE`; API сохраняет прежние identity/session
   grants и не получает владение, DDL или административные флаги. Проверить доступ
   роли резервного копирования. API нельзя запускать с DSN migrator.
3. Из любого каталога с psql задать `MIGRATOR_DATABASE_URL` безопасным способом
   из хранилища секретов (хостовый DSN migrator с согласованным TLS), затем открыть:

   ```sh
   psql "$MIGRATOR_DATABASE_URL" -v ON_ERROR_STOP=1
   ```

   В открытом psql:

   ```sql
   SELECT current_user, current_database(), current_schema();
   SHOW search_path;
   SELECT to_regclass('public.schema_migrations') AS history;
   SELECT tablename, tableowner FROM pg_tables
   WHERE schemaname='public'
     AND tablename IN ('schema_migrations','users','accounts','account_members','sessions','audit_events')
   ORDER BY tablename;
   ```

   Ожидаются целевая БД, migrator, схема `public`, история и таблицы релиза 0.0.1
   во владении migrator. Только если history не NULL, выполнить отдельно:

   ```sql
   SELECT version, dirty FROM public.schema_migrations;
   ```

   Штатное обновление начинается с `1,false`; повтор допускается с `2,false`.
   При `dirty=true`, неожиданной версии, пустой/отсутствующей истории при существующих
   бизнес-таблицах остановиться и выполнить
   [сверку и восстановление](../MIGRATIONS.md#если-произошла-ошибка).
   Автоматические `force` и down не применять. Новая пустая БД — отдельная первая
   установка: runner применит версии 1 и 2.

### Копия, исходники и образ

4. До изменений сохранить копию всей БД вне Git по
   [инструкции резервного копирования](../MIGRATIONS.md#резервная-копия).
   Из корня репозитория с `BACKUP_DATABASE_URL` (DSN роли с доступом ко всем таблицам)
   и `BACKUP_DIR` (существующий защищённый каталог вне репозитория):

   ```sh
   set -e
   umask 077
   pg_dump --format=custom --file="$BACKUP_DIR/before-002.dump" "$BACKUP_DATABASE_URL" && \
   pg_restore --list "$BACKUP_DIR/before-002.dump" > "$BACKUP_DIR/before-002.list"
   ```

   При ошибке копирования остановиться: `set -e` и `&&` запрещают продолжать
   проверку старого архива как новой копии. Ожидаются exit code 0 и читаемый
   список объектов. Версия `pg_dump` не старше
   сервера. Список не заменяет пробное восстановление в отдельную БД; до установки
   подтвердить доступность восстановления и сохранить исходные настройки/права.
5. Зафиксировать проверенный исходный снимок: проверенный коммит исходников, содержащий
   весь выпуск, либо контрольную сумму проверенного архива исходников. Базовый
   коммит из Diff недостаточен: часть файлов пока не отслеживается Git.
   Сохранить этот идентификатор как `SOURCE_BUILD_ID` в учёте поставки.
   Это параметр shell для маркировки, не новая настройка приложения.
   Из корня именно выбранного снимка с работающим Docker:

   ```sh
   set -e
   SOURCE_BUILD_ID='REPLACE_REVIEWED_COMMIT_OR_ARCHIVE_SHA256'
   docker build -f backend/Dockerfile \
     --label "org.opencontainers.image.revision=$SOURCE_BUILD_ID" \
     -t faq-api:0.0.2-local .
   IMAGE_ID=$(docker image inspect --format '{{.Id}}' faq-api:0.0.2-local)
   printf '%s\n' "$IMAGE_ID"
   ```

   При ошибке сборки `set -e` останавливает последовательность до inspect:
   старый образ с тем же тегом не принимается за результат новой сборки.
   Ожидается успешная сборка и `sha256:…`; сохранить `SOURCE_BUILD_ID` и `IMAGE_ID`
   вместе. `faq-api:0.0.2-local` — предложенный локальный тег, не опубликованный
   образ. Далее использовать этот immutable `IMAGE_ID` для migrator и API.
6. Runtime-переменные не меняются. Подготовить вне Git защищённые файлы с режимом
   600: `MIGRATOR_ENV_FILE` и `API_ENV_FILE` — их абсолютные пути. Первый содержит
   `APP_ENV=production`, `DATABASE_URL` роли migrator, `MIGRATIONS_PATH=/migrations`
   и **не содержит** четыре Auth-переменные. Второй сохраняет действующие настройки
   API, полный Auth-набор, прежний `AUTH_ORIGIN` и DSN отдельной роли API.
   В контейнере слушать прежний адрес (обычно `:8080`), а не хостовый loopback.
   Справочник и формат env-файла — [BACKEND.md](../BACKEND.md#переменные-окружения).
   Не выводить файлы и DSN в журнал. Все имена shell-параметров этой инструкции
   (`IMAGE_ID`, `API_ENV_FILE` и другие) приложение само не читает.

### Миграция и права

7. Из любого каталога Docker-хоста запустить отдельный migrator:

   ```sh
   docker run --rm --env-file "$MIGRATOR_ENV_FILE" \
     --entrypoint /migrate "$IMAGE_ID"
   ```

   Ожидаются exit code 0 и `migrations are up to date`. Runner вызывает `Up()`
   и применяет **все ожидающие** миграции из `/migrations`: при версии 1 — 000002,
   на пустой схеме — 000001 и 000002; при версии 2 — успешный no-op.
   Не запускать миграцию в каждой реплике API. Для БД в отдельной Docker-сети
   добавить `--network "$DOCKER_NETWORK"` с прежней сетью; адрес и CA должны
   быть доступны контейнеру. Требования к сети/TLS и альтернативный запуск Go —
   [MIGRATIONS.md](../MIGRATIONS.md#запуск-runner).
8. В psql migrator из шага 3 проверить:

   ```sql
   SELECT version, dirty FROM public.schema_migrations;
   SELECT to_regclass('public.audit_events') AS audit_table;
   SELECT conname, contype, convalidated, pg_get_constraintdef(oid)
   FROM pg_constraint WHERE conrelid='public.audit_events'::regclass
   ORDER BY conname;
   SELECT indexname, indexdef FROM pg_indexes
   WHERE schemaname='public' AND tablename='audit_events' ORDER BY indexname;
   ```

   Ожидаются `2,false`, `audit_events`, валидные PK/unique/FK/CHECK, соответствующие
   [000002 up](../../backend/migrations/000002_audit.up.sql), и **6 индексов**:
   `audit_events_pkey`, `audit_events_event_key_key`, `audit_account_time_idx`,
   `audit_account_action_time_idx`, `audit_entity_time_idx`, `audit_request_time_idx`.
   Последние четыре оканчиваются `recorded_at DESC, id DESC` (request — partial).
   Повтор шага 7 допустим, не очищает существующие события; версия остаётся `2,false`.
9. В psql владельца/администратора той же БД выдать только добавленные права журнала,
   заменив имя роли; прежние grants identity/session сохраняются:

   ```sql
   \set ON_ERROR_STOP on
   \set api_role REPLACE_API_ROLE
   BEGIN;
   REVOKE UPDATE, DELETE, TRUNCATE ON public.audit_events FROM :"api_role";
   GRANT SELECT, INSERT ON public.audit_events TO :"api_role";
   COMMIT;
   ```

   Ожидается COMMIT. SELECT нужен для сравнения повторов. Затем из любого каталога
   открыть `psql "$API_DATABASE_URL" -v ON_ERROR_STOP=1`, где DSN — хостовый DSN API,
   и выполнить [полную проверку эффективных прав](../MIGRATIONS.md#права-api-после-миграции).
   Ожидаются SELECT/INSERT журнала и прежние права API, отсутствие UPDATE/DELETE/
   TRUNCATE, CREATE схемы, административных флагов и владения таблицами.
   Прямой REVOKE не отменяет PUBLIC, членство и владение; при лишних правах
   остановиться для исправления администратором. Не обходить ошибку ролью migrator.
   Начальное заполнение `audit_events` и дополнительный SQL, кроме необходимых
   grants, **не требуются**. Фиктивные события в рабочую БД не вставлять.

### API и проверка установки

10. После успешных проверок остановить прежний контейнер и запустить новый из
    того же образа. Из любого каталога Docker-хоста задать `API_CONTAINER`,
    `DOCKER_NETWORK`, `API_HOST_IP`, `API_HOST_PORT` по сохранённому запуску:

    ```sh
    docker stop --time 75 "$API_CONTAINER"
    docker run -d --name "${API_CONTAINER}-002" \
      --network "$DOCKER_NETWORK" --env-file "$API_ENV_FILE" \
      -p "${API_HOST_IP}:${API_HOST_PORT}:8080" "$IMAGE_ID"
    ```

    Ожидаются завершение старого API и ID нового контейнера в состоянии running.
    `--time 75` оставляет больше времени, чем максимальный допустимый
    `HTTP_SHUTDOWN_TIMEOUT=1m`; Docker не должен прерывать штатный shutdown
    раньше таймаута приложения.
    Пример предполагает прежний контейнерный порт 8080; при ином порте сохранить
    фактическое соответствие и `HTTP_LISTEN_ADDR`. Сохранить прежние mounts CA,
    restart policy и прочие необходимые параметры штатного запуска. Для другого
    оркестратора использовать его штатный rollout с тем же `IMAGE_ID` и env.
    Старый контейнер/образ оставить для отката.
11. Маршрутизация прокси не меняется. Сохранить прежний upstream/binding,
    внешний TLS и origin; автоматический reload не нужен. Действующая схема —
    [установка 0.0.1](release_0.0.1.md#порядок-установки),
    [Auth](../AUTH.md#настройка). Если инфраструктура требует фактического
    переключения upstream, применить существующую процедуру стенда.
12. Из любого каталога с curl, где `API_BASE_URL` — доступный адрес нового API
    без конечного `/` (для внешней проверки HTTPS):

    ```sh
    curl --fail-with-body -i "$API_BASE_URL/health/startup"
    curl --fail-with-body -i "$API_BASE_URL/health/ready"
    curl --fail-with-body -i "$API_BASE_URL/health/live"
    ```

    Ожидается HTTP 200 во всех случаях. Health не проверяет таблицы, DML grants
    или Supabase. Отдельно повторить SQL шага 8 и проверку прав шага 9; затем
    выполнить login/session/logout по [Auth smoke](../AUTH.md#порядок-входа-и-проверка-запросами)
    через прежний origin с cookie/CSRF. Ожидания HTTP описаны там. При наличии
    проверочного аккаунта использовать его; новые события аудита Auth в этом
    выпуске не ожидаются. Результат установки фиксировать только после этих проверок.

## Порядок отката релиза

1. **Приложение.** Остановить новый API штатным способом. Для Docker из любого
   каталога с параметрами шага 10:

   ```sh
   docker stop --time 75 "${API_CONTAINER}-002"
   docker start "$API_CONTAINER"
   ```

   Остановка также использует `--time 75`, превышающий допустимый максимум
   таймаута приложения 1m. Ожидается running прежнего контейнера с сохранённым
   `OLD_IMAGE_ID`, прежним
   env и binding; сверить ID через `docker inspect --format '{{.Image}}'` как
   перед установкой. Если старого контейнера нет, восстановить его сохранённую
   команду запуска с `OLD_IMAGE_ID`. Повторить health и Auth smoke из шага 12.
   Старый API не использует `audit_events` и совместим со схемой 2: обычный
   откат приложения сохраняет схему и журнал, `2,false`.
2. **Настройки.** Новых runtime env и proxy changes нет. Восстановить лишь
   фактически изменённые при развёртывании настройки/секреты/сеть из снимка;
   при переключавшемся upstream вернуть прежний. Не менять настройки БД наугад.
3. **Права.** Отзыв доступа — отдельный шаг после остановки всех ещё использующих
   его писателей. Если SELECT/INSERT были добавлены именно этой установкой,
   в psql владельца/администратора той же БД:

   ```sql
   \set ON_ERROR_STOP on
   \set api_role REPLACE_API_ROLE
   REVOKE SELECT, INSERT ON public.audit_events FROM :"api_role";
   ```

   Ожидается REVOKE; проверить эффективные права по MIGRATIONS. Не отзывать
   исходные grants, зафиксированные до установки, и права identity/session.
   Отзыв не удаляет таблицу и не отменяет унаследованные привилегии.
4. **Схема.** Удаление журнала не является штатным первым шагом отката.
   [000002 down](../../backend/migrations/000002_audit.down.sql) безвозвратно
   удаляет `audit_events` и индексы. При отдельной необходимости остановить всех
   писателей, сохранить свежую копию и проверить восстановление. Только при
   `version=2, dirty=false` из `backend` с установленным внешним golang-migrate v4
   (драйверы file/postgres) и DSN migrator:

   ```sh
   migrate -path ./migrations -database "$MIGRATOR_DATABASE_URL" down 1
   ```

   Ожидаются `1,false`, отсутствие `audit_events`, сохранение identity-таблиц и
   данных; проверить psql запросами шагов 3 и 8 без обращения к удалённой regclass.
   DSN передаётся в argv CLI: выполнять в защищённом окружении без логирования
   команды и общего доступа к списку процессов. Встроенный runner выполняет только
   up. Не выполнять down.sql через psql, `down 2` или `force` автоматически.
   Полное восстановление копии теряет изменения после неё и не откатывает Supabase.
   Общая процедура ошибок — [MIGRATIONS.md](../MIGRATIONS.md#если-произошла-ошибка).

## Новые БС-ы

Не добавляются. `AuditService` — внутренний Go-компонент существующего backend.

## Новые/изменённые эндпоинты

Не добавляются и не меняются. API чтения и экспорта журнала отсутствует.
Прежние маршруты — в [HTTP.md](../HTTP.md).

| № | Метод, путь | Query | Описание | Доступ (хэдеры, роли) |
| --- | --- | --- | --- | --- |

## Новые топики Kafka

Не добавляются.

| Контур | Название | Количество партиций | Фактор репликации | Retention | cleanup.policy | Сжатие |
| --- | --- | --- | --- | --- | --- | --- |

## Новые и изменённые настройки

Новых/изменённых runtime env API и migrator нет. Новая настройка только тестовая:

| Настройка | Тип | Обязательное (по умолчанию) | Пример | Описание |
| --- | --- | --- | --- | --- |
| `TEST_AUDIT_ROLE` | string, имя роли PostgreSQL | Нет; отсутствует → skip `TestAuditSQLRestrictedRole` | `faq_audit_test` | Заранее созданная отдельная NOLOGIN роль без административных прав, DDL и владения; тестовое подключение должно уметь GRANT и SET ROLE. Не роль рабочего API |

Общий [справочник окружения](../BACKEND.md#переменные-окружения),
[подготовка тестовой роли](../DATABASE.md#sql-тесты). `TEST_DATABASE_URL` уже
используется тестами, не является новой настройкой этого выпуска.

## Diff

Проверенная база сравнения ветки `feature/transactional-audit` с `dev`:
`f858c8f314470033142d8b91f0dbbc5991a8f1cd`. Это база, не полный состав релиза:
изменения пока незакоммичены, часть файлов untracked и не видна в обычном `git diff`.
PR, релизный Git-тег и опубликованный образ не предоставлены.

- Контракт: [models/audit.go](../../backend/internal/models/audit.go),
  [service/audit.go](../../backend/internal/service/audit.go),
  [repository/audit.go](../../backend/internal/repository/audit.go).
- Проверки: [service/audit_test.go](../../backend/internal/service/audit_test.go),
  [migration/audit_test.go](../../backend/internal/migration/audit_test.go).
- SQL: [000002 up](../../backend/migrations/000002_audit.up.sql),
  [000002 down](../../backend/migrations/000002_audit.down.sql);
  runner [migration.go](../../backend/internal/migration/migration.go) не изменён.
- Документация: [аудит](../AUDIT.md), [архитектура](../ARCHITECTURE.md),
  [БД](../DATABASE.md), [миграции](../MIGRATIONS.md), [backend](../BACKEND.md),
  [навигация](../README.md).
- Неизменённые примеры и сборка: [backend/.env.example](../../backend/.env.example),
  [config.go](../../backend/internal/config/config.go),
  [Dockerfile](../../backend/Dockerfile). Нового proxy-конфига нет.

До установки зафиксировать проверенный коммит исходников либо SHA-256 проверенного архива,
`SOURCE_BUILD_ID` и immutable `IMAGE_ID` по шагу 5.

## E2E тесты

Локальные проверки повторно выполнены **17.11.2026** на отдельной временной
PostgreSQL 18.3 с отдельными LOGIN-ролями migrator/API и NOLOGIN-ролью тестов:

- `go test -count=1 ./...` и `go test -race -count=1 ./...` с
  `TEST_DATABASE_URL` и `TEST_AUDIT_ROLE` — каждый по 151 успешному случаю,
  0 ошибок и 0 пропусков. Форматирование gofmt/goimports без diff,
  `go vet ./...`, `go build ./...` и golangci-lint v2.1.6 — успешно, 0 замечаний.
- Custom backup под migrator, `pg_restore --list` и настоящее восстановление
  `--no-owner --no-acl` в отдельную БД — успешно; восстановлены версия 1 clean
  и проверочная компания. Это не проверка рабочих резервных копий.
- Настоящий runner с полным каталогом миграций: upgrade 1→2 и повтор — exit 0,
  `2,false`. Identity сохранена, журнал пуст; подтверждены 6 индексов,
  валидные PK/unique/FK/CHECK и владение migrator.
- Под отдельной LOGIN-ролью API подтверждены SELECT/INSERT журнала,
  отсутствие CREATE/владения и реальные отказы UPDATE/DELETE/TRUNCATE.
  Проверочный INSERT выполнялся в тестовой БД с последующим rollback;
  журнал остался пустым.
- Новый API и предыдущий API, собранный из коммита
  `e758d73ef9a649459cae642f009a94c80b52a779`, запущены под API-ролью на схеме 2:
  startup/ready/live — HTTP 200, штатный SIGTERM — exit 0. Оба запуска были
  development **без Auth/Supabase**. Это локальная проверка health-совместимости,
  не проверка входа и не запуск прежнего опубликованного контейнера.

Docker daemon повторно недоступен: socket отсутствует. Сборка/запуск Docker
не выполнялись. SQL-интеграция и локальный HTTP smoke не являются E2E стенда.

Операторская приёмка: копия и восстановимость; точный образ; миграция под migrator;
SQL версии/ограничений/индексов и эффективных прав API; health 200; внешний
login/session/logout через Supabase и прокси. Штатная установка стенда,
production Auth и Docker rollout в рамках подготовки документации не выполнялись.
При недоступности окружения результат остаётся «не проверено», а не PASS.

## Список обновляемых модулей

| Репозиторий | Модуль | Версия на проде | Новая версия | Примечания |
| --- | --- | --- | --- | --- |
| faq-bot-backend | Существующий backend: models/service/repository аудита | Не предоставлена | 0.0.2 — обозначение карточки | Публикация не подтверждена. До установки зафиксировать проверенный исходный снимок и immutable IMAGE_ID; локальный тег не является опубликованным образом |

## Postgres

| Репозиторий | Инстанс БД на проде | Название артефакта | Статус | Версия на проде | Предлагаемая версия | Артефакт для раскатки | Примечания |
| --- | --- | --- | --- | --- | --- | --- | --- |
| faq-bot-backend | Не предоставлен | 000002_audit | SQL в репозитории; установка стенда не подтверждена | Не предоставлена | Схема 2, dirty=false | [000002_audit.up.sql](../../backend/migrations/000002_audit.up.sql), отдельный cmd/migrate | После [000001_identity](../../backend/migrations/000001_identity.up.sql); runner применяет все pending. Migrator владеет схемой/историей; API SELECT/INSERT без UPDATE/DELETE/TRUNCATE и DDL; seed не нужен. Down удаляет журнал |
