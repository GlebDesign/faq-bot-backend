# Установка релиза 0.0.1

План проверен по исходникам 05.11.2026. Для описанной сборки выбран коммит
`e758d73ef9a649459cae642f009a94c80b52a779`. Это исходная ревизия поставки,
а не существующий тег `0.0.1`: тегов в исследованном репозитории нет.
Номер `0.0.1` используется в этом плане; приложение не сообщает версию через HTTP.
Незакоммиченные изменения документации и тестов в указанную сборку не входят.
При выборе другой ревизии команды, SQL и совместимость нужно сверить заново.

Релиз содержит Go API авторизации через Supabase, локальные компании,
пользователей и сессии PostgreSQL, три проверки состояния и отдельный runner
миграций. Frontend, HTTP API чата и управления Agent, интеграция с Python AI
и RabbitMQ в эту установку не входят. Подробный справочник сервиса:
[backend](../BACKEND.md), [HTTP](../HTTP.md), [авторизация](../AUTH.md).

## 1. Подготовить стенд

До запуска согласовать:

- Выделенную БД PostgreSQL, роли API и migrator, адрес/порт, TLS и резервное
  копирование. Минимальная версия PostgreSQL отдельно не установлена;
  ранее SQL-проверки проекта выполнялись на 18.3. В миграции нет extensions,
  sequences, Supabase-таблиц или зависимости от pgvector.
- Проект Supabase с email/password Auth. Его БД не обязана совпадать с БД
  приложения. Для успешного signup ответ должен содержать `user.id` и
  `access_token`; при включённом подтверждении email без токена текущий
  backend вернёт 502. На тестовом проекте согласовать отключение Confirm Email
  и разрешение signup; проверить это пробной регистрацией. Backend сам
  настройки Supabase и рассылку писем не меняет.
- Один внешний HTTPS-origin для клиента и API, например значение
  `https://REPLACE_PUBLIC_HOST`. Заменить его реальным адресом, без пути и
  завершающего `/`. CORS и OPTIONS/preflight в API не реализованы.
- Доступ из контейнеров API/migrator к PostgreSQL, DNS и HTTPS Supabase:443.
  Для мигратора нужен прямой или сохраняющий сессию endpoint PostgreSQL:
  runner использует отдельное соединение и advisory lock. Не подменять его
  transaction/statement pooling без отдельной проверки.
- Docker на узле сборки и запуска, HTTPS-прокси с сертификатом и доступом
  к upstream. Для SQL-проверок нужны `psql` и `pg_dump`, для smoke — Python 3.
  Версия `pg_dump` должна быть не старше сервера PostgreSQL; перед копированием
  сверить `pg_dump --version` и `SHOW server_version;`.
  Go 1.24+ нужен только при сборке без Docker; Dockerfile использует
  `golang:1.24-alpine`.

В репозитории нет принятой конфигурации кластера, адреса сервера, registry,
публичного домена и опубликованного образа 0.0.1. Ниже приведён пример одного
контейнера с Nginx на том же Docker-хосте. Имена контейнера, локальный тег и
порт — значения примера; перед установкой проверить, что они свободны.
Для другой топологии сохранить порядок шагов и заменить upstream/сеть.

Сохранить предыдущий образ или его digest, параметры запуска, настройки
прокси и резервную копию БД. Из корня репозитория, с заранее установленным
`MIGRATOR_DATABASE_URL` и доступным `pg_dump`:

```sh
set -e
cd backend
umask 077
backup_dir=$(mktemp -d ../../faqbot-before-0.0.1.XXXXXX)
pg_dump --format=custom --file="$backup_dir/database.dump" "$MIGRATOR_DATABASE_URL"
pg_restore --list "$backup_dir/database.dump" > "$backup_dir/contents.txt"
printf 'Резервная копия: %s\n' "$backup_dir/database.dump"
cd ..
```

`MIGRATOR_DATABASE_URL` — переменная этих команд, не настройка API.
Роль резервного копирования должна иметь доступ ко всем сохраняемым таблицам;
при необходимости использовать отдельную backup-роль. Копию хранить вне Git
по правилам стенда. Для новой пустой БД также сохранить параметры и созданные роли.

## 2. Задать окружение

