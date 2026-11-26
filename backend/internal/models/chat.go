package models

import (
	"encoding/json"
	"time"
)

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

type Conversation struct {
	ID        string
	AgentID   string
	CreatedAt time.Time
}

type Message struct {
	ID             string
	ConversationID string
	Status         string
	Question       string
	Answer         *string
	CreatedAt      time.Time
}

type ChatPosition struct {
	ID        string    `json:"id"`
	CreatedAt time.Time `json:"created_at"`
}
