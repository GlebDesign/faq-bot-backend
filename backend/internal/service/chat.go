package service

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"

	"faqbot/backend/internal/db"
	"faqbot/backend/internal/models"
)

var (
	ErrInvalidChat  = errors.New("invalid chat request")
	ErrChatNotFound = errors.New("chat resource not found")
)

type ChatStore interface {
	CreateConversation(context.Context, *sql.Tx, string, string, string, string, string) (models.Conversation, error)
	AcceptMessage(context.Context, *sql.Tx, models.AIRequestInput) (models.AIRequest, error)
	ListConversations(context.Context, string, string, string, int, *models.ChatPosition) ([]models.Conversation, error)
	ListMessages(context.Context, string, string, string, int, *models.ChatPosition) ([]models.Message, error)
}

type Chat struct {
	pool      *sql.DB
	store     ChatStore
	cursorKey []byte
}

func NewChat(pool *sql.DB, store ChatStore, cursorKey []byte) *Chat {
	return &Chat{pool: pool, store: store, cursorKey: append([]byte(nil), cursorKey...)}
}

func (c *Chat) CreateConversation(ctx context.Context, s Scope, agent, key string) (models.Conversation, error) {
	if err := s.authorizeAgents(); err != nil {
		return models.Conversation{}, err
	}
	if !auditUUID.MatchString(agent) {
		return models.Conversation{}, ErrChatNotFound
	}
	if !validAgentKey(key) {
		return models.Conversation{}, ErrInvalidChat
	}
	var out models.Conversation
	err := c.write(ctx, func(ctx context.Context, tx *sql.Tx) error {
		var err error
		out, err = c.store.CreateConversation(ctx, tx, s.accountID, s.actorID, agent, agentID(), key)
		return err
	})
	if err != nil {
		return models.Conversation{}, err
	}
	return out, nil
}

func (c *Chat) AcceptMessage(ctx context.Context, s Scope, agent, key, conversation, message string) (models.AIRequest, error) {
	if err := s.authorizeAgents(); err != nil {
		return models.AIRequest{}, err
	}
	if !auditUUID.MatchString(agent) || !auditUUID.MatchString(conversation) {
		return models.AIRequest{}, ErrChatNotFound
	}
	if !validAgentKey(key) || !utf8.ValidString(message) || strings.ContainsRune(message, 0) || strings.TrimSpace(message) == "" || utf8.RuneCountInString(message) > 4000 {
		return models.AIRequest{}, ErrInvalidChat
	}
	in := models.AIRequestInput{
		ID: agentID(), AccountID: s.accountID, AgentID: agent, MemberID: s.actorID, Operation: "test_message",
		IdempotencyKey: key, ConversationID: conversation, MessageID: agentID(), Message: message,
	}
	var out models.AIRequest
	err := c.write(ctx, func(ctx context.Context, tx *sql.Tx) error {
		var err error
		out, err = c.store.AcceptMessage(ctx, tx, in)
		return err
	})
	if err != nil {
		return models.AIRequest{}, err
	}
	return out, nil
}

func (c *Chat) write(ctx context.Context, fn func(context.Context, *sql.Tx) error) error {
	return db.WithTx(ctx, c.pool, func(ctx context.Context, tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, "SET TRANSACTION ISOLATION LEVEL READ COMMITTED"); err != nil {
			return db.WrapError("set chat isolation", err)
		}
		return fn(ctx, tx)
	})
}

func (c *Chat) ListConversations(ctx context.Context, s Scope, agent string, limit int, cursor string) ([]models.Conversation, *string, error) {
	page := chatCursor{Account: s.accountID, Member: s.actorID, Resource: "conversations", Parent: agent}
	pos, err := c.position(s, page, limit, cursor)
	if err != nil {
		return nil, nil, err
	}
	items, err := c.store.ListConversations(ctx, s.accountID, s.actorID, agent, limit+1, pos)
	if err != nil || len(items) <= limit {
		return items, nil, err
	}
	last := items[limit-1]
	page.Position = models.ChatPosition{ID: last.ID, CreatedAt: last.CreatedAt}
	next, err := c.encodeCursor(page)
	return items[:limit], next, err
}

func (c *Chat) ListMessages(ctx context.Context, s Scope, conversation string, limit int, cursor string) ([]models.Message, *string, error) {
	page := chatCursor{Account: s.accountID, Member: s.actorID, Resource: "messages", Parent: conversation}
	pos, err := c.position(s, page, limit, cursor)
	if err != nil {
		return nil, nil, err
	}
	items, err := c.store.ListMessages(ctx, s.accountID, s.actorID, conversation, limit+1, pos)
	if err != nil || len(items) <= limit {
		return items, nil, err
	}
	last := items[limit-1]
	page.Position = models.ChatPosition{ID: last.ID, CreatedAt: last.CreatedAt}
	next, err := c.encodeCursor(page)
	return items[:limit], next, err
}

type chatCursor struct {
	Account  string              `json:"account"`
	Member   string              `json:"member"`
	Resource string              `json:"resource"`
	Parent   string              `json:"parent"`
	Position models.ChatPosition `json:"position"`
}

func (c *Chat) position(s Scope, page chatCursor, limit int, cursor string) (*models.ChatPosition, error) {
	if err := s.authorizeAgents(); err != nil {
		return nil, err
	}
	if !auditUUID.MatchString(page.Parent) {
		return nil, ErrChatNotFound
	}
	if limit < 1 || limit > 100 || len(cursor) > 512 {
		return nil, ErrInvalidChat
	}
	if cursor == "" {
		return nil, nil
	}
	payloadPart, sigPart, ok := strings.Cut(cursor, ".")
	if !ok {
		return nil, ErrInvalidChat
	}
	encoding := base64.RawURLEncoding.Strict()
	payload, err := encoding.DecodeString(payloadPart)
	if err != nil {
		return nil, ErrInvalidChat
	}
	sig, err := encoding.DecodeString(sigPart)
	if err != nil || !hmac.Equal(sig, c.cursorMAC(payload)) {
		return nil, ErrInvalidChat
	}
	var saved chatCursor
	if json.Unmarshal(payload, &saved) != nil || saved.Account != page.Account || saved.Member != page.Member || saved.Resource != page.Resource ||
		saved.Parent != page.Parent || !auditUUID.MatchString(saved.Position.ID) || saved.Position.CreatedAt.IsZero() {
		return nil, ErrInvalidChat
	}
	return &saved.Position, nil
}

func (c *Chat) encodeCursor(page chatCursor) (*string, error) {
	payload, err := json.Marshal(page)
	if err != nil {
		return nil, fmt.Errorf("encode chat cursor: %w", err)
	}
	encoding := base64.RawURLEncoding
	out := encoding.EncodeToString(payload) + "." + encoding.EncodeToString(c.cursorMAC(payload))
	return &out, nil
}

func (c *Chat) cursorMAC(payload []byte) []byte {
	mac := hmac.New(sha256.New, c.cursorKey)
	_, _ = mac.Write([]byte("chat-history-v1\x00"))
	_, _ = mac.Write(payload)
	return mac.Sum(nil)
}
