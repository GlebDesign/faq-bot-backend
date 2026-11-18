package handlers

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"faqbot/backend/internal/db"
	"faqbot/backend/internal/handlers/dto"
	"faqbot/backend/internal/migration"
	"faqbot/backend/internal/models"
	"faqbot/backend/internal/repository"
	"faqbot/backend/internal/service"
)

var agentCursorKey = []byte("01234567890123456789012345678901")

const agentOrigin = "http://localhost:8080"
const agentBody = `{"name":"Помощник","website":"","description":""}`

type agentProvider struct{}

func (agentProvider) Authenticate(_ context.Context, email, _ string, _ bool) (string, error) {
	return email, nil
}

type agentFixture struct {
	pool   *sql.DB
	auth   *service.Auth
	store  *repository.Agents
	audit  *service.AuditService
	agents *service.Agents
	server *httptest.Server
}

type agentClient struct {
	cookie   *http.Cookie
	csrf     string
	identity dto.AuthIdentity
}
type agentResponse struct {
	status int
	body   []byte
	header http.Header
}

func newAgentFixture(t *testing.T) *agentFixture {
	t.Helper()
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL is required")
	}
	admin, err := db.Open(context.Background(), dsn)
	if err != nil {
		t.Fatal(err)
	}
	schema := fmt.Sprintf("agents_http_%d", time.Now().UnixNano())
	if _, err := admin.Exec("CREATE SCHEMA " + schema); err != nil {
		t.Fatal(db.WrapError("create agents test schema", err))
	}
	u, err := url.Parse(dsn)
	if err != nil {
		t.Fatal(err)
	}
	q := u.Query()
	q.Set("search_path", schema)
	u.RawQuery = q.Encode()
	pool, err := db.Open(context.Background(), u.String())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := pool.Close(); err != nil {
			t.Error(err)
		}
		if _, err := admin.Exec("DROP SCHEMA " + schema + " CASCADE"); err != nil {
			t.Error(db.WrapError("drop agents test schema", err))
		}
		if err := admin.Close(); err != nil {
			t.Error(err)
		}
	})
	if err := migration.Apply(context.Background(), u.String(), "../../migrations"); err != nil {
		t.Fatal(err)
	}
	f := &agentFixture{pool: pool, auth: service.New(repository.NewAuth(pool), agentProvider{}), store: repository.NewAgents(pool), audit: service.NewAudit(repository.NewAudit())}
	f.agents = service.NewAgents(pool, f.store, f.audit, f.store, agentCursorKey)
	f.server = httptest.NewServer(NewHandler(&Health{}, NewAuthHandler(f.auth, agentOrigin, true), NewAgentHandler(f.auth, f.agents, agentOrigin)))
	t.Cleanup(f.server.Close)
	return f
}

func (f *agentFixture) request(t *testing.T, c agentClient, method, path, body, key, revision string, headers map[string]string) agentResponse {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, method, f.server.URL+path, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Origin", agentOrigin)
	if c.cookie != nil {
		req.AddCookie(c.cookie)
	}
	req.Header.Set("X-CSRF-Token", c.csrf)
	if key != "" {
		req.Header.Set("Idempotency-Key", key)
	}
	if revision != "" {
		req.Header.Set("If-Match", revision)
	}
	for name, value := range headers {
		req.Header.Set(name, value)
	}
	response, err := f.server.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = response.Body.Close() }()
	raw, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatal(err)
	}
	if response.Header.Get("Cache-Control") != "no-store" || response.Header.Get("X-Request-ID") == "" {
		t.Fatal("standard headers missing")
	}
	return agentResponse{response.StatusCode, raw, response.Header}
}
func expectAgentStatus(t *testing.T, r agentResponse, status int) {
	t.Helper()
	if r.status != status {
		t.Fatalf("status=%d want=%d body=%s", r.status, status, r.body)
	}
}
func decodeAgentResponse[T any](t *testing.T, r agentResponse) T {
	t.Helper()
	var out T
	if err := json.Unmarshal(r.body, &out); err != nil {
		t.Fatal(err)
	}
	return out
}
func (f *agentFixture) login(t *testing.T, email string) agentClient {
	t.Helper()
	r := f.request(t, agentClient{}, "GET", "/api/v1/auth/csrf", "", "", "", nil)
	expectAgentStatus(t, r, 200)
	csrf := decodeAgentResponse[dto.AuthCSRF](t, r)
	cookie := (&http.Response{Header: r.header}).Cookies()[0]
	c := agentClient{cookie: cookie, csrf: csrf.CSRFToken}
	r = f.request(t, c, "POST", "/api/v1/auth/login", `{"email":"`+email+`","password":"synthetic-password"}`, "", "", nil)
	expectAgentStatus(t, r, 200)
	session := decodeAgentResponse[dto.AuthSession](t, r)
	return agentClient{cookie: (&http.Response{Header: r.header}).Cookies()[0], csrf: session.CSRFToken, identity: session.Identity}
}
func (f *agentFixture) count(t *testing.T, table string) int {
	t.Helper()
	var n int
	if err := f.pool.QueryRow("SELECT count(*) FROM " + table).Scan(&n); err != nil {
		t.Fatal(db.WrapError("count test rows", err))
	}
	return n
}

