package handlers

import (
	"bytes"
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"faqbot/backend/internal/handlers/dto"
	"faqbot/backend/internal/models"
	"faqbot/backend/internal/service"
)

type ChatService interface {
	CreateConversation(context.Context, service.Scope, string, string) (models.Conversation, error)
	AcceptMessage(context.Context, service.Scope, string, string, string, string) (models.AIRequest, error)
	ListConversations(context.Context, service.Scope, string, int, string) ([]models.Conversation, *string, error)
	ListMessages(context.Context, service.Scope, string, int, string) ([]models.Message, *string, error)
}

type ChatHandler struct {
	sessions CabinetSessions
	chat     ChatService
	origin   string
}

func NewChatHandler(sessions CabinetSessions, chat ChatService, origin string) *ChatHandler {
	return &ChatHandler{sessions: sessions, chat: chat, origin: origin}
}

func (h *ChatHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	switch r.Pattern {
	case "/api/v1/agents/{id}/conversations":
		h.conversations(w, r)
	case "/api/v1/agents/{id}/test-chat/messages":
		h.acceptMessage(w, r)
	case "/api/v1/conversations/{id}/messages":
		h.messages(w, r)
	default:
		writeChatError(w, service.ErrChatNotFound)
	}
}

func (h *ChatHandler) conversations(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodGet {
		h.history(w, r, true)
		return
	}
	if r.Method != http.MethodPost {
		chatMethodNotAllowed(w, "GET, POST")
		return
	}
	scope, err := h.scope(r, true)
	if err != nil {
		writeChatError(w, err)
		return
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, 1))
	if err != nil || len(body) != 0 {
		writeChatError(w, service.ErrInvalidChat)
		return
	}
	conversation, err := h.chat.CreateConversation(r.Context(), scope, r.PathValue("id"), r.Header.Get("Idempotency-Key"))
	if err != nil {
		writeChatError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, conversationDTO(conversation))
}

func (h *ChatHandler) messages(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		chatMethodNotAllowed(w, "GET")
		return
	}
	h.history(w, r, false)
}

func (h *ChatHandler) history(w http.ResponseWriter, r *http.Request, conversations bool) {
	scope, err := h.scope(r, false)
	if err != nil {
		writeChatError(w, err)
		return
	}
	limit, cursor, err := chatPage(r.URL.RawQuery)
	if err != nil {
		writeChatError(w, err)
		return
	}
	id := r.PathValue("id")
	if conversations {
		items, next, err := h.chat.ListConversations(r.Context(), scope, id, limit, cursor)
		if err != nil {
			writeChatError(w, err)
			return
		}
		out := dto.ConversationPage{Items: make([]dto.Conversation, 0, len(items)), NextCursor: next}
		for _, item := range items {
			out.Items = append(out.Items, conversationDTO(item))
		}
		writeJSON(w, http.StatusOK, out)
		return
	}
	items, next, err := h.chat.ListMessages(r.Context(), scope, id, limit, cursor)
	if err != nil {
		writeChatError(w, err)
		return
	}
	out := dto.MessagePage{Items: make([]dto.Message, 0, len(items)), NextCursor: next}
	for _, item := range items {
		out.Items = append(out.Items, dto.Message{ID: item.ID, ConversationID: item.ConversationID,
			Status: item.Status, Question: item.Question, Answer: item.Answer, CreatedAt: item.CreatedAt.UTC().Format(time.RFC3339Nano)})
	}
	writeJSON(w, http.StatusOK, out)
}

