package dto

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

type MessageCreate struct {
	ConversationID string `json:"conversation_id"`
	Message        string `json:"message"`
	Stream         bool   `json:"stream"`
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
