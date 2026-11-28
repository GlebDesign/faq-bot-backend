package migration

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"reflect"
	"sync"
	"testing"
	"time"

	"faqbot/backend/internal/db"
	"faqbot/backend/internal/models"
	"faqbot/backend/internal/repository"
	"faqbot/backend/internal/service"
)

func seedSettlement(t *testing.T, pool *sql.DB, other bool) models.AIRequestInput {
	t.Helper()
	in := chatInput()
	in.ID = chatID(60)
	in.VisitorID, in.MemberID, in.Operation = "", chatID(3), "test_message"
	in.ConversationID, in.MessageID = chatID(14), chatID(24)
	allocation := chatID(51)
	if other {
		in.ID, in.AccountID, in.AgentID = chatID(61), chatID(2), chatID(8)
		in.VisitorID, in.MemberID, in.Operation = "visitor-one", "", "widget_message"
		in.ConversationID, in.MessageID = chatID(13), chatID(23)
		allocation = chatID(52)
	}
	quotaExec(t, pool, "INSERT INTO quota_allocations(id,account_id,kind,package_id,request_count) VALUES($1,$2,'purchased',$3,1)", allocation, in.AccountID, chatID(50))
	if _, err := repository.NewAIRequests(pool).CreateOrRead(t.Context(), in); err != nil {
		t.Fatal(err)
	}
	return in
}

type settlementRows struct {
	requestState string
	messageState string
	reservation  string
	answer       sql.NullString
	result       string
	finalized    string
	usage        int
}

func readSettlement(t *testing.T, pool *sql.DB, in models.AIRequestInput) settlementRows {
	t.Helper()
	var row settlementRows
	err := pool.QueryRowContext(t.Context(), `
		SELECT a.state,m.status,r.state,m.answer,COALESCE(a.result::text,''),COALESCE(r.finalized_at::text,''),
			(SELECT count(*) FROM quota_usage_events u WHERE u.account_id=a.account_id AND u.ai_request_id=a.id AND u.operation_id=a.id)
		FROM ai_requests a JOIN messages m ON m.id=a.message_id
		JOIN quota_reservations r ON r.account_id=a.account_id AND r.ai_request_id=a.id
		WHERE a.account_id=$1 AND a.id=$2`, in.AccountID, in.ID).
		Scan(&row.requestState, &row.messageState, &row.reservation, &row.answer, &row.result, &row.finalized, &row.usage)
	if err != nil {
		t.Fatal(err)
	}
	return row
}

func assertSettlementState(t *testing.T, row settlementRows, state, reservation string, usage int) {
	t.Helper()
	if row.requestState != state || row.messageState != state || row.reservation != reservation ||
		(row.finalized != "") != (reservation != "active") || row.usage != usage {
		t.Fatalf("unexpected settlement: %+v", row)
	}
}

func assertSettlementFailure(t *testing.T, request models.AIRequest, state string, code models.GenerationFailure) {
	t.Helper()
	var result struct {
		Status string
		Code   models.GenerationFailure
	}
	if err := json.Unmarshal(request.Result, &result); err != nil || request.State != state || result.Status != state || result.Code != code {
		t.Fatalf("unexpected failure: %s %s, %v", request.State, request.Result, err)
	}
}

