package repository

import (
	"bytes"
	"context"
	"database/sql"
	"errors"

	"faqbot/backend/internal/db"
	"faqbot/backend/internal/models"
	"faqbot/backend/internal/service"
)

type Agents struct{ pool *sql.DB }

func NewAgents(pool *sql.DB) *Agents { return &Agents{pool: pool} }

const agentColumns = `id,account_id,public_id,name,website,description,status,revision,knowledge_revision,active_knowledge_revision,config_revision,published_config_revision,public_sources_enabled,created_at`

type agentScanner interface{ Scan(...any) error }

func scanAgent(row agentScanner) (models.Agent, error) {
	var a models.Agent
	err := row.Scan(&a.ID, &a.AccountID, &a.PublicID, &a.Name, &a.Website, &a.Description, &a.Status, &a.Revision, &a.KnowledgeRevision, &a.ActiveKnowledgeRevision, &a.ConfigRevision, &a.PublishedConfigRevision, &a.PublicSourcesEnabled, &a.CreatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return a, service.ErrAgentNotFound
	}
	return a, db.WrapError("read agent", err)
}

func (a *Agents) Create(ctx context.Context, tx *sql.Tx, in models.Agent) (models.Agent, error) {
	return scanAgent(tx.QueryRowContext(ctx, `INSERT INTO agents(id,account_id,public_id,name,website,description) VALUES($1,$2,$3,$4,$5,$6) RETURNING `+agentColumns, in.ID, in.AccountID, in.PublicID, in.Name, in.Website, in.Description))
}
func (a *Agents) Get(ctx context.Context, tx *sql.Tx, accountID, id string, lock bool) (models.Agent, error) {
	query := `SELECT ` + agentColumns + ` FROM agents WHERE account_id=$1 AND id=$2 AND deleted_at IS NULL`
	if lock {
		query += ` FOR UPDATE`
	}
	if tx != nil {
		return scanAgent(tx.QueryRowContext(ctx, query, accountID, id))
	}
	return scanAgent(a.pool.QueryRowContext(ctx, query, accountID, id))
}
func (a *Agents) List(ctx context.Context, accountID string, limit int, pos *models.AgentPosition) ([]models.Agent, error) {
	query := `SELECT ` + agentColumns + ` FROM agents WHERE account_id=$1 AND deleted_at IS NULL`
	args := []any{accountID, limit}
	if pos != nil {
		query += ` AND (created_at,id)<($3,$4)`
		args = append(args, pos.CreatedAt, pos.ID)
	}
	query += ` ORDER BY created_at DESC,id DESC LIMIT $2`
	rows, err := a.pool.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, db.WrapError("list agents", err)
	}
	defer func() { _ = rows.Close() }()
	out := []models.Agent{}
	for rows.Next() {
		item, err := scanAgent(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, item)
	}
	return out, db.WrapError("iterate agents", rows.Err())
}
func (a *Agents) Update(ctx context.Context, tx *sql.Tx, in models.Agent) (models.Agent, error) {
	return scanAgent(tx.QueryRowContext(ctx, `UPDATE agents SET name=$3,website=$4,description=$5,revision=revision+1 WHERE account_id=$1 AND id=$2 AND deleted_at IS NULL AND revision=$6 RETURNING `+agentColumns, in.AccountID, in.ID, in.Name, in.Website, in.Description, in.Revision))
}
func (a *Agents) Delete(ctx context.Context, tx *sql.Tx, accountID, id string) error {
	result, err := tx.ExecContext(ctx, `UPDATE agents SET status='deleted',deleted_at=now(),revision=revision+1 WHERE account_id=$1 AND id=$2 AND deleted_at IS NULL`, accountID, id)
	if err != nil {
		return db.WrapError("delete agent", err)
	}
	n, err := result.RowsAffected()
	if err != nil {
		return db.WrapError("count deleted agents", err)
	}
	if n != 1 {
		return service.ErrAgentNotFound
	}
	return nil
}
func (a *Agents) Reserve(ctx context.Context, tx *sql.Tx, in models.AgentOperation) (models.AgentOperation, error) {
	_, err := tx.ExecContext(ctx, `INSERT INTO agent_operations(id,account_id,actor_id,operation,resource,key_hash,payload_hash) VALUES($1,$2,$3,$4,$5,$6,$7) ON CONFLICT ON CONSTRAINT agent_operations_scope_key DO NOTHING`, in.ID, in.AccountID, in.ActorID, in.Operation, in.Resource, in.KeyHash, in.PayloadHash)
	if err != nil {
		return in, db.WrapError("reserve agent operation", err)
	}
	var hash []byte
	err = tx.QueryRowContext(ctx, `SELECT id,payload_hash,result FROM agent_operations WHERE account_id=$1 AND actor_id=$2 AND operation=$3 AND resource=$4 AND key_hash=$5 FOR UPDATE`, in.AccountID, in.ActorID, in.Operation, in.Resource, in.KeyHash).Scan(&in.ID, &hash, &in.Result)
	if err != nil {
		return in, db.WrapError("read agent operation", err)
	}
	if !bytes.Equal(hash, in.PayloadHash) {
		return in, service.ErrIdempotencyConflict
	}
	return in, nil
}
func (a *Agents) Complete(ctx context.Context, tx *sql.Tx, in models.AgentOperation) error {
	_, err := tx.ExecContext(ctx, `UPDATE agent_operations SET result=$6 WHERE account_id=$1 AND actor_id=$2 AND operation=$3 AND resource=$4 AND key_hash=$5`, in.AccountID, in.ActorID, in.Operation, in.Resource, in.KeyHash, in.Result)
	return db.WrapError("complete agent operation", err)
}
func (a *Agents) RequestInTx(ctx context.Context, tx *sql.Tx, id, accountID, agentID string) error {
	_, err := tx.ExecContext(ctx, `INSERT INTO agent_cleanup_requests(id,account_id,agent_id) VALUES($1,$2,$3)`, id, accountID, agentID)
	return db.WrapError("request agent cleanup", err)
}
