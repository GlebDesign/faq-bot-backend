package migration

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"errors"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
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

func registrationDatabase(t *testing.T) (string, *sql.DB, context.Context) {
	t.Helper()
	dsn, pool := database(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	t.Cleanup(cancel)
	if err := Apply(ctx, dsn, "../../migrations"); err != nil {
		t.Fatal(err)
	}
	return dsn, pool, ctx
}

func registrationCounts(t *testing.T, ctx context.Context, pool *sql.DB, users, accounts, members, intents int) {
	t.Helper()
	for table, want := range map[string]int{"users": users, "accounts": accounts, "account_members": members, "trial_bootstrap_intents": intents} {
		var n int
		if err := pool.QueryRowContext(ctx, "SELECT count(*) FROM "+table).Scan(&n); err != nil || n != want {
			t.Fatalf("%s count=%d want=%d err=%v", table, n, want, err)
		}
	}
}

func TestRegistrationRepeatConcurrentAndIsolation(t *testing.T) {
	_, pool, ctx := registrationDatabase(t)
	auth := repository.NewAuth(pool)
	data, err := os.ReadFile("testdata/registration-v1.json")
	if err != nil {
		t.Fatal(err)
	}
	var fixture struct {
		SchemaVersion int    `json:"schema_version"`
		Role          string `json:"role"`
		AuditRead     bool   `json:"audit_read"`
		TrialState    string `json:"trial_state"`
	}
	if err := json.Unmarshal(data, &fixture); err != nil {
		t.Fatal(err)
	}
	if fixture.SchemaVersion != 1 {
		t.Fatal("unsupported fixture")
	}
	const n = 8
	identities := make([]models.Identity, n)
	errs := make([]error, n)
	var wg sync.WaitGroup
	for i := range n {
		wg.Add(1)
		go func() { defer wg.Done(); identities[i], errs[i] = auth.Bootstrap(ctx, "verified-subject") }()
	}
	wg.Wait()
	for i := range n {
		if errs[i] != nil {
			t.Fatal(errs[i])
		}
		if identities[i] != identities[0] || identities[i].Role != fixture.Role || identities[i].AuditRead != fixture.AuditRead {
			t.Fatalf("unexpected identity: %+v", identities[i])
		}
	}
	var original, state string
	if err := pool.QueryRowContext(ctx, "SELECT row_to_json(trial_bootstrap_intents)::text,state FROM trial_bootstrap_intents").Scan(&original, &state); err != nil || state != fixture.TrialState {
		t.Fatalf("state=%s err=%v", state, err)
	}
	if _, err := auth.Bootstrap(ctx, "verified-subject"); err != nil {
		t.Fatal(err)
	}
	var repeated string
	if err := pool.QueryRowContext(ctx, "SELECT row_to_json(trial_bootstrap_intents)::text FROM trial_bootstrap_intents").Scan(&repeated); err != nil || repeated != original {
		t.Fatal("repeat changed intent", err)
	}
	other, err := auth.Bootstrap(ctx, "other-verified-subject")
	if err != nil || other.AccountID == identities[0].AccountID || other.UserID == identities[0].UserID {
		t.Fatal("identity isolation failed", err)
	}
	registrationCounts(t, ctx, pool, 2, 2, 2, 2)
	var audits int
	if err := pool.QueryRowContext(ctx, "SELECT count(*) FROM audit_events").Scan(&audits); err != nil || audits != 0 {
		t.Fatal("intent created allocation audit", err)
	}
}

func TestRegistrationRollback(t *testing.T) {
	for _, table := range []string{"account_members", "trial_bootstrap_intents"} {
		t.Run(table, func(t *testing.T) {
			_, pool, ctx := registrationDatabase(t)
			if _, err := pool.ExecContext(ctx, "ALTER TABLE "+table+" ADD CONSTRAINT reject_registration CHECK(false) NOT VALID"); err != nil {
				t.Fatal(err)
			}
			auth := repository.NewAuth(pool)
			_, err := auth.Bootstrap(ctx, "private-subject")
			var pgErr *pgconn.PgError
			if !errors.As(err, &pgErr) || pgErr.Code != "23514" || strings.Contains(err.Error(), "private-subject") || strings.Contains(err.Error(), "INSERT") {
				t.Fatalf("unsafe error or missing cause: %v", err)
			}
			registrationCounts(t, ctx, pool, 0, 0, 0, 0)
			if _, err := pool.ExecContext(ctx, "ALTER TABLE "+table+" DROP CONSTRAINT reject_registration"); err != nil {
				t.Fatal(err)
			}
			if _, err := auth.Bootstrap(ctx, "private-subject"); err != nil {
				t.Fatal(err)
			}
			registrationCounts(t, ctx, pool, 1, 1, 1, 1)
		})
	}
}

func TestRegistrationMembershipAndForeignKeys(t *testing.T) {
	_, pool, ctx := registrationDatabase(t)
	auth := repository.NewAuth(pool)
	owner, err := auth.Bootstrap(ctx, "owner-subject")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := pool.ExecContext(ctx, "INSERT INTO users(id,auth_subject) VALUES($1,'admin-subject')", "77777777-7777-4777-8777-777777777777"); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.ExecContext(ctx, "INSERT INTO account_members(user_id,account_id,role,audit_read) VALUES($1,$2,'admin',true)", "77777777-7777-4777-8777-777777777777", owner.AccountID); err != nil {
		t.Fatal(err)
	}
	admin, err := auth.Bootstrap(ctx, "admin-subject")
	if err != nil || admin.AccountID != owner.AccountID || admin.Role != "admin" || !admin.AuditRead {
		t.Fatal("admin membership lost", err)
	}
	registrationCounts(t, ctx, pool, 2, 1, 2, 1)
	for _, statement := range []string{
		"INSERT INTO trial_bootstrap_intents(account_id) VALUES('99999999-9999-4999-8999-999999999999')",
		"INSERT INTO account_members(user_id,account_id,role) VALUES('99999999-9999-4999-8999-999999999999','" + owner.AccountID + "','owner')",
		"INSERT INTO account_members(user_id,account_id,role) VALUES('" + owner.UserID + "','99999999-9999-4999-8999-999999999999','owner')",
	} {
		_, err := pool.ExecContext(ctx, statement)
		var pgErr *pgconn.PgError
		if !errors.As(err, &pgErr) || pgErr.Code != "23503" {
			t.Fatal("missing foreign key", db.WrapError("foreign key test", err))
		}
	}
	_, err = pool.ExecContext(ctx, "INSERT INTO trial_bootstrap_intents(account_id) VALUES($1)", owner.AccountID)
	var duplicate *pgconn.PgError
	if !errors.As(err, &duplicate) || duplicate.Code != "23505" {
		t.Fatal("duplicate trial intent accepted", db.WrapError("duplicate intent test", err))
	}
	if _, err := pool.ExecContext(ctx, "UPDATE trial_bootstrap_intents SET state='allocated'"); err == nil {
		t.Fatal("non-pending state accepted")
	}
	if _, err := pool.ExecContext(ctx, "DELETE FROM account_members WHERE user_id=$1", owner.UserID); err != nil {
		t.Fatal(err)
	}
	if _, err := auth.Bootstrap(ctx, "owner-subject"); !errors.Is(err, service.ErrUnauthorized) {
		t.Fatalf("deleted membership restored: %v", err)
	}
	registrationCounts(t, ctx, pool, 2, 1, 1, 1)
}

func TestTrialMigrationUpgradeNoBackfill(t *testing.T) {
	dsn, pool := database(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	dir := t.TempDir()
	for _, name := range []string{"000001_identity.up.sql", "000001_identity.down.sql", "000002_audit.up.sql", "000002_audit.down.sql"} {
		data, err := os.ReadFile(filepath.Join("../../migrations", name))
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, name), data, 0600); err != nil {
			t.Fatal(err)
		}
	}
	if err := Apply(ctx, dsn, dir); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.ExecContext(ctx, "INSERT INTO accounts(id) VALUES($1)", auditAccount); err != nil {
		t.Fatal(err)
	}
	actor, event := auditEvent()
	if _, err := pool.ExecContext(ctx, "INSERT INTO users(id,auth_subject) VALUES($1,'legacy-subject')", actor.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.ExecContext(ctx, "INSERT INTO account_members(user_id,account_id,role) VALUES($1,$2,'owner')", actor.ID, auditAccount); err != nil {
		t.Fatal(err)
	}
	if err := writeAudit(pool, actor, event); err != nil {
		t.Fatal(err)
	}
	var history string
	if err := pool.QueryRowContext(ctx, "SELECT row_to_json(audit_events)::text FROM audit_events").Scan(&history); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if err := Apply(ctx, dsn, "../../migrations"); err != nil {
			t.Fatal(err)
		}
	}
	var intents int
	if err := pool.QueryRowContext(ctx, "SELECT count(*) FROM trial_bootstrap_intents").Scan(&intents); err != nil || intents != 0 {
		t.Fatal("upgrade backfilled trial", err)
	}
	legacy, err := repository.NewAuth(pool).Bootstrap(ctx, "legacy-subject")
	if err != nil || legacy.UserID != actor.ID || legacy.AccountID != auditAccount {
		t.Fatal("legacy identity changed", err)
	}
	registrationCounts(t, ctx, pool, 1, 1, 1, 0)
	created, err := repository.NewAuth(pool).Bootstrap(ctx, "before-down-subject")
	if err != nil {
		t.Fatal(err)
	}
	registrationCounts(t, ctx, pool, 2, 2, 2, 1)
	conn, err := pool.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	driver, err := postgres.WithConnection(ctx, conn, &postgres.Config{StatementTimeout: 10 * time.Second})
	if err != nil {
		t.Fatal(db.WrapError("trial driver", errors.Join(err, conn.Close())))
	}
	path, err := filepath.Abs("../../migrations")
	if err != nil {
		t.Fatal(err)
	}
	runner, err := migrate.NewWithDatabaseInstance((&url.URL{Scheme: "file", Path: path}).String(), "postgres", driver)
	if err != nil {
		t.Fatal(db.WrapError("trial runner", errors.Join(err, driver.Close())))
	}
	defer func() {
		a, b := runner.Close()
		if err := errors.Join(a, b); err != nil {
			t.Error(err)
		}
	}()
	if err := runner.Steps(-1); err != nil {
		t.Fatal(db.WrapError("trial down", err))
	}
	var version int
	var dirty, removed bool
	if err := pool.QueryRowContext(ctx, "SELECT version,dirty FROM schema_migrations").Scan(&version, &dirty); err != nil || version != 2 || dirty {
		t.Fatalf("down version=%d dirty=%v err=%v", version, dirty, err)
	}
	if err := pool.QueryRowContext(ctx, "SELECT to_regclass('trial_bootstrap_intents') IS NULL").Scan(&removed); err != nil || !removed {
		t.Fatal("down retained hook", err)
	}
	if err := runner.Up(); err != nil {
		t.Fatal(db.WrapError("trial up", err))
	}
	if err := pool.QueryRowContext(ctx, "SELECT version,dirty FROM schema_migrations").Scan(&version, &dirty); err != nil || version != 6 || dirty {
		t.Fatalf("up version=%d dirty=%v err=%v", version, dirty, err)
	}
	repeated, err := repository.NewAuth(pool).Bootstrap(ctx, "before-down-subject")
	if err != nil || repeated != created {
		t.Fatal("down changed identity", err)
	}
	registrationCounts(t, ctx, pool, 2, 2, 2, 0)
	var accounts int
	if err := pool.QueryRowContext(ctx, "SELECT count(*) FROM accounts WHERE id=$1", auditAccount).Scan(&accounts); err != nil || accounts != 1 {
		t.Fatal("migration changed identity", err)
	}
	var unchanged string
	if err := pool.QueryRowContext(ctx, "SELECT row_to_json(audit_events)::text FROM audit_events").Scan(&unchanged); err != nil || unchanged != history {
		t.Fatal("migration changed audit", err)
	}
}