func TestQuotaSettlementAnswerAndReplaySQL(t *testing.T) {
	for _, state := range []string{"completed", "no_answer"} {
		t.Run(state, func(t *testing.T) {
			pool := chatDatabase(t)
			seedQuotaPackage(t, pool)
			in := seedSettlement(t, pool, false)
			quotaExec(t, pool, "UPDATE quota_allocations SET request_count=2 WHERE account_id=$1", in.AccountID)
			generation := service.NewGeneration(repository.NewQuota(pool))
			answer := models.GeneratedAnswer{Answer: "Ответ", HasAnswer: state == "completed", KnowledgeRevision: 8}
			if answer.HasAnswer {
				answer.Sources = []models.CabinetSource{{
					CitationID: "citation_1", DisplayName: "Справка", Locator: "Раздел 1", Excerpt: "Текст источника",
					DocumentID: "doc_1", DocumentVersion: 2, KnowledgeRevision: 8,
				}}
			}
			got, err := generation.Execute(t.Context(), in.AccountID, in.ID, time.Minute, func(context.Context) (*models.GeneratedAnswer, error) {
				return &answer, nil
			})
			if err != nil || got.ID != in.ID || got.MessageID != in.MessageID || got.State != state {
				t.Fatalf("unexpected result: %+v, %v", got, err)
			}
			var payload models.TestChatAnswer
			want := models.TestChatAnswer{
				MessageID: in.MessageID, ConversationID: in.ConversationID, Status: state, Answer: answer.Answer,
				HasAnswer: answer.HasAnswer, Sources: append([]models.CabinetSource{}, answer.Sources...), RemainingRequests: 1, KnowledgeRevision: 8,
			}
			if err := json.Unmarshal(got.Result, &payload); err != nil || !reflect.DeepEqual(payload, want) {
				t.Fatalf("unexpected answer: %+v, %v", payload, err)
			}
			before := readSettlement(t, pool, in)
			assertSettlementState(t, before, state, "consumed", 1)
			if !before.answer.Valid || before.answer.String != answer.Answer {
				t.Fatalf("message answer: %+v", before.answer)
			}
			replay, err := generation.Execute(t.Context(), in.AccountID, in.ID, time.Minute, func(context.Context) (*models.GeneratedAnswer, error) {
				t.Fatal("replay called adapter")
				return nil, nil
			})
			if err != nil || replay.ID != got.ID || replay.MessageID != got.MessageID || replay.State != state || string(replay.Result) != before.result {
				t.Fatalf("unexpected replay: %+v, %v", replay, err)
			}
			if after := readSettlement(t, pool, in); after != before {
				t.Fatalf("replay changed storage: before=%+v after=%+v", before, after)
			}
			quotaBalance(t, pool, in.AccountID, 1)
		})
	}
}

func TestQuotaSettlementRollsBackOnUsageFailureSQL(t *testing.T) {
	pool := chatDatabase(t)
	seedQuotaPackage(t, pool)
	in := seedSettlement(t, pool, false)
	quota := repository.NewQuota(pool)
	quotaExec(t, pool, "ALTER TABLE quota_usage_events ADD CONSTRAINT reject_usage CHECK (false) NOT VALID")
	outcome := models.GenerationOutcome{Answer: &models.GeneratedAnswer{Answer: "Ответ", HasAnswer: true, KnowledgeRevision: 1}}
	if _, err := quota.Reserve(t.Context(), in.AccountID, in.ID, time.Minute); err != nil {
		t.Fatal(err)
	}
	before := readSettlement(t, pool, in)
	_, err := quota.Settle(t.Context(), t.Context(), in.AccountID, in.ID, outcome)
	sqlState(t, err, "23514")
	assertSettlementState(t, before, "accepted", "active", 0)
	if after := readSettlement(t, pool, in); after != before {
		t.Fatalf("failed settlement changed storage: before=%+v after=%+v", before, after)
	}
	quotaExec(t, pool, "ALTER TABLE quota_usage_events DROP CONSTRAINT reject_usage")
	got, err := quota.Settle(t.Context(), t.Context(), in.AccountID, in.ID, outcome)
	if err != nil || got.State != "completed" {
		t.Fatalf("settlement retry: %+v, %v", got, err)
	}
	assertSettlementState(t, readSettlement(t, pool, in), "completed", "consumed", 1)
}

func TestQuotaSettlementCancellationAndFailureSQL(t *testing.T) {
	for _, tc := range []struct {
		name  string
		state string
		err   error
	}{
		{name: "cancelled", state: "cancelled", err: context.Canceled},
		{name: "provider failure", state: "failed", err: errors.New("provider unavailable")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			pool := chatDatabase(t)
			seedQuotaPackage(t, pool)
			in := seedSettlement(t, pool, false)
			generation := service.NewGeneration(repository.NewQuota(pool))
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			got, err := generation.Execute(ctx, in.AccountID, in.ID, time.Minute, func(context.Context) (*models.GeneratedAnswer, error) {
				if errors.Is(tc.err, context.Canceled) {
					cancel()
					return &models.GeneratedAnswer{Answer: "Поздний ответ", HasAnswer: true, KnowledgeRevision: 1}, nil
				}
				return nil, tc.err
			})
			if !errors.Is(err, tc.err) {
				t.Fatalf("adapter error: %v, want %v", err, tc.err)
			}
			assertSettlementFailure(t, got, tc.state, models.GenerationFailed)
			row := readSettlement(t, pool, in)
			assertSettlementState(t, row, tc.state, "released", 0)
			if row.answer.Valid {
				t.Fatalf("unsuccessful generation saved an answer: %+v", row)
			}
			quotaBalance(t, pool, in.AccountID, 1)
		})
	}
}

