package migration

import (
	"context"
	"database/sql"
	"errors"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"faqbot/backend/internal/db"
	"faqbot/backend/internal/models"
	"faqbot/backend/internal/repository"
	"faqbot/backend/internal/service"

	"github.com/golang-migrate/migrate/v4"
	"github.com/golang-migrate/migrate/v4/database/postgres"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

const auditAccount = "33333333-3333-4333-8333-333333333333"

func auditEvent() (models.AuditActor, models.AuditEvent) {
	return models.AuditActor{Type: "user", ID: "11111111-1111-4111-8111-111111111111"}, models.AuditEvent{
		OperationID: "22222222-2222-4222-8222-222222222222", Scope: "tenant", AccountID: auditAccount,
		Action: "document.delete_requested", EntityType: "document", EntityID: "44444444-4444-4444-8444-444444444444", Outcome: "pending",
		OccurredAt: time.Now(), RequestID: "request-1", SourceService: "go-api", Metadata: map[string]any{"new_status": "deleting"},
	}
}

func auditDatabase(t *testing.T) *sql.DB {
	t.Helper()
	dsn, pool := database(t)
	if err := Apply(context.Background(), dsn, "../../migrations"); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec("INSERT INTO accounts(id) VALUES ($1)", auditAccount); err != nil {
		t.Fatal(err)
	}
	return pool
}

func writeAudit(pool *sql.DB, actor models.AuditActor, event models.AuditEvent) error {
	return db.WithTx(context.Background(), pool, func(ctx context.Context, tx *sql.Tx) error {
		return service.NewAudit(repository.NewAudit()).WriteInTx(ctx, tx, actor, event)
	})
}

func TestAuditMigrationUpgradeRepeatAndDown(t *testing.T) {
	dsn, pool := database(t)
	dir := t.TempDir()
	for _, name := range []string{"000001_identity.up.sql", "000001_identity.down.sql"} {
		data, err := os.ReadFile(filepath.Join("../../migrations", name))
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, name), data, 0600); err != nil {
			t.Fatal(err)
		}
	}
	if err := Apply(context.Background(), dsn, dir); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec("INSERT INTO accounts(id) VALUES ($1)", auditAccount); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if err := Apply(context.Background(), dsn, "../../migrations"); err != nil {
			t.Fatal(err)
		}
	}
	var version, count int
	var dirty bool
	if err := pool.QueryRow("SELECT version,dirty FROM schema_migrations").Scan(&version, &dirty); err != nil || version != 2 || dirty {
		t.Fatalf("migration version=%d dirty=%v err=%v", version, dirty, err)
	}
	if err := pool.QueryRow("SELECT count(*) FROM accounts").Scan(&count); err != nil || count != 1 {
		t.Fatal("upgrade changed existing data")
	}
	if err := pool.QueryRow(`SELECT count(*) FROM pg_indexes WHERE schemaname=current_schema() AND tablename='audit_events'`).Scan(&count); err != nil || count != 6 {
		t.Fatalf("audit indexes=%d err=%v", count, err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	conn, err := pool.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	driver, err := postgres.WithConnection(ctx, conn, &postgres.Config{StatementTimeout: 10 * time.Second})
	if err != nil {
		t.Fatal(db.WrapError("initialize audit migration driver", errors.Join(err, conn.Close())))
	}
	path, err := filepath.Abs("../../migrations")
	if err != nil {
		t.Fatal(db.WrapError("resolve audit migration path", errors.Join(err, driver.Close())))
	}
	runner, err := migrate.NewWithDatabaseInstance((&url.URL{Scheme: "file", Path: path}).String(), "postgres", driver)
	if err != nil {
		t.Fatal(db.WrapError("initialize audit migration runner", errors.Join(err, driver.Close())))
	}
	defer func() {
		sourceErr, driverErr := runner.Close()
		if err := errors.Join(sourceErr, driverErr); err != nil {
			t.Error(db.WrapError("close audit migration runner", err))
		}
	}()
	if err := runner.Steps(-1); err != nil {
		t.Fatal(db.WrapError("audit migration down", err))
	}
	if err := pool.QueryRowContext(ctx, "SELECT version,dirty FROM schema_migrations").Scan(&version, &dirty); err != nil || version != 1 || dirty {
		t.Fatalf("down version=%d dirty=%v err=%v", version, dirty, err)
	}

	var removed bool
	if err := pool.QueryRow("SELECT to_regclass('audit_events') IS NULL").Scan(&removed); err != nil || !removed {
		t.Fatalf("audit table remains after down: %v", err)
	}
	if err := pool.QueryRow(`SELECT count(*) FROM pg_indexes WHERE schemaname=current_schema() AND tablename='audit_events'`).Scan(&count); err != nil || count != 0 {
		t.Fatalf("audit indexes remain after down: count=%d err=%v", count, err)
	}
	if err := pool.QueryRow("SELECT count(*) FROM accounts WHERE id=$1", auditAccount).Scan(&count); err != nil || count != 1 {
		t.Fatal("audit down changed identity data")
	}
	if err := runner.Up(); err != nil {
		t.Fatal(db.WrapError("audit migration up after down", err))
	}
	if err := pool.QueryRowContext(ctx, "SELECT version,dirty FROM schema_migrations").Scan(&version, &dirty); err != nil || version != 2 || dirty {
		t.Fatalf("restored version=%d dirty=%v err=%v", version, dirty, err)
	}
	if err := pool.QueryRowContext(ctx, "SELECT count(*) FROM audit_events").Scan(&count); err != nil || count != 0 {
		t.Fatalf("restored audit count=%d err=%v", count, err)
	}
	if err := pool.QueryRowContext(ctx, "SELECT count(*) FROM accounts WHERE id=$1", auditAccount).Scan(&count); err != nil || count != 1 {
		t.Fatal("restored migration changed identity data")
	}
}

func TestAuditSQLDuplicateStagesAndScope(t *testing.T) {
	pool := auditDatabase(t)
	actor, event := auditEvent()
	originalEvent := event
	if err := writeAudit(pool, actor, event); err != nil {
		t.Fatal(err)
	}
	var firstID string
	var occurred, recorded time.Time
	if err := pool.QueryRow("SELECT id,occurred_at,recorded_at FROM audit_events").Scan(&firstID, &occurred, &recorded); err != nil {
		t.Fatal(err)
	}
	event.OccurredAt = event.OccurredAt.Add(time.Minute)
	if err := writeAudit(pool, actor, event); err != nil {
		t.Fatal(err)
	}
	var id string
	var repeatedOccurred, repeatedRecorded time.Time
	if err := pool.QueryRow("SELECT id,occurred_at,recorded_at FROM audit_events").Scan(&id, &repeatedOccurred, &repeatedRecorded); err != nil {
		t.Fatal(err)
	}
	if id != firstID || !occurred.Equal(repeatedOccurred) || !recorded.Equal(repeatedRecorded) {
		t.Fatal("repeat changed stored event")
	}
	secondOperation := originalEvent
	secondOperation.OperationID = "66666666-6666-4666-8666-666666666666"
	if err := writeAudit(pool, actor, secondOperation); err != nil {
		t.Fatal(err)
	}
	var operations, keys int
	if err := pool.QueryRow("SELECT count(*),count(DISTINCT event_key) FROM audit_events").Scan(&operations, &keys); err != nil || operations != 2 || keys != 2 {
		t.Fatalf("independent operations rows=%d keys=%d err=%v", operations, keys, err)
	}
	event.Action, event.Outcome = "document.delete_completed", "success"
	if err := writeAudit(pool, actor, event); err != nil {
		t.Fatal(err)
	}
	other := "55555555-5555-4555-8555-555555555555"
	if _, err := pool.Exec("INSERT INTO accounts(id) VALUES ($1)", other); err != nil {
		t.Fatal(err)
	}
	event.AccountID = other
	if err := writeAudit(pool, actor, event); err != nil {
		t.Fatal(err)
	}
	var count int
	if err := pool.QueryRow("SELECT count(*) FROM audit_events WHERE request_id='request-1'").Scan(&count); err != nil || count != 4 {
		t.Fatalf("events=%d err=%v", count, err)
	}
}

func TestAuditSQLPayloadConflict(t *testing.T) {
	pool := auditDatabase(t)
	actor, event := auditEvent()
	if err := writeAudit(pool, actor, event); err != nil {
		t.Fatal(err)
	}
	var original string
	if err := pool.QueryRow("SELECT row_to_json(audit_events)::text FROM audit_events").Scan(&original); err != nil {
		t.Fatal(err)
	}
	for name, change := range map[string]func(*models.AuditActor, *models.AuditEvent){
		"actor":    func(a *models.AuditActor, _ *models.AuditEvent) { a.ID = "55555555-5555-4555-8555-555555555555" },
		"entity":   func(_ *models.AuditActor, e *models.AuditEvent) { e.EntityID = "55555555-5555-4555-8555-555555555555" },
		"metadata": func(_ *models.AuditActor, e *models.AuditEvent) { e.Metadata = map[string]any{"new_status": "failed"} },
		"request":  func(_ *models.AuditActor, e *models.AuditEvent) { e.RequestID = "request-2" },
		"trace":    func(_ *models.AuditActor, e *models.AuditEvent) { e.TraceID = "trace-2" },
		"source":   func(_ *models.AuditActor, e *models.AuditEvent) { e.SourceService = "ai-worker" },
	} {
		t.Run(name, func(t *testing.T) {
			a, e := auditEvent()
			change(&a, &e)
			if err := writeAudit(pool, a, e); !errors.Is(err, service.ErrAuditConflict) {
				t.Fatalf("conflict hidden: %v", err)
			}
		})
	}
	businessID := "77777777-7777-4777-8777-777777777777"
	event.Metadata = map[string]any{"new_status": "failed"}
	err := db.WithTx(context.Background(), pool, func(ctx context.Context, tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, "INSERT INTO accounts(id) VALUES ($1)", businessID); err != nil {
			return db.WrapError("create conflict test account", err)
		}
		return service.NewAudit(repository.NewAudit()).WriteInTx(ctx, tx, actor, event)
	})
	if !errors.Is(err, service.ErrAuditConflict) {
		t.Fatalf("conflict cause lost: %v", err)
	}
	var count int
	if err := pool.QueryRow("SELECT count(*) FROM accounts WHERE id=$1", businessID).Scan(&count); err != nil || count != 0 {
		t.Fatal("mutation survived audit payload conflict")
	}
	var unchanged string
	if err := pool.QueryRow("SELECT row_to_json(audit_events)::text FROM audit_events").Scan(&unchanged); err != nil || unchanged != original {
		t.Fatal("conflict changed original audit")
	}
}

