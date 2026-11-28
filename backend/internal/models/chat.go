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

type PublicSource struct {
	CitationID  string `json:"citation_id"`
	DisplayName string `json:"display_name"`
	Locator     string `json:"locator"`
	Excerpt     string `json:"excerpt"`
}

type CabinetSource struct {
	CitationID        string `json:"citation_id"`
	DisplayName       string `json:"display_name"`
	Locator           string `json:"locator"`
	Excerpt           string `json:"excerpt"`
	DocumentID        string `json:"document_id"`
	DocumentVersion   int64  `json:"document_version"`
	KnowledgeRevision int64  `json:"knowledge_revision"`
}

type ChatAnswer struct {
	MessageID         string         `json:"message_id"`
	ConversationID    string         `json:"conversation_id"`
	Status            string         `json:"status"`
	Answer            string         `json:"answer"`
	HasAnswer         bool           `json:"has_answer"`
	Sources           []PublicSource `json:"sources"`
	RemainingRequests int64          `json:"remaining_requests"`
}

type TestChatAnswer struct {
	MessageID         string          `json:"message_id"`
	ConversationID    string          `json:"conversation_id"`
	Status            string          `json:"status"`
	Answer            string          `json:"answer"`
	HasAnswer         bool            `json:"has_answer"`
	Sources           []CabinetSource `json:"sources"`
	RemainingRequests int64           `json:"remaining_requests"`
	KnowledgeRevision int64           `json:"knowledge_revision"`
}

type GeneratedAnswer struct {
	Answer            string
	HasAnswer         bool
	Sources           []CabinetSource
	KnowledgeRevision int64
}

type GenerationFailure string

const (
	GenerationFailed    GenerationFailure = "ai_unavailable"
	GenerationCancelled GenerationFailure = "cancelled"
	GenerationTimedOut  GenerationFailure = "deadline_exceeded"
)

type GenerationOutcome struct {
	Answer  *GeneratedAnswer
	Failure GenerationFailure
}

type ReservationKey struct {
	AccountID string
	RequestID string
	Deadline  time.Time
}