func (h *ChatHandler) acceptMessage(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		chatMethodNotAllowed(w, "POST")
		return
	}
	scope, err := h.scope(r, true)
	if err != nil {
		writeChatError(w, err)
		return
	}
	in, err := decodeMessage(w, r)
	if err != nil {
		writeChatError(w, err)
		return
	}
	op, err := h.chat.AcceptMessage(r.Context(), scope, r.PathValue("id"), r.Header.Get("Idempotency-Key"), in.ConversationID, in.Message)
	if err != nil {
		writeChatError(w, err)
		return
	}
	switch op.State {
	case "accepted", "generating":
		w.Header().Set("Retry-After", "1")
		writeJSON(w, http.StatusAccepted, dto.ChatOperation{OperationID: op.ID, MessageID: op.MessageID, ConversationID: in.ConversationID, Status: op.State})
	case "completed", "no_answer":
		answer, ok := replayChatAnswer(op, in.ConversationID)
		if !ok {
			writeChatError(w, errInvalidChatResult)
			return
		}
		writeJSON(w, http.StatusOK, answer)
	case "failed", "cancelled":
		writeChatFailure(w, op)
	default:
		writeChatError(w, errInvalidChatResult)
	}
}

var errInvalidChatResult = errors.New("invalid stored chat result")

func replayChatAnswer(op models.AIRequest, conversationID string) (dto.TestChatAnswer, bool) {
	var saved struct {
		dto.TestChatAnswer
		HasAnswer         *bool  `json:"has_answer"`
		RemainingRequests *int64 `json:"remaining_requests"`
	}
	dec := json.NewDecoder(bytes.NewReader(op.Result))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&saved); err != nil {
		return dto.TestChatAnswer{}, false
	}
	answer := saved.TestChatAnswer
	completed := op.State == "completed"
	if answer.MessageID != op.MessageID || answer.ConversationID != conversationID || answer.Status != op.State ||
		!validChatResultText(answer.Answer, 10000) || saved.HasAnswer == nil || *saved.HasAnswer != completed ||
		answer.Sources == nil || (!completed && len(answer.Sources) != 0) || saved.RemainingRequests == nil ||
		*saved.RemainingRequests < 0 || answer.KnowledgeRevision < 1 {
		return dto.TestChatAnswer{}, false
	}
	for _, source := range answer.Sources {
		if !requestIDPattern.MatchString(source.CitationID) || !requestIDPattern.MatchString(source.DocumentID) ||
			!validChatResultText(source.DisplayName, 200) || !validChatResultText(source.Locator, 200) ||
			!validChatResultText(source.Excerpt, 1000) || source.DocumentVersion < 1 || source.KnowledgeRevision < 1 {
			return dto.TestChatAnswer{}, false
		}
	}
	answer.HasAnswer, answer.RemainingRequests = *saved.HasAnswer, *saved.RemainingRequests
	return answer, true
}

func validChatResultText(s string, maxRunes int) bool {
	return s != "" && utf8.RuneCountInString(s) <= maxRunes
}

func writeChatFailure(w http.ResponseWriter, op models.AIRequest) {
	var result struct {
		Status string `json:"status"`
		Code   string `json:"code"`
	}
	if err := json.Unmarshal(op.Result, &result); err != nil || result.Status != op.State {
		writeChatError(w, errInvalidChatResult)
		return
	}
	switch result.Code {
	case "ai_unavailable":
		writeError(w, http.StatusServiceUnavailable, "ai_unavailable", "Ответ недоступен")
	case "deadline_exceeded":
		writeError(w, http.StatusGatewayTimeout, "deadline_exceeded", "Время ожидания ответа истекло")
	default:
		writeChatError(w, errInvalidChatResult)
	}
}

func (h *ChatHandler) scope(r *http.Request, mutation bool) (service.Scope, error) {
	origin := r.Header.Get("Origin")
	if len(r.Header.Values("Origin")) > 1 || ((mutation || origin != "") && origin != h.origin) || r.Header.Get("Sec-Fetch-Site") == "cross-site" {
		return service.Scope{}, service.ErrForbidden
	}
	token := ""
	if cookie, err := r.Cookie("faqbot_session"); err == nil {
		token = cookie.Value
	}
	scope, csrf, err := h.sessions.Scope(r.Context(), token)
	if err != nil || !mutation {
		return scope, err
	}
	provided := r.Header.Get("X-CSRF-Token")
	if len(r.Header.Values("X-CSRF-Token")) != 1 || provided == "" || subtle.ConstantTimeCompare([]byte(csrf), []byte(provided)) != 1 {
		return service.Scope{}, service.ErrForbidden
	}
	if len(r.Header.Values("Idempotency-Key")) != 1 || r.URL.RawQuery != "" {
		return service.Scope{}, service.ErrInvalidChat
	}
	return scope, nil
}

