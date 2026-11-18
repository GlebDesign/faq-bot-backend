package migration

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"faqbot/backend/internal/authprovider"
	"faqbot/backend/internal/handlers"
	"faqbot/backend/internal/handlers/dto"
	"faqbot/backend/internal/models"
	"faqbot/backend/internal/repository"
	"faqbot/backend/internal/service"
)

func TestAuthHTTPWithPostgreSQL(t *testing.T) {
	data, err := os.ReadFile("testdata/registration-v1.json")
	if err != nil {
		t.Fatal(err)
	}
	var fixture struct {
		HTTPStatus int `json:"http_status"`
	}
	if err := json.Unmarshal(data, &fixture); err != nil {
		t.Fatal(err)
	}
	_, pool, ctx := registrationDatabase(t)
	store := repository.NewAuth(pool)
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
		r := httptest.NewRequestWithContext(ctx, method, path, strings.NewReader(body))
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
		if result.Code != fixture.HTTPStatus || len(result.Result().Cookies()) != 1 {
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
	again, _ := register("first@example.org")
	if again.Identity != first.Identity {
		t.Fatal("repeat registration changed identity")
	}
	registrationCounts(t, ctx, pool, 2, 2, 2, 2)
	foreign := request(http.MethodGet, "/api/v1/auth/session?account_id="+second.Identity.AccountID, "", "", firstCookie)
	var actual dto.AuthSession
	if err := json.Unmarshal(foreign.Body.Bytes(), &actual); err != nil || foreign.Code != http.StatusOK || actual.Identity.AccountID != first.Identity.AccountID {
		t.Fatal("browser selected foreign identity", err)
	}
	if _, err := pool.ExecContext(ctx, `UPDATE account_members SET role='admin' WHERE user_id=$1`, second.Identity.UserID); err != nil {
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
	if _, err := pool.ExecContext(ctx, `DELETE FROM account_members WHERE user_id=$1`, first.Identity.UserID); err != nil {
		t.Fatal(err)
	}
	if removed := request(http.MethodGet, "/api/v1/auth/session", "", "", firstCookie); removed.Code != http.StatusUnauthorized {
		t.Fatal("removed membership accepted")
	}
}

func TestBootstrapRollbackThenRetry(t *testing.T) {
	_, pool, ctx := registrationDatabase(t)
	store := repository.NewAuth(pool)
	if _, err := pool.ExecContext(ctx, `ALTER TABLE accounts ADD CONSTRAINT reject_bootstrap CHECK (false)`); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Bootstrap(ctx, "verified-subject"); err == nil {
		t.Fatal("missing SQL failure")
	}
	var users int
	if err := pool.QueryRowContext(ctx, `SELECT count(*) FROM users`).Scan(&users); err != nil || users != 0 {
		t.Fatal("partial bootstrap persisted", users, err)
	}
	if _, err := pool.ExecContext(ctx, `ALTER TABLE accounts DROP CONSTRAINT reject_bootstrap`); err != nil {
		t.Fatal(err)
	}
	first, err := store.Bootstrap(ctx, "verified-subject")
	if err != nil {
		t.Fatal(err)
	}
	again, err := store.Bootstrap(ctx, "verified-subject")
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
			_, pool, _ := registrationDatabase(t)
			store := repository.NewAuth(pool)
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			entered := make(chan struct{}, tc.attempts)
			release := make(chan struct{})
			releaseProvider := sync.OnceFunc(func() { close(release) })
			var wg sync.WaitGroup
			provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				_, _ = io.Copy(io.Discard, r.Body)
				entered <- struct{}{}
				select {
				case <-release:
					_, _ = w.Write([]byte(`{"access_token":"fixture-token","user":{"id":"concurrent-subject"}}`))
				case <-r.Context().Done():
				}
			}))
			defer provider.Close()
			defer func() { cancel(); releaseProvider(); wg.Wait() }()
			auth := service.New(store, authprovider.NewSupabase(provider.URL, "fixture-key"))
			pre, err := auth.CSRF(ctx, "")
			if err != nil {
				t.Fatal(err)
			}
			results := make(chan error, tc.attempts)
			for range tc.attempts {
				wg.Add(1)
				go func() {
					defer wg.Done()
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
			releaseProvider()
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
			if err := pool.QueryRowContext(ctx, `SELECT count(*) FROM sessions WHERE user_id IS NOT NULL AND revoked_at IS NULL`).Scan(&sessions); err != nil || sessions != want {
				t.Fatalf("active sessions=%d want=%d err=%v", sessions, want, err)
			}
		})
	}
}

