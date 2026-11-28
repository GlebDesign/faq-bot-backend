package migration

import (
	"context"
	"database/sql"
	"errors"
	"net/url"
	"os"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"faqbot/backend/internal/db"
	"faqbot/backend/internal/repository"
	"faqbot/backend/internal/service"

	"github.com/jackc/pgx/v5"
)

func seedQuotaRequest(t *testing.T, pool *sql.DB, id int, other bool) {
	t.Helper()
	in := chatInput()
	in.ID = chatID(id)
	in.IdempotencyKey = in.ID
	if other {
		in.AccountID, in.AgentID = chatID(2), chatID(8)
		in.ConversationID, in.MessageID = chatID(13), chatID(23)
	}
	if _, err := repository.NewAIRequests(pool).CreateOrRead(t.Context(), in); err != nil {
		t.Fatal(err)
	}
}

func seedQuotaPackage(t *testing.T, pool *sql.DB) {
	t.Helper()
	quotaExec(t, pool, "INSERT INTO quota_packages(id,name,request_count) VALUES($1,'Учебный',1)", chatID(50))
}

func TestQuotaReserveConcurrentGeneration(t *testing.T) {
	pool := chatDatabase(t)
	role := os.Getenv("TEST_AUDIT_ROLE")
	if role != "" {
		var schema string
		if err := pool.QueryRow("SELECT current_schema()").Scan(&schema); err != nil {
			t.Fatal(db.WrapError("read quota schema", err))
		}
		quoted := pgx.Identifier{role}.Sanitize()
		for _, q := range []string{
			"GRANT USAGE ON SCHEMA " + pgx.Identifier{schema}.Sanitize(),
			"GRANT SELECT ON ai_requests, quota_allocations",
			"GRANT SELECT, INSERT ON quota_reservations",
		} {
			quotaExec(t, pool, q+" TO "+quoted)
		}
		u, err := url.Parse(os.Getenv("TEST_DATABASE_URL"))
		if err != nil {
			t.Fatal(db.WrapError("parse quota test database", err))
		}
		query := u.Query()
		query.Set("role", role)
		u.RawQuery = query.Encode()
		t.Setenv("TEST_DATABASE_URL", u.String())
	}
	writer := chatIsolationPool(t, pool)
	if role != "" {
		var canUpdate bool
		if err := writer.QueryRow("SELECT has_table_privilege(current_user,'accounts','UPDATE')").Scan(&canUpdate); err != nil || canUpdate {
			t.Fatalf("quota writer UPDATE accounts=%t: %v", canUpdate, err)
		}
	}
	ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
	defer cancel()
	account := chatID(1)
	seedQuotaPackage(t, pool)
	quotaExec(t, pool, "INSERT INTO quota_allocations(id,account_id,kind,package_id,request_count) VALUES($1,$2,'purchased',$3,1)", chatID(51), account, chatID(50))
	const competitors = 20
	for i := range competitors {
		in := chatInput()
		in.ID = chatID(100 + i)
		in.IdempotencyKey = in.ID
		if i%2 != 0 {
			in.AgentID = chatID(7)
			in.ConversationID, in.MessageID = chatID(12), chatID(22)
		}
		if _, err := repository.NewAIRequests(pool).CreateOrRead(ctx, in); err != nil {
			t.Fatal(err)
		}
	}
	generation := service.NewGeneration(repository.NewQuota(writer))
	start := make(chan struct{})
	var wg sync.WaitGroup
	var adapterCalls atomic.Int32
	results := make([]error, competitors)
	for i := range competitors {
		wg.Add(1)
		go func() {
			defer wg.Done()
			select {
			case <-start:
			case <-ctx.Done():
				results[i] = ctx.Err()
				return
			}
			id := chatID(100 + i)
			result, err := generation.Start(ctx, account, id, 5*time.Second, func(adapterCtx context.Context) (*string, error) {
				adapterCalls.Add(1)
				var reservedAt, deadline time.Time
				err := pool.QueryRowContext(adapterCtx, `SELECT reserved_at,deadline_at FROM quota_reservations
					WHERE ai_request_id=$1 AND account_id=$2 AND state='active'`, id, account).Scan(&reservedAt, &deadline)
				if err != nil {
					return nil, db.WrapError("read committed reservation from adapter", err)
				}
				adapterDeadline, ok := adapterCtx.Deadline()
				if !ok || !adapterDeadline.Equal(deadline) || deadline.Sub(reservedAt) != 5*time.Second {
					return nil, errors.New("adapter deadline does not match committed reservation")
				}
				answer := "Ответ"
				return &answer, nil
			})
			if err == nil && (result.Answer == nil || *result.Answer != "Ответ" || result.Deadline.IsZero()) {
				err = errors.New("generation did not return the adapter result and deadline")
			}
			results[i] = err
		}()
	}
	close(start)
	wg.Wait()
	successes := 0
	for _, err := range results {
		if err == nil {
			successes++
		} else if !errors.Is(err, service.ErrQuotaExhausted) {
			t.Fatal(err)
		}
	}
	var count int
	if err := pool.QueryRowContext(ctx, "SELECT count(*) FROM quota_reservations").Scan(&count); err != nil || count != 1 || successes != 1 || adapterCalls.Load() != 1 {
		t.Fatalf("reservations=%d successful starts=%d calls=%d: %v", count, successes, adapterCalls.Load(), err)
	}
	quotaBalance(t, pool, account, 0)
}

