package handlers

import (
	"bytes"
	"io"
	"net/http"
	"testing"

	"faqbot/backend/internal/handlers/dto"
)

func TestChatHTTPResponseContractSQL(t *testing.T) {
	f := newAgentFixture(t)
	client := f.login(t, "owner@example.org")
	agent := createChatAgent(t, f, client, "create-agent")
	doc := contract(t, "openapi.yaml")

	conversations := "/api/v1/agents/{id}/conversations"
	message := "/api/v1/agents/{id}/test-chat/messages"
	history := "/api/v1/conversations/{id}/messages"
	check := func(pattern, path, method, body, key string, status int, header, value string) agentResponse {
		t.Helper()
		r := f.request(t, client, method, path, body, key, "", nil)
		expectAgentStatus(t, r, status)
		responseContract(t, doc, pattern, method, &http.Response{
			StatusCode: r.status, Header: r.header, Body: io.NopCloser(bytes.NewReader(r.body)),
		})
		if header != "" && r.header.Get(header) != value {
			t.Fatalf("%s %s: %s = %q, want %q", method, path, header, r.header.Get(header), value)
		}
		return r
	}
	agentPath := "/api/v1/agents/" + agent
	r := check(conversations, agentPath+"/conversations", "POST", "", "conversation", 201, "", "")
	conversation := decodeAgentResponse[dto.Conversation](t, r).ID
	check(conversations, agentPath+"/conversations", "GET", "", "", 200, "", "")
	check(conversations, agentPath+"/conversations", "PUT", "", "", 405, "Allow", "GET, POST")
	check(message, agentPath+"/test-chat/messages", "POST", chatBody(conversation, "Вопрос"), "message", 202, "Retry-After", "1")
	check(message, agentPath+"/test-chat/messages", "GET", "", "", 405, "Allow", "POST")
	path := "/api/v1/conversations/" + conversation + "/messages"
	check(history, path, "GET", "", "", 200, "", "")
	check(history, path, "POST", "", "", 405, "Allow", "GET")
}
