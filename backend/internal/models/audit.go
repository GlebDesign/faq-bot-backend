package models

import (
	"encoding/json"
	"time"
)

type AuditActor struct {
	Type string
	ID   string
}

type AuditEvent struct {
	OperationID   string
	Scope         string
	AccountID     string
	Action        string
	EntityType    string
	EntityID      string
	Outcome       string
	OccurredAt    time.Time
	RequestID     string
	TraceID       string
	SourceService string
	Metadata      map[string]any
}

type AuditRecord struct {
	ID            string
	EventKey      string
	SchemaVersion int
	Scope         string
	AccountID     string
	Actor         AuditActor
	Action        string
	EntityType    string
	EntityID      string
	Outcome       string
	OccurredAt    time.Time
	RecordedAt    time.Time
	RequestID     string
	TraceID       string
	SourceService string
	Metadata      json.RawMessage
}
