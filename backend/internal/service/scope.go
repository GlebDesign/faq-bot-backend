package service

import (
	"context"

	"faqbot/backend/internal/models"
)

type Scope struct {
	accountID string
	actorID   string
	role      string
	auditRead bool
}

func (s Scope) AccountID() string { return s.accountID }
func (s Scope) ActorID() string   { return s.actorID }
func (s Scope) Role() string      { return s.role }
func (s Scope) AuditRead() bool   { return s.auditRead }

func (a *Auth) Scope(ctx context.Context, token string) (Scope, string, error) {
	session, err := a.Session(ctx, token)
	if err != nil {
		return Scope{}, "", err
	}
	s, err := scopeFromSession(session)
	return s, session.CSRF, err
}

func scopeFromSession(session models.Session) (Scope, error) {
	if session.Identity == nil {
		return Scope{}, ErrUnauthorized
	}
	i := *session.Identity
	if !auditUUID.MatchString(i.UserID) || !auditUUID.MatchString(i.AccountID) {
		return Scope{}, ErrUnauthorized
	}
	return Scope{accountID: i.AccountID, actorID: i.UserID, role: i.Role, auditRead: i.AuditRead}, nil
}

func (s Scope) authorizeAgents() error {
	if s.accountID == "" || s.actorID == "" {
		return ErrUnauthorized
	}
	if s.role != "owner" && s.role != "admin" {
		return ErrForbidden
	}
	return nil
}

func (s Scope) CanReadAudit() bool {
	if s.authorizeAgents() != nil {
		return false
	}
	return s.role == "owner" || s.auditRead
}
