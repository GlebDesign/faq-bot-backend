package repository

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"faqbot/backend/internal/authprovider"
	"faqbot/backend/internal/handlers"
	"faqbot/backend/internal/handlers/dto"
	"faqbot/backend/internal/service"
)

func TestAuthHTTPWithPostgreSQL(t *testing.T) {
	store := testAuth(t)
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var input dto.AuthCredentials
		if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
			t.Error(err)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		if r.URL.Path != "/auth/v1/signup" || r.Header.Get("apikey") != "fixture-key" {
			t.Error("unexpected provider request")
		}
		if err := json.NewEncoder(w).Encode(map[string]any{"access_token": "fixture-private-token", "user": map[string]string{"id": input.Email}}); err != nil {
			t.Error(err)
		}
	}))
	defer provider.Close()
	auth := service.New(store, authprovider.NewSupabase(provider.URL, "fixture-key"))
	h := handlers.NewHandler(&handlers.Health{}, handlers.NewAuthHandler(auth, "https://faq.example", false))
	request := func(method, path, body, csrf string, cookie *http.Cookie) *httptest.ResponseRecorder {
		t.Helper()
		r := httptest.NewRequest(method, path, strings.NewReader(body))
		r.Header.Set("Origin", "https://faq.example")
		r.Header.Set("Content-Type", "application/json")
		r.Header.Set("X-CSRF-Token", csrf)
		if cookie != nil {
			r.AddCookie(cookie)
		}
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w
	}
	register := func(email string) (dto.AuthSession, *http.Cookie) {
		t.Helper()
		pre := request(http.MethodGet, "/api/v1/auth/csrf", "", "", nil)
		if pre.Code != http.StatusOK || len(pre.Result().Cookies()) != 1 {
			t.Fatalf("presession status=%d", pre.Code)
		}
		var csrf dto.AuthCSRF
		if err := json.Unmarshal(pre.Body.Bytes(), &csrf); err != nil {
			t.Fatal(err)
		}
		body := `{"email":"` + email + `","password":"fixture-password"}`
		bad := request(http.MethodPost, "/api/v1/auth/register", body, "wrong", pre.Result().Cookies()[0])
		if bad.Code != http.StatusForbidden {
			t.Fatalf("wrong CSRF status=%d", bad.Code)
		}
		result := request(http.MethodPost, "/api/v1/auth/register", body, csrf.CSRFToken, pre.Result().Cookies()[0])
		if result.Code != http.StatusCreated || len(result.Result().Cookies()) != 1 {
			t.Fatalf("register status=%d body=%s", result.Code, result.Body.String())
		}
		cookie := result.Result().Cookies()[0]
		if cookie.Value == pre.Result().Cookies()[0].Value || !cookie.Secure || !cookie.HttpOnly || cookie.Domain != "" {
			t.Fatal("session cookie was not safely rotated")
		}
		var session dto.AuthSession
		if err := json.Unmarshal(result.Body.Bytes(), &session); err != nil {
			t.Fatal(err)
		}
		if session.CSRFToken == csrf.CSRFToken || strings.Contains(result.Body.String(), "fixture-private-token") {
			t.Fatal("CSRF not rotated or provider token exposed")
		}
		if old := request(http.MethodGet, "/api/v1/auth/session", "", "", pre.Result().Cookies()[0]); old.Code != http.StatusUnauthorized {
			t.Fatal("old presession still grants access")
		}
		return session, cookie
	}
	first, firstCookie := register("first@example.org")
	second, secondCookie := register("second@example.org")
	if first.Identity.AccountID == second.Identity.AccountID {
		t.Fatal("two subjects share a company")
	}
	foreign := request(http.MethodGet, "/api/v1/auth/session?account_id="+second.Identity.AccountID, "", "", firstCookie)
	var actual dto.AuthSession
	if err := json.Unmarshal(foreign.Body.Bytes(), &actual); err != nil || foreign.Code != http.StatusOK || actual.Identity.AccountID != first.Identity.AccountID {
		t.Fatal("browser selected foreign identity", err)
	}
	if _, err := store.pool.Exec(`UPDATE account_members SET role='admin' WHERE user_id=$1`, second.Identity.UserID); err != nil {
		t.Fatal(err)
	}
	admin := request(http.MethodGet, "/api/v1/auth/session", "", "", secondCookie)
	if err := json.Unmarshal(admin.Body.Bytes(), &actual); err != nil || actual.Identity.Role != "admin" || actual.Identity.AuditRead {
		t.Fatal("membership role was not read from SQL", err)
	}
	if logout := request(http.MethodPost, "/api/v1/auth/logout", "", second.CSRFToken, secondCookie); logout.Code != http.StatusNoContent {
		t.Fatal("logout failed", logout.Code)
	}
	if reused := request(http.MethodGet, "/api/v1/auth/session", "", "", secondCookie); reused.Code != http.StatusUnauthorized {
		t.Fatal("logout reuse accepted")
	}
	if _, err := store.pool.Exec(`DELETE FROM account_members WHERE user_id=$1`, first.Identity.UserID); err != nil {
		t.Fatal(err)
	}
	if removed := request(http.MethodGet, "/api/v1/auth/session", "", "", firstCookie); removed.Code != http.StatusUnauthorized {
		t.Fatal("removed membership accepted")
	}
}