func TestAuditSQLRollback(t *testing.T) {
	pool := auditDatabase(t)
	if _, err := pool.Exec("ALTER TABLE audit_events ADD CONSTRAINT reject_audit CHECK(false) NOT VALID"); err != nil {
		t.Fatal(err)
	}
	actor, event := auditEvent()
	newAccount := "55555555-5555-4555-8555-555555555555"
	event.AccountID = newAccount
	err := db.WithTx(context.Background(), pool, func(ctx context.Context, tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, "INSERT INTO accounts(id) VALUES ($1)", newAccount); err != nil {
			return db.WrapError("create test account", err)
		}
		return service.NewAudit(repository.NewAudit()).WriteInTx(ctx, tx, actor, event)
	})
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) || pgErr.Code != "23514" || strings.Contains(err.Error(), newAccount) || strings.Contains(err.Error(), "INSERT") {
		t.Fatalf("unsafe error or lost SQL cause: %v", err)
	}
	var count int
	if err := pool.QueryRow("SELECT count(*) FROM accounts WHERE id=$1", newAccount).Scan(&count); err != nil || count != 0 {
		t.Fatal("mutation survived failed audit")
	}
	if _, err := pool.Exec("ALTER TABLE audit_events DROP CONSTRAINT reject_audit"); err != nil {
		t.Fatal(err)
	}
	cause := errors.New("business failure")
	err = db.WithTx(context.Background(), pool, func(ctx context.Context, tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, "INSERT INTO accounts(id) VALUES ($1)", newAccount); err != nil {
			return db.WrapError("create test account", err)
		}
		if err := service.NewAudit(repository.NewAudit()).WriteInTx(ctx, tx, actor, event); err != nil {
			return err
		}
		return cause
	})
	if !errors.Is(err, cause) {
		t.Fatal("business error cause lost")
	}
	if err := pool.QueryRow("SELECT count(*) FROM accounts WHERE id=$1", newAccount).Scan(&count); err != nil || count != 0 {
		t.Fatal("mutation survived business failure")
	}
	if err := pool.QueryRow("SELECT count(*) FROM audit_events").Scan(&count); err != nil || count != 0 {
		t.Fatal("audit survived transaction rollback")
	}
}