func TestAgentsHTTPCRUDSQL(t *testing.T) {
	f := newAgentFixture(t)
	a := f.login(t, "a@example.org")
	b := f.login(t, "b@example.org")
	empty := f.request(t, a, "GET", "/api/v1/agents", "", "", "", nil)
	expectAgentStatus(t, empty, 200)
	if string(empty.body) != "{\"items\":[],\"next_cursor\":null}\n" {
		t.Fatalf("empty page %s", empty.body)
	}
	first := f.request(t, a, "POST", "/api/v1/agents", agentBody, "create-one", "", nil)
	expectAgentStatus(t, first, 201)
	agent := decodeAgentResponse[models.Agent](t, first)
	if agent.AccountID != a.identity.AccountID || agent.Revision != 1 || agent.ConfigRevision != 1 || agent.KnowledgeRevision != 0 || agent.PublicID == agent.ID || first.header.Get("ETag") != `"1"` {
		t.Fatal("create defaults")
	}
	replay := f.request(t, a, "POST", "/api/v1/agents", agentBody, "create-one", "", nil)
	expectAgentStatus(t, replay, 201)
	if string(replay.body) != string(first.body) || f.count(t, "audit_events") != 1 {
		t.Fatal("create replay changed result")
	}
	expectAgentStatus(t, f.request(t, a, "POST", "/api/v1/agents", strings.ReplaceAll(agentBody, "Помощник", "Другой"), "create-one", "", nil), 409)
	second := f.request(t, a, "POST", "/api/v1/agents", agentBody, "create-two", "", nil)
	expectAgentStatus(t, second, 201)
	other := f.request(t, b, "POST", "/api/v1/agents", agentBody, "create-one", "", nil)
	expectAgentStatus(t, other, 201)
	foreign := decodeAgentResponse[models.Agent](t, other)
	for _, method := range []string{"GET", "PATCH", "DELETE"} {
		body := ""
		revision := ""
		if method == "PATCH" {
			body = agentBody
			revision = `"1"`
		}
		expectAgentStatus(t, f.request(t, a, method, "/api/v1/agents/"+foreign.ID, body, "foreign", revision, nil), 404)
		expectAgentStatus(t, f.request(t, a, method, "/api/v1/agents/"+foreign.PublicID, body, "foreign", revision, nil), 404)
	}
	pageResponse := f.request(t, a, "GET", "/api/v1/agents?limit=1", "", "", "", nil)
	expectAgentStatus(t, pageResponse, 200)
	page := decodeAgentResponse[dto.AgentPage](t, pageResponse)
	secondAgent := decodeAgentResponse[models.Agent](t, second)
	if len(page.Items) != 1 || page.Items[0].ID != secondAgent.ID || page.NextCursor == nil {
		t.Fatal("first page order")
	}
	next := f.request(t, a, "GET", "/api/v1/agents?limit=1&cursor="+url.QueryEscape(*page.NextCursor), "", "", "", nil)
	expectAgentStatus(t, next, 200)
	page2 := decodeAgentResponse[dto.AgentPage](t, next)
	if len(page2.Items) != 1 || page2.Items[0].ID != agent.ID || page2.NextCursor != nil {
		t.Fatal("next page")
	}
	expectAgentStatus(t, f.request(t, b, "GET", "/api/v1/agents?cursor="+url.QueryEscape(*page.NextCursor), "", "", "", nil), 400)
	updateBody := `{"name":"  Новое имя  ","website":"не URL","description":"Описание"}`
	updated := f.request(t, a, "PATCH", "/api/v1/agents/"+agent.ID, updateBody, "update-one", `"1"`, nil)
	expectAgentStatus(t, updated, 200)
	changed := decodeAgentResponse[models.Agent](t, updated)
	if changed.Revision != 2 || changed.Name != "  Новое имя  " || changed.Description != "Описание" || changed.Website != "не URL" || changed.ConfigRevision != 1 || changed.KnowledgeRevision != 0 {
		t.Fatal("update lost fields or revisions")
	}
	replay = f.request(t, a, "PATCH", "/api/v1/agents/"+agent.ID, updateBody, "update-one", `"1"`, nil)
	expectAgentStatus(t, replay, 200)
	if string(replay.body) != string(updated.body) || replay.header.Get("ETag") != `"2"` {
		t.Fatal("patch replay stale")
	}
	expectAgentStatus(t, f.request(t, a, "PATCH", "/api/v1/agents/"+agent.ID, updateBody, "update-one", `"2"`, nil), 409)
	expectAgentStatus(t, f.request(t, a, "PATCH", "/api/v1/agents/"+agent.ID, updateBody, "stale", `"1"`, nil), 412)
	got := f.request(t, a, "GET", "/api/v1/agents/"+agent.ID, "", "", "", nil)
	expectAgentStatus(t, got, 200)
	if decodeAgentResponse[models.Agent](t, got).Description != "Описание" {
		t.Fatal("persisted description lost")
	}
	if got.header.Get("ETag") != `"2"` {
		t.Fatal("get revision")
	}
	deleted := f.request(t, a, "DELETE", "/api/v1/agents/"+agent.ID, "", "delete-one", "", nil)
	expectAgentStatus(t, deleted, 202)
	job := decodeAgentResponse[dto.Job](t, deleted)
	if job.Status != "pending" || job.ErrorCode != nil || job.ID == "" || f.count(t, "agent_cleanup_requests") != 1 {
		t.Fatal("cleanup not durable pending")
	}
	replay = f.request(t, a, "DELETE", "/api/v1/agents/"+agent.ID, "", "delete-one", "", nil)
	expectAgentStatus(t, replay, 202)
	if string(replay.body) != string(deleted.body) {
		t.Fatal("delete replay")
	}
	expectAgentStatus(t, f.request(t, a, "DELETE", "/api/v1/agents/"+agent.ID, "", "delete-two", "", nil), 404)
	expectAgentStatus(t, f.request(t, a, "GET", "/api/v1/agents/"+agent.ID, "", "", "", nil), 404)
	expectAgentStatus(t, f.request(t, a, "PATCH", "/api/v1/agents/"+agent.ID, agentBody, "update-deleted", `"2"`, nil), 404)
	final := f.request(t, a, "GET", "/api/v1/agents", "", "", "", nil)
	expectAgentStatus(t, final, 200)
	finalPage := decodeAgentResponse[dto.AgentPage](t, final)
	if len(finalPage.Items) != 1 || finalPage.Items[0].ID != secondAgent.ID || f.count(t, "audit_events") != 5 || f.count(t, "agent_operations") != 5 {
		t.Fatal("tombstone or atomic operation counts")
	}
	var fields string
	if err := f.pool.QueryRow("SELECT metadata::text FROM audit_events WHERE action='agent.updated'").Scan(&fields); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(fields, "Новое") || !strings.Contains(fields, "website") {
		t.Fatal("audit metadata leaked or missed field")
	}
}

