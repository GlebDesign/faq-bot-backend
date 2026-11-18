package models

import "time"

type Identity struct {
	UserID    string `json:"user_id"`
	AccountID string `json:"account_id"`
	Role      string `json:"role"`
	AuditRead bool   `json:"audit_read"`
}

type Session struct {
	Token     string    `json:"-"`
	CSRF      string    `json:"csrf_token"`
	Identity  *Identity `json:"identity"`
	ExpiresAt time.Time `json:"expires_at"`
}
