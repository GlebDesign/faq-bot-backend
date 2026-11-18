package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net"
	"net/http"
	"net/mail"
	"strings"
	"sync"
	"time"

	"faqbot/backend/internal/handlers/dto"
	"faqbot/backend/internal/models"
	"faqbot/backend/internal/service"
)

type AuthService interface {
	CSRF(context.Context, string) (models.Session, error)
	Authenticate(context.Context, string, string, string, string, bool) (models.Session, error)
	Session(context.Context, string) (models.Session, error)
	Logout(context.Context, string, string) error
}

type authAttempts struct {
	since time.Time
	count int
}

type AuthHandler struct {
	auth     AuthService
	origin   string
	insecure bool
	mu       sync.Mutex
	attempts map[string]authAttempts
}

func NewAuthHandler(auth AuthService, origin string, insecureDevelopment bool) *AuthHandler {
	return &AuthHandler{auth: auth, origin: origin, insecure: insecureDevelopment && strings.HasPrefix(origin, "http://"), attempts: make(map[string]authAttempts)}
}

func (a *AuthHandler) allow(r *http.Request, limit int) bool {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	key := host + ":" + r.URL.Path
	now := time.Now()
	a.mu.Lock()
	defer a.mu.Unlock()
	for key, entry := range a.attempts {
		if now.Sub(entry.since) >= time.Minute {
			delete(a.attempts, key)
		}
	}
	entry, ok := a.attempts[key]
	if !ok {
		if len(a.attempts) >= 4096 {
			return false
		}
		entry.since = now
	}
	if entry.count >= limit {
		return false
	}
	entry.count++
	a.attempts[key] = entry
	return true
}

func (a *AuthHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {

	path := strings.TrimPrefix(r.URL.Path, "/api/v1/auth/")
	method := http.MethodPost
	if path == "csrf" || path == "session" {
		method = http.MethodGet
	}
	if path != "csrf" && path != "session" && path != "register" && path != "login" && path != "logout" {
		writeError(w, http.StatusNotFound, "not_found", "Ресурс не найден")
		return
	}
	if r.Method != method {
		w.Header().Set("Allow", method)
		writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "Метод не поддерживается")
		return
	}
	if (r.Method == http.MethodPost && (r.Header.Get("Origin") != a.origin || r.Header.Get("X-CSRF-Token") == "")) || (r.Method == http.MethodGet && ((r.Header.Get("Origin") != "" && r.Header.Get("Origin") != a.origin) || r.Header.Get("Sec-Fetch-Site") == "cross-site")) {
		writeError(w, http.StatusForbidden, "forbidden", "Запрос запрещён")
		return
	}
	token := ""
	if cookie, err := r.Cookie("faqbot_session"); err == nil {
		token = cookie.Value
	}
	switch path {
	case "csrf":
		if !a.allow(r, 20) {
			a.rateLimit(w)
			return
		}
		session, err := a.auth.CSRF(r.Context(), token)
		if err != nil {
			a.writeAuthError(w, err)
			return
		}
		a.cookie(w, session.Token, session.ExpiresAt)
		writeJSON(w, http.StatusOK, dto.AuthCSRF{CSRFToken: session.CSRF})
	case "session":
		session, err := a.auth.Session(r.Context(), token)
		if err != nil {
			a.writeAuthError(w, err)
			return
		}
		if session.Identity == nil {
			a.writeAuthError(w, service.ErrUnauthorized)
			return
		}
		a.writeSession(w, http.StatusOK, session)
	case "logout":
		if err := a.auth.Logout(r.Context(), token, r.Header.Get("X-CSRF-Token")); err != nil {
			a.writeAuthError(w, err)
			return
		}
		a.cookie(w, "", time.Unix(1, 0))
		w.WriteHeader(http.StatusNoContent)
	default:
		if !a.allow(r, 10) {
			a.rateLimit(w)
			return
		}
		mediaType, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
		if err != nil || mediaType != "application/json" {
			writeError(w, http.StatusBadRequest, "invalid_request", "Ожидается JSON")
			return
		}
		r.Body = http.MaxBytesReader(w, r.Body, 4096)
		dec := json.NewDecoder(r.Body)
		dec.DisallowUnknownFields()
		var input dto.AuthCredentials
		if err := dec.Decode(&input); err != nil {
			writeError(w, http.StatusBadRequest, "invalid_request", "Некорректный JSON")
			return
		}
		if err := dec.Decode(new(any)); !errors.Is(err, io.EOF) {
			writeError(w, http.StatusBadRequest, "invalid_request", "Некорректный JSON")
			return
		}
		address, err := mail.ParseAddress(input.Email)
		if err != nil || address.Address != input.Email || len(input.Email) > 254 || len(input.Password) < 1 || len(input.Password) > 1024 {
			writeError(w, http.StatusBadRequest, "invalid_request", "Некорректные данные входа")
			return
		}
		session, err := a.auth.Authenticate(r.Context(), token, r.Header.Get("X-CSRF-Token"), input.Email, input.Password, path == "register")
		if err != nil {
			a.writeAuthError(w, err)
			return
		}
		a.cookie(w, session.Token, session.ExpiresAt)
		status := http.StatusOK
		if path == "register" {
			status = http.StatusCreated
		}
		a.writeSession(w, status, session)
	}
}

func (a *AuthHandler) cookie(w http.ResponseWriter, token string, expires time.Time) {
	maxAge := int(time.Until(expires).Seconds())
	if token == "" {
		maxAge = -1
	}
	http.SetCookie(w, &http.Cookie{Name: "faqbot_session", Value: token, Path: "/", HttpOnly: true, Secure: !a.insecure, SameSite: http.SameSiteLaxMode, Expires: expires, MaxAge: maxAge})
}

func (a *AuthHandler) writeSession(w http.ResponseWriter, status int, s models.Session) {
	if s.Identity == nil {
		a.writeAuthError(w, service.ErrUnauthorized)
		return
	}
	i := s.Identity
	writeJSON(w, status, dto.AuthSession{Identity: dto.AuthIdentity{UserID: i.UserID, AccountID: i.AccountID, Role: i.Role, AuditRead: i.AuditRead}, ExpiresAt: s.ExpiresAt, CSRFToken: s.CSRF})
}

func (a *AuthHandler) rateLimit(w http.ResponseWriter) {
	w.Header().Set("Retry-After", "60")
	writeError(w, http.StatusTooManyRequests, "rate_limited", "Слишком много запросов")
}

func (a *AuthHandler) writeAuthError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, service.ErrUnauthorized):
		writeError(w, http.StatusUnauthorized, "unauthorized", "Вход не выполнен")
	case errors.Is(err, service.ErrForbidden):
		writeError(w, http.StatusForbidden, "forbidden", "Запрос запрещён")
	case errors.Is(err, service.ErrUnavailable):
		writeError(w, http.StatusServiceUnavailable, "provider_unavailable", "Сервис входа недоступен")
	case errors.Is(err, service.ErrBadGateway):
		writeError(w, http.StatusBadGateway, "provider_bad_gateway", "Некорректный ответ сервиса входа")
	default:
		writeError(w, http.StatusInternalServerError, "internal_error", "Внутренняя ошибка")
	}
}
