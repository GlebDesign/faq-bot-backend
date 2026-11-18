package dto

type AgentCreate struct {
	Name string `json:"name"`
}

type AgentUpdate struct {
	Name string `json:"name"`
}

type Agent struct {
	ID                      string `json:"id"`
	AccountID               string `json:"account_id"`
	PublicID                string `json:"public_id"`
	Name                    string `json:"name"`
	Status                  string `json:"status"`
	Revision                int64  `json:"revision"`
	KnowledgeRevision       int64  `json:"knowledge_revision"`
	ActiveKnowledgeRevision int64  `json:"active_knowledge_revision"`
	ConfigRevision          int64  `json:"config_revision"`
	PublishedConfigRevision int64  `json:"published_config_revision"`
	PublicSourcesEnabled    bool   `json:"public_sources_enabled"`
}

type AgentPage struct {
	Items      []Agent `json:"items"`
	NextCursor *string `json:"next_cursor"`
}

type PublishRequest struct {
	KnowledgeRevision    int64 `json:"knowledge_revision"`
	ConfigRevision       int64 `json:"config_revision"`
	PublicSourcesEnabled bool  `json:"public_sources_enabled"`
}

type EmbedCode struct {
	AgentPublicID string `json:"agent_public_id"`
	Snippet       string `json:"snippet"`
}
