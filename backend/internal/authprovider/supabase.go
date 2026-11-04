package authprovider

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"faqbot/backend/internal/db"
	"faqbot/backend/internal/service"
)

type Supabase struct {
	base   string
	key    string
	client *http.Client
}

func NewSupabase(base, key string) *Supabase {
	return &Supabase{base: strings.TrimRight(base, "/"), key: key, client: &http.Client{Timeout: 5 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}}
}

func (s *Supabase) Authenticate(ctx context.Context, email, password string, register bool) (string, error) {
	path := "/auth/v1/token?grant_type=password"
	if register {
		path = "/auth/v1/signup"
	}
	body, err := json.Marshal(struct {
		Email    string `json:"email"`
		Password string `json:"password"`
	}{email, password})
	if err != nil {
		return "", errors.Join(service.ErrBadGateway, db.WrapError("encode authentication request", err))
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, s.base+path, bytes.NewReader(body))
	if err != nil {
		return "", errors.Join(service.ErrUnavailable, db.WrapError("create authentication request", err))
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("apikey", s.key)
	resp, err := s.client.Do(req)
	if err != nil {
		return "", &providerError{kind: service.ErrUnavailable, cause: err}
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode == http.StatusBadRequest || resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden || resp.StatusCode == http.StatusUnprocessableEntity {
		return "", service.ErrUnauthorized
	}
	if resp.StatusCode >= 500 || resp.StatusCode == http.StatusTooManyRequests {
		return "", service.ErrUnavailable
	}
	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusCreated {
		return "", service.ErrBadGateway
	}
	var data struct {
		AccessToken string `json:"access_token"`
		User        struct {
			ID string `json:"id"`
		} `json:"user"`
	}
	dec := json.NewDecoder(io.LimitReader(resp.Body, 64*1024+1))
	if err := dec.Decode(&data); err != nil {
		return "", errors.Join(service.ErrBadGateway, db.WrapError("decode authentication response", err))
	}
	if err := dec.Decode(new(any)); !errors.Is(err, io.EOF) {
		return "", errors.Join(service.ErrBadGateway, db.WrapError("finish authentication response", err))
	}
	if data.User.ID == "" || len(data.User.ID) > 128 || data.AccessToken == "" {
		return "", service.ErrBadGateway
	}
	if _, err := url.ParseRequestURI("https://identity.invalid/" + data.User.ID); err != nil {
		return "", service.ErrBadGateway
	}
	return data.User.ID, nil
}

type providerError struct {
	kind  error
	cause error
}

func (e *providerError) Error() string   { return e.kind.Error() }
func (e *providerError) Unwrap() []error { return []error{e.kind, e.cause} }
