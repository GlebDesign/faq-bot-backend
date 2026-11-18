package models

import "encoding/json"

type AIRequestInput struct {
	ID             string
	AccountID      string
	AgentID        string
	VisitorID      string
	MemberID       string
	Operation      string
	IdempotencyKey string
	ConversationID string
	MessageID      string
	Message        string
}

type AIRequest struct {
	MessageID string
	ID        string
	State     string
	Result    json.RawMessage
}