func TestQuotaReserveRepeatAndScope(t *testing.T) {
	pool := chatDatabase(t)
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	account, other := chatID(1), chatID(2)
	seedQuotaPackage(t, pool)
	quotaExec(t, pool, "INSERT INTO quota_allocations(id,account_id,kind,package_id,request_count) VALUES($1,$2,'purchased',$3,2)", chatID(51), account, chatID(50))
	seedQuotaRequest(t, pool, 60, false)
	seedQuotaRequest(t, pool, 61, true)
	seedQuotaRequest(t, pool, 62, false)
	quotaExec(t, pool, `UPDATE ai_requests SET state='completed',result='{"status":"completed"}' WHERE id=$1`, chatID(62))
	id := chatID(60)
	generation := service.NewGeneration(repository.NewQuota(pool))
	entered := make(chan struct{})
	finish := make(chan struct{})
	done := make(chan error, 1)
	var calls atomic.Int32
	go func() {
		result, err := generation.Start(ctx, account, id, 5*time.Second, func(adapterCtx context.Context) (*string, error) {
			calls.Add(1)
			close(entered)
			select {
			case <-finish:
				return nil, nil
			case <-adapterCtx.Done():
				return nil, adapterCtx.Err()
			}
		})
		if err == nil && (result.Answer != nil || result.Deadline.IsZero()) {
			err = errors.New("generation did not preserve no_answer result")
		}
		done <- err
	}()
	select {
	case <-entered:
	case err := <-done:
		t.Fatalf("first start finished without invoking adapter: %v", err)
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	generate := func(context.Context) (*string, error) {
		calls.Add(1)
		return nil, nil
	}
	for _, tc := range []struct {
		account, id string
		want        error
	}{
		{account, id, service.ErrQuotaAlreadyReserved},
		{account, chatID(61), service.ErrQuotaRequestNotFound},
		{other, id, service.ErrQuotaRequestNotFound},
		{other, chatID(61), service.ErrQuotaExhausted},
		{account, chatID(62), service.ErrQuotaRequestNotPending},
	} {
		out, err := generation.Start(ctx, tc.account, tc.id, time.Second, generate)
		if !errors.Is(err, tc.want) || !out.Deadline.IsZero() {
			t.Fatalf("start returned %+v, %v, want %v", out, err, tc.want)
		}
	}
	close(finish)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	quotaExec(t, pool, "UPDATE quota_reservations SET state='released',finalized_at=now() WHERE ai_request_id=$1", id)
	quotaBalance(t, pool, account, 2)
	if out, err := generation.Start(ctx, account, id, time.Second, generate); !errors.Is(err, service.ErrQuotaAlreadyReserved) || !out.Deadline.IsZero() || calls.Load() != 1 {
		t.Fatalf("released reservation allowed replay: %+v, %v, calls=%d", out, err, calls.Load())
	}
	var count int
	if err := pool.QueryRowContext(ctx, "SELECT count(*) FROM quota_reservations").Scan(&count); err != nil || count != 1 {
		t.Fatalf("unexpected reservations=%d: %v", count, err)
	}
	seedQuotaRequest(t, pool, 63, false)
	adapterErr := errors.New("adapter failure")
	result, err := generation.Start(ctx, account, chatID(63), time.Second, func(context.Context) (*string, error) {
		return nil, adapterErr
	})
	var savedDeadline time.Time
	if scanErr := pool.QueryRowContext(ctx, "SELECT deadline_at FROM quota_reservations WHERE ai_request_id=$1", chatID(63)).Scan(&savedDeadline); scanErr != nil {
		t.Fatal(db.WrapError("read reservation after adapter failure", scanErr))
	}
	if !errors.Is(err, adapterErr) || result.Answer != nil || !result.Deadline.Equal(savedDeadline) {
		t.Fatalf("generation did not preserve adapter failure and committed deadline: %+v, %v", result, err)
	}
}

func TestQuotaReserveAllocationOrder(t *testing.T) {
	pool := chatDatabase(t)
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	account, other := chatID(1), chatID(2)
	seedQuotaPackage(t, pool)
	quotaExec(t, pool, `INSERT INTO trial_bootstrap_intents(account_id,created_at)
		VALUES($1,now()-interval '1 day'),($2,now()-interval '15 days')`, account, other)
	quotaExec(t, pool, "SELECT grant_trial_quota($1,$2,1,14)", account, chatID(54))
	quotaExec(t, pool, "SELECT grant_trial_quota($1,$2,1,14)", other, chatID(55))
	quotaExec(t, pool, `INSERT INTO quota_allocations(id,account_id,kind,package_id,request_count,granted_at)
		VALUES($1,$4,'purchased',$5,1,now()-interval '3 days'),
		      ($2,$4,'purchased',$5,1,now()-interval '3 days'),
		      ($3,$4,'purchased',$5,1,now()-interval '2 days')`, chatID(53), chatID(52), chatID(51), account, chatID(50))
	for i, allocation := range []int{54, 52, 53, 51} {
		id := 60 + i
		seedQuotaRequest(t, pool, id, false)
		if _, err := repository.NewQuota(pool).Reserve(ctx, account, chatID(id), time.Minute); err != nil {
			t.Fatal(err)
		}
		var got string
		if err := pool.QueryRowContext(ctx, "SELECT allocation_id FROM quota_reservations WHERE ai_request_id=$1", chatID(id)).Scan(&got); err != nil || got != chatID(allocation) {
			t.Fatalf("reservation %d allocation=%s want=%d: %v", id, got, allocation, err)
		}
	}
	quotaBalance(t, pool, account, 0)
	seedQuotaRequest(t, pool, 64, true)
	calls := 0
	generation := service.NewGeneration(repository.NewQuota(pool))
	_, err := generation.Start(ctx, other, chatID(64), time.Minute, func(context.Context) (*string, error) {
		calls++
		return nil, nil
	})
	if !errors.Is(err, service.ErrQuotaExhausted) || calls != 0 {
		t.Fatalf("expired trial allowed generation: %v, calls=%d", err, calls)
	}
}

func TestQuotaReserveUsesPostLockClock(t *testing.T) {
	pool := chatDatabase(t)
	ctx, cancel := context.WithTimeout(t.Context(), 8*time.Second)
	defer cancel()
	account, id := chatID(1), chatID(60)
	seedQuotaPackage(t, pool)
	quotaExec(t, pool, "INSERT INTO quota_allocations(id,account_id,kind,package_id,request_count) VALUES($1,$2,'purchased',$3,1)", chatID(51), account, chatID(50))
	seedQuotaRequest(t, pool, 60, false)
	first, err := pool.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(db.WrapError("begin quota blocker", err))
	}
	defer func() {
		if err := first.Rollback(); err != nil && !errors.Is(err, sql.ErrTxDone) {
			t.Error(db.WrapError("close quota blocker", err))
		}
	}()
	if _, err := first.ExecContext(ctx, "SELECT pg_advisory_xact_lock(hashtextextended('quota:' || $1::uuid::text,0))", account); err != nil {
		t.Fatal(db.WrapError("block company quota", err))
	}
	var pid int
	if err := first.QueryRowContext(ctx, "SELECT pg_backend_pid()").Scan(&pid); err != nil {
		t.Fatal(db.WrapError("read quota blocker pid", err))
	}
	done := make(chan error, 1)
	go func() {
		_, err := repository.NewQuota(pool).Reserve(ctx, account, id, time.Minute)
		done <- err
	}()
	waitQuotaBlocking(t, ctx, pool, pid, done)
	var lockRelease time.Time
	if err := first.QueryRowContext(ctx, "SELECT clock_timestamp()").Scan(&lockRelease); err != nil {
		t.Fatal(db.WrapError("read clock before releasing quota lock", err))
	}
	if err := first.Commit(); err != nil {
		t.Fatal(db.WrapError("release quota blocker", err))
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	var reservedAt, deadline time.Time
	if err := pool.QueryRowContext(ctx, "SELECT reserved_at,deadline_at FROM quota_reservations WHERE ai_request_id=$1", id).Scan(&reservedAt, &deadline); err != nil {
		t.Fatal(db.WrapError("read post-lock reservation", err))
	}
	if reservedAt.Before(lockRelease) || deadline.Sub(reservedAt) != time.Minute {
		t.Fatalf("reserved_at=%s before release=%s, or deadline=%s has wrong duration", reservedAt, lockRelease, deadline)
	}
}

func TestQuotaReserveReadsClockAfterScope(t *testing.T) {
	pool := chatDatabase(t)
	ctx, cancel := context.WithTimeout(t.Context(), 8*time.Second)
	defer cancel()
	account, id := chatID(1), chatID(60)
	seedQuotaRequest(t, pool, 60, false)
	first, err := pool.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(db.WrapError("begin scope blocker", err))
	}
	defer func() {
		if err := first.Rollback(); err != nil && !errors.Is(err, sql.ErrTxDone) {
			t.Error(db.WrapError("close scope blocker", err))
		}
	}()
	if _, err := first.ExecContext(ctx, "LOCK TABLE ai_requests IN ACCESS EXCLUSIVE MODE"); err != nil {
		t.Fatal(db.WrapError("block quota scope check", err))
	}
	var pid int
	if err := first.QueryRowContext(ctx, "SELECT pg_backend_pid()").Scan(&pid); err != nil {
		t.Fatal(db.WrapError("read scope blocker pid", err))
	}
	done := make(chan error, 1)
	go func() {
		_, err := repository.NewQuota(pool).Reserve(ctx, account, id, time.Minute)
		done <- err
	}()
	waitQuotaBlocking(t, ctx, pool, pid, done)
	quotaExec(t, pool, `INSERT INTO trial_bootstrap_intents(account_id,created_at)
		VALUES($1,clock_timestamp()-interval '336 hours'+interval '10 milliseconds')`, account)
	quotaExec(t, pool, "SELECT grant_trial_quota($1,$2,1,14)", account, chatID(51))
	quotaExec(t, pool, "SELECT pg_sleep(0.02)")
	if err := first.Commit(); err != nil {
		t.Fatal(db.WrapError("release scope blocker", err))
	}
	if err := <-done; !errors.Is(err, service.ErrQuotaExhausted) {
		t.Fatalf("reservation accepted trial that expired during scope wait: %v", err)
	}
	var count int
	if err := pool.QueryRowContext(ctx, "SELECT count(*) FROM quota_reservations").Scan(&count); err != nil || count != 0 {
		t.Fatalf("unexpected reservations=%d: %v", count, err)
	}
}

func waitQuotaBlocking(t *testing.T, ctx context.Context, pool *sql.DB, pid int, done <-chan error) {
	t.Helper()
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	var blocked bool
	for !blocked {
		if err := pool.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM pg_stat_activity
			WHERE datname=current_database() AND $1::int=ANY(pg_blocking_pids(pid)))`, pid).Scan(&blocked); err != nil {
			t.Fatal(db.WrapError("observe quota lock wait", err))
		}
		if blocked {
			break
		}
		select {
		case err := <-done:
			t.Fatalf("reservation finished before the lock was released: %v", err)
		case <-ticker.C:
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		}
	}
}