func TestAuditSQLPlatformAndAnonymous(t *testing.T) {
	pool := auditDatabase(t)
	actor, event := auditEvent()
	actor.Type, actor.ID = "service", "infra"
	event.Scope, event.AccountID, event.Action, event.EntityType, event.EntityID, event.Outcome = "platform", "", "platform.config_changed", "platform", "", "success"
	if err := writeAudit(pool, actor, event); err != nil {
		t.Fatal(err)
	}
	actor.Type, actor.ID = "unknown", ""
	event.Scope, event.Action, event.EntityType, event.Outcome = "unattributed", "auth.access_denied", "auth", "denied"
	if err := writeAudit(pool, actor, event); err != nil {
		t.Fatal(err)
	}
	var count int
	if err := pool.QueryRow("SELECT count(*) FROM audit_events WHERE account_id IS NULL AND actor_id IS NULL").Scan(&count); err != nil || count != 1 {
		t.Fatal("anonymous scope was not preserved")
	}
}

func TestAuditSQLConstraints(t *testing.T) {
	pool := auditDatabase(t)
	actor, event := auditEvent()
	if err := writeAudit(pool, actor, event); err != nil {
		t.Fatal(err)
	}
	columns := []string{"id", "event_key", "schema_version", "scope", "account_id", "actor_type", "actor_id", "action", "entity_type", "entity_id", "outcome", "occurred_at", "recorded_at", "request_id", "trace_id", "source_service", "metadata"}
	for _, tc := range []struct {
		name                     string
		values                   map[string]string
		code, constraint, column string
	}{
		{"duplicate key", map[string]string{"event_key": "event_key"}, "23505", "audit_events_event_key_key", ""},
		{"metadata array", map[string]string{"metadata": "'[]'::jsonb"}, "23514", "audit_events_metadata_check", ""},
		{"metadata null", map[string]string{"metadata": "'null'::jsonb"}, "23514", "audit_events_metadata_check", ""},
		{"metadata oversized", map[string]string{"metadata": "jsonb_build_object('x',repeat('x',4097))"}, "23514", "audit_events_metadata_check", ""},
		{"blank action", map[string]string{"action": "''"}, "23514", "audit_events_action_check", ""},
		{"missing actor", map[string]string{"actor_type": "NULL"}, "23502", "", "actor_type"},
		{"missing source", map[string]string{"source_service": "NULL"}, "23502", "", "source_service"},
		{"missing time", map[string]string{"occurred_at": "NULL"}, "23502", "", "occurred_at"},
		{"missing account", map[string]string{"account_id": "NULL"}, "23514", "audit_events_check", ""},
		{"absent account", map[string]string{"account_id": "'99999999-9999-4999-8999-999999999999'"}, "23503", "audit_events_account_id_fkey", ""},
		{"service missing id", map[string]string{"actor_type": "'service'", "actor_id": "NULL"}, "23514", "audit_events_check4", ""},
		{"unknown mutation", map[string]string{"actor_type": "'unknown'", "actor_id": "NULL"}, "23514", "audit_events_check5", ""},
		{"bad correlation", map[string]string{"request_id": "E'bad\nvalue'"}, "23514", "audit_events_request_id_check", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {

			values := map[string]string{"id": "'66666666-6666-4666-8666-666666666666'", "event_key": "repeat('a',64)"}
			for column, expression := range tc.values {
				values[column] = expression
			}
			expressions := make([]string, 0, len(columns))
			for _, column := range columns {
				expression, ok := values[column]
				if !ok {
					expression = column
				}
				expressions = append(expressions, expression)
			}
			_, err := pool.Exec("INSERT INTO audit_events (" + strings.Join(columns, ",") + ") SELECT " + strings.Join(expressions, ",") + " FROM audit_events LIMIT 1")
			var pgErr *pgconn.PgError
			if !errors.As(err, &pgErr) {
				t.Fatalf("missing SQL cause: %v", db.WrapError("constraint test", err))
			}
			if pgErr.Code != tc.code || (tc.constraint != "" && pgErr.ConstraintName != tc.constraint) || pgErr.ColumnName != tc.column {
				t.Fatalf("SQLSTATE=%s constraint=%s column=%s, want %s/%s/%s", pgErr.Code, pgErr.ConstraintName, pgErr.ColumnName, tc.code, tc.constraint, tc.column)
			}
		})
	}
}

