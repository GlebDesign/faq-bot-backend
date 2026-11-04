package messaging

import "encoding/json"

type Message struct {
	SchemaVersion     int              `json:"schema_version"`
	EventType         string           `json:"event_type"`
	JobID             string           `json:"job_id"`
	OperationID       string           `json:"operation_id"`
	AccountID         string           `json:"account_id"`
	AgentID           string           `json:"agent_id"`
	RequestID         string           `json:"request_id"`
	TraceID           string           `json:"trace_id"`
	Attempt           int64            `json:"attempt"`
	KnowledgeRevision int64            `json:"knowledge_revision"`
	DocumentID        string           `json:"document_id,omitempty"`
	DocumentVersion   int64            `json:"document_version,omitempty"`
	CoverageRunID     string           `json:"coverage_run_id,omitempty"`
	Status            string           `json:"status,omitempty"`
	ErrorCode         json.RawMessage  `json:"error_code,omitempty"`
	ChunksIndexed     *int64           `json:"chunks_indexed,omitempty"`
	Results           []CoverageResult `json:"results,omitempty"`
}

type CoverageResult struct {
	Question  string `json:"question"`
	HasAnswer bool   `json:"has_answer"`
	Answer    string `json:"answer"`
}

func Route(event string) (exchange, key string) {
	for _, job := range []string{"document.index", "document.delete", "document.reindex", "coverage.run"} {
		switch event {
		case job:
			return "faq.jobs", job
		case job + ".completed":
			return "faq.job-results", "jobs.completed"
		case job + ".failed":
			return "faq.job-results", "jobs.failed"
		}
	}
	return "", ""
}
