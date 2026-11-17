package service

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"

	"faqbot/backend/internal/models"
)

var (
	ErrInvalidAudit  = errors.New("invalid audit event")
	ErrAuditConflict = errors.New("audit event key conflicts with existing payload")
)

type AuditStore interface {
	InsertAudit(context.Context, *sql.Tx, models.AuditRecord) error
}

type AuditService struct{ store AuditStore }

func NewAudit(store AuditStore) *AuditService { return &AuditService{store: store} }

type auditAction struct {
	entity  string
	outcome string
}

var auditActions = map[string]auditAction{
	"auth.login_succeeded": {"auth", "success"}, "auth.login_failed": {"auth", "failure"},
	"auth.access_denied": {"auth", "denied"},
	"member.added":       {"member", "success"}, "member.role_changed": {"member", "success"}, "member.removed": {"member", "success"},
	"agent.created": {"agent", "success"}, "agent.updated": {"agent", "success"}, "agent.deleted": {"agent", "success"},
	"agent.published": {"agent", "success"}, "agent.unpublished": {"agent", "success"},
	"document.uploaded": {"document", "success"}, "document.delete_requested": {"document", "pending"},
	"document.index_completed": {"document", "success"}, "document.index_failed": {"document", "failure"},
	"document.delete_completed": {"document", "success"}, "document.delete_failed": {"document", "failure"},
	"coverage.started": {"coverage", "pending"}, "coverage.completed": {"coverage", "success"}, "coverage.failed": {"coverage", "failure"},
	"widget.settings_updated": {"widget", "success"}, "widget.integration_enabled": {"widget", "success"},
	"widget.integration_disabled": {"widget", "success"}, "widget.embed_code_viewed": {"widget", "success"},
	"partner.key_created": {"partner", "success"}, "partner.key_rotated": {"partner", "success"}, "partner.key_revoked": {"partner", "success"},
	"package.requested": {"package", "pending"}, "package.request_approved": {"package", "success"},
	"package.request_rejected": {"package", "denied"}, "package.allocated": {"package", "success"},
	"package.adjusted": {"package", "success"}, "package.refunded": {"package", "success"},
	"support.ticket_created": {"support", "success"}, "support.ticket_status_changed": {"support", "success"},
	"deployment.requested": {"deployment", "pending"}, "deployment.status_changed": {"deployment", "success"},
	"audit.exported": {"audit", "success"}, "audit.access_denied": {"audit", "denied"},
	"security.rate_limit_exceeded": {"security", "denied"}, "security.suspicious_activity": {"security", "denied"},
	"platform.config_changed": {"platform", "success"}, "platform.deploy_rolled_back": {"platform", "success"},
}