func TestAuditSQLConcurrentDuplicate(t *testing.T) {
	for _, tc := range []struct {
		name                                  string
		conflict, rollbackFirst, cancelSecond bool
	}{
		{name: "identical"},
		{name: "conflicting", conflict: true},
		{name: "first rollback", rollbackFirst: true},
		{name: "second canceled", cancelSecond: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			pool := auditDatabase(t)
			actor, event := auditEvent()
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			first, err := pool.BeginTx(ctx, nil)
			if err != nil {
				t.Fatal(err)
			}
			defer func() {
				if err := first.Rollback(); err != nil && !errors.Is(err, sql.ErrTxDone) {
					t.Error(db.WrapError("rollback concurrent test", err))
				}
			}()
			if err := service.NewAudit(repository.NewAudit()).WriteInTx(ctx, first, actor, event); err != nil {
				t.Fatal(err)
			}
			var firstPID int
			var original string
			if err := first.QueryRowContext(ctx, "SELECT pg_backend_pid(),row_to_json(audit_events)::text FROM audit_events").Scan(&firstPID, &original); err != nil {
				t.Fatal(err)
			}
			secondEvent := event
			if tc.conflict || tc.rollbackFirst {
				secondEvent.Metadata = map[string]any{"new_status": "failed"}
			}
			secondCtx, cancelWriter := context.WithTimeout(ctx, 8*time.Second)
			defer cancelWriter()
			businessID := "77777777-7777-4777-8777-777777777777"
			pids := make(chan int, 1)
			results := make(chan error, 1)
			go func() {
				results <- db.WithTx(secondCtx, pool, func(ctx context.Context, tx *sql.Tx) error {
					if tc.cancelSecond {
						if _, err := tx.ExecContext(ctx, "INSERT INTO accounts(id) VALUES ($1)", businessID); err != nil {
							return db.WrapError("create canceled writer account", err)
						}
					}
					var pid int
					if err := tx.QueryRowContext(ctx, "SELECT pg_backend_pid()").Scan(&pid); err != nil {
						return db.WrapError("read concurrent test backend", err)
					}
					pids <- pid
					return service.NewAudit(repository.NewAudit()).WriteInTx(ctx, tx, actor, secondEvent)
				})
			}()
			received := false
			defer func() {
				cancel()
				if !received {
					<-results
				}
			}()
			var secondPID int
			select {
			case secondPID = <-pids:
			case err := <-results:
				received = true
				t.Fatalf("second transaction did not reach insert: %v", err)
			case <-ctx.Done():
				t.Fatal(ctx.Err())
			}
			ticker := time.NewTicker(10 * time.Millisecond)
			defer ticker.Stop()
			blocked := false
			for !blocked {
				if err := pool.QueryRowContext(ctx, "SELECT $1::int=ANY(pg_blocking_pids($2))", firstPID, secondPID).Scan(&blocked); err != nil {
					t.Fatal(db.WrapError("check concurrent insert blocking", err))
				}
				if !blocked {
					select {
					case <-ticker.C:
					case <-ctx.Done():
						t.Fatal("second insert did not block before timeout")
					}
				}
			}
			if tc.cancelSecond {
				cancelWriter()
				err = <-results
				received = true
				if !errors.Is(err, context.Canceled) {
					t.Fatalf("canceled insert cause lost: %v", err)
				}
			}
			if tc.rollbackFirst {
				err = first.Rollback()
			} else {
				err = first.Commit()
			}
			if err != nil {
				t.Fatal(db.WrapError("finish first concurrent test", err))
			}
			if !received {
				err = <-results
				received = true
			}
			if tc.conflict && !errors.Is(err, service.ErrAuditConflict) {
				t.Fatalf("concurrent payload conflict hidden: %v", err)
			}
			if !tc.conflict && !tc.cancelSecond && err != nil {
				t.Fatal(err)
			}
			var count int
			var unchanged string
			if err := pool.QueryRowContext(ctx, "SELECT count(*) FROM audit_events").Scan(&count); err != nil || count != 1 {
				t.Fatalf("concurrent duplicate events=%d err=%v", count, err)
			}
			if tc.rollbackFirst {
				if err := pool.QueryRowContext(ctx, "SELECT metadata->>'new_status' FROM audit_events").Scan(&unchanged); err != nil || unchanged != "failed" {
					t.Fatalf("waiting writer event missing: %v", err)
				}
			} else if err := pool.QueryRowContext(ctx, "SELECT row_to_json(audit_events)::text FROM audit_events").Scan(&unchanged); err != nil || unchanged != original {
				t.Fatal("concurrent repeat changed original audit")
			}
			if tc.cancelSecond {
				if err := pool.QueryRowContext(ctx, "SELECT count(*) FROM accounts WHERE id=$1", businessID).Scan(&count); err != nil || count != 0 {
					t.Fatalf("canceled business mutation survived: count=%d err=%v", count, err)
				}
				if err := writeAudit(pool, actor, event); err != nil {
					t.Fatal(err)
				}
				if pool.Stats().InUse != 0 {
					t.Fatal("canceled writer left connections in use")
				}
			}
		})
	}
}

