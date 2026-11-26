package handlers

import (
	"encoding/json"
	"fmt"
	"maps"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"faqbot/backend/internal/handlers/dto"
	"faqbot/backend/internal/models"
)

func createChatAgent(t *testing.T, f *agentFixture, c agentClient, key string) string {
	t.Helper()
	r := f.request(t, c, "POST", "/api/v1/agents", agentBody, key, "", nil)
	expectAgentStatus(t, r, 201)
	return decodeAgentResponse[models.Agent](t, r).ID
}

func chatBody(conversation, message string) string {
	return fmt.Sprintf(`{"conversation_id":%q,"message":%q,"stream":false}`, conversation, message)
}

func createChatConversation(t *testing.T, f *agentFixture, c agentClient, agent, key string) dto.Conversation {
	t.Helper()
	r := f.request(t, c, "POST", "/api/v1/agents/"+agent+"/conversations", "", key, "", nil)
	expectAgentStatus(t, r, 201)
	return decodeAgentResponse[dto.Conversation](t, r)
}

func TestChatHTTPHistoryScopesAndPagesSQL(t *testing.T) {
	f := newAgentFixture(t)
	a := f.login(t, "owner-a@example.org")
	b := f.login(t, "owner-b@example.org")
	same := f.login(t, "admin-a@example.org")
	if _, err := f.pool.Exec("DELETE FROM account_members WHERE account_id=$1 AND user_id=$2", same.identity.AccountID, same.identity.UserID); err != nil {
		t.Fatal(err)
	}
	if _, err := f.pool.Exec("INSERT INTO account_members(account_id,user_id,role) VALUES($1,$2,'admin')", a.identity.AccountID, same.identity.UserID); err != nil {
		t.Fatal(err)
	}
	same = f.login(t, "admin-a@example.org")
	agent := createChatAgent(t, f, a, "agent-one")
	otherAgent := createChatAgent(t, f, a, "agent-two")
	foreignAgent := createChatAgent(t, f, b, "foreign-agent")

	first := createChatConversation(t, f, a, agent, "one")
	if replay := createChatConversation(t, f, a, agent, "one"); replay != first {
		t.Fatal("repeat changed conversation")
	}
	second := createChatConversation(t, f, a, agent, "two")
	differentAgent := createChatConversation(t, f, a, otherAgent, "one")
	differentMember := createChatConversation(t, f, same, agent, "one")
	foreign := createChatConversation(t, f, b, foreignAgent, "one")
	if f.count(t, "conversations") != 5 {
		t.Fatal("conversation scope or repeat added unexpected rows")
	}
	if _, err := f.pool.Exec("UPDATE conversations SET created_at='2026-11-24 12:00Z' WHERE id IN ($1,$2)", first.ID, second.ID); err != nil {
		t.Fatal(err)
	}
	list := func(c agentClient, id, query string) agentResponse {
		return f.request(t, c, "GET", "/api/v1/agents/"+id+"/conversations"+query, "", "", "", nil)
	}
	messages := func(c agentClient, id, query string) agentResponse {
		return f.request(t, c, "GET", "/api/v1/conversations/"+id+"/messages"+query, "", "", "", nil)
	}
	r := list(a, agent, "?limit=1")
	expectAgentStatus(t, r, 200)
	page := decodeAgentResponse[dto.ConversationPage](t, r)
	if len(page.Items) != 1 || page.NextCursor == nil || page.Items[0].AgentID != agent {
		t.Fatalf("first page %+v", page)
	}
	wantFirst, wantSecond := first.ID, second.ID
	if wantFirst < wantSecond {
		wantFirst, wantSecond = wantSecond, wantFirst
	}
	if page.Items[0].ID != wantFirst {
		t.Fatal("not ordered by id on timestamp tie")
	}
	query := "?limit=1&cursor=" + url.QueryEscape(*page.NextCursor)
	r = list(a, agent, query)
	expectAgentStatus(t, r, 200)
	next := decodeAgentResponse[dto.ConversationPage](t, r)
	if len(next.Items) != 1 || next.Items[0].ID != wantSecond || next.NextCursor != nil {
		t.Fatalf("second page %+v", next)
	}
	for _, pair := range []struct {
		client agentClient
		id     string
	}{{a, otherAgent}, {same, agent}, {b, foreignAgent}} {
		expectAgentStatus(t, list(pair.client, pair.id, query), 400)
	}
	expectAgentStatus(t, list(a, agent, query+"tampered"), 400)
	expectAgentStatus(t, list(a, agent, "?limit=101"), 400)
	expectAgentStatus(t, list(a, agent, "?account_id="+b.identity.AccountID), 400)
	expectAgentStatus(t, list(a, foreignAgent, ""), 404)
	expectAgentStatus(t, list(agentClient{}, agent, ""), 401)
	for _, item := range []struct {
		client agentClient
		id     string
	}{{a, foreign.ID}, {a, differentMember.ID}, {same, first.ID}} {
		expectAgentStatus(t, messages(item.client, item.id, ""), 404)
	}
	expectAgentStatus(t, messages(a, first.ID, query), 400)
	r = messages(a, differentAgent.ID, "")
	expectAgentStatus(t, r, 200)
	if string(r.body) != "{\"items\":[],\"next_cursor\":null}\n" {
		t.Fatalf("empty messages %s", r.body)
	}
	send := func(c agentClient, agentID, conversation, key string) agentResponse {
		return f.request(t, c, "POST", "/api/v1/agents/"+agentID+"/test-chat/messages", chatBody(conversation, "Question"), key, "", nil)
	}
	expectAgentStatus(t, send(a, otherAgent, first.ID, "message"), 404)
	expectAgentStatus(t, send(a, agent, differentMember.ID, "message"), 404)
	expectAgentStatus(t, send(a, agent, foreign.ID, "message"), 404)
	expectAgentStatus(t, send(a, agent, first.ID, "message"), 202)
	expectAgentStatus(t, send(a, agent, first.ID, "message-two"), 202)
	r = messages(a, first.ID, "?limit=1")
	expectAgentStatus(t, r, 200)
	messagePage := decodeAgentResponse[dto.MessagePage](t, r)
	if len(messagePage.Items) != 1 || messagePage.Items[0].ConversationID != first.ID || messagePage.Items[0].Status != "accepted" || messagePage.Items[0].Answer != nil || messagePage.NextCursor == nil {
		t.Fatalf("message page %+v", messagePage)
	}
	messageQuery := "?limit=1&cursor=" + url.QueryEscape(*messagePage.NextCursor)
	expectAgentStatus(t, messages(a, second.ID, messageQuery), 400)
	r = messages(a, first.ID, messageQuery)
	expectAgentStatus(t, r, 200)
	nextMessages := decodeAgentResponse[dto.MessagePage](t, r)
	if len(nextMessages.Items) != 1 || nextMessages.Items[0].ID == messagePage.Items[0].ID || nextMessages.NextCursor != nil {
		t.Fatalf("second message page %+v", nextMessages)
	}
	expectAgentStatus(t, f.request(t, a, "DELETE", "/api/v1/agents/"+agent, "", "delete-agent", "", nil), 202)
	expectAgentStatus(t, list(a, agent, ""), 404)
	expectAgentStatus(t, messages(a, first.ID, ""), 404)
	expectAgentStatus(t, send(a, agent, first.ID, "message"), 404)
}

