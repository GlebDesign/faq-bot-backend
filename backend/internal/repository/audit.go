package repository

import (
	"context"
	"database/sql"

	"faqbot/backend/internal/db"
	"faqbot/backend/internal/models"
	"faqbot/backend/internal/service"
)

type Audit struct{}

func NewAudit() *Audit { return &Audit{} }

func (a *Audit) InsertAudit(ctx context.Context, tx *sql.Tx, event models.AuditRecord) error {
	if tx == nil {
		return service.ErrInvalidAudit
	}
	args := []any{
		event.ID, event.EventKey, event.SchemaVersion, event.Scope, nullableAudit(event.AccountID),
		event.Actor.Type, nullableAudit(event.Actor.ID), event.Action, event.EntityType, nullableAudit(event.EntityID),
		event.Outcome, event.OccurredAt, event.RecordedAt, nullableAudit(event.RequestID), nullableAudit(event.TraceID), event.SourceService, string(event.Metadata),
	}
	result, err := tx.ExecContext(ctx, `INSERT INTO audit_events
		(id,event_key,schema_version,scope,account_id,actor_type,actor_id,action,entity_type,entity_id,
		outcome,occurred_at,recorded_at,request_id,trace_id,source_service,metadata)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17::jsonb)
		ON CONFLICT (event_key) DO NOTHING`, args...)
	if err != nil {
		return db.WrapError("insert audit event", err)
	}
	n, err := result.RowsAffected()
	if err != nil {
		return db.WrapError("count inserted audit events", err)
	}
	if n == 1 {
		return nil
	}
	var matches bool
	err = tx.QueryRowContext(ctx, `SELECT schema_version=$2 AND scope=$3
		AND account_id IS NOT DISTINCT FROM $4::uuid AND actor_type=$5
		AND actor_id IS NOT DISTINCT FROM $6::text AND action=$7 AND entity_type=$8
		AND entity_id IS NOT DISTINCT FROM $9::text AND outcome=$10
		AND request_id IS NOT DISTINCT FROM $11::text AND trace_id IS NOT DISTINCT FROM $12::text
		AND source_service=$13 AND metadata=$14::jsonb
		FROM audit_events WHERE event_key=$1`,
		event.EventKey, event.SchemaVersion, event.Scope, nullableAudit(event.AccountID), event.Actor.Type,
		nullableAudit(event.Actor.ID), event.Action, event.EntityType, nullableAudit(event.EntityID), event.Outcome,
		nullableAudit(event.RequestID), nullableAudit(event.TraceID), event.SourceService, string(event.Metadata)).Scan(&matches)
	if err != nil {
		return db.WrapError("compare repeated audit event", err)
	}
	if !matches {
		return service.ErrAuditConflict
	}
	return nil
}

func nullableAudit(value string) any {
	if value == "" {
		return nil
	}
	return value
}
