package service

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"faqbot/backend/internal/db"
	"faqbot/backend/internal/models"
)

var (
	ErrAgentNotFound       = errors.New("agent not found")
	ErrInvalidAgent        = errors.New("invalid agent request")
	ErrRevisionConflict    = errors.New("agent revision conflict")
	ErrIdempotencyConflict = errors.New("agent idempotency conflict")
)

type AgentStore interface {
	Create(context.Context, *sql.Tx, models.Agent) (models.Agent, error)
	Get(context.Context, *sql.Tx, string, string, bool) (models.Agent, error)
	List(context.Context, string, int, *models.AgentPosition) ([]models.Agent, error)
	Update(context.Context, *sql.Tx, models.Agent) (models.Agent, error)
	Delete(context.Context, *sql.Tx, string, string) error
	Reserve(context.Context, *sql.Tx, models.AgentOperation) (models.AgentOperation, error)
	Complete(context.Context, *sql.Tx, models.AgentOperation) error
}

type AgentAudit interface {
	WriteInTx(context.Context, *sql.Tx, models.AuditActor, models.AuditEvent) error
}

type AgentCleanup interface {
	RequestInTx(context.Context, *sql.Tx, string, string, string) error
}

type Agents struct {
	pool      *sql.DB
	store     AgentStore
	audit     AgentAudit
	cleanup   AgentCleanup
	cursorKey []byte
}

func NewAgents(pool *sql.DB, store AgentStore, audit AgentAudit, cleanup AgentCleanup, cursorKey []byte) *Agents {
	return &Agents{pool: pool, store: store, audit: audit, cleanup: cleanup, cursorKey: append([]byte(nil), cursorKey...)}
}

func agentID() string {
	var b [16]byte
	_, _ = rand.Read(b[:])
	b[6] = (b[6] & 15) | 64
	b[8] = (b[8] & 63) | 128
	s := hex.EncodeToString(b[:])
	return s[:8] + "-" + s[8:12] + "-" + s[12:16] + "-" + s[16:20] + "-" + s[20:]
}

func validAgent(in models.AgentInput) bool {
	return utf8.ValidString(in.Name) && utf8.ValidString(in.Website) && utf8.ValidString(in.Description) && !strings.ContainsRune(in.Name, 0) && !strings.ContainsRune(in.Website, 0) && !strings.ContainsRune(in.Description, 0) && strings.TrimSpace(in.Name) != "" && utf8.RuneCountInString(in.Name) <= 100
}

func validAgentKey(key string) bool {
	if len(key) < 1 || len(key) > 128 {
		return false
	}
	for _, c := range key {
		if (c < 'A' || c > 'Z') && (c < 'a' || c > 'z') && (c < '0' || c > '9') && c != '_' && c != '-' {
			return false
		}
	}
	return true
}

func (a *Agents) Get(ctx context.Context, s Scope, id string) (models.Agent, error) {
	if err := s.authorizeAgents(); err != nil {
		return models.Agent{}, err
	}
	if !auditUUID.MatchString(id) {
		return models.Agent{}, ErrAgentNotFound
	}
	return a.store.Get(ctx, nil, s.accountID, id, false)
}

type agentCursor struct {
	AccountID string               `json:"account"`
	Resource  string               `json:"resource"`
	Sort      string               `json:"sort"`
	Position  models.AgentPosition `json:"position"`
}

func (a *Agents) List(ctx context.Context, s Scope, limit int, cursor string) ([]models.Agent, *string, error) {
	if err := s.authorizeAgents(); err != nil {
		return nil, nil, err
	}
	if limit < 1 || limit > 100 {
		return nil, nil, ErrInvalidAgent
	}
	var pos *models.AgentPosition
	if cursor != "" {
		if len(cursor) > 512 {
			return nil, nil, ErrInvalidAgent
		}
		parts := strings.Split(cursor, ".")
		if len(parts) != 2 {
			return nil, nil, ErrInvalidAgent
		}
		payload, err := base64.RawURLEncoding.DecodeString(parts[0])
		if err != nil {
			return nil, nil, ErrInvalidAgent
		}
		sig, err := base64.RawURLEncoding.DecodeString(parts[1])
		if err != nil {
			return nil, nil, ErrInvalidAgent
		}
		mac := hmac.New(sha256.New, a.cursorKey)
		_, _ = mac.Write(payload)
		var c agentCursor
		if !hmac.Equal(sig, mac.Sum(nil)) || json.Unmarshal(payload, &c) != nil || c.AccountID != s.accountID || c.Resource != "agents" || c.Sort != "created_at DESC,id DESC" || !auditUUID.MatchString(c.Position.ID) || c.Position.CreatedAt.IsZero() {
			return nil, nil, ErrInvalidAgent
		}
		pos = &c.Position
	}
	items, err := a.store.List(ctx, s.accountID, limit+1, pos)
	if err != nil {
		return nil, nil, err
	}
	if items == nil {
		items = []models.Agent{}
	}
	var next *string
	if len(items) > limit {
		items = items[:limit]
		last := items[len(items)-1]
		payload, err := json.Marshal(agentCursor{AccountID: s.accountID, Resource: "agents", Sort: "created_at DESC,id DESC", Position: models.AgentPosition{CreatedAt: last.CreatedAt, ID: last.ID}})
		if err != nil {
			return nil, nil, fmt.Errorf("encode agent cursor: %w", err)
		}
		mac := hmac.New(sha256.New, a.cursorKey)
		_, _ = mac.Write(payload)
		encoded := base64.RawURLEncoding.EncodeToString(payload) + "." + base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
		next = &encoded
	}
	return items, next, nil
}