func TestQuotaSettlementRecoversExpiredReservationsSQL(t *testing.T) {
	pool := chatDatabase(t)
	seedQuotaPackage(t, pool)
	in, other := seedSettlement(t, pool, false), seedSettlement(t, pool, true)
	quotaExec(t, pool, "INSERT INTO trial_bootstrap_intents(account_id,created_at) VALUES($1,now()-interval '1 day')", in.AccountID)
	trial := chatID(53)
	quotaExec(t, pool, "SELECT grant_trial_quota($1,$2,1,14)", in.AccountID, trial)
	quota := repository.NewQuota(pool)
	for _, request := range []models.AIRequestInput{in, other} {
		if _, err := quota.Reserve(t.Context(), request.AccountID, request.ID, time.Minute); err != nil {
			t.Fatal(err)
		}
	}
	quotaExec(t, pool, "UPDATE quota_allocations SET expires_at=clock_timestamp()-interval '1 minute' WHERE id=$1", trial)
	quotaExec(t, pool, `UPDATE quota_reservations SET reserved_at=clock_timestamp()-interval '2 minutes',
		deadline_at=clock_timestamp()-interval '1 minute' WHERE ai_request_id=$1`, in.ID)
	quotaBalance(t, pool, in.AccountID, 1)
	quotaBalance(t, pool, other.AccountID, 0)
	if err := quota.RecoverReservation(t.Context(), models.ReservationKey{AccountID: other.AccountID, RequestID: other.ID}); err != nil {
		t.Fatal(err)
	}
	assertSettlementState(t, readSettlement(t, pool, other), "accepted", "active", 0)
	quotaExec(t, pool, `UPDATE quota_reservations SET reserved_at=r.reserved_at,deadline_at=r.deadline_at
		FROM quota_reservations r WHERE quota_reservations.ai_request_id=$1 AND r.ai_request_id=$2`, other.ID, in.ID)
	horizon, err := quota.RecoveryHorizon(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	keys, err := quota.ExpiredReservations(t.Context(), 1, nil, horizon)
	if err != nil || len(keys) != 1 || keys[0].AccountID != in.AccountID || keys[0].RequestID != in.ID || keys[0].Deadline.IsZero() {
		t.Fatalf("expired reservations: %+v, %v", keys, err)
	}
	next, err := quota.ExpiredReservations(t.Context(), 1, &keys[0], keys[0].Deadline)
	if err != nil || len(next) != 1 || next[0].AccountID != other.AccountID || next[0].RequestID != other.ID ||
		!next[0].Deadline.Equal(keys[0].Deadline) {
		t.Fatalf("next expired reservation: %+v, %v", next, err)
	}
	quotaExec(t, pool, "UPDATE quota_reservations SET deadline_at=deadline_at+interval '1 second' WHERE ai_request_id=$1", other.ID)
	if next, err := quota.ExpiredReservations(t.Context(), 1, &keys[0], keys[0].Deadline); err != nil || len(next) != 0 {
		t.Fatalf("recovery included a reservation outside its horizon: %+v, %v", next, err)
	}
	for range 2 {
		if err := quota.RecoverReservation(t.Context(), keys[0]); err != nil {
			t.Fatal(err)
		}
	}
	got, err := quota.ReadGeneration(t.Context(), in.AccountID, in.ID)
	if err != nil {
		t.Fatal(err)
	}
	assertSettlementFailure(t, got, "failed", models.GenerationTimedOut)
	before := readSettlement(t, pool, in)
	assertSettlementState(t, before, "failed", "released", 0)
	late := &models.GeneratedAnswer{Answer: "Поздний ответ", HasAnswer: true, KnowledgeRevision: 1}
	got, err = quota.Settle(t.Context(), t.Context(), in.AccountID, in.ID, models.GenerationOutcome{Answer: late})
	if err != nil {
		t.Fatal(err)
	}
	assertSettlementFailure(t, got, "failed", models.GenerationTimedOut)
	if after := readSettlement(t, pool, in); after != before {
		t.Fatalf("late result changed storage: before=%+v after=%+v", before, after)
	}
	keys, err = quota.ExpiredReservations(t.Context(), 1, nil, horizon)
	if err != nil || len(keys) != 1 || keys[0].RequestID != other.ID {
		t.Fatalf("terminal reservation rediscovered or pending reservation lost: %+v, %v", keys, err)
	}
	assertSettlementState(t, readSettlement(t, pool, other), "accepted", "active", 0)
	quotaBalance(t, pool, in.AccountID, 1)
	quotaBalance(t, pool, other.AccountID, 0)
	if err := quota.RecoverReservation(t.Context(), keys[0]); err != nil {
		t.Fatal(err)
	}
	assertSettlementState(t, readSettlement(t, pool, other), "failed", "released", 0)
	quotaBalance(t, pool, other.AccountID, 1)
}

func TestQuotaSettlementRejectsLateAnswerBeforeRecoverySQL(t *testing.T) {
	pool := chatDatabase(t)
	seedQuotaPackage(t, pool)
	in := seedSettlement(t, pool, false)
	quota := repository.NewQuota(pool)
	ctx := t.Context()
	if _, err := quota.Reserve(ctx, in.AccountID, in.ID, time.Minute); err != nil {
		t.Fatal(err)
	}
	quotaExec(t, pool, `UPDATE quota_reservations SET reserved_at=clock_timestamp()-interval '2 minutes',
		deadline_at=clock_timestamp() WHERE ai_request_id=$1`, in.ID)
	outcome := models.GenerationOutcome{Answer: &models.GeneratedAnswer{Answer: "Поздний ответ", HasAnswer: true, KnowledgeRevision: 1}}
	got, err := quota.Settle(ctx, ctx, in.AccountID, in.ID, outcome)
	if err != nil {
		t.Fatal(err)
	}
	assertSettlementFailure(t, got, "failed", models.GenerationTimedOut)
	row := readSettlement(t, pool, in)
	assertSettlementState(t, row, "failed", "released", 0)
	if row.answer.Valid {
		t.Fatalf("late generation saved an answer: %+v", row)
	}
	quotaBalance(t, pool, in.AccountID, 1)
}

func TestQuotaSettlementObservesCancellationAfterLockSQL(t *testing.T) {
	pool := chatDatabase(t)
	seedQuotaPackage(t, pool)
	in := seedSettlement(t, pool, false)
	quota := repository.NewQuota(pool)
	ctx, cancel := context.WithTimeout(t.Context(), 8*time.Second)
	defer cancel()
	requestCtx, cancelRequest := context.WithCancel(ctx)
	defer cancelRequest()
	if _, err := quota.Reserve(ctx, in.AccountID, in.ID, time.Minute); err != nil {
		t.Fatal(err)
	}
	blocker, err := pool.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(db.WrapError("start settlement blocker", err))
	}
	defer func() {
		if err := blocker.Rollback(); err != nil && !errors.Is(err, sql.ErrTxDone) {
			t.Error(db.WrapError("close settlement blocker", err))
		}
	}()
	if _, err := blocker.ExecContext(ctx, "SELECT pg_advisory_xact_lock(hashtextextended('quota:' || $1::uuid::text,0))", in.AccountID); err != nil {
		t.Fatal(db.WrapError("block settlement", err))
	}
	var pid int
	if err := blocker.QueryRowContext(ctx, "SELECT pg_backend_pid()").Scan(&pid); err != nil {
		t.Fatal(db.WrapError("read settlement blocker pid", err))
	}
	done := make(chan error, 1)
	go func() {
		outcome := models.GenerationOutcome{Answer: &models.GeneratedAnswer{Answer: "Ответ", HasAnswer: true, KnowledgeRevision: 1}}
		_, err := quota.Settle(ctx, requestCtx, in.AccountID, in.ID, outcome)
		done <- err
	}()
	waitQuotaBlocking(t, ctx, pool, pid, done)
	cancelRequest()
	if err := blocker.Commit(); err != nil {
		t.Fatal(db.WrapError("release settlement blocker", err))
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	got, err := quota.ReadGeneration(ctx, in.AccountID, in.ID)
	if err != nil {
		t.Fatal(err)
	}
	assertSettlementFailure(t, got, "cancelled", models.GenerationFailed)
	assertSettlementState(t, readSettlement(t, pool, in), "cancelled", "released", 0)
	quotaBalance(t, pool, in.AccountID, 1)
}

func TestQuotaSettlementRaceSQL(t *testing.T) {
	pool := chatDatabase(t)
	seedQuotaPackage(t, pool)
	in := seedSettlement(t, pool, false)
	quota := repository.NewQuota(pool)
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	if _, err := quota.Reserve(ctx, in.AccountID, in.ID, time.Minute); err != nil {
		t.Fatal(err)
	}
	success := models.GenerationOutcome{Answer: &models.GeneratedAnswer{Answer: "Ответ", HasAnswer: true, KnowledgeRevision: 1}}
	failure := models.GenerationOutcome{Failure: models.GenerationCancelled}
	var wg sync.WaitGroup
	start := make(chan struct{})
	results := make([]models.AIRequest, 8)
	errs := make([]error, len(results))
	for i := range results {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			outcome := success
			if i%2 == 1 {
				outcome = failure
			}
			results[i], errs[i] = quota.Settle(ctx, ctx, in.AccountID, in.ID, outcome)
		}()
	}
	close(start)
	wg.Wait()
	got := readSettlement(t, pool, in)
	switch got.requestState {
	case "completed":
		assertSettlementState(t, got, "completed", "consumed", 1)
		quotaBalance(t, pool, in.AccountID, 0)
	case "cancelled":
		assertSettlementState(t, got, "cancelled", "released", 0)
		quotaBalance(t, pool, in.AccountID, 1)
	default:
		t.Fatalf("unexpected result of competing settlements: %+v", got)
	}
	var stored any
	if err := json.Unmarshal([]byte(got.result), &stored); err != nil {
		t.Fatal(err)
	}
	for i, result := range results {
		var payload any
		if err := json.Unmarshal(result.Result, &payload); errs[i] != nil || err != nil || result.State != got.requestState || !reflect.DeepEqual(payload, stored) {
			t.Fatalf("settlement %d: %+v, error=%v, decode=%v", i, result, errs[i], err)
		}
	}
}

