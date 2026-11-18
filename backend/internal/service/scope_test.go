package service

import (
	"errors"
	"testing"

	"faqbot/backend/internal/models"
)

func TestScopeRolesAndValueCopy(t *testing.T) {
	for _, role := range []string{"owner", "admin", "visitor", "partner", "platform_admin", "platform", ""} {
		for _, auditRead := range []bool{false, true} {
			i := &models.Identity{UserID: "00000000-0000-4000-8000-000000000001", AccountID: "00000000-0000-4000-8000-000000000002", Role: role, AuditRead: auditRead}
			s, err := scopeFromSession(models.Session{Identity: i})
			if err != nil {
				t.Fatal(err)
			}
			allowed := role == "owner" || role == "admin"
			if (s.authorizeAgents() == nil) != allowed || s.CanReadAudit() != (role == "owner" || role == "admin" && auditRead) {
				t.Fatalf("role=%s audit=%v", role, auditRead)
			}
			i.Role = "changed"
			i.AccountID = "changed"
			i.UserID = "changed"
			i.AuditRead = !auditRead
			if s.Role() != role || s.AccountID() == "changed" || s.ActorID() == "changed" || s.AuditRead() != auditRead {
				t.Fatal("mutable scope")
			}
		}
	}
	if _, err := scopeFromSession(models.Session{}); !errors.Is(err, ErrUnauthorized) {
		t.Fatal("anonymous scope")
	}
	if err := (Scope{}).authorizeAgents(); !errors.Is(err, ErrUnauthorized) {
		t.Fatal("zero scope")
	}
}

func TestAgentsRejectUnprivilegedScopeBeforeDependencies(t *testing.T) {
	a := NewAgents(nil, nil, nil, nil, []byte("01234567890123456789012345678901"))
	for _, role := range []string{"visitor", "partner", "platform_admin", "platform", ""} {
		for _, auditRead := range []bool{false, true} {
			s := Scope{accountID: "00000000-0000-4000-8000-000000000001", actorID: "00000000-0000-4000-8000-000000000002", role: role, auditRead: auditRead}
			_, err := a.Get(t.Context(), s, "00000000-0000-4000-8000-000000000003")
			if !errors.Is(err, ErrForbidden) {
				t.Fatalf("get %s: %v", role, err)
			}
			_, _, err = a.List(t.Context(), s, 20, "")
			if !errors.Is(err, ErrForbidden) {
				t.Fatalf("list %s: %v", role, err)
			}
			_, err = a.Create(t.Context(), s, "key", "request", models.AgentInput{Name: "name"})
			if !errors.Is(err, ErrForbidden) {
				t.Fatalf("create %s: %v", role, err)
			}
			_, err = a.Update(t.Context(), s, "id", "key", "request", models.AgentInput{Name: "name"}, 1)
			if !errors.Is(err, ErrForbidden) {
				t.Fatalf("update %s: %v", role, err)
			}
			_, err = a.Delete(t.Context(), s, "id", "key", "request")
			if !errors.Is(err, ErrForbidden) {
				t.Fatalf("delete %s: %v", role, err)
			}
		}
	}
}