func TestRegistrationFailureRecovery(t *testing.T) {
	for _, failure := range []string{"provider_timeout", "provider_rejection", "provider_invalid", "local_bootstrap", "local_session"} {
		t.Run(failure, func(t *testing.T) {
			_, pool, ctx := registrationDatabase(t)
			var calls atomic.Int32
			provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				_, _ = io.Copy(io.Discard, r.Body)
				call := calls.Add(1)
				if r.URL.Path != "/auth/v1/signup" && r.URL.RequestURI() != "/auth/v1/token?grant_type=password" {
					t.Error("unexpected provider endpoint")
				}
				if call == 1 {
					switch failure {
					case "provider_timeout":
						<-r.Context().Done()
						return
					case "provider_rejection":
						w.WriteHeader(http.StatusUnprocessableEntity)
						return
					case "provider_invalid":
						_, _ = w.Write([]byte(`{"user":{"id":"recovery-subject"}}`))
						return
					}
				}
				_, _ = w.Write([]byte(`{"user":{"id":"recovery-subject"},"access_token":"private-token"}`))
			}))
			defer provider.Close()
			store := repository.NewAuth(pool)
			auth := service.New(store, authprovider.NewSupabase(provider.URL, "fixture-key"))
			h := handlers.NewHandler(&handlers.Health{}, handlers.NewAuthHandler(auth, "https://faq.example", false))
			request := func(ctx context.Context, path string, pre models.Session) *httptest.ResponseRecorder {
				t.Helper()
				r := httptest.NewRequestWithContext(ctx, http.MethodPost, path, strings.NewReader(`{"email":"recovery@example.invalid","password":"fixture-password"}`))
				r.Header.Set("Origin", "https://faq.example")
				r.Header.Set("Content-Type", "application/json")
				r.Header.Set("X-CSRF-Token", pre.CSRF)
				r.AddCookie(&http.Cookie{Name: "faqbot_session", Value: pre.Token})
				w := httptest.NewRecorder()
				h.ServeHTTP(w, r)
				return w
			}
			pre, err := auth.CSRF(ctx, "")
			if err != nil {
				t.Fatal(err)
			}
			switch failure {
			case "local_bootstrap":
				if _, err := pool.ExecContext(ctx, "ALTER TABLE trial_bootstrap_intents ADD CONSTRAINT fail_registration CHECK(false) NOT VALID"); err != nil {
					t.Fatal(err)
				}
			case "local_session":
				if _, err := pool.ExecContext(ctx, "ALTER TABLE sessions ADD CONSTRAINT fail_registration CHECK(user_id IS NULL) NOT VALID"); err != nil {
					t.Fatal(err)
				}
			}
			firstCtx := ctx
			if failure == "provider_timeout" {
				var cancel context.CancelFunc
				firstCtx, cancel = context.WithTimeout(ctx, time.Second)
				defer cancel()
			}
			result := request(firstCtx, "/api/v1/auth/register", pre)
			want := map[string]int{"provider_timeout": 503, "provider_rejection": 401, "provider_invalid": 502, "local_bootstrap": 500, "local_session": 500}[failure]
			if result.Code != want || len(result.Result().Cookies()) != 0 || strings.Contains(result.Body.String(), "private-token") {
				t.Fatalf("failure status=%d want=%d", result.Code, want)
			}
			n := 0
			if failure == "local_session" {
				n = 1
			}
			registrationCounts(t, ctx, pool, n, n, n, n)
			switch failure {
			case "local_bootstrap":
				if _, err := pool.ExecContext(ctx, "ALTER TABLE trial_bootstrap_intents DROP CONSTRAINT fail_registration"); err != nil {
					t.Fatal(err)
				}
			case "local_session":
				if _, err := pool.ExecContext(ctx, "ALTER TABLE sessions DROP CONSTRAINT fail_registration"); err != nil {
					t.Fatal(err)
				}
			}
			pre, err = auth.CSRF(ctx, "")
			if err != nil {
				t.Fatal(err)
			}
			retry := request(ctx, "/api/v1/auth/login", pre)
			if retry.Code != http.StatusOK || len(retry.Result().Cookies()) != 1 {
				t.Fatalf("recovery status=%d body=%s", retry.Code, retry.Body.String())
			}
			registrationCounts(t, ctx, pool, 1, 1, 1, 1)
			if calls.Load() != 2 {
				t.Fatalf("provider calls=%d", calls.Load())
			}
		})
	}
}