func (a *Agents) Create(ctx context.Context, s Scope, key, requestID string, in models.AgentInput) (models.Agent, error) {
	out, _, err := a.mutate(ctx, s, "create", "", key, requestID, in, 0)
	return out, err
}
func (a *Agents) Update(ctx context.Context, s Scope, id, key, requestID string, in models.AgentInput, revision int64) (models.Agent, error) {
	out, _, err := a.mutate(ctx, s, "update", id, key, requestID, in, revision)
	return out, err
}
func (a *Agents) Delete(ctx context.Context, s Scope, id, key, requestID string) (models.AgentCleanup, error) {
	_, out, err := a.mutate(ctx, s, "delete", id, key, requestID, models.AgentInput{}, 0)
	return out, err
}

func (a *Agents) mutate(ctx context.Context, s Scope, operation, id, key, requestID string, in models.AgentInput, revision int64) (models.Agent, models.AgentCleanup, error) {
	var agent models.Agent
	var job models.AgentCleanup
	if err := s.authorizeAgents(); err != nil {
		return agent, job, err
	}
	if !validAgentKey(key) || operation != "delete" && !validAgent(in) || operation == "update" && revision < 1 {
		return agent, job, ErrInvalidAgent
	}
	if operation != "create" && !auditUUID.MatchString(id) {
		return agent, job, ErrAgentNotFound
	}
	payload, err := json.Marshal(struct {
		Input    models.AgentInput
		Revision int64
	}{in, revision})
	if err != nil {
		return agent, job, fmt.Errorf("encode agent payload: %w", err)
	}
	hash := sha256.Sum256(payload)
	keyHash := sha256.Sum256([]byte(key))
	candidate := models.AgentOperation{ID: agentID(), AccountID: s.accountID, ActorID: s.actorID, Operation: operation, Resource: id, KeyHash: keyHash[:], PayloadHash: hash[:]}
	err = db.WithTx(ctx, a.pool, func(ctx context.Context, tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, "SET TRANSACTION ISOLATION LEVEL READ COMMITTED"); err != nil {
			return db.WrapError("set agent operation isolation", err)
		}
		op, err := a.store.Reserve(ctx, tx, candidate)
		if err != nil {
			return err
		}
		if len(op.Result) > 0 {
			if operation == "delete" {
				err = json.Unmarshal(op.Result, &job)
			} else {
				err = json.Unmarshal(op.Result, &agent)
			}
			if err != nil {
				return fmt.Errorf("decode agent replay: %w", err)
			}
			return nil
		}
		fields := []string{}
		switch operation {
		case "create":
			agent, err = a.store.Create(ctx, tx, models.Agent{ID: agentID(), AccountID: s.accountID, PublicID: rand.Text(), Name: in.Name, Website: in.Website, Description: in.Description})
			fields = []string{"name", "website", "description"}
		case "update":
			agent, err = a.store.Get(ctx, tx, s.accountID, id, true)
			if err != nil {
				return err
			}
			if agent.Revision != revision {
				return ErrRevisionConflict
			}
			if agent.Name != in.Name {
				fields = append(fields, "name")
			}
			if agent.Website != in.Website {
				fields = append(fields, "website")
			}
			if agent.Description != in.Description {
				fields = append(fields, "description")
			}
			agent.Name = in.Name
			agent.Website = in.Website
			agent.Description = in.Description
			agent, err = a.store.Update(ctx, tx, agent)
		case "delete":
			agent, err = a.store.Get(ctx, tx, s.accountID, id, true)
			if err != nil {
				return err
			}
			err = a.store.Delete(ctx, tx, s.accountID, id)
			if err == nil {
				err = a.cleanup.RequestInTx(ctx, tx, op.ID, s.accountID, id)
			}
			job = models.AgentCleanup{ID: op.ID, Status: "pending"}
		}
		if err != nil {
			return err
		}
		metadata := map[string]any{}
		if len(fields) > 0 {
			metadata["field_names_changed"] = fields
		}
		if operation == "delete" {
			metadata["new_status"] = "deleted"
			metadata["job_id"] = job.ID
		}
		action := map[string]string{"create": "agent.created", "update": "agent.updated", "delete": "agent.deleted"}[operation]
		if err := a.audit.WriteInTx(ctx, tx, models.AuditActor{Type: "user", ID: s.actorID}, models.AuditEvent{OperationID: op.ID, Scope: "tenant", AccountID: s.accountID, Action: action, EntityType: "agent", EntityID: agent.ID, Outcome: "success", OccurredAt: time.Now().UTC(), RequestID: requestID, SourceService: "go-api", Metadata: metadata}); err != nil {
			return err
		}
		if operation == "delete" {
			op.Result, err = json.Marshal(job)
		} else {
			op.Result, err = json.Marshal(agent)
		}
		if err != nil {
			return fmt.Errorf("encode agent result: %w", err)
		}
		return a.store.Complete(ctx, tx, op)
	})
	return agent, job, err
}