func TestAuditSQLPreservesHistory(t *testing.T) {
	pool := auditDatabase(t)
	actor, event := auditEvent()
	if _, err := pool.Exec("INSERT INTO users(id,auth_subject) VALUES ($1,'audit-history-test')", actor.ID); err != nil {
		t.Fatal(err)
	}
	if err := writeAudit(pool, actor, event); err != nil {
		t.Fatal(err)
	}
	var original string
	if err := pool.QueryRow("SELECT row_to_json(audit_events)::text FROM audit_events").Scan(&original); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec("DELETE FROM users WHERE id=$1", actor.ID); err != nil {
		t.Fatal(db.WrapError("delete history test actor", err))
	}
	var count int
	if err := pool.QueryRow("SELECT count(*) FROM users WHERE id=$1", actor.ID).Scan(&count); err != nil || count != 0 {
		t.Fatal("test actor was not deleted")
	}
	var unchanged string
	if err := pool.QueryRow("SELECT row_to_json(audit_events)::text FROM audit_events").Scan(&unchanged); err != nil || unchanged != original {
		t.Fatal("deleting actor changed audit history")
	}
	_, err := pool.Exec("DELETE FROM accounts WHERE id=$1", auditAccount)
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) || pgErr.Code != "23503" {
		t.Fatalf("account deletion was not rejected by FK: %v", db.WrapError("delete history test account", err))
	}
	if err := pool.QueryRow("SELECT count(*) FROM accounts WHERE id=$1", auditAccount).Scan(&count); err != nil || count != 1 {
		t.Fatal("referenced account was deleted")
	}
	if err := pool.QueryRow("SELECT row_to_json(audit_events)::text FROM audit_events").Scan(&unchanged); err != nil || unchanged != original {
		t.Fatal("account deletion changed audit history")
	}
}

