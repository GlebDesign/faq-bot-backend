package service

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"testing"
	"time"

	"faqbot/backend/internal/models"
)

type auditStore struct {
	events []models.AuditRecord
	err    error
}

func (s *auditStore) InsertAudit(_ context.Context, _ *sql.Tx, event models.AuditRecord) error {
	s.events = append(s.events, event)
	return s.err
}

func auditFixture() (models.AuditActor, models.AuditEvent) {
	return models.AuditActor{Type: "user", ID: "11111111-1111-4111-8111-111111111111"}, models.AuditEvent{
		OperationID: "22222222-2222-4222-8222-222222222222", Scope: "tenant", AccountID: "33333333-3333-4333-8333-333333333333",
		Action: "document.delete_requested", EntityType: "document", EntityID: "44444444-4444-4444-8444-444444444444", Outcome: "pending",
		OccurredAt: time.Date(2026, 11, 11, 12, 0, 0, 0, time.FixedZone("MSK", 3*3600)), RequestID: "request-1", TraceID: "trace-1", SourceService: "go-api",
		Metadata: map[string]any{"old_status": "ready", "new_status": "deleting", "document_version": int64(2)},
	}
}

func TestAuditWriteAndEventKeys(t *testing.T) {
	store := &auditStore{}
	audit := NewAudit(store)
	actor, event := auditFixture()
	for range 2 {
		if err := audit.WriteInTx(context.Background(), &sql.Tx{}, actor, event); err != nil {
			t.Fatal(err)
		}
		event.OccurredAt = event.OccurredAt.Add(time.Minute)
	}
	event.Action, event.Outcome = "document.delete_completed", "success"
	if err := audit.WriteInTx(context.Background(), &sql.Tx{}, actor, event); err != nil {
		t.Fatal(err)
	}
	event.AccountID = "55555555-5555-4555-8555-555555555555"
	if err := audit.WriteInTx(context.Background(), &sql.Tx{}, actor, event); err != nil {
		t.Fatal(err)
	}
	actor, operationEvent := auditFixture()
	operationEvent.OperationID = "66666666-6666-4666-8666-666666666666"
	if err := audit.WriteInTx(context.Background(), &sql.Tx{}, actor, operationEvent); err != nil {
		t.Fatal(err)
	}
	first := store.events[0]
	if first.SchemaVersion != 1 || first.OccurredAt.Location() != time.UTC || first.RecordedAt.Location() != time.UTC || !auditUUID.MatchString(first.ID) {
		t.Fatal("invalid generated fields")
	}
	if first.EventKey != store.events[1].EventKey || first.ID == store.events[1].ID {
		t.Fatal("redelivery must preserve key and generate a new identifier")
	}
	if first.EventKey == store.events[2].EventKey || store.events[2].EventKey == store.events[3].EventKey {
		t.Fatal("stage and tenant must be part of event key")
	}
	if first.EventKey == store.events[4].EventKey {
		t.Fatal("operation must be part of event key")
	}
}

func TestAuditOccurredAtUTCBoundaries(t *testing.T) {
	for _, test := range []struct {
		name     string
		occurred time.Time
		valid    bool
	}{
		{"before minimum UTC", time.Date(1, 1, 1, 1, 0, 0, 0, time.FixedZone("east", 3*3600)), false},
		{"after maximum UTC", time.Date(9999, 12, 31, 23, 0, 0, 0, time.FixedZone("west", -3*3600)), false},
		{"minimum UTC", time.Date(1, 1, 1, 3, 0, 0, 1, time.FixedZone("east", 3*3600)), true},
		{"maximum UTC", time.Date(9999, 12, 31, 20, 59, 59, 999999999, time.FixedZone("west", -3*3600)), true},
	} {
		t.Run(test.name, func(t *testing.T) {
			store := &auditStore{}
			actor, event := auditFixture()
			event.OccurredAt = test.occurred
			err := NewAudit(store).WriteInTx(context.Background(), &sql.Tx{}, actor, event)
			if !test.valid {
				if !errors.Is(err, ErrInvalidAudit) || len(store.events) != 0 {
					t.Fatal("out-of-range UTC time reached store")
				}
				return
			}
			if err != nil || len(store.events) != 1 {
				t.Fatalf("valid UTC boundary rejected: %v", err)
			}
			if stored := store.events[0].OccurredAt; stored.Location() != time.UTC || !stored.Equal(test.occurred) {
				t.Fatal("boundary time was not normalized to UTC")
			}
		})
	}
}

