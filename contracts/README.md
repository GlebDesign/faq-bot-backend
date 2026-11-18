# REST-контракт FAQ Bot

[openapi.yaml](openapi.yaml) версии `1.0.0-draft.4` описывает кабинет, виджет и
партнёрское подключение. [health.openapi.yaml](health.openapi.yaml) описывает
реальные проверки процесса. `x-implementation-status: implemented` отмечает
работающий Auth; `planned` — проектируемые операции. Наличие схемы не создаёт
HTTP endpoint. Полный список работающих маршрутов — [HTTP.md](../docs/HTTP.md).

[Fixtures](fixtures/identities.json) содержат только синтетические данные: две
компании, два агента первой компании, success/forbidden/empty/failure и шаги
пользовательских сценариев. Public ID не даёт кабинетных прав. Account и tenant
обозначают компанию, project — агента. Кабинетная cookie, visitor token и
partner signature имеют разные полномочия. Actor/account/agent выводятся из
проверенного серверного контекста, история чата загружается сервером.

Поля DTO обязательны, кроме явно необязательных параметров. Nullable поля
передаются как `null`; пустые списки — `[]`. `website` и `description` агента
обязательны в JSON и допускают пустую строку. SQL хранит их без потерь.
Cursor непрозрачен, привязан к scope, фильтрам и сортировке; последняя страница
имеет `next_cursor: null`. `If-Match` содержит quoted revision из ETag;
устаревшая версия даёт 412. Изменение знания и оформления повышает разные
revisions; active/published snapshots могут отставать от draft.

Бизнес-idempotency исключает Auth и первичную анонимную выдачу widget session.
До bootstrap visitor actor ещё не существует; ответы с bearer token не
кешируются и не выдаются повторно по бизнес-ключу. Partner signature policy
по-прежнему требует BE-055. Для чата scope ключа —
account+agent+visitor/member+operation. SHA-256 включает `conversation_id` и
точный текст `message`; сгенерированные сервером ID и транспортный `stream`
не меняют смысл операции. Повтор возвращает исходные ID, состояние и результат
на уровне репозитория. Проектируемый HTTP возвращает 202 ChatOperation для
accepted/generating с operation_id/message_id/conversation_id; 409 означает
конфликт значимого payload. Первый INSERT требует существующего scoped сообщения;
HTTP ChatService, создание сообщения и переходы состояния ещё не реализованы.
Нельзя коммитить новое candidate сообщение при replay: будущий вызывающий сервис
должен согласовать резервирование message ID и создание сообщения с этой записью
в одной бизнес-транзакции. Текущий отдельный repository API не предоставляет
такую оркестрацию. Другой текст или диалог с тем же ключом даёт conflict.

Auth проверяет тело до 4096 байт, email до 254 байт, пароль 1–1024 байта UTF-8.
`x-maxBytes`/`x-maxBodyBytes` фиксируют эти правила: стандартный `maxLength`
считает символы и сам не проверяет байты. Email проверяет Go `net/mail` без
display name. Неверный входной Request-ID заменяется; ответ всегда содержит
ASCII ID `[A-Za-z0-9_-]{1,80}`. Origin/CSRF, cookie, 405/Allow и HEAD проверяются
через настоящий локальный HTTP server; logout принимает и предварительную сессию.

Числовые лимиты проектируемых операций, MIME/ranges оформления и значения
fixtures остаются предложениями для review. Они не утверждают продуктовую
политику. Исключения — реальные ограничения Auth и структурные SQL-инварианты.
Срок replay/retention требует SRE-027, trial/charge/no-answer/test/Coverage и
возврат обращений — BE-029, ручные grants — SRE-028. Подпись, nonce и clock skew
партнёра требуют BE-055; consent copy/minimal contacts — BE-058.
Онлайн-оплаты нет, баланс измеряется обращениями компании. Support ticket
адресуется платформе, contact request — компании. Public sources содержат только
разрешённые excerpts/locators; исходные файлы и storage keys не передаются.

Из `backend`: `go test ./internal/handlers/... ./internal/repository ./internal/migration`.
Для SQL нужна отдельная `TEST_DATABASE_URL`, для audit-role проверки —
`TEST_AUDIT_ROLE`; инструкции — [DATABASE.md](../docs/DATABASE.md#sql-тесты).
Тесты проверяют refs, схемы, request/response/flow fixtures, DTO JSON parity,
реальные health/Auth и scoped SQL. `NoopAuthenticationFunc` в fixture validation
проверяет формат, но не runtime RBAC. Consumer review, cloud Supabase/no-email,
реальные AI/MQ/browser проверки и G01 ещё не выполнены.


Кабинетные mutations требуют cookie, Origin и X-CSRF-Token. У feedback
кабинетная security branch задаёт AND cookie+CSRF+Origin, альтернативная
WidgetSession не требует кабинетных headers. Чувствительные JSON responses
содержат обязательный Cache-Control: no-store. If-Match принимает только всю
строку quoted положительной revision, без постороннего текста.

Проектируемый GET /api/v1/widget/messages возвращает MessagePage собственного
conversation из WidgetSession. Допускаются только limit/cursor; account/agent/
conversation и browser history не задают scope. Extension
x-reject-unknown-query-parameters фиксирует запрет остальных query параметров.
Он проверяется fixture validator; runtime endpoint пока отсутствует.

flows.json — независимые примеры состояний, а не полный последовательный запуск.
[scenarios.json](fixtures/scenarios.json) связывает canonical request/response
примеры четырёх веток: owner knowledge/publish, visitor no-answer/contact,
support/manual packages и partner bootstrap. [identities.json](fixtures/identities.json)
задаёт известные account/agent/resource связи. Тесты проверяют route/body IDs,
tenant/agent связи и continuity identity/revisions; это форматные примеры,
не доказательство runtime RBAC, signature protocol или продуктовой оркестрации.
Name/message отвергают весь набор Unicode White_Space-only; содержательный текст
не нормализуется, значимые пробелы сохраняются.
