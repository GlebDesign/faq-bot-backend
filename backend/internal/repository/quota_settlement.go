package repository

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"

	"faqbot/backend/internal/db"
	"faqbot/backend/internal/models"
	"faqbot/backend/internal/service"
)

func (q *Quota) ReadGeneration(ctx context.Context, accountID, requestID string) (models.AIRequest, error) {
	var request models.AIRequest
	var stored []byte
	err := q.pool.QueryRowContext(ctx, "SELECT id,message_id,state,result FROM ai_requests WHERE account_id=$1 AND id=$2",
		accountID, requestID).Scan(&request.ID, &request.MessageID, &request.State, &stored)
	if errors.Is(err, sql.ErrNoRows) {
		return models.AIRequest{}, service.ErrQuotaRequestNotFound
	}
	request.Result = stored
	return request, db.WrapError("read generation result", err)
}

func (q *Quota) Settle(ctx, requestCtx context.Context, accountID, requestID string, outcome models.GenerationOutcome) (models.AIRequest, error) {
	return q.settle(ctx, requestCtx, accountID, requestID, outcome, false)
}

func (q *Quota) RecoverReservation(ctx context.Context, key models.ReservationKey) error {
	_, err := q.settle(ctx, ctx, key.AccountID, key.RequestID, models.GenerationOutcome{}, true)
	return err
}

func (q *Quota) settle(ctx, requestCtx context.Context, accountID, requestID string, outcome models.GenerationOutcome, recoverOnly bool) (models.AIRequest, error) {
	var result models.AIRequest
	err := q.write(ctx, accountID, func(ctx context.Context, tx *sql.Tx) error {
		var reservation, agentID, conversationID, operation, messageState string
		var deadline time.Time
		var stored []byte
		err := tx.QueryRowContext(ctx, `
			SELECT q.state,q.deadline_at,r.id,r.message_id,r.state,r.result,r.agent_id,r.conversation_id,r.operation,m.status
			FROM quota_reservations q
			JOIN ai_requests r ON (r.account_id,r.id)=(q.account_id,q.ai_request_id)
			JOIN messages m ON (m.account_id,m.agent_id,m.conversation_id,m.id)=(r.account_id,r.agent_id,r.conversation_id,r.message_id)
			WHERE q.account_id=$1 AND q.ai_request_id=$2
			FOR NO KEY UPDATE OF q,r,m`, accountID, requestID).
			Scan(&reservation, &deadline, &result.ID, &result.MessageID, &result.State, &stored, &agentID, &conversationID, &operation, &messageState)
		if errors.Is(err, sql.ErrNoRows) {
			return service.ErrQuotaReservationAbsent
		}
		if err != nil {
			return db.WrapError("lock quota reservation and chat result", err)
		}
		result.Result = stored
		if reservation != "active" {
			if result.Result == nil {
				return service.ErrQuotaInconsistent
			}
			return nil
		}
		var now time.Time
		if err := tx.QueryRowContext(ctx, "SELECT clock_timestamp()").Scan(&now); err != nil {
			return db.WrapError("read settlement clock", err)
		}
		expired := !now.Before(deadline)
		if recoverOnly && !expired {
			return nil
		}
		if result.Result != nil {
			return writeQuotaRow(ctx, tx, "release inconsistent quota reservation", `
				UPDATE quota_reservations SET state='released',finalized_at=$3 WHERE account_id=$1 AND ai_request_id=$2`,
				accountID, requestID, now)
		}
		messagePending := messageState == "accepted" || messageState == "generating"
		failure := outcome.Failure
		if expired {
			failure = models.GenerationTimedOut
		} else if errors.Is(requestCtx.Err(), context.DeadlineExceeded) {
			failure = models.GenerationTimedOut
		} else if requestCtx.Err() != nil {
			failure = models.GenerationCancelled
		} else if failure == "" && (!messagePending || !validGeneratedAnswer(outcome.Answer, operation == "test_message")) {
			failure = models.GenerationFailed
		}
		state, reservationState := "failed", "released"
		var answer *string
		switch failure {
		case "":
			state, reservationState = "no_answer", "consumed"
			if outcome.Answer.HasAnswer {
				state = "completed"
			}
			answer = &outcome.Answer.Answer
		case models.GenerationCancelled:
			state = "cancelled"
			failure = models.GenerationFailed
		case models.GenerationTimedOut:
		default:
			failure = models.GenerationFailed
		}
		if err := writeQuotaRow(ctx, tx, "finalize quota reservation", `
			UPDATE quota_reservations SET state=$3,finalized_at=$4 WHERE account_id=$1 AND ai_request_id=$2`,
			accountID, requestID, reservationState, now); err != nil {
			return err
		}
		if messagePending {
			if err := writeQuotaRow(ctx, tx, "save generated message", `
				UPDATE messages SET status=$5,answer=$6 WHERE account_id=$1 AND agent_id=$2 AND conversation_id=$3 AND id=$4`,
				accountID, agentID, conversationID, result.MessageID, state, answer); err != nil {
				return err
			}
		}
		var payload any
		if failure == "" {
			if err := writeQuotaRow(ctx, tx, "save quota usage", `
				INSERT INTO quota_usage_events(operation_id,ai_request_id,account_id,occurred_at) VALUES($2,$2,$1,$3)`,
				accountID, requestID, now); err != nil {
				return err
			}
			var remaining int64
			if err := tx.QueryRowContext(ctx, quotaAvailableSQL, accountID).Scan(&remaining); err != nil {
				return db.WrapError("read remaining quota after settlement", err)
			}
			payload = chatResult(result.MessageID, conversationID, state, remaining, outcome.Answer, operation == "test_message")
		} else {
			payload = struct {
				Status string                   `json:"status"`
				Code   models.GenerationFailure `json:"code"`
			}{Status: state, Code: failure}
		}
		raw, err := json.Marshal(payload)
		if err != nil {
			return db.WrapError("encode generated chat result", err)
		}
		if err := writeQuotaRow(ctx, tx, "save generated request", `
			UPDATE ai_requests SET state=$3,result=$4 WHERE account_id=$1 AND id=$2`, accountID, requestID, state, raw); err != nil {
			return err
		}
		result.State, result.Result = state, raw
		return nil
	})
	if err != nil {
		return models.AIRequest{}, err
	}
	return result, nil
}

