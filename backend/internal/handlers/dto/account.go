package dto

type Package struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Requests int64  `json:"requests"`
}

type PackagePage struct {
	Items      []Package `json:"items"`
	NextCursor *string   `json:"next_cursor"`
}

type Balance struct {
	AccountID         string  `json:"account_id"`
	RemainingRequests int64   `json:"remaining_requests"`
	ReservedRequests  int64   `json:"reserved_requests"`
	ExpiresAt         *string `json:"expires_at"`
}

type PackageRequestCreate struct {
	PackageID string `json:"package_id"`
}

type PackageRequest struct {
	ID        string `json:"id"`
	PackageID string `json:"package_id"`
	Status    string `json:"status"`
	CreatedAt string `json:"created_at"`
}

type PackageRequestPage struct {
	Items      []PackageRequest `json:"items"`
	NextCursor *string          `json:"next_cursor"`
}

type PackageDecision struct {
	Status string `json:"status"`
	Reason string `json:"reason"`
}

type AuditEvent struct {
	ID           string `json:"id"`
	AccountID    string `json:"account_id"`
	ActorID      string `json:"actor_id"`
	Action       string `json:"action"`
	ResourceType string `json:"resource_type"`
	ResourceID   string `json:"resource_id"`
	RequestID    string `json:"request_id"`
	CreatedAt    string `json:"created_at"`
}

type AuditPage struct {
	Items      []AuditEvent `json:"items"`
	NextCursor *string      `json:"next_cursor"`
}
