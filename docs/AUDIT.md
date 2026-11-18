# Транзакционный аудит

Внутренний `service.AuditService.WriteInTx` проверяет событие и записывает его
через `repository.Audit` в переданную `*sql.Tx`. Сервис не начинает и не завершает
транзакцию. Схему создаёт миграция `000002_audit`. Подключение к авторизации,
бизнес-сервисам и HTTP API чтения пока отсутствует. Каталог ниже задаёт допустимые
коды для будущих вызывающих сервисов, а не перечень работающих API.

## Контракт записи

`models.AuditActor` передаётся отдельно: `Type` и `ID` берутся из доверенного
внутреннего контекста после проверки инициатора. Это не HTTP DTO и не замена
проверки членства, RBAC или принадлежности объекта компании.

`models.AuditEvent` содержит:

| Поле | Правило |
| --- | --- |
| `OperationID` | UUID сохранённой бизнес-операции, созданный сервером; одинаковый при повторной доставке |
| `Scope` | `tenant`, `platform` или `unattributed` |
| `AccountID` | UUID компании; обязателен только для tenant, в остальных scope пуст |
| `Action`, `EntityType`, `Outcome` | Точное сочетание из каталога |
| `EntityID` | UUID объекта; может быть пуст только для auth/audit/security/platform |
| `OccurredAt` | Ненулевое время события; после перевода в UTC год должен быть 1..9999 |
| `RequestID`, `TraceID` | Необязательные строки до 128 байт: буква/цифра в начале, затем буквы ASCII, цифры, `.`, `_`, `:`, `/`, `-` |
| `SourceService` | `go-api`, `ai-worker`, `auth-gateway` или `infra` |
| `Metadata` | `map[string]any` по whitelist ниже; nil сохраняется как `{}` |

`RequestID` и `TraceID` берутся из доверенного серверного контекста.
Нельзя напрямую копировать произвольный клиентский header или помещать сюда
секреты: допустимый синтаксис сам по себе не делает значение безопасным.
При повторной доставке сохраняются исходные request/trace и `SourceService`
события; контекст новой доставки не подменяет их.

Все UUID передаются в каноническом нижнем регистре `8-4-4-4-12`.
`user`, `platform_admin`, `partner` требуют UUID actor ID. `service` требует ID
из списка `SourceService`. `visitor` допускает UUID или пустой ID; `unknown`
требует пустой ID. `unknown` допустим только с исходом `denied` или `failure`.
`visitor`, `partner`, `unknown` допускаются только для auth/audit/security:
ими нельзя обозначать инициатора административного изменения объекта.

Tenant-события требуют существующую компанию. Platform scope допускает только
`platform.*` от `platform_admin` или `service`, без account.
Unattributed scope без account допускает `auth.login_failed`, `auth.access_denied`,
`audit.access_denied`, `security.rate_limit_exceeded`, `security.suspicious_activity`.
Прочие события без компании отклоняются до INSERT.

Сервис создаёт `ID` UUID, `SchemaVersion=1`, `RecordedAt` UTC и `EventKey`:
SHA-256 от `OperationID`, `Action`, `Scope`, `AccountID`, разделённых нулевым байтом.
Клиентский `request_id` не является ключом операции и не уникален.
Один operation/action в двух компаниях и два этапа одного request сохраняются отдельно.
`SchemaVersion=1` — версия формата записи, а не версия схемы PostgreSQL: после
`000002_audit` версия БД равна 2.

Повтор ключа сравнивает версию, scope/account, actor, action/entity, outcome,
request/trace, source и JSON metadata. Порядок ключей JSON значения не имеет.
Новые ID и времена `OccurredAt`/`RecordedAt` при повторной доставке игнорируются;
первая запись сохраняется целиком. Прочее расхождение возвращает
`service.ErrAuditConflict`, а не успешный повтор. Бизнес-состояние и повтор
самого изменения проверяет вызывающий сервис. При изоляции выше стандартной
READ COMMITTED возможную ошибку сериализации обрабатывает вызывающая операция.

## Каталог

| Action | EntityType | Outcome |
| --- | --- | --- |
| `auth.login_succeeded` | auth | success |
| `auth.login_failed` | auth | failure |
| `auth.access_denied` | auth | denied |
| `member.added`, `member.role_changed`, `member.removed` | member | success |
| `agent.created`, `agent.updated`, `agent.deleted`, `agent.published`, `agent.unpublished` | agent | success |
| `document.uploaded` | document | success |
| `document.delete_requested` | document | pending |
| `document.index_completed`, `document.delete_completed` | document | success |
| `document.index_failed`, `document.delete_failed` | document | failure |
| `coverage.started` | coverage | pending |
| `coverage.completed` | coverage | success |
| `coverage.failed` | coverage | failure |
| `widget.settings_updated`, `widget.integration_enabled`, `widget.integration_disabled`, `widget.embed_code_viewed` | widget | success |
| `partner.key_created`, `partner.key_rotated`, `partner.key_revoked` | partner | success |
| `package.requested` | package | pending |
| `package.request_approved`, `package.allocated`, `package.adjusted`, `package.refunded` | package | success |
| `package.request_rejected` | package | denied |
| `support.ticket_created`, `support.ticket_status_changed` | support | success |
| `deployment.requested` | deployment | pending |
| `deployment.status_changed` | deployment | success |
| `audit.exported` | audit | success |
| `audit.access_denied` | audit | denied |
| `security.rate_limit_exceeded`, `security.suspicious_activity` | security | denied |
| `platform.config_changed`, `platform.deploy_rolled_back` | platform | success |

Всего 43 кода. Старое `document.deleted_requested` отклоняется; каноническое
написание — `document.delete_requested`. Предложение `audit.viewed` не включено.
Событие requested означает принятую операцию, а не завершение внешнего действия.