func TestAuditSQLRestrictedRole(t *testing.T) {
	role := os.Getenv("TEST_AUDIT_ROLE")
	if role == "" {
		t.Skip("set TEST_AUDIT_ROLE for audit INSERT/SELECT and denied UPDATE/DELETE/TRUNCATE checks")
	}
	pool := auditDatabase(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	var unsafe bool
	if err := pool.QueryRowContext(ctx, `SELECT rolcanlogin OR rolsuper OR rolcreatedb OR rolcreaterole OR rolreplication OR rolbypassrls FROM pg_roles WHERE rolname=$1`, role).Scan(&unsafe); err != nil {
		t.Fatal(db.WrapError("inspect audit test role", err))
	}
	if unsafe {
		t.Fatal("TEST_AUDIT_ROLE must be a restricted NOLOGIN role")
	}
	var schema string
	if err := pool.QueryRowContext(ctx, "SELECT current_schema()").Scan(&schema); err != nil {
		t.Fatal(err)
	}
	quotedRole := pgx.Identifier{role}.Sanitize()
	if _, err := pool.ExecContext(ctx, "GRANT USAGE ON SCHEMA "+pgx.Identifier{schema}.Sanitize()+" TO "+quotedRole); err != nil {
		t.Fatal(db.WrapError("grant audit test schema usage", err))
	}
	if _, err := pool.ExecContext(ctx, "GRANT SELECT, INSERT ON audit_events TO "+quotedRole); err != nil {
		t.Fatal(db.WrapError("grant audit test writer access", err))
	}
	actor, event := auditEvent()
	var original string
	for range 2 {
		err := db.WithTx(ctx, pool, func(ctx context.Context, tx *sql.Tx) error {
			if _, err := tx.ExecContext(ctx, "SET LOCAL ROLE "+quotedRole); err != nil {
				return db.WrapError("select audit test role", err)
			}
			var ddl, owner bool
			if err := tx.QueryRowContext(ctx, `SELECT has_schema_privilege(current_user,current_schema(),'CREATE'),EXISTS(SELECT 1 FROM pg_class WHERE oid='audit_events'::regclass AND pg_has_role(current_user,relowner,'USAGE'))`).Scan(&ddl, &owner); err != nil {
				return db.WrapError("inspect audit role ownership", err)
			}
			if ddl || owner {
				return errors.New("audit test role has schema CREATE or table ownership")
			}
			if err := service.NewAudit(repository.NewAudit()).WriteInTx(ctx, tx, actor, event); err != nil {
				return err
			}
			var count int
			var row string
			if err := tx.QueryRowContext(ctx, "SELECT count(*),min(row_to_json(audit_events)::text) FROM audit_events").Scan(&count, &row); err != nil {
				return db.WrapError("read restricted audit history", err)
			}
			if count != 1 {
				return errors.New("restricted writer did not preserve one event")
			}
			if original != "" && row != original {
				return errors.New("restricted repeat changed original event")
			}
			original = row
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	for _, statement := range []string{"UPDATE audit_events SET outcome='failure'", "DELETE FROM audit_events", "TRUNCATE audit_events"} {
		err := db.WithTx(ctx, pool, func(ctx context.Context, tx *sql.Tx) error {
			if _, err := tx.ExecContext(ctx, "SET LOCAL ROLE "+quotedRole); err != nil {
				return db.WrapError("select denied audit test role", err)
			}
			_, err := tx.ExecContext(ctx, statement)
			return db.WrapError("denied audit mutation", err)
		})
		var pgErr *pgconn.PgError
		if !errors.As(err, &pgErr) || pgErr.Code != "42501" {
			t.Fatalf("audit mutation was not denied with 42501: %v", err)
		}
		var unchanged string
		if err := pool.QueryRowContext(ctx, "SELECT row_to_json(audit_events)::text FROM audit_events").Scan(&unchanged); err != nil || unchanged != original {
			t.Fatalf("denied mutation changed audit history: %v", err)
		}
	}
}