func chatPage(raw string) (int, string, error) {
	query, err := url.ParseQuery(raw)
	if err != nil {
		return 0, "", service.ErrInvalidChat
	}
	limit := 20
	for name, values := range query {
		if len(values) != 1 || values[0] == "" || (name != "limit" && name != "cursor") {
			return 0, "", service.ErrInvalidChat
		}
		if name == "limit" {
			limit, err = strconv.Atoi(values[0])
			if err != nil {
				return 0, "", service.ErrInvalidChat
			}
		}
	}
	return limit, query.Get("cursor"), nil
}

func decodeMessage(w http.ResponseWriter, r *http.Request) (dto.MessageCreate, error) {
	var out dto.MessageCreate
	media, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || media != "application/json" {
		return out, service.ErrInvalidChat
	}
	raw, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 1<<20))
	if err != nil || !utf8.Valid(raw) || !validJSONSurrogates(raw) {
		return out, service.ErrInvalidChat
	}
	dec := json.NewDecoder(strings.NewReader(string(raw)))
	token, err := dec.Token()
	if err != nil || token != json.Delim('{') {
		return out, service.ErrInvalidChat
	}
	fields := make(map[string]bool, 3)
	for dec.More() {
		token, err := dec.Token()
		if err != nil {
			return out, service.ErrInvalidChat
		}
		key, ok := token.(string)
		if !ok || fields[key] {
			return out, service.ErrInvalidChat
		}
		fields[key] = true
		switch key {
		case "conversation_id", "message":
			var value *string
			if err := dec.Decode(&value); err != nil || value == nil {
				return out, service.ErrInvalidChat
			}
			if key == "conversation_id" {
				out.ConversationID = *value
			} else {
				out.Message = *value
			}
		case "stream":
			var value *bool
			if err := dec.Decode(&value); err != nil || value == nil || *value {
				return out, service.ErrInvalidChat
			}
		default:
			return out, service.ErrInvalidChat
		}
	}
	if _, err := dec.Token(); err != nil {
		return out, service.ErrInvalidChat
	}
	if err := dec.Decode(new(any)); !errors.Is(err, io.EOF) || len(fields) != 3 {
		return out, service.ErrInvalidChat
	}
	return out, nil
}

func conversationDTO(c models.Conversation) dto.Conversation {
	return dto.Conversation{ID: c.ID, AgentID: c.AgentID, CreatedAt: c.CreatedAt.UTC().Format(time.RFC3339Nano)}
}

func chatMethodNotAllowed(w http.ResponseWriter, allow string) {
	w.Header().Set("Allow", allow)
	writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "Метод не поддерживается")
}

func writeChatError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, service.ErrUnauthorized):
		writeError(w, http.StatusUnauthorized, "unauthorized", "Вход не выполнен")
	case errors.Is(err, service.ErrForbidden):
		writeError(w, http.StatusForbidden, "forbidden", "Запрос запрещён")
	case errors.Is(err, service.ErrInvalidChat):
		writeError(w, http.StatusBadRequest, "invalid_request", "Некорректный запрос")
	case errors.Is(err, service.ErrChatNotFound):
		writeError(w, http.StatusNotFound, "not_found", "Ресурс не найден")
	case errors.Is(err, service.ErrIdempotencyConflict):
		writeError(w, http.StatusConflict, "idempotency_conflict", "Ключ операции уже использован")
	default:
		writeError(w, http.StatusInternalServerError, "internal_error", "Внутренняя ошибка")
	}
}