func TestAuditInvalidBeforeStore(t *testing.T) {
	for name, change := range map[string]func(*models.AuditActor, *models.AuditEvent){
		"unknown action":  func(_ *models.AuditActor, e *models.AuditEvent) { e.Action = "unknown.action" },
		"legacy spelling": func(_ *models.AuditActor, e *models.AuditEvent) { e.Action = "document.deleted_requested" },
		"proposed viewed": func(_ *models.AuditActor, e *models.AuditEvent) { e.Action = "audit.viewed" },
		"missing account": func(_ *models.AuditActor, e *models.AuditEvent) { e.AccountID = "" },
		"scope":           func(_ *models.AuditActor, e *models.AuditEvent) { e.Scope = "platform" },
		"user platform event": func(_ *models.AuditActor, e *models.AuditEvent) {
			e.Scope, e.AccountID, e.Action, e.EntityType, e.EntityID, e.Outcome = "platform", "", "platform.config_changed", "platform", "", "success"
		},
		"actor type":         func(a *models.AuditActor, _ *models.AuditEvent) { a.Type = "robot" },
		"anonymous mutation": func(a *models.AuditActor, _ *models.AuditEvent) { a.Type, a.ID = "unknown", "" },
		"visitor mutation":   func(a *models.AuditActor, _ *models.AuditEvent) { a.Type = "visitor" },
		"partner mutation":   func(a *models.AuditActor, _ *models.AuditEvent) { a.Type = "partner" },
		"service UUID":       func(a *models.AuditActor, _ *models.AuditEvent) { a.Type = "service" },
		"user without id":    func(a *models.AuditActor, _ *models.AuditEvent) { a.ID = "" },
		"entity type":        func(_ *models.AuditActor, e *models.AuditEvent) { e.EntityType = "agent" },
		"entity id":          func(_ *models.AuditActor, e *models.AuditEvent) { e.EntityID = "" },
		"outcome":            func(_ *models.AuditActor, e *models.AuditEvent) { e.Outcome = "success" },
		"operation":          func(_ *models.AuditActor, e *models.AuditEvent) { e.OperationID = "client-operation" },
		"time":               func(_ *models.AuditActor, e *models.AuditEvent) { e.OccurredAt = time.Time{} },
		"request newline":    func(_ *models.AuditActor, e *models.AuditEvent) { e.RequestID = "request\nsecret" },
		"request oversized":  func(_ *models.AuditActor, e *models.AuditEvent) { e.RequestID = strings.Repeat("x", 129) },
		"trace nul":          func(_ *models.AuditActor, e *models.AuditEvent) { e.TraceID = "trace\x00secret" },
		"source":             func(_ *models.AuditActor, e *models.AuditEvent) { e.SourceService = "secret" },
		"secret metadata":    func(_ *models.AuditActor, e *models.AuditEvent) { e.Metadata["JWT"] = "secret" },
		"unknown metadata":   func(_ *models.AuditActor, e *models.AuditEvent) { e.Metadata["body"] = "secret" },
		"metadata type":      func(_ *models.AuditActor, e *models.AuditEvent) { e.Metadata["document_version"] = "secret" },
		"metadata status":    func(_ *models.AuditActor, e *models.AuditEvent) { e.Metadata["new_status"] = "secret" },
		"metadata oversized field": func(_ *models.AuditActor, e *models.AuditEvent) {
			e.Metadata["field_names_changed"] = []string{strings.Repeat("secret", 1000)}
		},
		"metadata field": func(_ *models.AuditActor, e *models.AuditEvent) {
			e.Metadata["field_names_changed"] = []string{"password"}
		},
	} {
		t.Run(name, func(t *testing.T) {
			store := &auditStore{}
			actor, event := auditFixture()
			change(&actor, &event)
			err := NewAudit(store).WriteInTx(context.Background(), &sql.Tx{}, actor, event)
			if !errors.Is(err, ErrInvalidAudit) || strings.Contains(err.Error(), "secret") || len(store.events) != 0 {
				t.Fatalf("invalid audit reached store or unsafe error: %v", err)
			}
		})
	}
}

