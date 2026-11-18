package handlers

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"faqbot/backend/internal/models"
	"faqbot/backend/internal/service"

	"github.com/getkin/kin-openapi/openapi3"
)

func contract(t *testing.T, path string) *openapi3.T {
	t.Helper()
	loader := openapi3.NewLoader()
	loader.IsExternalRefsAllowed = true
	doc, err := loader.LoadFromFile("../../../contracts/" + path)
	if err != nil {
		t.Fatal(err)
	}
	if err := doc.Validate(context.Background()); err != nil {
		t.Fatal(err)
	}
	return doc
}

func responseContract(t *testing.T, doc *openapi3.T, path, method string, response *http.Response) []byte {
	t.Helper()
	defer func() {
		if err := response.Body.Close(); err != nil {
			t.Error(err)
		}
	}()
	body, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatal(err)
	}
	item := doc.Paths.Value(path)
	if item == nil {
		t.Fatalf("missing path %s", path)
	}
	operation := item.GetOperation(method)
	if operation == nil {
		operation = item.Get
		if operation == nil {
			operation = item.Post
		}
	}
	ref := operation.Responses.Status(response.StatusCode)
	if ref == nil {
		t.Fatalf("undocumented %s %s status %d", method, path, response.StatusCode)
	}
	if response.Header.Get("Cache-Control") != "no-store" || !requestIDPattern.MatchString(response.Header.Get("X-Request-ID")) {
		t.Fatal("missing canonical response headers")
	}
	if method == http.MethodHead {
		if len(body) != 0 {
			t.Fatal("HEAD returned body")
		}
		return body
	}
	if response.StatusCode == http.StatusNoContent {
		if len(body) != 0 {
			t.Fatal("204 returned body")
		}
		return body
	}
	content := ref.Value.Content.Get("application/json")
	if content == nil {
		t.Fatal("missing JSON response schema")
	}
	if response.Header.Get("Content-Type") != "application/json" {
		t.Fatal("wrong content type")
	}
	var value any
	if err := json.Unmarshal(body, &value); err != nil {
		t.Fatal(err)
	}
	if err := content.Schema.Value.VisitJSON(value); err != nil {
		t.Fatal(err)
	}
	return body
}

func TestProbeContract(t *testing.T) {
	doc := contract(t, "health.openapi.yaml")
	for _, state := range []string{"starting", "ready", "dependency failure", "draining"} {
		t.Run(state, func(t *testing.T) {
			health := &Health{}
			if state != "starting" {
				health.Start()
			}
			if state == "dependency failure" {
				health.Check = func(context.Context) error { return errors.New("private dependency detail") }
			}
			if state == "draining" {
				health.Drain()
			}
			server := httptest.NewServer(NewHandler(health))
			defer server.Close()
			for _, path := range []string{"/health/live", "/health/startup", "/health/ready"} {
				for _, method := range []string{http.MethodGet, http.MethodHead, http.MethodPost} {
					req, err := http.NewRequestWithContext(t.Context(), method, server.URL+path, nil)
					if err != nil {
						t.Fatal(err)
					}
					res, err := server.Client().Do(req)
					if err != nil {
						t.Fatal(err)
					}
					want := http.StatusOK
					if method == http.MethodPost {
						want = http.StatusMethodNotAllowed
					} else if (path == "/health/startup" && state == "starting") || (path == "/health/ready" && state != "ready") {
						want = http.StatusServiceUnavailable
					}
					if res.StatusCode != want {
						t.Fatalf("%s %s: %d want %d", method, path, res.StatusCode, want)
					}
					if method == http.MethodPost && res.Header.Get("Allow") != "GET, HEAD" {
						t.Fatal("missing probe Allow")
					}
					body := responseContract(t, doc, path, method, res)
					if bytes.Contains(body, []byte("private dependency")) {
						t.Fatal("probe leaked dependency error")
					}
				}
			}
		})
	}
}

