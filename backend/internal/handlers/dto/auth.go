package dto

import "time"

type AuthCredentials struct {
	Email    string `json:"email"`
	Password string `json:"password"`
}

type AuthCSRF struct {
	CSRFToken string `json:"csrf_token"`
}

type AuthIdentity struct {
	UserID    string `json:"user_id"`
	AccountID string `json:"account_id"`
	Role      string `json:"role"`
	AuditRead bool   `json:"audit_read"`
}

type AuthSession struct {
	Identity  AuthIdentity `json:"identity"`
	ExpiresAt time.Time    `json:"expires_at"`
	CSRFToken string       `json:"csrf_token"`
}