func TestAgentsHTTPValidationSecuritySQL(t *testing.T) {
	f := newAgentFixture(t)
	a := f.login(t, "a@example.org")
	expectAgentStatus(t, f.request(t, agentClient{}, "GET", "/api/v1/agents", "", "", "", map[string]string{"Authorization": "Bearer ignored", "X-Account-ID": a.identity.AccountID}), 401)
	for _, headers := range []map[string]string{{"Origin": ""}, {"Origin": "http://foreign.invalid"}, {"X-CSRF-Token": ""}, {"X-CSRF-Token": "wrong"}, {"Sec-Fetch-Site": "cross-site"}} {
		expectAgentStatus(t, f.request(t, a, "POST", "/api/v1/agents", agentBody, "unsafe", "", headers), 403)
	}
	for _, headers := range []map[string]string{{"Origin": "http://foreign.invalid"}, {"Sec-Fetch-Site": "cross-site"}} {
		expectAgentStatus(t, f.request(t, a, "GET", "/api/v1/agents", "", "", "", headers), 403)
	}
	for _, body := range []string{`{}`, `null`, `[]`, `{"name":"ok","website":""}`, `{"name":"ok","website":null,"description":""}`, `{"name":null,"website":"","description":""}`, `{"name":"ok","website":"","description":null}`, `{"name":"ok","name":"other","website":"","description":""}`, `{"name":1,"website":"","description":""}`, agentBody + ` {}`, strings.ReplaceAll(agentBody, "Помощник", strings.Repeat("я", 101)), strings.ReplaceAll(agentBody, "Помощник", "\u00a0\u2003\u202f"), strings.ReplaceAll(agentBody, "Помощник", strings.Repeat("x", 1<<20)), "{\"name\":\"\xff\",\"website\":\"\",\"description\":\"\"}"} {
		expectAgentStatus(t, f.request(t, a, "POST", "/api/v1/agents", body, "invalid", "", nil), 400)
	}
	for _, field := range []string{"account_id", "tenant_id", "actor_id", "public_id", "id", "revision", "status", "config_revision", "knowledge_revision"} {
		body := strings.TrimSuffix(agentBody, "}") + `,"` + field + `":"spoof"}`
		expectAgentStatus(t, f.request(t, a, "POST", "/api/v1/agents", body, "invalid", "", nil), 400)
	}
	for _, key := range []string{"", strings.Repeat("a", 129), "bad key", "key!", "кириллица"} {
		expectAgentStatus(t, f.request(t, a, "POST", "/api/v1/agents", agentBody, key, "", nil), 400)
	}
	for _, query := range []string{"limit=0", "limit=101", "limit=oops", "limit=", "limit=1&limit=2", "cursor=", "cursor=bad", "account_id=foreign"} {
		expectAgentStatus(t, f.request(t, a, "GET", "/api/v1/agents?"+query, "", "", "", nil), 400)
	}
	expectAgentStatus(t, f.request(t, a, "POST", "/api/v1/agents", agentBody, "content-type", "", map[string]string{"Content-Type": "text/plain"}), 400)
	if f.count(t, "agents") != 0 || f.count(t, "agent_operations") != 0 || f.count(t, "audit_events") != 0 {
		t.Fatal("rejected requests wrote business data")
	}
	created := f.request(t, a, "POST", "/api/v1/agents", agentBody, "created", "", nil)
	expectAgentStatus(t, created, 201)
	agent := decodeAgentResponse[models.Agent](t, created)
	for _, revision := range []string{"", "1", `"0"`, `"01"`, `"-1"`, `"1" trailing`, `W/"1"`, `"1","2"`, `"9223372036854775808"`, `"+1"`} {
		expectAgentStatus(t, f.request(t, a, "PATCH", "/api/v1/agents/"+agent.ID, agentBody, "invalid-revision", revision, nil), 400)
	}
	expectAgentStatus(t, f.request(t, a, "PATCH", "/api/v1/agents/"+agent.ID, `{"name":"only"}`, "partial", `"1"`, nil), 400)
	for _, path := range []string{"/api/v1/agents", "/api/v1/agents/" + agent.ID} {
		r := f.request(t, a, "HEAD", path, "", "", "", nil)
		expectAgentStatus(t, r, 405)
		if len(r.body) != 0 || r.header.Get("Allow") == "" {
			t.Fatal("head semantics")
		}
	}
	for _, auditRead := range []bool{false, true} {
		if _, err := f.pool.Exec("UPDATE account_members SET role='admin',audit_read=$1 WHERE user_id=$2", auditRead, a.identity.UserID); err != nil {
			t.Fatal(err)
		}
		expectAgentStatus(t, f.request(t, a, "GET", "/api/v1/agents", "", "", "", nil), 200)
		expectAgentStatus(t, f.request(t, a, "POST", "/api/v1/agents", agentBody, fmt.Sprintf("admin-%t", auditRead), "", nil), 201)
	}
	if _, err := f.pool.Exec("DELETE FROM account_members WHERE user_id=$1", a.identity.UserID); err != nil {
		t.Fatal(err)
	}
	expectAgentStatus(t, f.request(t, a, "POST", "/api/v1/agents", agentBody, "created", "", nil), 401)
}

