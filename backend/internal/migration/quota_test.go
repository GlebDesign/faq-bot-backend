package migration

import (
	"context"
	"database/sql"
	"errors"
	"net/url"
	"path/filepath"
	"testing"
	"time"

	"faqbot/backend/internal/db"
	"faqbot/backend/internal/models"
	"faqbot/backend/internal/repository"

	"github.com/golang-migrate/migrate/v4"
	"github.com/golang-migrate/migrate/v4/database/postgres"
)

func legacyBootstrap(ctx context.Context, pool *sql.DB, subject, userID, accountID string) (models.Identity, error) {
	var identity models.Identity
	err := db.WithTx(ctx, pool, func(ctx context.Context, tx *sql.Tx) error {
		var inserted string
		err := tx.QueryRowContext(ctx, `INSERT INTO users(id,auth_subject) VALUES($1,$2)
			ON CONFLICT(auth_subject) DO NOTHING RETURNING id`, userID, subject).Scan(&inserted)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return db.WrapError("create version 7 identity", err)
		}
		if err == nil {
			if _, err := tx.ExecContext(ctx, "INSERT INTO accounts(id) VALUES($1)", accountID); err != nil {
				return db.WrapError("create version 7 account", err)
			}
			if _, err := tx.ExecContext(ctx, "INSERT INTO account_members(user_id,account_id,role) VALUES($1,$2,'owner')", userID, accountID); err != nil {
				return db.WrapError("create version 7 membership", err)
			}
			if _, err := tx.ExecContext(ctx, "INSERT INTO trial_bootstrap_intents(account_id) VALUES($1)", accountID); err != nil {
				return db.WrapError("create version 7 trial intent", err)
			}
		}
		return db.WrapError("read version 7 identity", tx.QueryRowContext(ctx,
			`SELECT u.id,m.account_id,m.role,m.audit_read FROM users u JOIN account_members m ON m.user_id=u.id
			WHERE u.auth_subject=$1 ORDER BY m.account_id LIMIT 1`, subject).
			Scan(&identity.UserID, &identity.AccountID, &identity.Role, &identity.AuditRead))
	})
	return identity, err
}

func assertQuotaSQLState(t *testing.T, pool *sql.DB, state, query string, args ...any) {
	t.Helper()
	_, err := pool.Exec(query, args...)
	sqlState(t, err, state)
}

func quotaExec(t *testing.T, pool *sql.DB, query string, args ...any) {
	t.Helper()
	if _, err := pool.Exec(query, args...); err != nil {
		t.Fatal(db.WrapError("prepare quota test data", err))
	}
}

func quotaBalance(t *testing.T, pool *sql.DB, accountID string, want int64) {
	t.Helper()
	got, err := repository.NewQuota(pool).Available(context.Background(), accountID)
	if err != nil || got != want {
		t.Fatalf("available=%d want=%d: %v", got, want, err)
	}
}

