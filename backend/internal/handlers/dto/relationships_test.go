package dto

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"reflect"
	"strconv"
	"strings"
	"testing"

	"github.com/getkin/kin-openapi/openapi3"
	"github.com/getkin/kin-openapi/openapi3filter"
	"github.com/getkin/kin-openapi/routers/legacy"
)

func TestPlannedCabinetWriteProtection(t *testing.T) {
	doc := loadContract(t)
	for path, item := range doc.Paths.Map() {
		for method, op := range item.Operations() {
			if method == "GET" || method == "HEAD" || op.OperationID == "createFeedback" || op.Security == nil || !reflect.DeepEqual(*op.Security, openapi3.SecurityRequirements{{"CabinetIdentity": {}}}) {
				continue
			}
			for _, name := range []string{"Origin", "X-CSRF-Token"} {
				found := false
				for _, p := range op.Parameters {
					if p.Value.In == "header" && p.Value.Name == name && p.Value.Required {
						found = true
					}
				}
				if !found {
					t.Fatalf("%s %s lacks required %s", method, path, name)
				}
			}
		}
	}
	router, err := legacy.NewRouter(doc)
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name, cookie, csrf, origin, bearer string
		valid                              bool
	}{
		{name: "widget without cabinet headers", bearer: "Bearer fixture-widget", valid: true},
		{name: "cabinet AND", cookie: "faqbot_session=fixture", csrf: "fixture-csrf", origin: "https://cabinet.example.invalid", valid: true},
		{name: "cabinet missing csrf", cookie: "faqbot_session=fixture", origin: "https://cabinet.example.invalid"},
		{name: "cabinet missing origin", cookie: "faqbot_session=fixture", csrf: "fixture-csrf"},
		{name: "cabinet foreign origin", cookie: "faqbot_session=fixture", csrf: "fixture-csrf", origin: "https://foreign.example.invalid"},
		{name: "unauthenticated"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req, err := http.NewRequest("POST", "https://api.example.invalid/api/v1/messages/msg_a1/feedback", strings.NewReader(`{"rating":"helpful"}`))
			if err != nil {
				t.Fatal(err)
			}
			req.Header.Set("Content-Type", "application/json")
			req.Header.Set("Idempotency-Key", "same-key")
			req.Header.Set("Cookie", tc.cookie)
			req.Header.Set("X-CSRF-Token", tc.csrf)
			req.Header.Set("Origin", tc.origin)
			req.Header.Set("Authorization", tc.bearer)
			route, params, err := router.FindRoute(req)
			if err != nil {
				t.Fatal(err)
			}
			auth := func(_ context.Context, in *openapi3filter.AuthenticationInput) error {
				r := in.RequestValidationInput.Request
				switch in.SecuritySchemeName {
				case "CabinetIdentity":
					if _, err := r.Cookie("faqbot_session"); err == nil {
						return nil
					}
				case "CabinetCSRF":
					if r.Header.Get("X-CSRF-Token") != "" {
						return nil
					}
				case "CabinetOrigin":
					if r.Header.Get("Origin") == "https://cabinet.example.invalid" {
						return nil
					}
				case "WidgetSession":
					if r.Header.Get("Authorization") == "Bearer fixture-widget" {
						return nil
					}
				}
				return errors.New("synthetic credential/header requirement absent")
			}
			err = openapi3filter.ValidateRequest(t.Context(), &openapi3filter.RequestValidationInput{Request: req, PathParams: params, Route: route, Options: &openapi3filter.Options{AuthenticationFunc: auth}})
			if (err == nil) != tc.valid {
				t.Fatalf("valid=%v: %v", tc.valid, err)
			}
		})
	}
}