func TestAgentsHTTPSessionLifecycleSQL(t *testing.T) {
	f := newAgentFixture(t)
	for _, mode := range []string{"idle", "absolute", "revoked", "logout"} {
		t.Run(mode, func(t *testing.T) {
			c := f.login(t, mode+"@example.org")
			expectAgentStatus(t, f.request(t, c, "GET", "/api/v1/agents", "", "", "", nil), 200)
			hash := sha256.Sum256([]byte(c.cookie.Value))
			switch mode {
			case "idle":
				if _, err := f.pool.Exec("UPDATE sessions SET idle_expires_at=now()-interval '1 second' WHERE token_hash=$1", hash[:]); err != nil {
					t.Fatal(err)
				}
			case "absolute":
				if _, err := f.pool.Exec("UPDATE sessions SET created_at=now()-interval '9 hours',expires_at=now()-interval '1 second',idle_expires_at=now()-interval '1 second' WHERE token_hash=$1", hash[:]); err != nil {
					t.Fatal(err)
				}
			case "revoked":
				if _, err := f.pool.Exec("UPDATE sessions SET revoked_at=now() WHERE token_hash=$1", hash[:]); err != nil {
					t.Fatal(err)
				}
			case "logout":
				expectAgentStatus(t, f.request(t, c, "POST", "/api/v1/auth/logout", "", "", "", nil), 204)
			}
			expectAgentStatus(t, f.request(t, c, "GET", "/api/v1/agents", "", "", "", nil), 401)
			expectAgentStatus(t, f.request(t, c, "POST", "/api/v1/agents", agentBody, "after-expiry", "", nil), 401)
		})
	}
	pre := f.request(t, agentClient{}, "GET", "/api/v1/auth/csrf", "", "", "", nil)
	expectAgentStatus(t, pre, 200)
	c := agentClient{cookie: (&http.Response{Header: pre.header}).Cookies()[0], csrf: decodeAgentResponse[dto.AuthCSRF](t, pre).CSRFToken}
	expectAgentStatus(t, f.request(t, c, "GET", "/api/v1/agents", "", "", "", nil), 401)
}

func TestAgentsHTTPConcurrentReplayAndRevisionSQL(t *testing.T) {
	f := newAgentFixture(t)
	a := f.login(t, "a@example.org")
	const n = 6
	start := make(chan struct{})
	responses := make(chan agentResponse, n)
	var wg sync.WaitGroup
	for range n {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			responses <- f.request(t, a, "POST", "/api/v1/agents", agentBody, "concurrent", "", nil)
		}()
	}
	close(start)
	wg.Wait()
	close(responses)
	var original string
	for r := range responses {
		expectAgentStatus(t, r, 201)
		if original != "" && original != string(r.body) {
			t.Fatal("concurrent create differs")
		}
		original = string(r.body)
	}
	if f.count(t, "agents") != 1 || f.count(t, "audit_events") != 1 || f.count(t, "agent_operations") != 1 {
		t.Fatal("duplicate concurrent operation")
	}
	var agent models.Agent
	if err := json.Unmarshal([]byte(original), &agent); err != nil {
		t.Fatal(err)
	}
	start = make(chan struct{})
	responses = make(chan agentResponse, n)
	for i := range n {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			responses <- f.request(t, a, "PATCH", "/api/v1/agents/"+agent.ID, agentBody, fmt.Sprintf("revision-%d", i), `"1"`, nil)
		}()
	}
	close(start)
	wg.Wait()
	close(responses)
	success, conflicts := 0, 0
	for r := range responses {
		switch r.status {
		case 200:
			success++
		case 412:
			conflicts++
		default:
			t.Fatalf("unexpected concurrent update %d %s", r.status, r.body)
		}
	}
	if success != 1 || conflicts != n-1 || f.count(t, "audit_events") != 2 || f.count(t, "agent_operations") != 2 {
		t.Fatal("revision CAS failed")
	}
}