func TestRegistrationRestrictedRole(t *testing.T) {
	role := os.Getenv("TEST_AUDIT_ROLE")
	if role == "" {
		t.Skip("set TEST_AUDIT_ROLE for restricted registration checks")
	}
	dsn, pool, ctx := registrationDatabase(t)
	var unsafe bool
	if err := pool.QueryRowContext(ctx, `SELECT rolcanlogin OR rolsuper OR rolcreatedb OR rolcreaterole OR rolbypassrls FROM pg_roles WHERE rolname=$1`, role).Scan(&unsafe); err != nil {
		t.Fatal(db.WrapError("inspect registration role", err))
	}
	if unsafe {
		t.Fatal("test role must be restricted NOLOGIN")
	}
	var schema string
	if err := pool.QueryRowContext(ctx, "SELECT current_schema()").Scan(&schema); err != nil {
		t.Fatal(err)
	}
	quotedRole := pgx.Identifier{role}.Sanitize()
	for _, q := range []string{
		"GRANT USAGE ON SCHEMA " + pgx.Identifier{schema}.Sanitize() + " TO " + quotedRole,
		"GRANT SELECT,INSERT ON users,accounts,account_members TO " + quotedRole,
		"GRANT SELECT,INSERT,UPDATE,DELETE ON sessions TO " + quotedRole,
		"GRANT INSERT ON trial_bootstrap_intents TO " + quotedRole,
	} {
		if _, err := pool.ExecContext(ctx, q); err != nil {
			t.Fatal(db.WrapError("grant registration role", err))
		}
	}
	u, err := url.Parse(dsn)
	if err != nil {
		t.Fatal(err)
	}
	q := u.Query()
	q.Set("role", role)
	u.RawQuery = q.Encode()
	restricted, err := db.Open(ctx, u.String())
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := restricted.Close(); err != nil {
			t.Error(err)
		}
	}()
	var extra, ddl bool
	if err := restricted.QueryRowContext(ctx, `SELECT has_table_privilege(current_user,'trial_bootstrap_intents','SELECT') OR has_table_privilege(current_user,'trial_bootstrap_intents','UPDATE') OR has_table_privilege(current_user,'trial_bootstrap_intents','DELETE') OR has_table_privilege(current_user,'trial_bootstrap_intents','TRUNCATE'),has_schema_privilege(current_user,current_schema(),'CREATE')`).Scan(&extra, &ddl); err != nil {
		t.Fatal(db.WrapError("inspect registration grants", err))
	}
	if extra || ddl {
		t.Fatal("registration role has excess privileges")
	}
	auth := repository.NewAuth(restricted)
	identity, err := auth.Bootstrap(ctx, "restricted-subject")
	if err != nil {
		t.Fatal(err)
	}
	repeated, err := auth.Bootstrap(ctx, "restricted-subject")
	if err != nil || repeated != identity {
		t.Fatal("restricted repeat failed", err)
	}
	now := time.Now().UTC()
	hash := sha256.Sum256([]byte("restricted-session"))
	if err := auth.CreateSession(ctx, hash[:], "csrf", &identity, now, now.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	if _, err := auth.GetSession(ctx, hash[:], now); err != nil {
		t.Fatal(err)
	}
	registrationCounts(t, ctx, pool, 1, 1, 1, 1)
	if _, err := pool.ExecContext(ctx, "REVOKE INSERT ON trial_bootstrap_intents FROM "+quotedRole); err != nil {
		t.Fatal(db.WrapError("revoke trial intent insert", err))
	}
	_, err = auth.Bootstrap(ctx, "denied-subject")
	var denied *pgconn.PgError
	if !errors.As(err, &denied) || denied.Code != "42501" {
		t.Fatal("missing INSERT permission did not fail safely", err)
	}
	registrationCounts(t, ctx, pool, 1, 1, 1, 1)
}
