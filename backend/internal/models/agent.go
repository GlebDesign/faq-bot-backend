package models

import "time"

type Agent struct {
	ID                      string    `json:"id"`
	AccountID               string    `json:"account_id"`
	PublicID                string    `json:"public_id"`
	Name                    string    `json:"name"`
	Website                 string    `json:"website"`
	Description             string    `json:"description"`
	Status                  string    `json:"status"`
	Revision                int64     `json:"revision"`
	KnowledgeRevision       int64     `json:"knowledge_revision"`
	ActiveKnowledgeRevision int64     `json:"active_knowledge_revision"`
	ConfigRevision          int64     `json:"config_revision"`
	PublishedConfigRevision int64     `json:"published_config_revision"`
	PublicSourcesEnabled    bool      `json:"public_sources_enabled"`
	CreatedAt               time.Time `json:"-"`
}

type AgentInput struct {
	Name        string `json:"name"`
	Website     string `json:"website"`
	Description string `json:"description"`
}

type AgentPosition struct {
	CreatedAt time.Time `json:"created_at"`
	ID        string    `json:"id"`
}

type AgentOperation struct {
	ID          string
	AccountID   string
	ActorID     string
	Operation   string
	Resource    string
	KeyHash     []byte
	PayloadHash []byte
	Result      []byte
}

type AgentCleanup struct {
	ID        string  `json:"id"`
	Status    string  `json:"status"`
	ErrorCode *string `json:"error_code"`
}
