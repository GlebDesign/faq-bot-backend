package repository

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"errors"

	"faqbot/backend/internal/db"
	"faqbot/backend/internal/models"
	"faqbot/backend/internal/service"
)

type Chat struct{ pool *sql.DB }

func NewChat(pool *sql.DB) *Chat { return &Chat{pool: pool} }

func (r *Chat) CreateConversation(ctx context.Context, tx *sql.Tx, accountID, memberID, agentID, id, key string) (models.Conversation, error) {
	if err := lockChatWrite(ctx, tx, accountID, memberID, agentID, ""); err != nil {
		return models.Conversation{}, err
	}
	hash := sha256.Sum256([]byte(key))
	if _, err := tx.ExecContext(ctx, `INSERT INTO conversations (id,account_id,agent_id,member_id,create_key_hash)
  VALUES ($1,$2,$3,$4,$5)
  ON CONFLICT (account_id,agent_id,member_id,create_key_hash) WHERE create_key_hash IS NOT NULL DO NOTHING`,
		id, accountID, agentID, memberID, hash[:]); err != nil {
		return models.Conversation{}, db.WrapError("insert conversation", err)
	}
	var out models.Conversation
	err := tx.QueryRowContext(ctx, `SELECT id,agent_id,created_at FROM conversations
  WHERE account_id=$1 AND agent_id=$2 AND member_id=$3 AND create_key_hash=$4`, accountID, agentID, memberID, hash[:]).
		Scan(&out.ID, &out.AgentID, &out.CreatedAt)
	return out, db.WrapError("read conversation", err)
}

func (r *Chat) AcceptMessage(ctx context.Context, tx *sql.Tx, in models.AIRequestInput) (models.AIRequest, error) {
	hash, err := aiRequestHash(in)
	if err != nil {
		return models.AIRequest{}, err
	}
	if err := lockChatWrite(ctx, tx, in.AccountID, in.MemberID, in.AgentID, in.ConversationID); err != nil {
		return models.AIRequest{}, err
	}
	out, err := createOrReadAIRequest(ctx, tx, in, hash, true)
	if errors.Is(err, ErrAIRequestConflict) {
		return models.AIRequest{}, errors.Join(service.ErrIdempotencyConflict, err)
	}
	return out, err
}

func lockChatWrite(ctx context.Context, tx *sql.Tx, accountID, memberID, agentID, conversationID string) error {
	var id string
	err := tx.QueryRowContext(ctx, `SELECT id FROM agents
  WHERE account_id=$1 AND id=$2 AND deleted_at IS NULL FOR SHARE`, accountID, agentID).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return service.ErrChatNotFound
	}
	if err != nil {
		return db.WrapError("check chat agent", err)
	}
	var allowed bool
	err = tx.QueryRowContext(ctx, `SELECT lock_chat_write($1,$2,$3,$4)`,
		accountID, memberID, agentID, nullableChat(conversationID)).Scan(&allowed)
	if err != nil {
		return db.WrapError("lock chat membership and conversation", err)
	}
	if !allowed {
		return service.ErrChatNotFound
	}
	return nil
}

func (r *Chat) ListConversations(ctx context.Context, accountID, memberID, agentID string, limit int, pos *models.ChatPosition) ([]models.Conversation, error) {
	query := `SELECT page.id,a.id,page.created_at
  FROM agents a JOIN account_members m ON m.account_id=a.account_id AND m.user_id=$2
  LEFT JOIN LATERAL (
   SELECT c.id,c.created_at FROM conversations c
   WHERE c.account_id=a.account_id AND c.agent_id=a.id AND c.member_id=m.user_id`
	args := []any{accountID, memberID, agentID, limit}
	if pos != nil {
		query += ` AND (c.created_at,c.id)<($5,$6)`
		args = append(args, pos.CreatedAt, pos.ID)
	}
	query += ` ORDER BY c.created_at DESC,c.id DESC LIMIT $4
  ) page ON true
  WHERE a.account_id=$1 AND a.id=$3 AND a.deleted_at IS NULL
  ORDER BY page.created_at DESC,page.id DESC`
	rows, err := r.pool.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, db.WrapError("list conversations", err)
	}
	defer func() { _ = rows.Close() }()
	out := make([]models.Conversation, 0)
	found := false
	for rows.Next() {
		found = true
		var item models.Conversation
		var id sql.NullString
		var createdAt sql.NullTime
		if err := rows.Scan(&id, &item.AgentID, &createdAt); err != nil {
			return nil, db.WrapError("read conversation page", err)
		}
		if id.Valid {
			item.ID, item.CreatedAt = id.String, createdAt.Time
			out = append(out, item)
		}
	}
	if err := rows.Err(); err != nil {
		return nil, db.WrapError("iterate conversations", err)
	}
	if !found {
		return nil, service.ErrChatNotFound
	}
	return out, nil
}

func (r *Chat) ListMessages(ctx context.Context, accountID, memberID, conversationID string, limit int, pos *models.ChatPosition) ([]models.Message, error) {
	query := `SELECT page.id,c.id,page.status,page.question,page.answer,page.created_at
  FROM conversations c
  JOIN agents a ON a.account_id=c.account_id AND a.id=c.agent_id AND a.deleted_at IS NULL
  JOIN account_members m ON m.account_id=c.account_id AND m.user_id=c.member_id
  LEFT JOIN LATERAL (
   SELECT msg.id,msg.status,msg.question,msg.answer,msg.created_at FROM messages msg
   WHERE msg.account_id=c.account_id AND msg.agent_id=c.agent_id AND msg.conversation_id=c.id`
	args := []any{accountID, memberID, conversationID, limit}
	if pos != nil {
		query += ` AND (msg.created_at,msg.id)<($5,$6)`
		args = append(args, pos.CreatedAt, pos.ID)
	}
	query += ` ORDER BY msg.created_at DESC,msg.id DESC LIMIT $4
  ) page ON true
  WHERE c.account_id=$1 AND c.member_id=$2 AND c.id=$3
  ORDER BY page.created_at DESC,page.id DESC`
	rows, err := r.pool.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, db.WrapError("list messages", err)
	}
	defer func() { _ = rows.Close() }()
	out := make([]models.Message, 0)
	found := false
	for rows.Next() {
		found = true
		var item models.Message
		var id, status, question sql.NullString
		var createdAt sql.NullTime
		if err := rows.Scan(&id, &item.ConversationID, &status, &question, &item.Answer, &createdAt); err != nil {
			return nil, db.WrapError("read message page", err)
		}
		if id.Valid {
			item.ID, item.Status, item.Question, item.CreatedAt = id.String, status.String, question.String, createdAt.Time
			out = append(out, item)
		}
	}
	if err := rows.Err(); err != nil {
		return nil, db.WrapError("iterate messages", err)
	}
	if !found {
		return nil, service.ErrChatNotFound
	}
	return out, nil
}