func TestSensitiveJSONCachePolicy(t *testing.T) {
	doc := loadContract(t)
	for path, item := range doc.Paths.Map() {
		for method, op := range item.Operations() {
			for status, ref := range op.Responses.Map() {
				if ref.Value.Content.Get("application/json") == nil {
					continue
				}
				header := ref.Value.Headers["Cache-Control"]
				if header == nil || !header.Value.Required || !reflect.DeepEqual(header.Value.Schema.Value.Enum, []any{"no-store"}) {
					t.Fatalf("%s %s %s lacks no-store", method, path, status)
				}
			}
		}
	}
	router, err := legacy.NewRouter(doc)
	if err != nil {
		t.Fatal(err)
	}
	cases := readFixtures[[]struct {
		Name string
		Body json.RawMessage
	}](t, "fixtures/responses.json")
	var body []byte
	for _, c := range cases {
		if c.Name == "getAgent.success" {
			body = c.Body
		}
	}
	req, err := http.NewRequest("GET", "https://api.example.invalid/api/v1/agents/agent_a1", nil)
	if err != nil {
		t.Fatal(err)
	}
	route, params, err := router.FindRoute(req)
	if err != nil {
		t.Fatal(err)
	}
	for _, policy := range []string{"", "public", "no-store"} {
		input := &openapi3filter.ResponseValidationInput{RequestValidationInput: &openapi3filter.RequestValidationInput{Request: req, PathParams: params, Route: route}, Status: 200, Header: http.Header{"Content-Type": {"application/json"}}}
		if policy != "" {
			input.Header.Set("Cache-Control", policy)
		}
		input.SetBodyBytes(body)
		err := openapi3filter.ValidateResponse(t.Context(), input)
		if (err == nil) != (policy == "no-store") {
			t.Fatalf("cache policy %q accepted=%v: %v", policy, err == nil, err)
		}
	}
}

func TestWidgetHistoryUsesSessionScope(t *testing.T) {
	doc := loadContract(t)
	op := doc.Paths.Value("/api/v1/widget/messages").Get
	if op == nil || op.Security == nil || !reflect.DeepEqual(*op.Security, openapi3.SecurityRequirements{{"WidgetSession": {}}}) {
		t.Fatal("history must require WidgetSession")
	}
	if op.RequestBody != nil {
		t.Fatal("history must not accept browser history")
	}
	for _, p := range op.Parameters {
		if p.Value.In == "query" && p.Value.Name != "limit" && p.Value.Name != "cursor" {
			t.Fatal("caller controls history scope")
		}
	}
	identity := readFixtures[struct {
		WidgetScope map[string]string `json:"widget_scope"`
	}](t, "fixtures/identities.json")
	for _, row := range readFixtures[[]struct {
		Name   string
		Status int
		Body   json.RawMessage
	}](t, "fixtures/responses.json") {
		if strings.HasPrefix(row.Name, "listWidgetMessages.") && row.Status == 200 {
			var page MessagePage
			if err := json.Unmarshal(row.Body, &page); err != nil {
				t.Fatal(err)
			}
			for _, message := range page.Items {
				if message.ConversationID != identity.WidgetScope["conversation_id"] {
					t.Fatal("history fixture crosses visitor conversation")
				}
			}
		}
	}
	if op.Responses.Status(200).Value.Content.Get("application/json").Schema.Ref != "#/components/schemas/MessagePage" {
		t.Fatal("history is not MessagePage")
	}
}