func TestAuthRuntimeContract(t *testing.T) {
	doc := contract(t, "openapi.yaml")
	for _, tc := range []struct {
		name, path, method, origin, csrf, body, requestID string
		err                                               error
		status                                            int
		identity                                          bool
	}{
		{name: "csrf", path: "csrf", method: "GET", status: 200},
		{name: "login", path: "login", method: "POST", origin: "https://faq.example", csrf: "csrf", body: `{"email":"a@example.org","password":"pw"}`, status: 200, identity: true, requestID: "client-1"},
		{name: "register", path: "register", method: "POST", origin: "https://faq.example", csrf: "csrf", body: `{"email":"a@example.org","password":"pw"}`, status: 201, identity: true},
		{name: "member session", path: "session", method: "GET", status: 200, identity: true},
		{name: "preliminary session", path: "session", method: "GET", status: 401},
		{name: "preliminary logout", path: "logout", method: "POST", origin: "https://faq.example", csrf: "csrf", status: 204},
		{name: "missing origin", path: "login", method: "POST", csrf: "csrf", status: 403},
		{name: "missing CSRF", path: "logout", method: "POST", origin: "https://faq.example", status: 403},
		{name: "foreign get", path: "session", method: "GET", origin: "https://foreign.example", status: 403},
		{name: "bad JSON", path: "login", method: "POST", origin: "https://faq.example", csrf: "csrf", body: `{} {}`, status: 400},
		{name: "untrusted scope", path: "register", method: "POST", origin: "https://faq.example", csrf: "csrf", body: `{"email":"a@example.org","password":"pw","account_id":"foreign"}`, status: 400},
		{name: "body byte boundary", path: "login", method: "POST", origin: "https://faq.example", csrf: "csrf", body: `{"email":"a@example.org","password":"pw"}` + strings.Repeat(" ", 4096-len(`{"email":"a@example.org","password":"pw"}`)), status: 200, identity: true},
		{name: "body exceeds byte boundary", path: "login", method: "POST", origin: "https://faq.example", csrf: "csrf", body: `{"email":"a@example.org","password":"pw"}` + strings.Repeat(" ", 4097-len(`{"email":"a@example.org","password":"pw"}`)), status: 400},
		{name: "email UTF8 boundary", path: "login", method: "POST", origin: "https://faq.example", csrf: "csrf", body: `{"email":"` + strings.Repeat("я", 122) + `@e.invalid","password":"pw"}`, status: 200, identity: true},
		{name: "email exceeds byte boundary", path: "login", method: "POST", origin: "https://faq.example", csrf: "csrf", body: `{"email":"` + strings.Repeat("я", 123) + `@e.invalid","password":"pw"}`, status: 400},
		{name: "UTF8 boundary", path: "login", method: "POST", origin: "https://faq.example", csrf: "csrf", body: `{"email":"a@example.org","password":"` + strings.Repeat("я", 512) + `"}`, status: 200, identity: true},
		{name: "UTF8 over boundary", path: "login", method: "POST", origin: "https://faq.example", csrf: "csrf", body: `{"email":"a@example.org","password":"` + strings.Repeat("я", 512) + `a"}`, status: 400},
		{name: "upstream unavailable", path: "login", method: "POST", origin: "https://faq.example", csrf: "csrf", body: `{"email":"a@example.org","password":"pw"}`, err: service.ErrUnavailable, status: 503},
		{name: "upstream invalid", path: "login", method: "POST", origin: "https://faq.example", csrf: "csrf", body: `{"email":"a@example.org","password":"pw"}`, err: service.ErrBadGateway, status: 502},
		{name: "internal error", path: "session", method: "GET", err: errors.New("private database information"), status: 500},
		{name: "request ID replacement", path: "csrf", method: "GET", requestID: "invalid correlation ID", status: 200},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := &fakeAuth{err: tc.err}
			if tc.identity {
				f.identity = &models.Identity{UserID: "user_a", AccountID: "account_a", Role: "owner"}
			}
			server := httptest.NewServer(NewHandler(&Health{}, NewAuthHandler(f, "https://faq.example", false)))
			defer server.Close()
			path := "/api/v1/auth/" + tc.path
			req, err := http.NewRequestWithContext(t.Context(), tc.method, server.URL+path, strings.NewReader(tc.body))
			if err != nil {
				t.Fatal(err)
			}
			req.Header.Set("Origin", tc.origin)
			req.Header.Set("X-CSRF-Token", tc.csrf)
			req.Header.Set("Content-Type", "application/json")
			req.Header.Set("X-Request-ID", tc.requestID)
			res, err := server.Client().Do(req)
			if err != nil {
				t.Fatal(err)
			}
			if res.StatusCode != tc.status {
				t.Fatalf("status=%d want=%d", res.StatusCode, tc.status)
			}
			if tc.requestID == "client-1" && res.Header.Get("X-Request-ID") != tc.requestID {
				t.Fatal("valid correlation ID not preserved")
			}
			body := responseContract(t, doc, path, tc.method, res)
			if bytes.Contains(body, []byte("private database")) {
				t.Fatal("internal error leaked")
			}
		})
	}
	for _, path := range []string{"csrf", "session", "login", "register", "logout"} {
		for _, method := range []string{"GET", "HEAD", "POST", "PUT", "OPTIONS"} {
			allowed := "POST"
			if path == "csrf" || path == "session" {
				allowed = "GET"
			}
			if method == allowed {
				continue
			}
			t.Run(path+method, func(t *testing.T) {
				server := httptest.NewServer(NewHandler(&Health{}, NewAuthHandler(&fakeAuth{}, "https://faq.example", false)))
				defer server.Close()
				route := "/api/v1/auth/" + path
				req, err := http.NewRequestWithContext(t.Context(), method, server.URL+route, nil)
				if err != nil {
					t.Fatal(err)
				}
				res, err := server.Client().Do(req)
				if err != nil {
					t.Fatal(err)
				}
				if res.StatusCode != 405 || res.Header.Get("Allow") != allowed {
					t.Fatal("invalid method response")
				}
				responseContract(t, doc, route, method, res)
			})
		}
	}
}