Полная таблица с defaults и ограничениями находится в
[BACKEND.md](../BACKEND.md#переменные-окружения). Здесь — значения установки.
API и мигратор получают настройки через окружение процесса; `.env` автоматически
не читается. В командах ниже файл читает Docker через `--env-file`.

Из корня репозитория на Docker-хосте создать **вне репозитория** каталог секретов:

```sh
install -d -m 700 ../faqbot-release-0.0.1-secrets
umask 077
cat > ../faqbot-release-0.0.1-secrets/api.env <<'ENV'
APP_ENV=production
HTTP_LISTEN_ADDR=:8080
HTTP_SHUTDOWN_TIMEOUT=10s
DATABASE_URL=postgresql://REPLACE_API_ROLE:REPLACE_URL_ENCODED_PASSWORD@REPLACE_DB_HOST:5432/REPLACE_DB_NAME?sslmode=verify-full
MIGRATIONS_PATH=/migrations
SUPABASE_URL=https://REPLACE_PROJECT_HOST
SUPABASE_KEY=REPLACE_SUPABASE_API_KEY
AUTH_ORIGIN=https://REPLACE_PUBLIC_HOST
ENV
cat > ../faqbot-release-0.0.1-secrets/migrate.env <<'ENV'
APP_ENV=production
DATABASE_URL=postgresql://REPLACE_MIGRATOR_ROLE:REPLACE_URL_ENCODED_PASSWORD@REPLACE_DB_HOST:5432/REPLACE_DB_NAME?sslmode=verify-full
MIGRATIONS_PATH=/migrations
ENV
chmod 600 ../faqbot-release-0.0.1-secrets/api.env ../faqbot-release-0.0.1-secrets/migrate.env
```

До запуска заменить все `REPLACE_*` в защищённом редакторе. Эти строки являются
шаблоном, не рабочими секретами и адресами. Docker env-file не выполняет shell
и не подставляет `$VARIABLE`; записать готовые значения без `export`.
Пароль в URL должен быть percent-encoded. `sslmode=verify-full` требует совпадения
имени сервера с сертификатом и доверенного CA; частный CA нужно передать контейнеру
отдельным read-only mount и указать через `sslrootcert` в DSN.
Например, добавить к обоим `docker run` аргумент
`--mount type=bind,src=/REPLACE_HOST_CA_FILE,dst=/run/db-ca.pem,readonly`,
а в контейнерном DSN — `sslrootcert=/run/db-ca.pem`. Абсолютный путь источника
заменить; монтировать отдельный CA-файл, читаемый UID 65532 внутри контейнера
(например, публичный сертификат с режимом 644, без приватного ключа).
Переменные `MIGRATOR_DATABASE_URL`/`API_DATABASE_URL` для host-команд psql/pg_dump
указывают на ту же БД, но адрес и `sslrootcert` должны быть доступны с хоста.
Контейнерные DNS-имена и `/run/db-ca.pem` могут не подходить хосту. Не заменять TLS
отключением проверки без принятого решения о защищённой сети.

| Настройка | Использование в установке |
| --- | --- |
| `APP_ENV` | `config.Load`; для API `production` или `staging` требуют БД и полного Auth |
| `HTTP_LISTEN_ADDR` | `cmd/api`, `:8080` внутри контейнера; публикация порта ограничена loopback хоста |
| `HTTP_SHUTDOWN_TIMEOUT` | `cmd/api`, завершение запросов; >0 и ≤1m, пример `10s` |
| `DATABASE_URL` | `db.Open` у API/migrator; одна БД и схема, разные учётные данные |
| `MIGRATIONS_PATH` | Только runner; `/migrations` уже задан в образе; для `go run` default `./migrations` относительно `backend` |
| `SUPABASE_URL` | `authprovider`, HTTPS-origin проекта без `/auth/v1`, query и credentials |
| `SUPABASE_KEY` | `authprovider`, API key выбранного проекта для заголовка `apikey`; хранить как секрет |
| `AUTH_ORIGIN` | HTTP-обработчик, точный внешний origin клиента; без завершающего `/` |
| `AUTH_ALLOW_INSECURE_HTTP` | В файлах стенда отсутствует; default `false`; `true` допускается только в development для loopback HTTP |

Установка любой Auth-переменной, даже `AUTH_ALLOW_INSECURE_HTTP=false`, включает
проверку полного набора Supabase URL/key, origin и DATABASE_URL. Частичная
конфигурация не запускается. В `migrate.env` Auth-переменных нет: мигратор может
работать в production только с БД, поскольку дополнительное требование Auth
находится в `cmd/api`, а не в runner. Пустые/неверные общие настройки также
могут остановить мигратор. `TEST_DATABASE_URL` установке не нужен.

`127.0.0.1` в DSN контейнера — сам контейнер. Для БД на хосте Docker Desktop
используется доступный из контейнера адрес, например `host.docker.internal`;
для БД в общей Docker-сети — её DNS-имя с добавлением `--network` к обеим
командам запуска. Выбранную сеть записать в параметры стенда.

## 3. Собрать один образ для API и мигратора

Из корня checkout с указанным коммитом, на узле с Docker daemon:

Команды сборки выполнить в Bash; `pipefail` прекращает шаг также при ошибке
`git archive`, а `set -e` не позволяет продолжить после неудачной сборки.

```sh
bash <<'BUILD'
set -euo pipefail
REV=e758d73ef9a649459cae642f009a94c80b52a779
test "$(git rev-parse --verify "$REV^{commit}")" = "$REV"
git show --no-patch --format=fuller "$REV"
git archive --format=tar "$REV" | docker build -f backend/Dockerfile -t faq-api:0.0.1-local -
docker image inspect faq-api:0.0.1-local --format '{{.Id}}'
BUILD
```

При ошибке проверки коммита не выполнять сборку. `git archive` берёт файлы
фиксированного коммита, исключая текущие незакоммиченные правки. Тег
`faq-api:0.0.1-local` — предлагаемый **локальный** тег, не адрес опубликованного
образа. Сохранить полученный image ID в журнале развёртывания.
Если сборка и запуск выполняются на разных узлах, передать этот образ через
принятый registry или `docker save`/`docker load`, сверить ID либо digest
и использовать один и тот же образ для обоих процессов.

В Dockerfile базовый тег не закреплён digest, поэтому одинаковая исходная ревизия
не гарантирует побитово одинаковую повторную сборку. Для повторного развёртывания
сохранить уже собранный образ. Он содержит `/api`, `/migrate`, SQL-каталог
и CA-сертификаты; основа `scratch`, shell и curl отсутствуют,
пользователь `65532:65532`, entrypoint `/api`.

## 4. Подготовить роли и применить миграции

Роли и БД создаёт администратор PostgreSQL штатным способом. Обе роли должны
иметь `LOGIN` и доступ по сетевым правилам БД; суперпользователь, создание БД
и создание других ролей API/migrator не требуются. Ниже предполагаются выделенная
БД приложения, схема `public` и отсутствие одноимённых таблиц из другой системы.
Не выполнять миграцию в `auth` или другой служебной схеме Supabase.

Подключившись администратором **к целевой БД**, выполнить в `psql` этот SQL,
заменив имена двух существующих ролей в `\set`:

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

Мигратор должен владеть создаваемыми `schema_migrations` и четырьмя таблицами
приложения, чтобы изменять/удалять их в последующих миграциях. При существующей
схеме сначала проверить владельцев. Для выделенной БД администратор должен также
проверить, что API не получает `CREATE` через PUBLIC/другие роли; одних узких
GRANT ниже недостаточно для удаления уже унаследованных прав.

Из корня репозитория на Docker-хосте:

```sh
docker run --rm --env-file ../faqbot-release-0.0.1-secrets/migrate.env \
  --entrypoint /migrate faq-api:0.0.1-local
```

Ожидаются exit code 0 и JSON-лог с сообщением `migrations are up to date`.
Runner запускает только `up` и хранит версию в `schema_migrations`.
Применяется [000001_identity.up.sql](../../backend/migrations/000001_identity.up.sql):
`users`, `accounts`, `account_members`, `sessions` и `sessions_expiry_idx`.
API при старте таблицы не создаёт. Повторный запуск runner допустим;
не запускать его одновременно в каждой реплике API.

Из корня репозитория, с заданным `MIGRATOR_DATABASE_URL` той же БД:

```sh
psql "$MIGRATOR_DATABASE_URL" -v ON_ERROR_STOP=1 <<'SQL'
SELECT version, dirty FROM public.schema_migrations;
SELECT name, to_regclass('public.' || name) AS actual_table
FROM (VALUES ('users'), ('accounts'), ('account_members'), ('sessions')) AS t(name);
SELECT to_regclass('public.sessions_expiry_idx') AS expiry_index;
SELECT tablename, tableowner FROM pg_tables
WHERE schemaname='public'
  AND tablename IN ('schema_migrations','users','accounts','account_members','sessions')
ORDER BY tablename;
SQL
```

Ожидаются одна строка `version=1, dirty=false`, четыре ненулевых `actual_table`,
ненулевой `expiry_index` и согласованный владелец-migrator. На новой БД все
четыре таблицы данных пусты. Если версия выше 1, этот план не является инструкцией
понижения схемы. При `dirty=true`, отсутствующей истории или частично созданных
таблицах остановиться и следовать [восстановлению миграций](../MIGRATIONS.md#если-произошла-ошибка).
Не менять номер версии наугад и не исполнять up.sql напрямую вместо runner.

## 5. Выдать API права на данные

После миграции под владельцем таблиц/администратором, подключённым к той же БД,
выполнить в `psql`, заменив имя API-роли:

```sql
\set ON_ERROR_STOP on
\set api_role REPLACE_API_ROLE
BEGIN;
GRANT SELECT, INSERT ON public.users, public.accounts, public.account_members TO :"api_role";
GRANT SELECT, INSERT, UPDATE, DELETE ON public.sessions TO :"api_role";
COMMIT;
```

Это достаточный набор для текущего SQL-репозитория; SELECT для `accounts`
можно не выдавать при строго минимальной политике, поскольку API только вставляет
в неё строки. UPDATE/DELETE пользователей, компаний и членства текущему API
не нужны. Права на sequences и `schema_migrations` не нужны; UUID создаёт Go.
Права таблиц не появляются у API автоматически после миграции другой ролью.

Проверить соединение под ролью API. Из корня репозитория, с заранее заданным
`API_DATABASE_URL` (служебное имя переменной команды):

```sh
psql "$API_DATABASE_URL" -v ON_ERROR_STOP=1 <<'SQL'
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
   AND has_table_privilege(current_user,'public.sessions','DELETE')) AS sessions_access;
SQL
```

Ожидаются роль API, целевая БД, схема `public` и все пять булевых полей `true`.
Дополнительно под ролью API проверить отсутствие административных прав:

```sql
SELECT rolsuper, rolcreatedb, rolcreaterole, rolbypassrls
FROM pg_roles WHERE rolname=current_user;
SELECT has_schema_privilege(current_user,'public','CREATE') AS schema_create;
SELECT tablename FROM pg_tables
WHERE schemaname='public' AND tableowner=current_user;
```

Ожидаются четыре `false`, `schema_create=false` и отсутствие принадлежащих API
таблиц. Если это не так, администратор должен проверить членство/унаследованные
права и скорректировать их до запуска. Эти запросы не меняют grants.
Это проверка grants, а не замена пробного входа; политики RLS, дополнительные
ограничения и настройки существующей БД могут влиять на фактические запросы.

Обязательные seed-данные отсутствуют. При первом успешном входе или регистрации
`repository.Auth.Bootstrap` в одной транзакции создаёт `users`, `accounts` и
`account_members` с `role='owner'`, `audit_read=false`. Повторный вход того же
Supabase subject не создаёт вторую компанию. Предварительные и авторизованные
сессии создаёт приложение. Не вставлять пользователей/пароли вручную в Supabase
или в локальные таблицы ради установки.

## 6. Запустить API

Из корня репозитория на Docker-хосте:

```sh
docker run -d --name faq-api-0-0-1 --restart unless-stopped --stop-timeout 20 \
  --env-file ../faqbot-release-0.0.1-secrets/api.env \
  -p 127.0.0.1:8081:8080 faq-api:0.0.1-local
docker logs --tail 30 faq-api-0-0-1
curl --fail --retry 15 --retry-connrefused --retry-delay 1 --max-time 3 \
  -i http://127.0.0.1:8081/health/startup
curl --fail --retry 15 --retry-connrefused --retry-delay 1 --max-time 3 \
  -i http://127.0.0.1:8081/health/ready
curl -i http://127.0.0.1:8081/health/live
```

Ожидаются сообщение `API started` и HTTP 200 с `{"status":"ok"}` для всех трёх
GET. Повторы curl ограничены 15; при ненулевом коде завершения не переключать
прокси, проверить состояние контейнера и его логи. При заданном DATABASE_URL API проверяет подключение до открытия порта.
`startup` показывает запуск процесса, `ready` — готовность принимать запросы
и Ping БД с таймаутом 1s, `live` — ответ обработчика без проверки зависимостей.
Успех любой из этих проверок не доказывает наличие таблиц, DB grants,
доступность Supabase, корректность proxy или входа.

В образе нет Docker HEALTHCHECK. Проверки выполняет внешний оркестратор/мониторинг,
не `docker exec ... curl`. При остановке readiness меняется на 503, затем сервер
завершает запросы в пределах `HTTP_SHUTDOWN_TIMEOUT`; срок остановки контейнера
должен быть больше этого значения. Например, `docker stop --time 20` при `10s`.
Prometheus-экспорта и `/metrics` нет; health и JSON-логи не являются метриками.
Подробности — [наблюдаемость HTTP](../HTTP.md).

## 7. Настроить внешний HTTPS-прокси

Добавить внешний префикс **`/api/v1/auth/`** на upstream API, сохранив путь целиком:

| Метод | Внешний путь | Проверка установки |
| --- | --- | --- |
| GET | `/api/v1/auth/csrf` | 200, JSON `csrf_token`, cookie `faqbot_session` |
| POST | `/api/v1/auth/register` | 201 для нового тестового email |
| POST | `/api/v1/auth/login` | 200 для существующего тестового пользователя |
| GET | `/api/v1/auth/session` | 200 после входа; 401 до входа/после выхода |
| POST | `/api/v1/auth/logout` | 204 при действующей cookie и CSRF |

Это **пример** фрагмента Nginx внутри уже настроенного HTTPS `server` того
origin, который указан в AUTH_ORIGIN. Фактического конфига стенда в репозитории нет.
В примере Nginx работает на Docker-хосте, API опубликован на `127.0.0.1:8081`:

```nginx
location ^~ /api/v1/auth/ {
    proxy_pass http://127.0.0.1:8081;
    proxy_http_version 1.1;
    proxy_set_header Host $host;
    proxy_set_header X-Request-ID $request_id;
    proxy_set_header Origin $http_origin;
    proxy_set_header X-CSRF-Token $http_x_csrf_token;
    proxy_set_header Cookie $http_cookie;
    proxy_set_header Sec-Fetch-Site $http_sec_fetch_site;
    proxy_set_header X-Forwarded-Proto $scheme;
    proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;
    proxy_connect_timeout 5s;
    proxy_send_timeout 15s;
    proxy_read_timeout 35s;
    proxy_cache off;
    proxy_intercept_errors off;
}
```

У `proxy_pass` нет URI и завершающего `/`, поэтому `/api/v1/auth/` сохраняется.
Сохранить Content-Type и тело запроса; в ответе не скрывать/не переписывать
`Set-Cookie`, `Cache-Control: no-store`, `X-Request-ID`, `Retry-After`.
Не кэшировать ответы Auth. Cookie host-only, `Path=/`, `HttpOnly`, `Secure`,
`SameSite=Lax`; прокси не должен снимать Secure или менять Domain/Path.
POST требует точного Origin и X-CSRF-Token, связанного с cookie.
GET отвергает чужой непустой Origin и `Sec-Fetch-Site: cross-site`.
`Authorization: Bearer` не заменяет cookie этой реализации.

Для Nginx в другом контейнере loopback указывает на сам Nginx; заменить upstream
DNS-именем API в общей сети. `/health/*` можно оставить доступным только
внутреннему мониторингу; публиковать весь `/` или выдуманный `/metrics` не требуется.
Внутренний rate limit считает RemoteAddr, игнорируя X-Forwarded-For:
за одним прокси клиенты делят 20 csrf и 10 login/register запросов в минуту
на адрес и путь. Несколько реплик имеют отдельные лимиты в памяти.

После изменения фактического файла конфигурации, на хосте Nginx из каталога
его конфигурации выполнить `sudo nginx -t`; только при успехе —
`sudo nginx -s reload`. Проверить внешний HTTPS без `curl -k`.

## 8. Проверить авторизацию и данные

Сначала из корня репозитория на машине с доступом к внешнему HTTPS:

```sh
AUTH_ORIGIN='https://REPLACE_PUBLIC_HOST'
export AUTH_ORIGIN
curl -i "$AUTH_ORIGIN/api/v1/auth/csrf"
```

Ожидается 200, JSON с непустым `csrf_token` и Set-Cookie с указанными атрибутами.
Этот вызов проверяет запись предварительной сессии в БД и путь через proxy,
но ещё не обращается к Supabase. Не публиковать cookie/CSRF из вывода.

Следующая команда из корня репозитория проверяет один согласованный тестовый
аккаунт. Email и пароль вводятся интерактивно; пароль не записывается в shell
history/аргументы процесса. Cookie и CSRF остаются в памяти Python.
По умолчанию используется login; `export AUTH_CHECK_MODE=register` перед запуском
проверит регистрацию нового email и создаст реальные данные в обоих сервисах.
Для возврата к login выполнить `unset AUTH_CHECK_MODE`.

```sh
python3 - <<'PY'
import getpass
import http.cookiejar
import json
import os
import sys
import urllib.error
import urllib.request

origin = os.environ['AUTH_ORIGIN']
mode = os.environ.get('AUTH_CHECK_MODE', 'login')
if mode not in ('login', 'register'):
    raise SystemExit('AUTH_CHECK_MODE: login или register')
jar = http.cookiejar.CookieJar()
opener = urllib.request.build_opener(urllib.request.HTTPCookieProcessor(jar))
sys.stdin = open('/dev/tty')

def call(path, expected, body=None, csrf=None, request_origin=None):
    headers = {}
    if request_origin is not None:
        headers['Origin'] = request_origin
    if csrf is not None:
        headers['X-CSRF-Token'] = csrf
    data = None
    if body is not None:
        headers['Content-Type'] = 'application/json'
        data = json.dumps(body).encode()
    method = 'POST' if body is not None or path == 'logout' else 'GET'
    request = urllib.request.Request(origin + '/api/v1/auth/' + path,
                                     data=data, headers=headers, method=method)
    try:
        response = opener.open(request, timeout=40)
    except urllib.error.HTTPError as error:
        response = error
    with response:
        payload = response.read()
        print(path, response.code)
        if response.code != expected:
            raise SystemExit('Неожиданный HTTP-код; проверить сервер без вывода секретов')
        return json.loads(payload) if payload else None

initial = call('csrf', 200)
cookie = next(c for c in jar if c.name == 'faqbot_session')
assert cookie.secure and cookie.has_nonstandard_attr('HttpOnly')
assert cookie.get_nonstandard_attr('SameSite') == 'Lax' and cookie.path == '/'
call('session', 401)
email = input('Тестовый email: ')
password = getpass.getpass('Пароль: ')
credentials = {'email': email, 'password': password}
call(mode, 403, credentials, initial['csrf_token'], origin + '/')
session = call(mode, 201 if mode == 'register' else 200,
               credentials, initial['csrf_token'], origin)
assert session['identity']['role'] in ('owner', 'admin')
current = call('session', 200)
assert current['identity'] == session['identity']
print('identity:', json.dumps(session['identity']))
call('logout', 204, csrf=session['csrf_token'], request_origin=origin)
call('session', 401)
print('Проверка завершена')
PY
```

Ожидаемая последовательность кодов: `200, 401, 403, 200, 200, 204, 401`
для login; для register четвёртый код — 201. Скрипт не заменяет браузерную
проверку cookie, проверку отправки писем и изоляции двух компаний.
Для двух разных тестовых пользователей должны различаться `account_id`.
Первый вход/регистрация нового subject должны дать `owner` и `audit_read=false`.
Нельзя ожидать эти значения для существующего пользователя с изменёнными правами.

После первого входа нового тестового пользователя проверить БД под migrator
или диагностической ролью. Из корня репозитория, указав UUID из выведенной identity:

```sh
psql "$MIGRATOR_DATABASE_URL" -v ON_ERROR_STOP=1 \
  -v user_id=REPLACE_USER_UUID -v account_id=REPLACE_ACCOUNT_UUID <<'SQL'
SELECT u.id AS user_id, a.id AS account_id, m.role, m.audit_read
FROM public.users u
JOIN public.account_members m ON m.user_id=u.id
JOIN public.accounts a ON a.id=m.account_id
WHERE u.id=:'user_id'::uuid AND a.id=:'account_id'::uuid;
SELECT count(*) AS active_sessions
FROM public.sessions
WHERE user_id=:'user_id'::uuid AND account_id=:'account_id'::uuid
  AND revoked_at IS NULL AND expires_at>now() AND idle_expires_at>now();
SQL
```

Ожидается одна строка членства; для нового subject — `owner,false`.
После logout в изолированном тесте без других входов `active_sessions=0`.
Строка отозванной сессии может оставаться до следующей выдачи сессии, которая
очищает отозванные/истёкшие записи; отсутствие этой строки не является ошибкой.
При повторном входе того же subject сохраняются user_id/account_id.
Не проверять logout только общим числом строк `sessions`: предварительные
сессии имеют NULL user_id/account_id, а другие пользователи могут работать параллельно.

Основные причины отказа: 400 — JSON/Content-Type/email/пароль, 401 — сессия
или данные входа, 403 — Origin/CSRF, 404 — Auth не включён или неверный путь,
429 с Retry-After:60 — лимит, 502 — неожиданный ответ Supabase, включая signup
без токена, 503 — недоступность/429/5xx Supabase; 500 — локальная ошибка хранения.
При ошибке после успешного signup пользователь Supabase может уже существовать:
после исправления БД повторить login, а не signup.

## 9. Откат

Сначала закрыть/вернуть внешний маршрут на прежний upstream и остановить новый
контейнер. Из корня репозитория на Docker-хосте примера:

```sh
docker stop --time 20 faq-api-0-0-1
docker rm faq-api-0-0-1
```

Вернуть сохранённый предыдущий образ и его параметры средствами принятого
развёртывания, проверить его health и вход, затем переключить proxy.
Универсальной команды запуска прежней версии нет: её образ, конфигурация
и совместимость должны быть записаны перед установкой.

Обычный откат API сохраняет четыре новые таблицы и их данные. Не удалять
их автоматически. Уже созданные пользователи Supabase также сохраняются.
Если прежняя версия использует другую модель данных, сначала проверить её
совместимость или оставить Auth-маршрут закрытым до восстановления.

Встроенный `/migrate` выполняет только Up, команд Down/Force у него нет.
[000001_identity.down.sql](../../backend/migrations/000001_identity.down.sql)
удаляет `sessions`, `account_members`, `accounts`, `users` вместе со всеми
локальными компаниями, пользователями и сессиями. Он не удаляет пользователей
Supabase и не корректирует `schema_migrations`. Прямое исполнение down.sql
оставит историю версии несогласованной: следующий Up может ничего не сделать.
Для полного отката схемы нужен отдельный согласованный план: остановка всех
писателей, резервная копия, внешний CLI golang-migrate v4 с драйверами file/postgres
для `down 1` либо восстановление БД из проверенной копии. Перед ним ещё раз
проверить целевую БД и последствия удаления. Восстановление копии потеряет
все записи, появившиеся после неё; одно восстановление локальной БД не откатывает
изменения Supabase.

## Локальные проверки 04–05.11.2026

Из указанной ревизии через `git archive` собраны `cmd/api` и `cmd/migrate`
на Go 1.24.0. На отдельной временной PostgreSQL 18.3 runner выполнен дважды:
оба раза exit code 0, `version=1`, `dirty=false`. Выполнены SQL-блоки подготовки
ролей, выдачи прав и проверки схемы/прав из этого плана. Под ролью API проверены
csrf, register, login, session, logout и автоматическое создание owner-членства;
Supabase был заменён локальной HTTP-заглушкой. Повторный вход сохранил identity,
после logout активных сессий тестового пользователя не осталось. Все health
вернули 200; `/metrics` — 404. Это не проверка реального Supabase или HTTPS.

В текущем рабочем дереве `go test -count=1 ./...` с отдельной тестовой БД
выполнил 743 теста и подтеста без пропусков, `go vet ./...` прошёл.
Этот прогон включает дополнительные локальные тесты и контракты, которые
не входят в выбранную ревизию образа. Проверены относительные ссылки и синтаксис
shell/Python-примеров. Docker daemon недоступен, Nginx не установлен:
контейнерная сборка и `nginx -t` не выполнялись. Действующий сервер не изменялся.

## Что ещё требует проверки на стенде

Этот файл описывает порядок установки, а не протокол выполненного развёртывания.
По исходникам сверены ревизия, Dockerfile, пути, конфигурация, SQL, grants,
bootstrap, cookie, HTTP-коды и ограничения health. Требуют реальных параметров
и отдельной проверки: сборка/передача образа и его ID/digest, доступ к БД и TLS,
владельцы/унаследованные права/RLS, применение миграции и повторный запуск,
фактический Nginx-конфиг и `nginx -t`, HTTPS/сертификат, Supabase signup/login,
браузерная cookie, два пользователя, резервное копирование и восстановление.
Адреса БД/Supabase, секреты, внешний origin, сеть/upstream и предыдущий образ
заполняются владельцем стенда до установки.