func TestQuotaMigrationPreservesVersion7(t *testing.T) {
	dsn, pool := database(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	dir := t.TempDir()
	files, err := filepath.Glob("../../migrations/00000[1-7]_*.sql")
	if err != nil || len(files) != 14 {
		t.Fatalf("version 7 files: %d: %v", len(files), err)
	}
	for _, path := range files {
		copyMigrations(t, dir, filepath.Base(path))
	}
	if err := Apply(ctx, dsn, dir); err != nil {
		t.Fatal(err)
	}
	schemaVersion(t, pool, 7)
	old, err := legacyBootstrap(ctx, pool, "version-7-subject", chatID(1), chatID(2))
	if err != nil {
		t.Fatal(err)
	}
	quotaExec(t, pool, "UPDATE trial_bootstrap_intents SET created_at=now()-interval '360 hours'")
	quotaExec(t, pool, "INSERT INTO users(id,auth_subject) VALUES($1,'no-intent')", chatID(3))
	quotaExec(t, pool, "INSERT INTO accounts(id) VALUES($1)", chatID(4))
	quotaExec(t, pool, "INSERT INTO account_members(user_id,account_id,role) VALUES($1,$2,'owner')", chatID(3), chatID(4))
	quotaExec(t, pool, "INSERT INTO agents(id,account_id,public_id,name) VALUES($1,$2,'old-agent','Помощник')", chatID(5), old.AccountID)
	quotaExec(t, pool, "INSERT INTO conversations(id,account_id,agent_id,visitor_id) VALUES($1,$2,$3,'visitor')", chatID(6), old.AccountID, chatID(5))
	quotaExec(t, pool, "INSERT INTO messages(id,account_id,agent_id,conversation_id,question) VALUES($1,$2,$3,$4,'Вопрос')", chatID(7), old.AccountID, chatID(5), chatID(6))
	quotaExec(t, pool, `INSERT INTO ai_requests(id,account_id,agent_id,visitor_id,operation,idempotency_key,conversation_id,message_id,payload_hash)
		VALUES($1,$2,$3,'visitor','widget_message','old',$4,$5,sha256('old'))`, chatID(8), old.AccountID, chatID(5), chatID(6), chatID(7))
	snapshot := func() string {
		t.Helper()
		var value string
		err := pool.QueryRowContext(ctx, `SELECT json_build_array(row_to_json(u),row_to_json(a),row_to_json(i),row_to_json(m),row_to_json(r))::text
			FROM users u JOIN account_members m ON m.user_id=u.id JOIN accounts a ON a.id=m.account_id
			JOIN trial_bootstrap_intents i ON i.account_id=a.id JOIN ai_requests r ON r.account_id=a.id
			WHERE a.id=$1`, old.AccountID).Scan(&value)
		if err != nil {
			t.Fatal(db.WrapError("read version 7 data", err))
		}
		return value
	}
	before := snapshot()
	copyMigrations(t, dir, "000008_quota_foundation.up.sql", "000008_quota_foundation.down.sql")
	for range 2 {
		if err := Apply(ctx, dsn, dir); err != nil {
			t.Fatal(err)
		}
	}
	schemaVersion(t, pool, 8)
	var allocations int
	if err := pool.QueryRowContext(ctx, "SELECT count(*) FROM quota_allocations WHERE kind='trial'").Scan(&allocations); err != nil || allocations != 0 {
		t.Fatalf("trial allocations after upgrade=%d want=0: %v", allocations, err)
	}
	auth := repository.NewAuth(pool)
	got, err := auth.Bootstrap(ctx, "version-7-subject")
	if err != nil || got != old {
		t.Fatal("version 7 identity changed", err)
	}
	var allocationOK bool
	err = pool.QueryRowContext(ctx, `SELECT a.granted_at=i.created_at AND a.expires_at=i.created_at+interval '336 hours'
		FROM quota_allocations a JOIN trial_bootstrap_intents i ON i.account_id=a.trial_account_id
		WHERE a.account_id=$1`, old.AccountID).Scan(&allocationOK)
	if err != nil || !allocationOK {
		t.Fatal("version 7 intent did not determine trial dates", err)
	}
	quotaBalance(t, pool, old.AccountID, 0)
	noIntent, err := auth.Bootstrap(ctx, "no-intent")
	if err != nil || noIntent.AccountID != chatID(4) {
		t.Fatal("legacy account without intent changed", err)
	}
	if err := pool.QueryRowContext(ctx, "SELECT count(*) FROM quota_allocations").Scan(&allocations); err != nil || allocations != 1 {
		t.Fatalf("allocations=%d want=1: %v", allocations, err)
	}
	if before != snapshot() {
		t.Fatal("upgrade changed version 7 data")
	}

	conn, err := pool.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	driver, err := postgres.WithConnection(ctx, conn, &postgres.Config{StatementTimeout: 10 * time.Second})
	if err != nil {
		t.Fatal(db.WrapError("quota migration driver", errors.Join(err, conn.Close())))
	}
	runner, err := migrate.NewWithDatabaseInstance((&url.URL{Scheme: "file", Path: dir}).String(), "postgres", driver)
	if err != nil {
		t.Fatal(db.WrapError("quota migration runner", errors.Join(err, driver.Close())))
	}
	defer func() {
		a, b := runner.Close()
		if err := errors.Join(a, b); err != nil {
			t.Error(err)
		}
	}()
	if err := runner.Steps(-1); err != nil {
		t.Fatal(db.WrapError("down version 8", err))
	}
	schemaVersion(t, pool, 7)
	if err := runner.Up(); err != nil {
		t.Fatal(db.WrapError("up version 8", err))
	}
	schemaVersion(t, pool, 8)
	if before != snapshot() {
		t.Fatal("down/up changed version 7 data")
	}
}

func TestQuotaAvailabilityAndConstraints(t *testing.T) {
	pool := chatDatabase(t)
	ctx := context.Background()
	account, other := chatID(1), chatID(2)
	trial, small, large, foreign := chatID(51), chatID(52), chatID(53), chatID(54)
	quotaExec(t, pool, "INSERT INTO trial_bootstrap_intents(account_id,created_at) VALUES($1,now()-interval '15 days')", account)
	quotaExec(t, pool, "SELECT grant_trial_quota($1,$2,25,14)", account, trial)
	quotaExec(t, pool, "INSERT INTO quota_packages(id,name,request_count) VALUES($1,'Учебный',8)", chatID(50))
	for _, row := range []struct {
		id, account string
		count       int
	}{{small, account, 1}, {large, account, 8}, {foreign, other, 3}} {
		quotaExec(t, pool, `INSERT INTO quota_allocations(id,account_id,kind,package_id,request_count,granted_at)
			VALUES($1,$2,'purchased',$3,$4,now()-interval '3 days')`,
			row.id, row.account, chatID(50), row.count)
	}
	quotaBalance(t, pool, account, 9)
	quotaBalance(t, pool, other, 3)
	createRequest := repository.NewAIRequests(pool)
	for id := 60; id <= 65; id++ {
		in := chatInput()
		in.ID = chatID(id)
		in.IdempotencyKey = in.ID
		if _, err := createRequest.CreateOrRead(ctx, in); err != nil {
			t.Fatal(err)
		}
	}
	reserve := `INSERT INTO quota_reservations(ai_request_id,account_id,allocation_id,reserved_at,deadline_at)
		VALUES($1,$2,$3,now()-$4::integer*interval '1 minute',now()+$5::integer*interval '1 minute')`
	assertQuotaSQLState(t, pool, "23503", reserve, chatID(60), account, foreign, 1, 1)
	assertQuotaSQLState(t, pool, "23503", reserve, chatID(60), other, foreign, 1, 1)
	for _, row := range []struct {
		id         int
		allocation string
		age, ttl   int
	}{{60, small, 1, 1}, {61, large, 1, 1}, {62, large, 1, 1}, {63, trial, 2880, 1}, {64, large, 1, 0}, {65, small, 1, 1}} {
		quotaExec(t, pool, reserve, chatID(row.id), account, row.allocation, row.age, row.ttl)
	}
	assertQuotaSQLState(t, pool, "23514",
		"UPDATE quota_reservations SET state='consumed',finalized_at=deadline_at WHERE ai_request_id=$1", chatID(64))
	quotaBalance(t, pool, account, 5)
	spend := "INSERT INTO quota_usage_events(operation_id,ai_request_id,account_id) VALUES($1,$2,$3)"
	assertQuotaSQLState(t, pool, "23503", spend, chatID(70), chatID(60), account)
	quotaExec(t, pool, "UPDATE quota_reservations SET state='consumed',finalized_at=now() WHERE ai_request_id IN ($1,$2)", chatID(60), chatID(61))
	assertQuotaSQLState(t, pool, "23505", reserve, chatID(60), account, large, 1, 1)
	assertQuotaSQLState(t, pool, "23503", spend, chatID(70), chatID(60), other)
	quotaExec(t, pool, spend, chatID(70), chatID(60), account)
	assertQuotaSQLState(t, pool, "23505", spend, chatID(70), chatID(61), account)
	assertQuotaSQLState(t, pool, "23505", spend, chatID(71), chatID(60), account)
	quotaExec(t, pool, "UPDATE quota_reservations SET state='released',finalized_at=now() WHERE ai_request_id IN ($1,$2,$3)",
		chatID(62), chatID(63), chatID(64))
	assertQuotaSQLState(t, pool, "23505", reserve, chatID(62), account, large, 1, 1)
	quotaBalance(t, pool, account, 7)
	quotaBalance(t, pool, other, 3)
}
