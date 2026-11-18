package authprovider

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"faqbot/backend/internal/service"
)

func TestSupabaseEndpointsAndResponses(t *testing.T) {
	for _, tc := range []struct {
		name     string
		register bool
		status   int
		body     string
		want     error
	}{
		{"signup", true, 201, `{"user":{"id":"subject"},"access_token":"secret"}`, nil},
		{"password", false, 200, `{"user":{"id":"subject"},"access_token":"secret"}`, nil},
		{"rejected", true, 422, `{}`, service.ErrUnauthorized},
		{"unauthorized", false, 401, `{}`, service.ErrUnauthorized},
		{"unavailable", true, 503, `{}`, service.ErrUnavailable},
		{"rate limited", false, 429, `{}`, service.ErrUnavailable},
		{"invalid json", false, 200, `{`, service.ErrBadGateway},
		{"missing token", true, 200, `{"user":{"id":"subject"}}`, service.ErrBadGateway},
		{"missing subject", false, 200, `{"access_token":"secret"}`, service.ErrBadGateway},
		{"trailing json", false, 200, `{"user":{"id":"subject"},"access_token":"secret"}{}`, service.ErrBadGateway},
		{"redirect", false, 302, `{}`, service.ErrBadGateway},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var calls atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				want := "/auth/v1/token?grant_type=password"
				if tc.register {
					want = "/auth/v1/signup"
				}
				if r.Method != "POST" || r.URL.RequestURI() != want || r.Header.Get("apikey") != "test-key" {
					t.Errorf("unexpected provider request %s %s", r.Method, r.URL)
				}
				var body map[string]string
				if err := json.NewDecoder(r.Body).Decode(&body); err != nil || len(body) != 2 || body["email"] != "user@example.org" || body["password"] != "password" {
					t.Error("invalid credentials request", err)
				}
				w.Header().Set("Location", "/email")
				w.WriteHeader(tc.status)
				_, _ = w.Write([]byte(tc.body))
			}))
			defer server.Close()
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			subject, err := NewSupabase(server.URL, "test-key").Authenticate(ctx, "user@example.org", "password", tc.register)
			if !errors.Is(err, tc.want) || (tc.want == nil && subject != "subject") || (tc.want != nil && subject != "") || calls.Load() != 1 {
				t.Fatalf("subject=%q err=%v calls=%d", subject, err, calls.Load())
			}
		})
	}
}

func TestSupabaseCancellationAndTimeout(t *testing.T) {
	for _, timeout := range []bool{false, true} {
		t.Run(map[bool]string{false: "cancellation", true: "timeout"}[timeout], func(t *testing.T) {
			entered := make(chan struct{})
			finished := make(chan struct{})
			server := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
				_, _ = io.Copy(io.Discard, r.Body)
				close(entered)
				<-r.Context().Done()
				close(finished)
			}))
			defer server.Close()
			provider := NewSupabase(server.URL, "key")
			if provider.client.Timeout != 5*time.Second {
				t.Fatal("wrong production timeout")
			}
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			var wg sync.WaitGroup
			defer func() { cancel(); wg.Wait() }()
			if timeout {
				provider.client.Timeout = 30 * time.Millisecond
			}
			result := make(chan error, 1)
			wg.Add(1)
			go func() {
				defer wg.Done()
				_, err := provider.Authenticate(ctx, "user@example.org", "password", false)
				result <- err
			}()
			select {
			case <-entered:
			case <-ctx.Done():
				t.Fatal("provider request did not arrive")
			}
			if !timeout {
				cancel()
			}
			err := <-result
			if !errors.Is(err, service.ErrUnavailable) {
				t.Fatal(err)
			}
			if !timeout && !errors.Is(err, context.Canceled) {
				t.Fatal("cancellation cause lost", err)
			}
			select {
			case <-finished:
			case <-time.After(time.Second):
				t.Fatal("provider request did not finish")
			}
		})
	}
}
