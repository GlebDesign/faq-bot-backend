package dto

type SupportTicketCreate struct {
	Category    string  `json:"category"`
	AgentID     *string `json:"agent_id"`
	Description string  `json:"description"`
}

type SupportStatusUpdate struct {
	Status string `json:"status"`
}

type SupportStatusEvent struct {
	Status    string `json:"status"`
	ChangedAt string `json:"changed_at"`
}

type SupportTicket struct {
	Category      string               `json:"category"`
	AgentID       *string              `json:"agent_id"`
	Description   string               `json:"description"`
	ID            string               `json:"id"`
	Status        string               `json:"status"`
	History       []SupportStatusEvent `json:"history"`
	AttachmentIDs []string             `json:"attachment_ids"`
	CreatedAt     string               `json:"created_at"`
}

type SupportTicketPage struct {
	Items      []SupportTicket `json:"items"`
	NextCursor *string         `json:"next_cursor"`
}

type AttachmentIntent struct {
	Filename    string `json:"filename"`
	ContentType string `json:"content_type"`
	Size        int64  `json:"size"`
}

type Attachment struct {
	ID           string  `json:"id"`
	TicketID     string  `json:"ticket_id"`
	Filename     string  `json:"filename"`
	DetectedType *string `json:"detected_type"`
	Size         int64   `json:"size"`
	Status       string  `json:"status"`
}

type ContactRequestCreate struct {
	MessageID      string `json:"message_id"`
	Contact        string `json:"contact"`
	Consent        bool   `json:"consent"`
	ConsentVersion string `json:"consent_version"`
}

type ContactReceipt struct {
	RequestID string `json:"request_id"`
	Status    string `json:"status"`
}

type ContactRequest struct {
	ID             string `json:"id"`
	AgentID        string `json:"agent_id"`
	ConversationID string `json:"conversation_id"`
	MessageID      string `json:"message_id"`
	Contact        string `json:"contact"`
	ConsentVersion string `json:"consent_version"`
	Status         string `json:"status"`
	CreatedAt      string `json:"created_at"`
}

type ContactRequestPage struct {
	Items      []ContactRequest `json:"items"`
	NextCursor *string          `json:"next_cursor"`
}

type ContactStatusUpdate struct {
	Status string `json:"status"`
}