type failingAgentAudit struct{}

func (failingAgentAudit) WriteInTx(context.Context, *sql.Tx, models.AuditActor, models.AuditEvent) error {
	return errors.New("synthetic audit failure")
}

type failingAgentCleanup struct{}

func (failingAgentCleanup) RequestInTx(context.Context, *sql.Tx, string, string, string) error {
	return errors.New("synthetic cleanup failure")
}

type failingAgentComplete struct{ *repository.Agents }

func (failingAgentComplete) Complete(context.Context, *sql.Tx, models.AgentOperation) error {
	return errors.New("synthetic business completion failure")
}

func TestAgentsSQLRollbackAndCleanupRetry(t *testing.T) {
	f := newAgentFixture(t)
	a := f.login(t, "a@example.org")
	scope, _, err := f.auth.Scope(context.Background(), a.cookie.Value)
	if err != nil {
		t.Fatal(err)
	}
	input := models.AgentInput{Name: "Помощник"}
	for _, svc := range []*service.Agents{
		service.NewAgents(f.pool, f.store, failingAgentAudit{}, f.store, agentCursorKey),
		service.NewAgents(f.pool, failingAgentComplete{f.store}, f.audit, f.store, agentCursorKey),
	} {
		if _, err := svc.Create(context.Background(), scope, "rollback", "request", input); err == nil {
			t.Fatal("failed dependency accepted")
		}
		if f.count(t, "agents") != 0 || f.count(t, "agent_operations") != 0 || f.count(t, "audit_events") != 0 {
			t.Fatal("failed operation committed partial state")
		}
	}
	agent, err := f.agents.Create(context.Background(), scope, "rollback", "request", input)
	if err != nil {
		t.Fatal(err)
	}
	bad := service.NewAgents(f.pool, f.store, f.audit, failingAgentCleanup{}, agentCursorKey)
	if _, err := bad.Delete(context.Background(), scope, agent.ID, "delete", "request"); err == nil {
		t.Fatal("cleanup error hidden")
	}
	if f.count(t, "agent_cleanup_requests") != 0 || f.count(t, "audit_events") != 1 || f.count(t, "agent_operations") != 1 {
		t.Fatal("cleanup failure committed partial state")
	}
	if _, err := f.agents.Get(context.Background(), scope, agent.ID); err != nil {
		t.Fatal("cleanup failure tombstoned agent")
	}
	job, err := f.agents.Delete(context.Background(), scope, agent.ID, "delete", "request")
	if err != nil || job.Status != "pending" {
		t.Fatalf("cleanup retry failed: %v", err)
	}
	if f.count(t, "agent_cleanup_requests") != 1 || f.count(t, "audit_events") != 2 || f.count(t, "agent_operations") != 2 {
		t.Fatal("cleanup retry not atomic")
	}
}

func TestAgentsHTTPRuntimeContractSQL(t *testing.T) {
	f := newAgentFixture(t)
	a := f.login(t, "contract@example.org")
	doc := contract(t, "openapi.yaml")
	check := func(method, path, route, body, key, revision string, status int) agentResponse {
		t.Helper()
		r := f.request(t, a, method, path, body, key, revision, nil)
		expectAgentStatus(t, r, status)
		responseContract(t, doc, route, method, &http.Response{StatusCode: r.status, Header: r.header, Body: io.NopCloser(strings.NewReader(string(r.body)))})
		return r
	}
	create := check("POST", "/api/v1/agents", "/api/v1/agents", agentBody, "contract-create", "", 201)
	agent := decodeAgentResponse[models.Agent](t, create)
	path := "/api/v1/agents/" + agent.ID
	check("GET", "/api/v1/agents", "/api/v1/agents", "", "", "", 200)
	check("GET", path, "/api/v1/agents/{id}", "", "", "", 200)
	check("PATCH", path, "/api/v1/agents/{id}", agentBody, "contract-update", `"1"`, 200)
	check("PATCH", path, "/api/v1/agents/{id}", agentBody, "contract-stale", `"1"`, 412)
	check("PATCH", path, "/api/v1/agents/{id}", agentBody, "contract-update", `"2"`, 409)
	check("DELETE", path, "/api/v1/agents/{id}", "", "contract-delete", "", 202)
	check("GET", path, "/api/v1/agents/{id}", "", "", "", 404)
}