func TestFixtureRelationships(t *testing.T) {
	identities := readFixtures[struct {
		Accounts []struct {
			AccountID string   `json:"account_id"`
			OwnerID   string   `json:"owner_id"`
			AgentIDs  []string `json:"agent_ids"`
		}
		WidgetScope map[string]string `json:"widget_scope"`
		Resources   map[string]map[string]string
	}](t, "fixtures/identities.json")
	accounts, owners, agents := map[string]bool{}, map[string]string{}, map[string]string{}
	for _, a := range identities.Accounts {
		accounts[a.AccountID] = true
		owners[a.OwnerID] = a.AccountID
		for _, id := range a.AgentIDs {
			agents[id] = a.AccountID
		}
	}
	if len(accounts) != 2 || agents["agent_a1"] != agents["agent_a2"] || agents["agent_a1"] == agents["agent_b1"] {
		t.Fatal("scope fixture must exercise two tenants and two sibling agents")
	}
	if agents[identities.WidgetScope["agent_id"]] != identities.WidgetScope["account_id"] || identities.Resources[identities.WidgetScope["conversation_id"]]["agent_id"] != identities.WidgetScope["agent_id"] {
		t.Fatal("widget identity is inconsistent")
	}
	var check func(any)
	check = func(value any) {
		switch v := value.(type) {
		case []any:
			for _, x := range v {
				check(x)
			}
		case map[string]any:
			if account, ok := v["account_id"].(string); ok && !accounts[account] {
				t.Fatalf("unknown tenant %s", account)
			}
			if user, ok := v["user_id"].(string); ok && owners[user] != v["account_id"] {
				t.Fatal("identity owner/account mismatch")
			}
			for _, field := range []string{"agent_id", "conversation_id", "message_id", "document_id", "ticket_id", "package_id", "key_id", "partner_id", "citation_id", "resource_id"} {
				if id, ok := v[field].(string); ok {
					if _, exists := identities.Resources[id]; !exists {
						t.Fatalf("unknown %s=%s", field, id)
					}
				}
			}
			id, _ := v["id"].(string)
			if id == "" {
				id, _ = v["message_id"].(string)
			}
			if id == "" {
				id, _ = v["citation_id"].(string)
			}
			if id != "" {
				resource, ok := identities.Resources[id]
				if !ok {
					t.Fatalf("unknown resource %s", id)
				}
				for field, want := range resource {
					if got, ok := v[field]; ok && got != want {
						t.Fatalf("%s %s=%v want %s", id, field, got, want)
					}
				}
			}
			if ids, ok := v["agent_ids"].([]any); ok {
				for _, raw := range ids {
					agent, ok := raw.(string)
					if !ok || agents[agent] == "" {
						t.Fatal("unknown agent in credential scope")
					}
				}
			}
			if agent, ok := v["agent_id"].(string); ok {
				if account, ok := v["account_id"].(string); ok && agents[agent] != account {
					t.Fatal("agent crossed tenant")
				}
			}
			for _, x := range v {
				check(x)
			}
		}
	}
	for id, resource := range identities.Resources {
		for field, value := range resource {
			if field == "account_id" && !accounts[value] {
				t.Fatalf("resource %s has unknown account", id)
			}
			if field == "agent_id" && agents[value] == "" {
				t.Fatalf("resource %s has unknown agent", id)
			}
		}
	}
	doc := loadContract(t)
	router, err := legacy.NewRouter(doc)
	if err != nil {
		t.Fatal(err)
	}
	for _, row := range readFixtures[[]struct {
		Name, Method, Path string
		Valid              bool
		Body               any
	}](t, "fixtures/requests.json") {
		req, err := http.NewRequest(row.Method, "https://api.example.invalid"+row.Path, nil)
		if err != nil {
			t.Fatal(err)
		}
		_, params, err := router.FindRoute(req)
		if err != nil {
			t.Fatal(err)
		}
		for _, id := range params {
			if _, ok := identities.Resources[id]; !ok {
				t.Fatalf("request %s uses unknown path ID %s", row.Name, id)
			}
		}
		if row.Valid {
			check(row.Body)
		}
	}
	for _, file := range []string{"responses", "flows"} {
		rows := readFixtures[[]struct {
			Name, Method, Path string
			Status             int
			Body               any
			Kind               string
		}](t, "fixtures/"+file+".json")
		for _, row := range rows {
			t.Run(file+"/"+row.Name, func(t *testing.T) {
				if file == "flows" && row.Kind != "independent_state_example" {
					t.Fatal("flow states are not labeled independent")
				}
				req, err := http.NewRequest(row.Method, "https://api.example.invalid"+row.Path, nil)
				if err != nil {
					t.Fatal(err)
				}
				_, params, err := router.FindRoute(req)
				if err != nil {
					t.Fatal(err)
				}
				for _, id := range params {
					if _, ok := identities.Resources[id]; !ok {
						t.Fatalf("unknown path resource %s", id)
					}
				}
				if row.Status >= 300 {
					return
				}
				check(row.Body)
				body, ok := row.Body.(map[string]any)
				if !ok {
					return
				}
				for _, pathID := range params {
					if bodyID, ok := body["id"].(string); ok && bodyID != pathID {
						related := false
						for _, parent := range identities.Resources[bodyID] {
							if parent == pathID {
								related = true
							}
						}
						for _, v := range body {
							if s, ok := v.(string); ok && s == pathID {
								related = true
							}
						}
						if !related {
							t.Fatalf("path resource %s disagrees with body %s", pathID, bodyID)
						}
					}
					if strings.Contains(row.Path, "/sources/") {
						if body["citation_id"] != params["citation_id"] {
							t.Fatal("citation path/body mismatch")
						}
					}
				}
			})
		}
	}
}