func TestQuotaSettlementPreservesSharedTerminalMessageSQL(t *testing.T) {
	pool := chatDatabase(t)
	seedQuotaPackage(t, pool)
	first := seedSettlement(t, pool, false)
	second := first
	second.ID, second.IdempotencyKey = chatID(62), "other-key"
	ctx := t.Context()
	if _, err := repository.NewAIRequests(pool).CreateOrRead(ctx, second); err != nil {
		t.Fatal(err)
	}
	quotaExec(t, pool, "UPDATE quota_allocations SET request_count=2 WHERE account_id=$1", first.AccountID)
	quota := repository.NewQuota(pool)
	for _, in := range []models.AIRequestInput{first, second} {
		if _, err := quota.Reserve(ctx, in.AccountID, in.ID, time.Minute); err != nil {
			t.Fatal(err)
		}
	}
	outcome := models.GenerationOutcome{Answer: &models.GeneratedAnswer{Answer: "Первый ответ", HasAnswer: true, KnowledgeRevision: 1}}
	if got, err := quota.Settle(ctx, ctx, first.AccountID, first.ID, outcome); err != nil || got.State != "completed" {
		t.Fatalf("first settlement: %+v, %v", got, err)
	}
	before := readSettlement(t, pool, first)
	assertSettlementState(t, before, "completed", "consumed", 1)
	outcome.Answer.Answer = "Другой ответ"
	got, err := quota.Settle(ctx, ctx, second.AccountID, second.ID, outcome)
	if err != nil {
		t.Fatal(err)
	}
	assertSettlementFailure(t, got, "failed", models.GenerationFailed)
	if after := readSettlement(t, pool, first); after != before {
		t.Fatalf("second request changed the first result: before=%+v after=%+v", before, after)
	}
	if row := readSettlement(t, pool, second); row.requestState != "failed" || row.messageState != "completed" ||
		row.reservation != "released" || row.finalized == "" || row.usage != 0 {
		t.Fatalf("unexpected second settlement: %+v", row)
	}
	quotaBalance(t, pool, first.AccountID, 1)
}