func TestChatHTTPConcurrentReplayAndTerminalSQL(t *testing.T) {
	f := newAgentFixture(t)
	c := f.login(t, "owner@example.org")
	agent := createChatAgent(t, f, c, "agent")
	conversation := createChatConversation(t, f, c, agent, "conversation")
	other := createChatConversation(t, f, c, agent, "other-conversation")
	path := "/api/v1/agents/" + agent + "/test-chat/messages"
	body := chatBody(conversation.ID, "  Привет?  ")
	submit := func(body, key string) agentResponse { return f.request(t, c, "POST", path, body, key, "", nil) }
	const workers = 6
	var wg sync.WaitGroup
	start := make(chan struct{})
	responses := make([]agentResponse, workers)
	workerErrors := make([]error, workers)
	for i := range responses {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			responses[i], workerErrors[i] = f.doRequest(t.Context(), c, "POST", path, body, "same", "", nil)
		}()
	}
	close(start)
	wg.Wait()
	for _, err := range workerErrors {
		if err != nil {
			t.Fatal(err)
		}
	}
	first := responses[0]
	expectAgentStatus(t, first, 202)
	pending := decodeAgentResponse[dto.ChatOperation](t, first)
	if pending.OperationID == "" || pending.MessageID == "" || pending.ConversationID != conversation.ID || pending.Status != "accepted" || first.header.Get("Retry-After") == "" {
		t.Fatalf("pending %+v", pending)
	}
	for _, candidate := range responses {
		expectAgentStatus(t, candidate, 202)
		if string(candidate.body) != string(first.body) {
			t.Fatal("parallel replay changed operation")
		}
	}
	if f.count(t, "ai_requests") != 1 || f.count(t, "messages") != 1 {
		t.Fatal("parallel replay saved extra rows")
	}
	for _, body := range []string{chatBody(conversation.ID, "Привет?"), chatBody(other.ID, "  Привет?  ")} {
		conflict := submit(body, "same")
		expectAgentStatus(t, conflict, 409)
		if !strings.Contains(string(conflict.body), `"idempotency_conflict"`) {
			t.Fatalf("conflict %s", conflict.body)
		}
	}
	if _, err := f.pool.Exec(`UPDATE ai_requests SET state='generating' WHERE id=$1`, pending.OperationID); err != nil {
		t.Fatal(err)
	}
	replayGenerating := submit(body, "same")
	expectAgentStatus(t, replayGenerating, 202)
	generating := decodeAgentResponse[dto.ChatOperation](t, replayGenerating)
	pending.Status = "generating"
	if generating != pending {
		t.Fatalf("generating replay changed operation: %+v", generating)
	}
	for _, status := range []string{"completed", "no_answer"} {
		answer := dto.TestChatAnswer{MessageID: pending.MessageID, ConversationID: conversation.ID, Status: status, Answer: "Сохранённый ответ", HasAnswer: status == "completed", Sources: []dto.CabinetSource{}, RemainingRequests: 5, KnowledgeRevision: 1}
		stored, err := json.Marshal(answer)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := f.pool.Exec(`UPDATE messages SET status=$1,answer=$2 WHERE id=$3`, status, answer.Answer, answer.MessageID); err != nil {
			t.Fatal(err)
		}
		if _, err := f.pool.Exec(`UPDATE ai_requests SET state=$1,result=$2::jsonb WHERE id=$3`, status, stored, pending.OperationID); err != nil {
			t.Fatal(err)
		}
		replay := submit(body, "same")
		expectAgentStatus(t, replay, 200)
		if strings.TrimSpace(string(replay.body)) != string(stored) {
			t.Fatalf("terminal replay changed result %s", replay.body)
		}
		if status == "completed" {
			history := f.request(t, c, "GET", "/api/v1/conversations/"+conversation.ID+"/messages", "", "", "", nil)
			expectAgentStatus(t, history, 200)
			items := decodeAgentResponse[dto.MessagePage](t, history).Items
			if len(items) != 1 || items[0].ID != answer.MessageID || items[0].Status != status ||
				items[0].Question != "  Привет?  " || items[0].Answer == nil || *items[0].Answer != answer.Answer {
				t.Fatalf("completed history: %+v", items)
			}
			if _, err := time.Parse(time.RFC3339Nano, items[0].CreatedAt); err != nil {
				t.Fatalf("completed history timestamp: %v", err)
			}
		}
	}
	for _, caseData := range []struct {
		state, code string
		status      int
	}{{"failed", "ai_unavailable", 503}, {"cancelled", "deadline_exceeded", 504}} {
		if _, err := f.pool.Exec(`UPDATE ai_requests SET state=$1,result=jsonb_build_object('status',$1::text,'code',$2::text) WHERE id=$3`, caseData.state, caseData.code, pending.OperationID); err != nil {
			t.Fatal(err)
		}
		got := submit(body, "same")
		expectAgentStatus(t, got, caseData.status)
		if !strings.Contains(string(got.body), caseData.code) {
			t.Fatalf("error replay %s", got.body)
		}
	}
	valid := map[string]any{"status": "completed", "message_id": pending.MessageID, "conversation_id": conversation.ID, "answer": "answer", "has_answer": true, "sources": []any{}, "remaining_requests": 0, "knowledge_revision": 1}
	for _, test := range []struct {
		name, field string
		value       any
	}{
		{"missing_has_answer", "has_answer", nil},
		{"null_remaining_requests", "remaining_requests", json.RawMessage("null")},
		{"negative_remaining_requests", "remaining_requests", -1},
		{"missing_revision", "knowledge_revision", nil},
		{"wrong_message", "message_id", conversation.ID},
		{"wrong_conversation", "conversation_id", other.ID},
		{"wrong_has_answer", "has_answer", false},
		{"empty_answer", "answer", ""},
		{"oversize_answer", "answer", strings.Repeat("ё", 10001)},
		{"null_sources", "sources", json.RawMessage("null")},
		{"invalid_source", "sources", []any{map[string]any{"citation_id": "citation"}}},
	} {
		t.Run(test.name, func(t *testing.T) {
			result := maps.Clone(valid)
			if test.value == nil {
				delete(result, test.field)
			} else {
				result[test.field] = test.value
			}
			stored, err := json.Marshal(result)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := f.pool.Exec(`UPDATE ai_requests SET state='completed',result=$1::jsonb WHERE id=$2`, stored, pending.OperationID); err != nil {
				t.Fatal(err)
			}
			r := submit(body, "same")
			expectAgentStatus(t, r, 500)
			if !strings.Contains(string(r.body), `"internal_error"`) {
				t.Fatalf("invalid replay result %s", r.body)
			}
		})
	}
	for _, stored := range []string{`{"status":"failed"}`, `{"status":"failed","code":"unknown"}`, `{"status":"failed","code":4}`} {
		if _, err := f.pool.Exec(`UPDATE ai_requests SET state='failed',result=$1::jsonb WHERE id=$2`, stored, pending.OperationID); err != nil {
			t.Fatal(err)
		}
		r := submit(body, "same")
		expectAgentStatus(t, r, 500)
		if !strings.Contains(string(r.body), `"internal_error"`) {
			t.Fatalf("invalid failure replay %s", r.body)
		}
	}
	if f.count(t, "ai_requests") != 1 || f.count(t, "messages") != 1 {
		t.Fatal("replay/conflict changed row counts")
	}
	var text string
	if err := f.pool.QueryRow(`SELECT question FROM messages WHERE id=$1`, pending.MessageID).Scan(&text); err != nil || text != "  Привет?  " {
		t.Fatal("question bytes changed", err)
	}
	for _, invalid := range []string{
		strings.Replace(body, `"stream":false`, `"stream":null`, 1),
		strings.Replace(body, `"stream":false`, `"stream":true`, 1),
		strings.Replace(body, `"stream":false`, `"stream":false,"account_id":"other"`, 1),
		strings.Replace(body, `"stream":false`, `"stream":false,"stream":false`, 1),
		chatBody(conversation.ID, "\u00a0\u202f"), chatBody(conversation.ID, strings.Repeat("ё", 4001)),
	} {
		expectAgentStatus(t, submit(invalid, "invalid"), 400)
	}
	expectAgentStatus(t, f.request(t, c, "POST", path, body, "bad-csrf", "", map[string]string{"X-CSRF-Token": "forged"}), 403)
	expectAgentStatus(t, f.request(t, c, "POST", path, body, "bad-origin", "", map[string]string{"Origin": "https://other.example.invalid"}), 403)
	if _, err := f.pool.Exec("DELETE FROM account_members WHERE account_id=$1 AND user_id=$2", c.identity.AccountID, c.identity.UserID); err != nil {
		t.Fatal(err)
	}
	expectAgentStatus(t, submit(body, "same"), 401)
}
