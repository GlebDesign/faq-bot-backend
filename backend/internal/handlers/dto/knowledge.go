package dto

type FileUpload struct {
	File string `json:"file"`
}

type DocumentCreate struct {
	Name string `json:"name"`
	Text string `json:"text"`
}

type DocumentUpdate struct {
	Text string `json:"text"`
}

type Document struct {
	ID                string  `json:"id"`
	AgentID           string  `json:"agent_id"`
	Name              string  `json:"name"`
	Version           int64   `json:"version"`
	KnowledgeRevision int64   `json:"knowledge_revision"`
	Status            string  `json:"status"`
	ErrorCode         *string `json:"error_code"`
}

type DocumentPage struct {
	Items      []Document `json:"items"`
	NextCursor *string    `json:"next_cursor"`
}

type Job struct {
	ID        string  `json:"id"`
	Status    string  `json:"status"`
	ErrorCode *string `json:"error_code"`
}

type CoverageCreate struct {
	KnowledgeRevision int64    `json:"knowledge_revision"`
	Questions         []string `json:"questions"`
}

type CoverageResult struct {
	Question  string `json:"question"`
	HasAnswer bool   `json:"has_answer"`
	Answer    string `json:"answer"`
}

type CoverageRun struct {
	ID                string           `json:"id"`
	AgentID           string           `json:"agent_id"`
	KnowledgeRevision int64            `json:"knowledge_revision"`
	Status            string           `json:"status"`
	Stale             bool             `json:"stale"`
	Results           []CoverageResult `json:"results"`
	ErrorCode         *string          `json:"error_code"`
}

type CoveragePage struct {
	Items      []CoverageRun `json:"items"`
	NextCursor *string       `json:"next_cursor"`
}