## Metadata

Неизвестное поле, неверный Go-тип или значение отклоняет всё событие.
Значения не удаляются молча. После JSON-кодирования действует предел 4096 байт;
БД дополнительно ограничивает размер текстового представления JSONB 4096 байтами
и требует JSON object. Структурные поля ограничены отдельно. Текущий whitelist
с ограниченными значениями даёт допустимые payload меньше общего защитного
предела 4096 байт; длинное значение поля отклоняется ещё при проверке whitelist.

| Ключ | Тип Go | Допустимые значения |
| --- | --- | --- |
| `old_status`, `new_status` | string | pending, processing, ready, failed, deleting, deleted, running, completed, approved, rejected, cancelled, active, inactive, published, unpublished, open, closed, in_progress, resolved |
| `field_names_changed` | []string | 1..32 имени из списка ниже |
| `file_size`, `document_version`, `config_version` | int64 | ≥0; float64, int и строки не принимаются |
| `mime_type` | string | application/pdf, text/plain, text/markdown, text/csv, application/vnd.openxmlformats-officedocument.wordprocessingml.document |
| `job_result` | string | completed, failed, stale, duplicate, rejected |
| `denial_code` | string | unauthenticated, forbidden, tenant_mismatch, quota_exceeded, rate_limited, invalid_state, invalid_credentials, suspicious_activity |
| `package_id`, `integration_id`, `job_id` | string | UUID |
| `idempotency_key_hash` | string | SHA-256, 64 шестнадцатеричных символа в нижнем регистре |

Имена изменённых полей: `name`, `description`, `status`, `role`, `audit_read`,
`settings`, `widget_settings`, `integration_enabled`, `config`, `visibility`,
`request_limit`, `remaining_requests`. Сохраняются только названия, без значений.

Пароли, JWT, ключи, body, строки подключения, тексты вопросов/ответов, документов
и промптов, имена документов и произвольный before/after запрещены. IP и
user-agent не сохраняются до принятия политики персональных данных.

## Вызов в бизнес-транзакции

Пример общей границы операции; `apply` — внутренняя функция вызывающего сервиса,
которая проверяет права и принадлежность объекта и меняет бизнес-данные.
Actor, operation ID и account берутся из проверенного серверного контекста.

```go
audit := service.NewAudit(repository.NewAudit())
err := db.WithTx(ctx, pool, func(ctx context.Context, tx *sql.Tx) error {
    if err := apply(ctx, tx); err != nil {
        return err
    }
    return audit.WriteInTx(ctx, tx, actor, models.AuditEvent{
        OperationID: operationID,
        Scope: "tenant", AccountID: accountID,
        Action: "document.delete_requested",
        EntityType: "document", EntityID: documentID, Outcome: "pending",
        OccurredAt: occurredAt, RequestID: requestID, SourceService: "go-api",
        Metadata: map[string]any{"new_status": "deleting"},
    })
})
```

`WriteInTx` не вызывает Begin/Commit/Rollback. Общая транзакция создаётся
`db.WithTx(ctx, pool, fn)`; callback получает тот же `*sql.Tx` для бизнес-запросов
и репозитория аудита. Commit выполняется только после успешного callback; ошибка
commit также возвращается вызывающему коду. При панике deferred rollback
выполняется, сама паника не подавляется. Граница `db.WithTx` ограничена 5 секундами.

Ошибку записи нужно вернуть из callback: тогда `db.WithTx` отменит и изменение,
и аудит. После успешного `WriteInTx` последующая бизнес-ошибка также отменяет обе
записи. Отказ доступа, который требуется сохранить, записывается отдельной
транзакцией после отката отказанной операции. Внешние Storage/RabbitMQ/AI не
входят в SQL commit; для них потребуются outbox, состояние задания и аудит этапов.

Ошибки валидации не содержат входных значений. SQL-ошибки обёрнуты в
`db.WrapError`; причины доступны через `errors.Is/As`. Развёрнутые причины,
SQL и входные данные не выводятся в журналы.

## Схема и права

`account_id` ссылается на accounts без каскадного удаления. Это проверяет
существование компании, но не членство actor и не принадлежность объекта:
проверки выполняет вызывающий сервис до записи. Подставлять account из
непроверенного запроса нельзя; включение account в ключ не заменяет авторизацию. Actor/entity не имеют
каскадных ссылок: удаление бизнес-объекта не удаляет аудит. Индексы account/time,
account/action/time, account/entity/time и request/time завершаются `id DESC`
для устойчивого порядка при одинаковом времени.

Прикладной роли нужны `INSERT, SELECT` на audit_events: SELECT используется для
сравнения повтора. Запись остаётся только добавлением; UPDATE/DELETE/TRUNCATE и
владение таблицей запрещены. Роль доверенная и технически может читать журнал;
это не реализует tenant RBAC. API чтения/экспорта и его проверок прав пока нет.
Выдача и проверка grants — в [MIGRATIONS.md](MIGRATIONS.md#права-api-после-миграции).
Администратор БД может менять таблицы; абсолютная неизменяемость не заявляется.
Retention, архивирование и политика удаления персональных данных не назначены.

## Границы готовности

Реализованы модель события, валидация и каталог, SQL-репозиторий, миграция
и тесты общей транзакции/повторов/ограничений. Точка входа `cmd/api` не создаёт
AuditService, поэтому Auth и другие бизнес-операции пока не вызывают запись.
Следующие этапы — подключение бизнес-сценариев с доверенным контекстом компании,
проверки прав, API чтения и экспорт. Они не входят в готовые возможности.
Установка и откат инфраструктуры — [релиз 0.0.2](upgrade_cluster/release_0.0.2.md).
