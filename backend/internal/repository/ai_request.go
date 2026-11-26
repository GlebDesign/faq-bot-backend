package repository

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"errors"
	"strings"

	"faqbot/backend/internal/db"
	"faqbot/backend/internal/models"
)

var (
	ErrAIRequestConflict = errors.New("AI request payload conflict")
	ErrInvalidAIRequest  = errors.New("invalid AI request")
)

type AIRequests struct{ pool *sql.DB }

func NewAIRequests(pool *sql.DB) *AIRequests { return &AIRequests{pool: pool} }

func (r *AIRequests) CreateOrRead(ctx context.Context, in models.AIRequestInput) (models.AIRequest, error) {
	hash, err := aiRequestHash(in)
	if err != nil {
		return models.AIRequest{}, err
	}
	var out models.AIRequest
	err = db.WithTx(ctx, r.pool, func(ctx context.Context, tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, "SET TRANSACTION ISOLATION LEVEL READ COMMITTED"); err != nil {
			return db.WrapError("set AI request isolation", err)
		}
		var err error
		out, err = createOrReadAIRequest(ctx, tx, in, hash, false)
		return err
	})
	if err != nil {
		return models.AIRequest{}, err
	}
	return out, nil
}

func aiRequestHash(in models.AIRequestInput) ([sha256.Size]byte, error) {
	if (in.VisitorID == "") == (in.MemberID == "") || strings.TrimSpace(in.Message) == "" {
		return [sha256.Size]byte{}, ErrInvalidAIRequest
	}
	payload, err := json.Marshal(struct {
		ConversationID string `json:"conversation_id"`
		Message        string `json:"message"`
	}{in.ConversationID, in.Message})
	if err != nil {
		return [sha256.Size]byte{}, err
	}
	return sha256.Sum256(payload), nil
}

func createOrReadAIRequest(ctx context.Context, tx *sql.Tx, in models.AIRequestInput, hash [sha256.Size]byte, createMessage bool) (models.AIRequest, error) {
	query := `INSERT INTO ai_requests
  (id,account_id,agent_id,visitor_id,member_id,operation,idempotency_key,conversation_id,message_id,payload_hash)
  VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10)
  ON CONFLICT ON CONSTRAINT ai_requests_scope_key DO NOTHING`
	args := []any{in.ID, in.AccountID, in.AgentID, nullableChat(in.VisitorID), nullableChat(in.MemberID), in.Operation, in.IdempotencyKey, in.ConversationID, in.MessageID, hash[:]}
	if createMessage {
		query = `WITH request AS (` + query + ` RETURNING account_id,agent_id,conversation_id,message_id)
   INSERT INTO messages (id,account_id,agent_id,conversation_id,question,created_at)
   SELECT message_id,account_id,agent_id,conversation_id,$11,statement_timestamp() FROM request`
		args = append(args, in.Message)
	}
	if _, err := tx.ExecContext(ctx, query, args...); err != nil {
		return models.AIRequest{}, db.WrapError("insert AI request", err)
	}
	var out models.AIRequest
	var stored, result []byte
	err := tx.QueryRowContext(ctx, `SELECT id,message_id,state,result,payload_hash FROM ai_requests
   WHERE account_id=$1 AND agent_id=$2 AND visitor_id IS NOT DISTINCT FROM $3::text
   AND member_id IS NOT DISTINCT FROM $4::uuid AND operation=$5 AND idempotency_key=$6`,
		in.AccountID, in.AgentID, nullableChat(in.VisitorID), nullableChat(in.MemberID), in.Operation, in.IdempotencyKey).Scan(&out.ID, &out.MessageID, &out.State, &result, &stored)
	if err != nil {
		return models.AIRequest{}, db.WrapError("read AI request", err)
	}
	if !bytes.Equal(stored, hash[:]) {
		return models.AIRequest{}, ErrAIRequestConflict
	}
	out.Result = result
	return out, nil
}

func nullableChat(value string) any {
	if value == "" {
		return nil
	}
	return value
}
