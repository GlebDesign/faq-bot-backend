package jobs

type Identity struct {
	JobID             string
	OperationID       string
	AccountID         string
	AgentID           string
	Kind              string
	ResourceID        string
	Version           int64
	KnowledgeRevision int64
	Attempt           int64
}

type Record struct {
	Identity
	Status string
}

type Resource struct {
	AccountID         string
	AgentID           string
	ID                string
	CurrentJobID      string
	Version           int64
	KnowledgeRevision int64
	Status            string
}

func DecideResult(job Record, current Resource, result Record) string {
	if result.Status != "completed" && result.Status != "failed" {
		return "reject"
	}
	switch job.Kind {
	case "document.index", "document.delete", "document.reindex", "coverage.run":
	default:
		return "reject"
	}
	if job.JobID != result.JobID ||
		job.OperationID != result.OperationID ||
		job.AccountID != result.AccountID ||
		job.AgentID != result.AgentID ||
		job.Kind != result.Kind ||
		job.ResourceID != result.ResourceID {
		return "reject"
	}
	if current.AccountID != job.AccountID || current.AgentID != job.AgentID || current.ID != job.ResourceID {
		return "reject"
	}
	if result.Version != job.Version ||
		result.KnowledgeRevision != job.KnowledgeRevision ||
		result.Attempt != job.Attempt {
		return "stale"
	}
	if current.CurrentJobID != job.JobID ||
		current.Version != job.Version ||
		current.KnowledgeRevision != job.KnowledgeRevision {
		return "stale"
	}
	if job.Status == "completed" || job.Status == "failed" {
		if job.Status == result.Status {
			return "duplicate"
		}
		return "reject"
	}
	if !CanTransition("job", job.Status, result.Status) {
		return "stale"
	}
	switch job.Kind {
	case "document.delete":
		if current.Status != "deleting" {
			return "stale"
		}
	case "coverage.run":
		if current.Status != "pending" && current.Status != "running" {
			return "stale"
		}
	default:
		if current.Status != "pending" && current.Status != "processing" {
			return "stale"
		}
	}
	return "apply"
}