func TestAgentsReplayScopeAndStableCursorSQL(t *testing.T) {
	f := newAgentFixture(t)
	a := f.login(t, "a@example.org")
	b := f.login(t, "b@example.org")
	if _, err := f.pool.Exec("INSERT INTO account_members(user_id,account_id,role) VALUES($1,$2,'admin')", b.identity.UserID, a.identity.AccountID); err != nil {
		t.Fatal(err)
	}
	hash := sha256.Sum256([]byte(b.cookie.Value))
	if _, err := f.pool.Exec("UPDATE sessions SET account_id=$1 WHERE token_hash=$2", a.identity.AccountID, hash[:]); err != nil {
		t.Fatal(err)
	}
	ids := []string{}
	for _, c := range []agentClient{a, b} {
		r := f.request(t, c, "POST", "/api/v1/agents", agentBody, "shared-key", "", nil)
		expectAgentStatus(t, r, 201)
		ids = append(ids, decodeAgentResponse[models.Agent](t, r).ID)
	}
	if ids[0] == ids[1] {
		t.Fatal("actor scope merged")
	}
	for _, id := range ids {
		expectAgentStatus(t, f.request(t, a, "PATCH", "/api/v1/agents/"+id, agentBody, "same-resource-key", `"1"`, nil), 200)
	}
	if _, err := f.pool.Exec("UPDATE agents SET created_at='2026-11-01 00:00:00+00' WHERE account_id=$1", a.identity.AccountID); err != nil {
		t.Fatal(err)
	}
	first := f.request(t, a, "GET", "/api/v1/agents?limit=1", "", "", "", nil)
	expectAgentStatus(t, first, 200)
	page := decodeAgentResponse[dto.AgentPage](t, first)
	expectedFirst, expectedLast := ids[0], ids[1]
	if expectedFirst < expectedLast {
		expectedFirst, expectedLast = expectedLast, expectedFirst
	}
	if page.NextCursor == nil || page.Items[0].ID != expectedFirst {
		t.Fatal("tie order")
	}
	next := f.request(t, a, "GET", "/api/v1/agents?limit=1&cursor="+url.QueryEscape(*page.NextCursor), "", "", "", nil)
	expectAgentStatus(t, next, 200)
	last := decodeAgentResponse[dto.AgentPage](t, next)
	if len(last.Items) != 1 || last.Items[0].ID != expectedLast || last.NextCursor != nil {
		t.Fatal("tie pagination")
	}
	cursor := *page.NextCursor
	parts := strings.Split(cursor, ".")
	parts[0] = "e30"
	expectAgentStatus(t, f.request(t, a, "GET", "/api/v1/agents?cursor="+strings.Join(parts, "."), "", "", "", nil), 400)
	expectAgentStatus(t, f.request(t, a, "GET", "/api/v1/agents?cursor=%zz", "", "", "", nil), 400)
	if f.count(t, "agent_operations") != 4 || f.count(t, "audit_events") != 4 {
		t.Fatal("scope replay counts")
	}
}

func TestAgentReviewValidationAndPaginationSQL(t *testing.T) {
	f := newAgentFixture(t)
	a := f.login(t, "review@example.org")
	for _, field := range []string{"name", "website", "description"} {
		for _, value := range []string{`"\u0000"`, `"\ud800"`, `"\udc00"`, `"\ud800x"`, `"\ud800\u0041"`} {
			body := map[string]json.RawMessage{"name": json.RawMessage(`"name"`), "website": json.RawMessage(`""`), "description": json.RawMessage(`""`)}
			body[field] = json.RawMessage(value)
			raw, _ := json.Marshal(body)
			expectAgentStatus(t, f.request(t, a, "POST", "/api/v1/agents", string(raw), "invalid", "", nil), 400)
		}
	}
	if f.count(t, "agents") != 0 || f.count(t, "agent_operations") != 0 || f.count(t, "audit_events") != 0 {
		t.Fatal("invalid Unicode wrote rows")
	}
	body := `{"name":"\ud83d\ude00�","website":"\ufffd","description":"nonempty 😀�"}`
	r := f.request(t, a, "POST", "/api/v1/agents", body, "unicode", "", map[string]string{"X-Request-ID": "synthetic-secret-client"})
	expectAgentStatus(t, r, 201)
	agent := decodeAgentResponse[models.Agent](t, r)
	if r.header.Get("X-Request-ID") != "synthetic-secret-client" {
		t.Fatal("external Request-ID changed")
	}
	if agent.Name != "😀�" || agent.Description != "nonempty 😀�" || agent.Website != "�" {
		t.Fatal("Unicode normalized")
	}
	replay := f.request(t, a, "POST", "/api/v1/agents", body, "unicode", "", nil)
	if string(replay.body) != string(r.body) {
		t.Fatal("Unicode replay changed")
	}
	var auditID string
	if err := f.pool.QueryRow("SELECT request_id FROM audit_events").Scan(&auditID); err != nil || auditID == "synthetic-secret-client" || auditID == "" || f.count(t, "audit_events") != 1 {
		t.Fatal("untrusted audit request ID")
	}
	for _, value := range []string{`"\u0000"`, `"\ud800"`, `"\udc00"`} {
		for _, field := range []string{"name", "website", "description"} {
			fields := map[string]json.RawMessage{"name": json.RawMessage(`"name"`), "website": json.RawMessage(`""`), "description": json.RawMessage(`""`)}
			fields[field] = json.RawMessage(value)
			raw, _ := json.Marshal(fields)
			expectAgentStatus(t, f.request(t, a, "PATCH", "/api/v1/agents/"+agent.ID, string(raw), "invalid-patch", `"1"`, nil), 400)
		}
	}
	if f.count(t, "agent_operations") != 1 || f.count(t, "audit_events") != 1 {
		t.Fatal("invalid PATCH committed")
	}
	for i := 0; i < 20; i++ {
		expectAgentStatus(t, f.request(t, a, "POST", "/api/v1/agents", agentBody, fmt.Sprintf("page-%d", i), "", nil), 201)
	}
	page := decodeAgentResponse[dto.AgentPage](t, f.request(t, a, "GET", "/api/v1/agents", "", "", "", nil))
	if len(page.Items) != 20 || page.NextCursor == nil {
		t.Fatal("default limit")
	}
	key := append([]byte(nil), agentCursorKey...)
	other := service.NewAgents(f.pool, f.store, f.audit, f.store, key)
	key[0] = 'X'
	scope, _, err := f.auth.Scope(t.Context(), a.cookie.Value)
	if err != nil {
		t.Fatal(err)
	}
	rest, next, err := other.List(t.Context(), scope, 20, *page.NextCursor)
	if err != nil || len(rest) != 1 || rest[0].ID != agent.ID || next != nil {
		t.Fatal("shared/copied cursor key")
	}
	wrong := service.NewAgents(f.pool, f.store, f.audit, f.store, []byte("wrong-wrong-wrong-wrong-wrong-key-32"))
	if _, _, err := wrong.List(t.Context(), scope, 20, *page.NextCursor); !errors.Is(err, service.ErrInvalidAgent) {
		t.Fatal("wrong key accepted")
	}
	for _, method := range []string{"PATCH", "DELETE"} {
		for _, headers := range []map[string]string{{"Origin": ""}, {"Origin": "https://foreign.invalid"}, {"X-CSRF-Token": "wrong"}} {
			expectAgentStatus(t, f.request(t, a, method, "/api/v1/agents/"+agent.ID, agentBody, "unsafe", `"1"`, headers), 403)
		}
	}
	for _, flag := range []bool{false, true} {
		if _, err := f.pool.Exec("UPDATE account_members SET role='admin',audit_read=$1", flag); err != nil {
			t.Fatal(err)
		}
		created := f.request(t, a, "POST", "/api/v1/agents", agentBody, fmt.Sprintf("admin-create-%t", flag), "", nil)
		expectAgentStatus(t, created, 201)
		id := decodeAgentResponse[models.Agent](t, created).ID
		patched := f.request(t, a, "PATCH", "/api/v1/agents/"+id, body, fmt.Sprintf("admin-patch-%t", flag), `"1"`, nil)
		expectAgentStatus(t, patched, 200)
		if decodeAgentResponse[models.Agent](t, patched).Description != "nonempty 😀�" {
			t.Fatal("patch description")
		}
		replay := f.request(t, a, "PATCH", "/api/v1/agents/"+id, body, fmt.Sprintf("admin-patch-%t", flag), `"1"`, nil)
		if string(replay.body) != string(patched.body) {
			t.Fatal("patch replay")
		}
		get := f.request(t, a, "GET", "/api/v1/agents/"+id, "", "", "", nil)
		if decodeAgentResponse[models.Agent](t, get).Description != "nonempty 😀�" {
			t.Fatal("persisted Unicode")
		}
		expectAgentStatus(t, f.request(t, a, "DELETE", "/api/v1/agents/"+id, "", fmt.Sprintf("admin-delete-%t", flag), "", nil), 202)
	}
}

