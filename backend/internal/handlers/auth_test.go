package handlers

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"faqbot/backend/internal/models"
	"faqbot/backend/internal/service"
)

type fakeAuth struct {
	calls    int
	err      error
	identity *models.Identity
}

func (f *fakeAuth) CSRF(context.Context, string) (models.Session, error) {
	f.calls++
	return models.Session{Token: "new-token", CSRF: "csrf-value", ExpiresAt: time.Now().Add(time.Hour)}, f.err
}
func (f *fakeAuth) Authenticate(context.Context, string, string, string, string, bool) (models.Session, error) {
	f.calls++
	return models.Session{Token: "rotated-token", CSRF: "rotated-csrf", Identity: f.identity, ExpiresAt: time.Now().Add(8 * time.Hour)}, f.err
}
func (f *fakeAuth) Session(context.Context, string) (models.Session, error) {
	f.calls++
	return models.Session{CSRF: "session-csrf", Identity: f.identity, ExpiresAt: time.Now().Add(time.Hour)}, f.err
}
func (f *fakeAuth) Logout(context.Context, string, string) error { f.calls++; return f.err }

func authRequest(h http.Handler, method, path, origin, csrf, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req.Header.Set("Origin", origin)
	req.Header.Set("X-CSRF-Token", csrf)
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func TestAuthHTTPRejectsUnsafeRequests(t *testing.T) {
	for _, path := range []string{"login", "register", "logout"} {
		for _, tc := range []struct{ origin, csrf string }{{"", ""}, {"https://foreign.example", "csrf"}, {"https://faq.example", ""}} {
			f := &fakeAuth{}
			h := NewHandler(&Health{}, NewAuthHandler(f, "https://faq.example", false))
			rec := authRequest(h, http.MethodPost, "/api/v1/auth/"+path, tc.origin, tc.csrf, `{"email":"a@example.org","password":"password"}`)
			if rec.Code != 403 || f.calls != 0 {
				t.Fatalf("%s: status=%d calls=%d", path, rec.Code, f.calls)
			}
		}
	}
}

func TestAuthHTTPCookieAndIdentity(t *testing.T) {
	f := &fakeAuth{identity: &models.Identity{UserID: "u", AccountID: "a", Role: "owner"}}
	h := NewHandler(&Health{}, NewAuthHandler(f, "https://faq.example", false))
	rec := authRequest(h, http.MethodGet, "/api/v1/auth/csrf", "", "", "")
	if rec.Code != 200 {
		t.Fatal(rec.Code)
	}
	cookies := rec.Result().Cookies()
	if len(cookies) != 1 || !cookies[0].HttpOnly || !cookies[0].Secure || cookies[0].Domain != "" || cookies[0].SameSite != http.SameSiteLaxMode {
		t.Fatalf("unsafe cookies: %v", cookies)
	}
	rec = authRequest(h, http.MethodPost, "/api/v1/auth/login", "https://faq.example", "csrf", `{"email":"a@example.org","password":"password"}`)
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), `"account_id":"a"`) || strings.Contains(rec.Body.String(), "rotated-token") {
		t.Fatalf("%d %s", rec.Code, rec.Body.String())
	}
	if rec.Header().Get("Cache-Control") != "no-store" || rec.Header().Get("X-Request-ID") == "" {
		t.Fatal("missing standard headers")
	}
	rec = authRequest(h, http.MethodPost, "/api/v1/auth/logout", "https://faq.example", "csrf", "")
	if rec.Code != 204 || rec.Result().Cookies()[0].MaxAge != -1 {
		t.Fatal("logout did not remove cookie")
	}
}

func TestAuthHTTPValidationAndLimits(t *testing.T) {
	f := &fakeAuth{}
	h := NewHandler(&Health{}, NewAuthHandler(f, "https://faq.example", true))
	for _, body := range []string{`{"email":"a@example.org","password":"pw","account_id":"foreign"}`, `{} {}`, `{"email":"bad","password":"pw"}`, strings.Repeat("x", 5000)} {
		rec := authRequest(h, http.MethodPost, "/api/v1/auth/login", "https://faq.example", "csrf", body)
		if rec.Code != 400 || f.calls != 0 {
			t.Fatalf("status=%d calls=%d", rec.Code, f.calls)
		}
	}
	for i := 0; i < 21; i++ {
		rec := authRequest(h, http.MethodGet, "/api/v1/auth/csrf", "", "", "")
		if i == 20 && rec.Code != 429 {
			t.Fatal("pre-session rate limit missing")
		}
	}
	rec := authRequest(h, http.MethodGet, "/api/v1/auth/session", "", "", "")
	if rec.Code != 401 {
		t.Fatal("pre-session granted identity")
	}
}

func TestAuthHTTPErrors(t *testing.T) {
	for _, tc := range []struct {
		err    error
		status int
	}{{service.ErrUnauthorized, 401}, {service.ErrForbidden, 403}, {service.ErrUnavailable, 503}, {service.ErrBadGateway, 502}, {errors.New("secret"), 500}} {
		f := &fakeAuth{err: tc.err}
		h := NewHandler(&Health{}, NewAuthHandler(f, "https://faq.example", false))
		rec := authRequest(h, http.MethodGet, "/api/v1/auth/session", "", "", "")
		if rec.Code != tc.status || strings.Contains(rec.Body.String(), "secret") {
			t.Fatalf("%d %s", rec.Code, rec.Body.String())
		}
	}
}

func TestAuthCookieSecureOrigin(t *testing.T) {
	for _, tc := range []struct {
		origin   string
		insecure bool
		secure   bool
	}{
		{"https://faq.example", true, true},
		{"http://localhost:8080", true, false},
		{"https://faq.example", false, true},
	} {
		t.Run(tc.origin, func(t *testing.T) {
			h := NewHandler(&Health{}, NewAuthHandler(&fakeAuth{}, tc.origin, tc.insecure))
			rec := authRequest(h, http.MethodGet, "/api/v1/auth/csrf", "", "", "")
			cookies := rec.Result().Cookies()
			if rec.Code != http.StatusOK || len(cookies) != 1 || cookies[0].Secure != tc.secure {
				t.Fatalf("status=%d cookies=%v", rec.Code, cookies)
			}
		})
	}
}
