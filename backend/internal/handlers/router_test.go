package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestHealthLifecycle(t *testing.T) {
	health := &Health{}
	handler := NewHandler(health)
	check := func(path string, want int) {
		t.Helper()
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, httptest.NewRequest("GET", path, nil))
		if response.Code != want {
			t.Fatalf("%s: got %d, want %d", path, response.Code, want)
		}
		if response.Header().Get("Cache-Control") != "no-store" {
			t.Fatal("probe can be cached")
		}
	}
	check("/health/live", 200)
	check("/health/startup", 503)
	check("/health/ready", 503)
	health.Start()
	check("/health/startup", 200)
	check("/health/ready", 200)
	health.Drain()
	check("/health/ready", 503)
	check("/health/live", 200)
	check("/health/startup", 200)
}

func TestErrorsAndRequestID(t *testing.T) {
	handler := NewHandler(&Health{})
	for _, c := range []struct {
		method, path, id, code string
		status                 int
	}{
		{"GET", "/missing", "req_demo", "not_found", 404},
		{"POST", "/health/live", "req_demo", "method_not_allowed", 405},
		{"GET", "/health/ready", "invalid id", "service_not_ready", 503},
		{"GET", "/health/startup", "", "service_starting", 503},
	} {
		t.Run(c.code, func(t *testing.T) {
			r := httptest.NewRequest(c.method, c.path, nil)
			r.Header.Set("X-Request-ID", c.id)
			w := httptest.NewRecorder()
			handler.ServeHTTP(w, r)
			if w.Code != c.status {
				t.Fatalf("status %d", w.Code)
			}
			var body apiError
			if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
				t.Fatal(err)
			}
			if body.Code != c.code || body.RequestID != w.Header().Get("X-Request-ID") || !requestIDPattern.MatchString(body.RequestID) {
				t.Fatalf("unexpected error: %+v", body)
			}
			if c.id == "req_demo" && body.RequestID != c.id {
				t.Fatal("request ID changed")
			}
			if c.status == 405 && w.Header().Get("Allow") != "GET, HEAD" {
				t.Fatal("missing Allow")
			}
		})
	}
}

func TestHeadProbe(t *testing.T) {
	server := httptest.NewServer(NewHandler(&Health{}))
	defer server.Close()
	response, err := http.Head(server.URL + "/health/live")
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := response.Body.Close(); err != nil {
			t.Error(err)
		}
	}()
	if response.StatusCode != 200 || response.ContentLength < 0 {
		t.Fatal("invalid HEAD response")
	}
}

func TestDependencyReadiness(t *testing.T) {
	health := &Health{Check: func(ctx context.Context) error {
		if _, ok := ctx.Deadline(); !ok {
			t.Error("dependency check has no deadline")
		}
		return errors.New("private database error")
	}}
	health.Start()
	handler := NewHandler(health)
	for path, want := range map[string]int{"/health/ready": 503, "/health/live": 200, "/health/startup": 200} {
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, httptest.NewRequest("GET", path, nil))
		if w.Code != want {
			t.Fatalf("%s: status=%d", path, w.Code)
		}
		if strings.Contains(w.Body.String(), "private database error") {
			t.Fatal("dependency error leaked")
		}
	}
}