func TestAuditCatalog(t *testing.T) {
	expected := []string{
		"auth.login_succeeded|auth|success", "auth.login_failed|auth|failure", "auth.access_denied|auth|denied",
		"member.added|member|success", "member.role_changed|member|success", "member.removed|member|success",
		"agent.created|agent|success", "agent.updated|agent|success", "agent.deleted|agent|success", "agent.published|agent|success", "agent.unpublished|agent|success",
		"document.uploaded|document|success", "document.delete_requested|document|pending", "document.index_completed|document|success", "document.index_failed|document|failure", "document.delete_completed|document|success", "document.delete_failed|document|failure",
		"coverage.started|coverage|pending", "coverage.completed|coverage|success", "coverage.failed|coverage|failure",
		"widget.settings_updated|widget|success", "widget.integration_enabled|widget|success", "widget.integration_disabled|widget|success", "widget.embed_code_viewed|widget|success",
		"partner.key_created|partner|success", "partner.key_rotated|partner|success", "partner.key_revoked|partner|success",
		"package.requested|package|pending", "package.request_approved|package|success", "package.request_rejected|package|denied", "package.allocated|package|success", "package.adjusted|package|success", "package.refunded|package|success",
		"support.ticket_created|support|success", "support.ticket_status_changed|support|success",
		"deployment.requested|deployment|pending", "deployment.status_changed|deployment|success",
		"audit.exported|audit|success", "audit.access_denied|audit|denied",
		"security.rate_limit_exceeded|security|denied", "security.suspicious_activity|security|denied",
		"platform.config_changed|platform|success", "platform.deploy_rolled_back|platform|success",
	}
	if len(auditActions) != len(expected) {
		t.Fatalf("catalog size=%d want=%d", len(auditActions), len(expected))
	}
	for _, fixture := range expected {
		parts := strings.Split(fixture, "|")
		t.Run(parts[0], func(t *testing.T) {
			actor, event := auditFixture()
			event.Action, event.EntityType, event.Outcome = parts[0], parts[1], parts[2]
			if strings.HasPrefix(event.Action, "platform.") {
				event.Scope, event.AccountID = "platform", ""
				actor.Type = "platform_admin"
			}
			if err := NewAudit(&auditStore{}).WriteInTx(context.Background(), &sql.Tx{}, actor, event); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestAuditAnonymousAndService(t *testing.T) {
	for _, actor := range []models.AuditActor{{Type: "unknown"}, {Type: "visitor"}, {Type: "partner", ID: "11111111-1111-4111-8111-111111111111"}, {Type: "service", ID: "go-api"}} {
		_, event := auditFixture()
		event.Scope, event.AccountID, event.Action, event.EntityType, event.EntityID, event.Outcome = "unattributed", "", "auth.access_denied", "auth", "", "denied"
		if err := NewAudit(&auditStore{}).WriteInTx(context.Background(), &sql.Tx{}, actor, event); err != nil {
			t.Fatal(err)
		}
	}
}

func TestAuditMetadataWhitelist(t *testing.T) {
	metadata := map[string]any{
		"old_status": "pending", "new_status": "ready", "field_names_changed": []string{"name", "status"},
		"file_size": int64(1), "document_version": int64(1), "config_version": int64(1), "mime_type": "application/pdf",
		"job_result": "completed", "denial_code": "forbidden", "package_id": "11111111-1111-4111-8111-111111111111",
		"integration_id": "11111111-1111-4111-8111-111111111111", "job_id": "11111111-1111-4111-8111-111111111111",
		"idempotency_key_hash": strings.Repeat("a", 64),
	}
	if _, err := auditMetadata(metadata); err != nil {
		t.Fatal(err)
	}
	if data, err := auditMetadata(nil); err != nil || string(data) != "{}" {
		t.Fatal("nil metadata must be an object")
	}
	for _, key := range []string{"file_size", "document_version", "config_version"} {
		if validAuditMetadataField(key, int64(-1)) || validAuditMetadataField(key, float64(1)) {
			t.Fatal("invalid numeric metadata accepted")
		}
	}
}

func TestAuditTransactionAndStoreError(t *testing.T) {
	actor, event := auditFixture()
	cause := errors.New("store failed")
	audit := NewAudit(&auditStore{err: cause})
	if err := audit.WriteInTx(context.Background(), nil, actor, event); !errors.Is(err, ErrInvalidAudit) {
		t.Fatal("nil transaction accepted")
	}
	if err := audit.WriteInTx(context.Background(), &sql.Tx{}, actor, event); !errors.Is(err, cause) {
		t.Fatal("store cause lost")
	}
}

func TestAuditWebsiteFieldNameOnly(t *testing.T) {
	if !validAuditMetadataField("field_names_changed", []string{"website"}) {
		t.Fatal("website name rejected")
	}
	if validAuditMetadataField("website", "https://example.invalid") || validAuditMetadataField("field_names_changed", []string{"website_value"}) {
		t.Fatal("website whitelist widened")
	}
}
