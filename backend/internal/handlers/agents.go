package handlers

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"unicode/utf8"

	"faqbot/backend/internal/models"
	"faqbot/backend/internal/service"
)

type AgentSessions interface {
	Scope(context.Context, string) (service.Scope, string, error)
}
type AgentService interface {
	List(context.Context, service.Scope, int, string) ([]models.Agent, *string, error)
	Get(context.Context, service.Scope, string) (models.Agent, error)
	Create(context.Context, service.Scope, string, string, models.AgentInput) (models.Agent, error)
	Update(context.Context, service.Scope, string, string, string, models.AgentInput, int64) (models.Agent, error)
	Delete(context.Context, service.Scope, string, string, string) (models.AgentCleanup, error)
}
type AgentHandler struct {
	sessions AgentSessions
	agents   AgentService
	origin   string
}

func NewAgentHandler(sessions AgentSessions, agents AgentService, origin string) *AgentHandler {
	return &AgentHandler{sessions: sessions, agents: agents, origin: origin}
}

func (a *AgentHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimPrefix(r.URL.Path, "/api/v1/agents")
	collection := id == ""
	if !collection {
		if !strings.HasPrefix(id, "/") || strings.Contains(id[1:], "/") || len(id) == 1 {
			writeError(w, 404, "not_found", "Ресурс не найден")
			return
		}
		id = id[1:]
	}
	allow := "GET, POST"
	valid := r.Method == http.MethodGet || r.Method == http.MethodPost
	if !collection {
		allow = "GET, PATCH, DELETE"
		valid = r.Method == http.MethodGet || r.Method == http.MethodPatch || r.Method == http.MethodDelete
	}
	if !valid {
		w.Header().Set("Allow", allow)
		writeError(w, 405, "method_not_allowed", "Метод не поддерживается")
		return
	}
	mutation := r.Method != http.MethodGet
	if (mutation && r.Header.Get("Origin") != a.origin) || (!mutation && r.Header.Get("Origin") != "" && r.Header.Get("Origin") != a.origin) || r.Header.Get("Sec-Fetch-Site") == "cross-site" {
		a.writeError(w, service.ErrForbidden)
		return
	}
	token := ""
	if c, err := r.Cookie("faqbot_session"); err == nil {
		token = c.Value
	}
	scope, csrf, err := a.sessions.Scope(r.Context(), token)
	if err != nil {
		a.writeError(w, err)
		return
	}
	if mutation && (r.Header.Get("X-CSRF-Token") == "" || subtle.ConstantTimeCompare([]byte(csrf), []byte(r.Header.Get("X-CSRF-Token"))) != 1) {
		a.writeError(w, service.ErrForbidden)
		return
	}
	query, err := url.ParseQuery(r.URL.RawQuery)
	if err != nil {
		a.writeError(w, service.ErrInvalidAgent)
		return
	}
	for name, values := range query {
		if !collection || r.Method != http.MethodGet || (name != "limit" && name != "cursor") || len(values) != 1 {
			a.writeError(w, service.ErrInvalidAgent)
			return
		}
	}
	if mutation && (len(r.Header.Values("Idempotency-Key")) != 1 || len(r.Header.Values("Origin")) != 1 || len(r.Header.Values("X-CSRF-Token")) != 1) {
		a.writeError(w, service.ErrInvalidAgent)
		return
	}
	requestID := rand.Text()
	switch r.Method {
	case http.MethodGet:
		if collection {
			limit := 20
			if v, ok := query["limit"]; ok {
				limit, err = strconv.Atoi(v[0])
				if err != nil {
					a.writeError(w, service.ErrInvalidAgent)
					return
				}
			}
			cursor := query.Get("cursor")
			if _, ok := query["cursor"]; ok && cursor == "" {
				a.writeError(w, service.ErrInvalidAgent)
				return
			}
			items, next, err := a.agents.List(r.Context(), scope, limit, cursor)
			if err != nil {
				a.writeError(w, err)
				return
			}
			writeJSON(w, 200, struct {
				Items      []models.Agent `json:"items"`
				NextCursor *string        `json:"next_cursor"`
			}{items, next})
			return
		}
		agent, err := a.agents.Get(r.Context(), scope, id)
		if err != nil {
			a.writeError(w, err)
			return
		}
		a.writeAgent(w, 200, agent)
	case http.MethodPost, http.MethodPatch:
		input, err := decodeAgent(w, r)
		if err != nil {
			a.writeError(w, err)
			return
		}
		var agent models.Agent
		status := 201
		if r.Method == http.MethodPost {
			agent, err = a.agents.Create(r.Context(), scope, r.Header.Get("Idempotency-Key"), requestID, input)
		} else {
			if len(r.Header.Values("If-Match")) != 1 {
				a.writeError(w, service.ErrInvalidAgent)
				return
			}
			header := r.Header.Get("If-Match")
			if len(header) < 3 || header[0] != '"' || header[len(header)-1] != '"' || header[1] == '0' {
				a.writeError(w, service.ErrInvalidAgent)
				return
			}
			value := header[1 : len(header)-1]
			for _, c := range value {
				if c < '0' || c > '9' {
					a.writeError(w, service.ErrInvalidAgent)
					return
				}
			}
			revision, parseErr := strconv.ParseInt(value, 10, 64)
			if parseErr != nil || revision < 1 {
				a.writeError(w, service.ErrInvalidAgent)
				return
			}
			agent, err = a.agents.Update(r.Context(), scope, id, r.Header.Get("Idempotency-Key"), requestID, input, revision)
			status = 200
		}
		if err != nil {
			a.writeError(w, err)
			return
		}
		a.writeAgent(w, status, agent)
	case http.MethodDelete:
		body, readErr := io.ReadAll(io.LimitReader(r.Body, 1))
		if readErr != nil || len(body) != 0 {
			a.writeError(w, service.ErrInvalidAgent)
			return
		}
		job, err := a.agents.Delete(r.Context(), scope, id, r.Header.Get("Idempotency-Key"), requestID)
		if err != nil {
			a.writeError(w, err)
			return
		}
		writeJSON(w, 202, job)
	}
}
func decodeAgent(w http.ResponseWriter, r *http.Request) (models.AgentInput, error) {
	var out models.AgentInput
	media, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || media != "application/json" {
		return out, service.ErrInvalidAgent
	}
	raw, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 1<<20))
	if err != nil || !utf8.Valid(raw) || !validJSONSurrogates(raw) {
		return out, service.ErrInvalidAgent
	}
	dec := json.NewDecoder(strings.NewReader(string(raw)))
	token, err := dec.Token()
	if err != nil || token != json.Delim('{') {
		return out, service.ErrInvalidAgent
	}
	fields := map[string]string{}
	for dec.More() {
		token, err := dec.Token()
		if err != nil {
			return out, service.ErrInvalidAgent
		}
		key, ok := token.(string)
		if !ok || (key != "name" && key != "website" && key != "description") {
			return out, service.ErrInvalidAgent
		}
		if _, exists := fields[key]; exists {
			return out, service.ErrInvalidAgent
		}
		var value *string
		if err := dec.Decode(&value); err != nil || value == nil {
			return out, service.ErrInvalidAgent
		}
		fields[key] = *value
	}
	if _, err := dec.Token(); err != nil {
		return out, service.ErrInvalidAgent
	}
	if err := dec.Decode(new(any)); !errors.Is(err, io.EOF) || len(fields) != 3 {
		return out, service.ErrInvalidAgent
	}
	return models.AgentInput{Name: fields["name"], Website: fields["website"], Description: fields["description"]}, nil
}
func (a *AgentHandler) writeAgent(w http.ResponseWriter, status int, agent models.Agent) {
	w.Header().Set("ETag", `"`+strconv.FormatInt(agent.Revision, 10)+`"`)
	writeJSON(w, status, agent)
}
func (a *AgentHandler) writeError(w http.ResponseWriter, err error) {
	status := 500
	code := "internal_error"
	message := "Внутренняя ошибка"
	switch {
	case errors.Is(err, service.ErrUnauthorized):
		status = 401
		code = "unauthorized"
		message = "Вход не выполнен"
	case errors.Is(err, service.ErrForbidden):
		status = 403
		code = "forbidden"
		message = "Запрос запрещён"
	case errors.Is(err, service.ErrInvalidAgent):
		status = 400
		code = "invalid_request"
		message = "Некорректный запрос"
	case errors.Is(err, service.ErrAgentNotFound):
		status = 404
		code = "not_found"
		message = "Ресурс не найден"
	case errors.Is(err, service.ErrRevisionConflict):
		status = 412
		code = "revision_conflict"
		message = "Версия ресурса изменилась"
	case errors.Is(err, service.ErrIdempotencyConflict):
		status = 409
		code = "idempotency_conflict"
		message = "Ключ операции уже использован"
	}
	writeError(w, status, code, message)
}

func validJSONSurrogates(raw []byte) bool {
	for i := 0; i < len(raw); i++ {
		if raw[i] != '\\' {
			continue
		}
		i++
		if i >= len(raw) {
			return false
		}
		if raw[i] != 'u' {
			continue
		}
		if i+4 >= len(raw) {
			return false
		}
		code, err := strconv.ParseUint(string(raw[i+1:i+5]), 16, 16)
		if err != nil {
			return false
		}
		i += 4
		if code >= 0xdc00 && code <= 0xdfff {
			return false
		}
		if code < 0xd800 || code > 0xdbff {
			continue
		}
		if i+6 >= len(raw) || raw[i+1] != '\\' || raw[i+2] != 'u' {
			return false
		}
		low, err := strconv.ParseUint(string(raw[i+3:i+7]), 16, 16)
		if err != nil || low < 0xdc00 || low > 0xdfff {
			return false
		}
		i += 6
	}
	return true
}
