package dto

type PartnerSessionCreate struct {
	AgentPublicID  string `json:"agent_public_id"`
	VisitorSubject string `json:"visitor_subject"`
}

type PartnerCredentialCreate struct {
	AgentIDs  []string `json:"agent_ids"`
	Scopes    []string `json:"scopes"`
	ExpiresAt string   `json:"expires_at"`
}

type PartnerCredential struct {
	KeyID     string   `json:"key_id"`
	PartnerID string   `json:"partner_id"`
	AgentIDs  []string `json:"agent_ids"`
	Scopes    []string `json:"scopes"`
	ExpiresAt string   `json:"expires_at"`
	Status    string   `json:"status"`
}

type PartnerCredentialPage struct {
	Items      []PartnerCredential `json:"items"`
	NextCursor *string             `json:"next_cursor"`
}

type PartnerCredentialIssued struct {
	Credential PartnerCredential `json:"credential"`
	Secret     string            `json:"secret"`
}