func TestAgentDeleteSQLFailureAndConcurrentSQL(t *testing.T) {
	f := newAgentFixture(t)
	a := f.login(t, "delete@example.org")
	makeAgent := func(key string) string {
		r := f.request(t, a, "POST", "/api/v1/agents", agentBody, key, "", nil)
		expectAgentStatus(t, r, 201)
		return decodeAgentResponse[models.Agent](t, r).ID
	}
	for _, table := range []string{"audit_events", "agent_operations"} {
		id := makeAgent("failure-" + table)
		nAudit, nOps := f.count(t, "audit_events"), f.count(t, "agent_operations")
		statement := `CREATE FUNCTION fail_delete() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'synthetic SQL failure'; END $$; CREATE TRIGGER fail_delete BEFORE `
		if table == "audit_events" {
			statement += `INSERT ON audit_events FOR EACH ROW WHEN (NEW.action='agent.deleted') EXECUTE FUNCTION fail_delete()`
		} else {
			statement += `UPDATE ON agent_operations FOR EACH ROW WHEN (NEW.operation='delete') EXECUTE FUNCTION fail_delete()`
		}
		if _, err := f.pool.Exec(statement); err != nil {
			t.Fatal(err)
		}
		failed := f.request(t, a, "DELETE", "/api/v1/agents/"+id, "", "retry-"+table, "", nil)
		expectAgentStatus(t, failed, 500)
		responseContract(t, contract(t, "openapi.yaml"), "/api/v1/agents/{id}", "DELETE", &http.Response{StatusCode: failed.status, Header: failed.header, Body: io.NopCloser(strings.NewReader(string(failed.body)))})
		if f.count(t, "agent_cleanup_requests") != 0 || f.count(t, "agent_operations") != nOps || f.count(t, "audit_events") != nAudit {
			t.Fatal("DELETE SQL failure partial commit")
		}
		expectAgentStatus(t, f.request(t, a, "GET", "/api/v1/agents/"+id, "", "", "", nil), 200)
		if _, err := f.pool.Exec("DROP TRIGGER fail_delete ON " + table + "; DROP FUNCTION fail_delete()"); err != nil {
			t.Fatal(err)
		}
		expectAgentStatus(t, f.request(t, a, "DELETE", "/api/v1/agents/"+id, "", "retry-"+table, "", nil), 202)
		if _, err := f.pool.Exec("DELETE FROM agent_cleanup_requests"); err != nil {
			t.Fatal(err)
		}
	}
	for _, same := range []bool{true, false} {
		id := makeAgent(fmt.Sprintf("concurrent-%t", same))
		before := f.count(t, "audit_events")
		responses := make(chan agentResponse, 4)
		start := make(chan struct{})
		var wg sync.WaitGroup
		for i := range 4 {
			wg.Add(1)
			go func() {
				defer wg.Done()
				<-start
				key := "same"
				if !same {
					key = fmt.Sprintf("different-%d", i)
				}
				responses <- f.request(t, a, "DELETE", "/api/v1/agents/"+id, "", key, "", nil)
			}()
		}
		close(start)
		wg.Wait()
		close(responses)
		accepted, missing := 0, 0
		var original string
		for r := range responses {
			switch r.status {
			case 202:
				accepted++
				if original != "" && original != string(r.body) {
					t.Fatal("different delete Job")
				}
				original = string(r.body)
			case 404:
				missing++
			default:
				t.Fatalf("delete status %d", r.status)
			}
		}
		if same && accepted != 4 || !same && (accepted != 1 || missing != 3) || f.count(t, "audit_events") != before+1 || f.count(t, "agent_cleanup_requests") != 1 {
			t.Fatal("concurrent delete duplicates")
		}
		if _, err := f.pool.Exec("DELETE FROM agent_cleanup_requests"); err != nil {
			t.Fatal(err)
		}
	}
}