func fixtureValue(t *testing.T, row map[string]any, pointer string) any {
	t.Helper()
	if pointer == "/path_agent_id" {
		parts := strings.Split(row["path"].(string), "/")
		if len(parts) > 4 && parts[3] == "agents" {
			return parts[4]
		}
		t.Fatal("missing agent path")
	}
	var value any = row
	for _, part := range strings.Split(strings.TrimPrefix(pointer, "/"), "/") {
		switch v := value.(type) {
		case map[string]any:
			var ok bool
			value, ok = v[part]
			if !ok {
				t.Fatalf("missing fixture pointer %s", pointer)
			}
		case []any:
			i, err := strconv.Atoi(part)
			if err != nil || i < 0 || i >= len(v) {
				t.Fatal("invalid array fixture pointer")
			}
			value = v[i]
		default:
			t.Fatalf("invalid fixture pointer %s", pointer)
		}
	}
	return value
}

func TestLinkedScenarioContinuity(t *testing.T) {
	requests, responses := map[string]map[string]any{}, map[string]map[string]any{}
	for _, row := range readFixtures[[]map[string]any](t, "fixtures/requests.json") {
		requests[row["name"].(string)] = row
	}
	for _, file := range []string{"responses", "flows"} {
		for _, row := range readFixtures[[]map[string]any](t, "fixtures/"+file+".json") {
			responses[row["name"].(string)] = row
		}
	}
	scenarios := readFixtures[[]struct {
		Name  string
		Steps []struct{ Request, Response string }
		Links [][]any
	}](t, "fixtures/scenarios.json")
	if len(scenarios) != 4 {
		t.Fatal("missing owner, visitor, support/package or partner scenario")
	}
	for _, scenario := range scenarios {
		t.Run(scenario.Name, func(t *testing.T) {
			for _, step := range scenario.Steps {
				req, res := requests[step.Request], responses[step.Response]
				if req == nil || res == nil || req["valid"] != true {
					t.Fatal("scenario references missing/invalid example")
				}
				if req["method"] != res["method"] || strings.Split(req["path"].(string), "?")[0] != res["path"] {
					t.Fatal("linked request/response route differs")
				}
			}
			for _, link := range scenario.Links {
				from, to := scenario.Steps[int(link[0].(float64))], scenario.Steps[int(link[3].(float64))]
				a, b := requests[from.Request], requests[to.Request]
				if link[1] == "response" {
					a = responses[from.Response]
				}
				if link[4] == "response" {
					b = responses[to.Response]
				}
				if !reflect.DeepEqual(fixtureValue(t, a, link[2].(string)), fixtureValue(t, b, link[5].(string))) {
					t.Fatalf("scenario continuity failed at %v", link)
				}
			}
			if scenario.Name == "owner-knowledge-publish" {
				first := responses[scenario.Steps[0].Response]["body"].(map[string]any)
				last := responses[scenario.Steps[3].Response]["body"].(map[string]any)
				if last["status"] != "published" || last["account_id"] != first["account_id"] || last["revision"].(float64) <= first["revision"].(float64) || last["knowledge_revision"].(float64) < first["knowledge_revision"].(float64) || last["active_knowledge_revision"] != last["knowledge_revision"] || last["published_config_revision"] != last["config_revision"] {
					t.Fatal("published identity/revision continuity is invalid")
				}
			}
		})
	}
}

func TestNonblankTextPreservesMeaningfulWhitespace(t *testing.T) {
	doc := loadContract(t)
	for _, field := range []struct{ schema, property string }{{"AgentCreate", "name"}, {"MessageCreate", "message"}} {
		schema := doc.Components.Schemas[field.schema].Value.Properties[field.property].Value
		if err := schema.VisitJSON("\u2003  Вопрос\tс пробелом  \u00a0"); err != nil {
			t.Fatal(err)
		}
	}
}