var auditUUID = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)
var auditToken = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:/-]*$`)
var auditHash = regexp.MustCompile(`^[0-9a-f]{64}$`)

func (a *AuditService) WriteInTx(ctx context.Context, tx *sql.Tx, actor models.AuditActor, event models.AuditEvent) error {
	if tx == nil || a.store == nil {
		return ErrInvalidAudit
	}
	definition, ok := auditActions[event.Action]
	if !ok || !auditUUID.MatchString(event.OperationID) || event.EntityType != definition.entity || event.Outcome != definition.outcome {
		return ErrInvalidAudit
	}
	if !validAuditScope(actor, event) || !validAuditActor(actor) || !validAuditEntity(event) || !validAuditActorEvent(actor, event) {
		return ErrInvalidAudit
	}
	if !boundedAuditToken(event.RequestID, 128, true) || !boundedAuditToken(event.TraceID, 128, true) {
		return ErrInvalidAudit
	}
	event.OccurredAt = event.OccurredAt.UTC()
	if !oneOf(event.SourceService, "go-api", "ai-worker", "auth-gateway", "infra") || event.OccurredAt.IsZero() || event.OccurredAt.Year() < 1 || event.OccurredAt.Year() > 9999 {
		return ErrInvalidAudit
	}
	metadata, err := auditMetadata(event.Metadata)
	if err != nil {
		return err
	}
	var id [16]byte
	if _, err := rand.Read(id[:]); err != nil {
		return fmt.Errorf("generate audit identifier: %w", err)
	}
	id[6] = (id[6] & 15) | 64
	id[8] = (id[8] & 63) | 128
	s := hex.EncodeToString(id[:])
	key := sha256.Sum256([]byte(strings.Join([]string{event.OperationID, event.Action, event.Scope, event.AccountID}, "\x00")))
	return a.store.InsertAudit(ctx, tx, models.AuditRecord{
		ID:       s[:8] + "-" + s[8:12] + "-" + s[12:16] + "-" + s[16:20] + "-" + s[20:],
		EventKey: hex.EncodeToString(key[:]), SchemaVersion: 1,
		Scope: event.Scope, AccountID: event.AccountID, Actor: actor, Action: event.Action,
		EntityType: event.EntityType, EntityID: event.EntityID, Outcome: event.Outcome,
		OccurredAt: event.OccurredAt, RecordedAt: time.Now().UTC(),
		RequestID: event.RequestID, TraceID: event.TraceID, SourceService: event.SourceService, Metadata: metadata,
	})
}

func validAuditScope(actor models.AuditActor, event models.AuditEvent) bool {
	switch event.Scope {
	case "tenant":
		return auditUUID.MatchString(event.AccountID) && !strings.HasPrefix(event.Action, "platform.")
	case "platform":
		return event.AccountID == "" && strings.HasPrefix(event.Action, "platform.") && oneOf(actor.Type, "platform_admin", "service")
	case "unattributed":
		return event.AccountID == "" && oneOf(event.Action, "auth.login_failed", "auth.access_denied", "audit.access_denied", "security.rate_limit_exceeded", "security.suspicious_activity")
	default:
		return false
	}
}

func validAuditActorEvent(actor models.AuditActor, event models.AuditEvent) bool {
	if actor.Type == "unknown" && !oneOf(event.Outcome, "denied", "failure") {
		return false
	}
	return !oneOf(actor.Type, "unknown", "visitor", "partner") || oneOf(event.EntityType, "auth", "audit", "security")
}

func validAuditActor(actor models.AuditActor) bool {
	switch actor.Type {
	case "user", "platform_admin", "partner":
		return auditUUID.MatchString(actor.ID)
	case "visitor":
		return actor.ID == "" || auditUUID.MatchString(actor.ID)
	case "unknown":
		return actor.ID == ""
	case "service":
		return oneOf(actor.ID, "go-api", "ai-worker", "auth-gateway", "infra")
	default:
		return false
	}
}

func validAuditEntity(event models.AuditEvent) bool {
	if oneOf(event.EntityType, "auth", "audit", "security", "platform") {
		return event.EntityID == "" || auditUUID.MatchString(event.EntityID)
	}
	return auditUUID.MatchString(event.EntityID)
}

func boundedAuditToken(value string, max int, optional bool) bool {
	return (optional && value == "") || (len(value) <= max && auditToken.MatchString(value))
}

func oneOf(value string, allowed ...string) bool {
	for _, candidate := range allowed {
		if value == candidate {
			return true
		}
	}
	return false
}

func auditMetadata(metadata map[string]any) ([]byte, error) {
	if metadata == nil {
		return []byte("{}"), nil
	}
	for key, value := range metadata {
		if !validAuditMetadataField(key, value) {
			return nil, ErrInvalidAudit
		}
	}
	data, err := json.Marshal(metadata)
	if err != nil || len(data) > 4096 {
		return nil, ErrInvalidAudit
	}
	return data, nil
}

func validAuditMetadataField(key string, value any) bool {
	s, isString := value.(string)
	switch key {
	case "old_status", "new_status":
		return isString && oneOf(s, "pending", "processing", "ready", "failed", "deleting", "deleted", "running", "completed", "approved", "rejected", "cancelled", "active", "inactive", "published", "unpublished", "open", "closed", "in_progress", "resolved")
	case "field_names_changed":
		fields, ok := value.([]string)
		if !ok || len(fields) == 0 || len(fields) > 32 {
			return false
		}
		for _, field := range fields {
			if !oneOf(field, "name", "description", "status", "role", "audit_read", "settings", "widget_settings", "integration_enabled", "config", "visibility", "request_limit", "remaining_requests") {
				return false
			}
		}
		return true
	case "file_size", "document_version", "config_version":
		n, ok := value.(int64)
		return ok && n >= 0
	case "mime_type":
		return isString && oneOf(s, "application/pdf", "text/plain", "text/markdown", "text/csv", "application/vnd.openxmlformats-officedocument.wordprocessingml.document")
	case "job_result":
		return isString && oneOf(s, "completed", "failed", "stale", "duplicate", "rejected")
	case "denial_code":
		return isString && oneOf(s, "unauthenticated", "forbidden", "tenant_mismatch", "quota_exceeded", "rate_limited", "invalid_state", "invalid_credentials", "suspicious_activity")
	case "package_id", "integration_id", "job_id":
		return isString && auditUUID.MatchString(s)
	case "idempotency_key_hash":
		return isString && auditHash.MatchString(s)
	default:
		return false
	}
}