func writeQuotaRow(ctx context.Context, tx *sql.Tx, action, query string, args ...any) error {
	result, err := tx.ExecContext(ctx, query, args...)
	if err != nil {
		return db.WrapError(action, err)
	}
	n, err := result.RowsAffected()
	if err != nil {
		return db.WrapError(action, err)
	}
	if n != 1 {
		return db.WrapError(action, service.ErrQuotaInconsistent)
	}
	return nil
}

func (q *Quota) RecoveryHorizon(ctx context.Context) (time.Time, error) {
	var horizon time.Time
	err := q.pool.QueryRowContext(ctx, "SELECT statement_timestamp()").Scan(&horizon)
	return horizon, db.WrapError("read recovery clock", err)
}

func (q *Quota) ExpiredReservations(ctx context.Context, limit int, after *models.ReservationKey, horizon time.Time) ([]models.ReservationKey, error) {
	var deadline, requestID any
	if after != nil {
		deadline, requestID = after.Deadline, after.RequestID
	}
	rows, err := q.pool.QueryContext(ctx, `
		SELECT account_id,ai_request_id,deadline_at FROM quota_reservations
		WHERE state='active' AND deadline_at<=$4
		  AND ($2::timestamptz IS NULL OR (deadline_at,ai_request_id)>($2,$3::uuid))
		ORDER BY deadline_at,ai_request_id LIMIT $1`, limit, deadline, requestID, horizon)
	if err != nil {
		return nil, db.WrapError("find expired quota reservations", err)
	}
	defer func() { _ = rows.Close() }()
	var expired []models.ReservationKey
	for rows.Next() {
		var key models.ReservationKey
		if err := rows.Scan(&key.AccountID, &key.RequestID, &key.Deadline); err != nil {
			return nil, db.WrapError("read expired quota reservation", err)
		}
		expired = append(expired, key)
	}
	if err := rows.Err(); err != nil {
		return nil, db.WrapError("read expired quota reservations", err)
	}
	return expired, nil
}

var chatSourceID = regexp.MustCompile(`^[A-Za-z0-9_-]{1,80}$`)

func validGeneratedAnswer(answer *models.GeneratedAnswer, testChat bool) bool {
	if answer == nil || !validGeneratedText(answer.Answer, 10000) || !answer.HasAnswer && len(answer.Sources) > 0 ||
		testChat && answer.KnowledgeRevision < 1 {
		return false
	}
	for _, source := range answer.Sources {
		if !chatSourceID.MatchString(source.CitationID) ||
			!validGeneratedText(source.DisplayName, 200) || !validGeneratedText(source.Locator, 200) ||
			!validGeneratedText(source.Excerpt, 1000) {
			return false
		}
		if testChat && (!chatSourceID.MatchString(source.DocumentID) || source.DocumentVersion < 1 || source.KnowledgeRevision < 1) {
			return false
		}
	}
	return true
}

func validGeneratedText(s string, maxRunes int) bool {
	return utf8.ValidString(s) && !strings.ContainsRune(s, 0) && strings.TrimSpace(s) != "" && utf8.RuneCountInString(s) <= maxRunes
}

func chatResult(messageID, conversationID, state string, remaining int64, answer *models.GeneratedAnswer, testChat bool) any {
	if testChat {
		return models.TestChatAnswer{
			MessageID: messageID, ConversationID: conversationID, Status: state,
			Answer: answer.Answer, HasAnswer: answer.HasAnswer, Sources: append([]models.CabinetSource{}, answer.Sources...),
			RemainingRequests: remaining, KnowledgeRevision: answer.KnowledgeRevision,
		}
	}
	sources := make([]models.PublicSource, 0, len(answer.Sources))
	for _, source := range answer.Sources {
		sources = append(sources, models.PublicSource{
			CitationID: source.CitationID, DisplayName: source.DisplayName, Locator: source.Locator, Excerpt: source.Excerpt,
		})
	}
	return models.ChatAnswer{
		MessageID: messageID, ConversationID: conversationID, Status: state,
		Answer: answer.Answer, HasAnswer: answer.HasAnswer, Sources: sources, RemainingRequests: remaining,
	}
}
