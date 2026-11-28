package dto

import "faqbot/backend/internal/models"

type PublicSource = models.PublicSource
type CabinetSource = models.CabinetSource

type MessageCreate struct {
	ConversationID string `json:"conversation_id"`
	Message        string `json:"message"`
	Stream         bool   `json:"stream"`
}

type ChatAnswer = models.ChatAnswer
type TestChatAnswer = models.TestChatAnswer

type Conversation struct {
	ID        string `json:"id"`
	AgentID   string `json:"agent_id"`
	CreatedAt string `json:"created_at"`
}

type ConversationPage struct {
	Items      []Conversation `json:"items"`
	NextCursor *string        `json:"next_cursor"`
}

type Message struct {
	ID             string  `json:"id"`
	ConversationID string  `json:"conversation_id"`
	Status         string  `json:"status"`
	Question       string  `json:"question"`
	Answer         *string `json:"answer"`
	CreatedAt      string  `json:"created_at"`
}

type MessagePage struct {
	Items      []Message `json:"items"`
	NextCursor *string   `json:"next_cursor"`
}

type FeedbackCreate struct {
	Rating string `json:"rating"`
}

type Feedback struct {
	MessageID string `json:"message_id"`
	Rating    string `json:"rating"`
}

type Analytics struct {
	Answered   int64 `json:"answered"`
	Unanswered int64 `json:"unanswered"`
	Helpful    int64 `json:"helpful"`
	Unhelpful  int64 `json:"unhelpful"`
}

type Unanswered struct {
	MessageID string `json:"message_id"`
	Question  string `json:"question"`
	CreatedAt string `json:"created_at"`
}

type UnansweredPage struct {
	Items      []Unanswered `json:"items"`
	NextCursor *string      `json:"next_cursor"`
}

type ChatOperation struct {
	OperationID    string `json:"operation_id"`
	MessageID      string `json:"message_id"`
	ConversationID string `json:"conversation_id"`
	Status         string `json:"status"`
}