func TestBootstrapRollbackThenRetry(t *testing.T) {
	store := testAuth(t)
	if _, err := store.pool.Exec(`ALTER TABLE accounts ADD CONSTRAINT reject_bootstrap CHECK (false)`); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Bootstrap(context.Background(), "verified-subject"); err == nil {
		t.Fatal("missing SQL failure")
	}
	var users int
	if err := store.pool.QueryRow(`SELECT count(*) FROM users`).Scan(&users); err != nil || users != 0 {
		t.Fatal("partial bootstrap persisted", users, err)
	}
	if _, err := store.pool.Exec(`ALTER TABLE accounts DROP CONSTRAINT reject_bootstrap`); err != nil {
		t.Fatal(err)
	}
	first, err := store.Bootstrap(context.Background(), "verified-subject")
	if err != nil {
		t.Fatal(err)
	}
	again, err := store.Bootstrap(context.Background(), "verified-subject")
	if err != nil || first != again {
		t.Fatal("retry did not reuse bootstrap", err)
	}
}

func TestConcurrentAuthenticationConsumesSessionOnce(t *testing.T) {
	for _, tc := range []struct {
		name     string
		attempts int
		logout   bool
	}{
		{"parallel_login", 2, false},
		{"logout_during_login", 1, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store := testAuth(t)
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			entered := make(chan struct{}, tc.attempts)
			release := make(chan struct{})
			provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				entered <- struct{}{}
				select {
				case <-release:
					_, _ = w.Write([]byte(`{"access_token":"fixture-token","user":{"id":"concurrent-subject"}}`))
				case <-r.Context().Done():
				}
			}))
			defer provider.Close()
			defer cancel()
			auth := service.New(store, authprovider.NewSupabase(provider.URL, "fixture-key"))
			pre, err := auth.CSRF(ctx, "")
			if err != nil {
				t.Fatal(err)
			}
			results := make(chan error, tc.attempts)
			for range tc.attempts {
				go func() {
					_, err := auth.Authenticate(ctx, pre.Token, pre.CSRF, "fixture@example.org", "password", false)
					results <- err
				}()
			}
			for range tc.attempts {
				select {
				case <-entered:
				case <-ctx.Done():
					t.Fatal("authentication did not reach provider")
				}
			}
			if tc.logout {
				if err := auth.Logout(ctx, pre.Token, pre.CSRF); err != nil {
					t.Fatal(err)
				}
			}
			close(release)
			successes := 0
			for range tc.attempts {
				select {
				case err := <-results:
					if err == nil {
						successes++
					} else if !errors.Is(err, service.ErrUnauthorized) {
						t.Fatal(err)
					}
				case <-ctx.Done():
					t.Fatal("authentication did not finish")
				}
			}
			want := 1
			if tc.logout {
				want = 0
			}
			if successes != want {
				t.Fatalf("successful logins=%d want=%d", successes, want)
			}
			var sessions int
			if err := store.pool.QueryRowContext(ctx, `SELECT count(*) FROM sessions WHERE user_id IS NOT NULL AND revoked_at IS NULL`).Scan(&sessions); err != nil || sessions != want {
				t.Fatalf("active sessions=%d want=%d err=%v", sessions, want, err)
			}
		})
	}
}