func TestAgentDeleteChunkedAndErrorContractSQL(t *testing.T) {
	f := newAgentFixture(t)
	a := f.login(t, "chunked@example.org")
	doc := contract(t, "openapi.yaml")
	for _, tc := range []struct {
		c            agentClient
		method, body string
		headers      map[string]string
		status       int
	}{{a, "POST", `{}`, nil, 400}, {agentClient{}, "GET", "", nil, 401}, {a, "POST", agentBody, map[string]string{"Origin": "https://foreign.invalid"}, 403}} {
		r := f.request(t, tc.c, tc.method, "/api/v1/agents", tc.body, "contract-negative", "", tc.headers)
		expectAgentStatus(t, r, tc.status)
		responseContract(t, doc, "/api/v1/agents", tc.method, &http.Response{StatusCode: r.status, Header: r.header, Body: io.NopCloser(strings.NewReader(string(r.body)))})
	}
	r := f.request(t, a, "POST", "/api/v1/agents", agentBody, "chunked-create", "", nil)
	id := decodeAgentResponse[models.Agent](t, r).ID
	for _, body := range []string{"x", ""} {
		ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
		req, err := http.NewRequestWithContext(ctx, "DELETE", f.server.URL+"/api/v1/agents/"+id, io.NopCloser(strings.NewReader(body)))
		if err != nil {
			t.Fatal(err)
		}
		req.ContentLength = -1
		req.AddCookie(a.cookie)
		req.Header.Set("Origin", agentOrigin)
		req.Header.Set("X-CSRF-Token", a.csrf)
		req.Header.Set("Idempotency-Key", "chunked-delete")
		response, err := f.server.Client().Do(req)
		if err != nil {
			cancel()
			t.Fatal(err)
		}
		expected := 400
		if body == "" {
			expected = 202
		}
		if response.StatusCode != expected {
			t.Fatal("chunked DELETE")
		}
		responseContract(t, doc, "/api/v1/agents/{id}", "DELETE", response)
		cancel()
	}
}

func TestAgentPublishedCRUDStateSQL(t *testing.T) {
	f := newAgentFixture(t)
	a := f.login(t, "published@example.org")
	created := f.request(t, a, "POST", "/api/v1/agents", agentBody, "published-create", "", nil)
	expectAgentStatus(t, created, 201)
	id := decodeAgentResponse[models.Agent](t, created).ID
	if _, err := f.pool.Exec(`UPDATE agents SET status='published',knowledge_revision=4,active_knowledge_revision=3,config_revision=5,published_config_revision=2 WHERE id=$1`, id); err != nil {
		t.Fatal(err)
	}
	forbidden := strings.TrimSuffix(agentBody, "}") + `,"status":"draft"}`
	expectAgentStatus(t, f.request(t, a, "PATCH", "/api/v1/agents/"+id, forbidden, "forbidden-status", `"1"`, nil), 400)
	patched := f.request(t, a, "PATCH", "/api/v1/agents/"+id, agentBody, "published-patch", `"1"`, nil)
	expectAgentStatus(t, patched, 200)
	agent := decodeAgentResponse[models.Agent](t, patched)
	if agent.Status != "published" || agent.KnowledgeRevision != 4 || agent.ActiveKnowledgeRevision != 3 || agent.ConfigRevision != 5 || agent.PublishedConfigRevision != 2 {
		t.Fatal("PATCH changed snapshots")
	}
	expectAgentStatus(t, f.request(t, a, "DELETE", "/api/v1/agents/"+id, "", "published-delete", "", nil), 202)
	var status string
	var deleted bool
	if err := f.pool.QueryRow("SELECT status,deleted_at IS NOT NULL FROM agents WHERE id=$1", id).Scan(&status, &deleted); err != nil || status != "deleted" || !deleted {
		t.Fatal("published agent not tombstoned")
	}
}
