package service

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"errors"
	"time"

	"faqbot/backend/internal/models"
)

var (
	ErrUnauthorized = errors.New("unauthorized")
	ErrForbidden    = errors.New("forbidden")
	ErrUnavailable  = errors.New("authentication unavailable")
	ErrBadGateway   = errors.New("invalid authentication response")
)

type AuthStore interface {
	Bootstrap(context.Context, string) (models.Identity, error)
	CreateSession(context.Context, []byte, string, *models.Identity, time.Time, time.Time) error
	GetSession(context.Context, []byte, time.Time) (models.Session, error)
	RevokeSession(context.Context, []byte, time.Time) error
}

type AuthProvider interface {
	Authenticate(context.Context, string, string, bool) (string, error)
}

type Auth struct {
	store    AuthStore
	provider AuthProvider
}

func New(store AuthStore, provider AuthProvider) *Auth {
	return &Auth{store: store, provider: provider}
}

func sessionHash(token string) []byte {
	sum := sha256.Sum256([]byte(token))
	return sum[:]
}

func (a *Auth) CSRF(ctx context.Context, oldToken string) (models.Session, error) {
	if oldToken != "" {
		s, err := a.Session(ctx, oldToken)
		if err == nil {
			return s, nil
		}
		if !errors.Is(err, ErrUnauthorized) {
			return models.Session{}, err
		}
	}
	return a.issue(ctx, nil)
}

func (a *Auth) issue(ctx context.Context, identity *models.Identity) (models.Session, error) {
	now := time.Now().UTC()
	ttl := 10 * time.Minute
	if identity != nil {
		ttl = 8 * time.Hour
	}
	s := models.Session{Token: rand.Text(), CSRF: rand.Text(), Identity: identity, ExpiresAt: now.Add(ttl)}
	if err := a.store.CreateSession(ctx, sessionHash(s.Token), s.CSRF, identity, now, s.ExpiresAt); err != nil {
		return models.Session{}, err
	}
	return s, nil
}

func (a *Auth) Session(ctx context.Context, token string) (models.Session, error) {
	if len(token) != 26 {
		return models.Session{}, ErrUnauthorized
	}
	s, err := a.store.GetSession(ctx, sessionHash(token), time.Now().UTC())
	if err != nil {
		return models.Session{}, err
	}
	s.Token = token
	return s, nil
}

func (a *Auth) validate(ctx context.Context, token, csrf string) (models.Session, error) {
	s, err := a.Session(ctx, token)
	if err != nil {
		return models.Session{}, err
	}
	if csrf == "" || subtle.ConstantTimeCompare([]byte(s.CSRF), []byte(csrf)) != 1 {
		return models.Session{}, ErrForbidden
	}
	return s, nil
}

func (a *Auth) Authenticate(ctx context.Context, oldToken, csrf, email, password string, register bool) (models.Session, error) {
	if _, err := a.validate(ctx, oldToken, csrf); err != nil {
		return models.Session{}, err
	}
	subject, err := a.provider.Authenticate(ctx, email, password, register)
	if err != nil {
		return models.Session{}, err
	}
	identity, err := a.store.Bootstrap(ctx, subject)
	if err != nil {
		return models.Session{}, err
	}
	if err := a.store.RevokeSession(ctx, sessionHash(oldToken), time.Now().UTC()); err != nil {
		return models.Session{}, err
	}
	return a.issue(ctx, &identity)
}

func (a *Auth) Logout(ctx context.Context, token, csrf string) error {
	if _, err := a.validate(ctx, token, csrf); err != nil {
		return err
	}
	return a.store.RevokeSession(ctx, sessionHash(token), time.Now().UTC())
}
