package migration

import (
	"context"
	"database/sql"
	"errors"
	"net/url"
	"os"
	"path/filepath"
	"testing"
	"time"

	"faqbot/backend/internal/db"

	"github.com/golang-migrate/migrate/v4"
	"github.com/golang-migrate/migrate/v4/database/postgres"
	"github.com/jackc/pgx/v5/pgconn"
)

func copyMigrations(t *testing.T, dir string, names ...string) {
	t.Helper()
	for _, name := range names {
		data, err := os.ReadFile(filepath.Join("../../migrations", name))
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, name), data, 0600); err != nil {
			t.Fatal(err)
		}
	}
}

func sqlState(t *testing.T, err error, code string) {
	t.Helper()
	var cause *pgconn.PgError
	if !errors.As(err, &cause) || cause.Code != code {
		t.Fatalf("expected SQLSTATE %s: %v", code, db.WrapError("constraint check", err))
	}
}

func schemaVersion(t *testing.T, pool *sql.DB, want int) {
	t.Helper()
	var version int
	var dirty bool
	if err := pool.QueryRow("SELECT version,dirty FROM schema_migrations").Scan(&version, &dirty); err != nil || version != want || dirty {
		t.Fatalf("version=%d dirty=%v want=%d: %v", version, dirty, want, db.WrapError("read version", err))
	}
}

func TestAgentsSQLConstraints(t *testing.T) {
	dsn, pool := database(t)
	if err := Apply(context.Background(), dsn, "../../migrations"); err != nil {
		t.Fatal(err)
	}
	schemaVersion(t, pool, 4)
	if _, err := pool.Exec(`INSERT INTO accounts(id) VALUES ('00000000-0000-4000-8000-000000000001'),('00000000-0000-4000-8000-000000000002')`); err != nil {
		t.Fatal(db.WrapError("create accounts", err))
	}
	if _, err := pool.Exec(`INSERT INTO agents(id,account_id,public_id,name,website,description) VALUES ('00000000-0000-4000-8000-000000000003','00000000-0000-4000-8000-000000000001','public-agent','Помощник','https://company.example.invalid','Описание')`); err != nil {
		t.Fatal(db.WrapError("create agent", err))
	}
	for _, tc := range []struct{ name, column, value, code string }{
		{"missing account", "account_id", "'00000000-0000-4000-8000-999999999999'", "23503"},
		{"duplicate public ID", "public_id", "'public-agent'", "23505"},
		{"missing name", "name", "NULL", "23502"},
		{"empty name", "name", "'   '", "23514"},
		{"missing website", "website", "NULL", "23502"},
		{"missing description", "description", "NULL", "23502"},
		{"unknown status", "status", "'unknown'", "23514"},
		{"deleted without timestamp", "status", "'deleted'", "23514"},
		{"timestamp without deleted", "deleted_at", "now()", "23514"},
		{"unready publish", "status", "'published'", "23514"},
		{"invalid revision", "revision", "0", "23514"},
		{"active exceeds candidate", "active_knowledge_revision", "1", "23514"},
		{"published exceeds config", "published_config_revision", "2", "23514"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cols := []string{"id", "account_id", "public_id", "name", "website", "description", "status", "revision", "knowledge_revision", "active_knowledge_revision", "config_revision", "published_config_revision", "deleted_at"}
			expr := []string{"'00000000-0000-4000-8000-000000000004'", "account_id", "'different-public'", "name", "website", "description", "status", "revision", "knowledge_revision", "active_knowledge_revision", "config_revision", "published_config_revision", "deleted_at"}
			for i, col := range cols {
				if col == tc.column {
					expr[i] = tc.value
				}
			}
			query := "INSERT INTO agents ("
			values := " SELECT "
			for i, col := range cols {
				if i > 0 {
					query += ","
					values += ","
				}
				query += col
				values += expr[i]
			}
			_, err := pool.Exec(query + ")" + values + " FROM agents LIMIT 1")
			sqlState(t, err, tc.code)
		})
	}
	if _, err := pool.Exec(`UPDATE agents SET knowledge_revision=2,active_knowledge_revision=1,published_config_revision=1,status='published'`); err != nil {
		t.Fatal(db.WrapError("publish agent", err))
	}
	if _, err := pool.Exec(`UPDATE agents SET status='deleted',deleted_at=now()`); err != nil {
		t.Fatal(db.WrapError("tombstone agent", err))
	}
	var count int
	if err := pool.QueryRow("SELECT count(*) FROM agents WHERE deleted_at IS NULL").Scan(&count); err != nil || count != 0 {
		t.Fatal("tombstone remained visible")
	}
	_, err := pool.Exec(`INSERT INTO agents(id,account_id,public_id,name) VALUES ('00000000-0000-4000-8000-000000000005','00000000-0000-4000-8000-000000000002','public-agent','Другой')`)
	sqlState(t, err, "23505")
}

func TestPackageMigrationUpgradeRepeatDownUp(t *testing.T) {
	dsn, pool := database(t)
	dir := t.TempDir()
	copyMigrations(t, dir, "000001_identity.up.sql", "000001_identity.down.sql", "000002_audit.up.sql", "000002_audit.down.sql")
	if err := Apply(context.Background(), dsn, dir); err != nil {
		t.Fatal(err)
	}
	schemaVersion(t, pool, 2)
	if _, err := pool.Exec("INSERT INTO accounts(id) VALUES ($1)", auditAccount); err != nil {
		t.Fatal(db.WrapError("create upgrade account", err))
	}
	actor, event := auditEvent()
	if _, err := pool.Exec("INSERT INTO users(id,auth_subject) VALUES ($1,'package-upgrade')", actor.ID); err != nil {
		t.Fatal(db.WrapError("create upgrade user", err))
	}
	if _, err := pool.Exec("INSERT INTO account_members(user_id,account_id,role) VALUES ($1,$2,'owner')", actor.ID, auditAccount); err != nil {
		t.Fatal(db.WrapError("create upgrade member", err))
	}
	if _, err := pool.Exec("INSERT INTO sessions(token_hash,csrf,user_id,account_id,created_at,expires_at,idle_expires_at) VALUES (decode(repeat('ab',32),'hex'),'synthetic-upgrade-csrf',$1,$2,now(),now()+interval '1 hour',now()+interval '1 hour')", actor.ID, auditAccount); err != nil {
		t.Fatal(db.WrapError("create upgrade session", err))
	}
	identitySQL := "SELECT jsonb_build_object('users',(SELECT jsonb_agg(users) FROM users),'members',(SELECT jsonb_agg(account_members) FROM account_members),'sessions',(SELECT jsonb_agg(sessions) FROM sessions))::text"
	var identityBefore string
	if err := pool.QueryRow(identitySQL).Scan(&identityBefore); err != nil {
		t.Fatal(db.WrapError("snapshot upgrade identity", err))
	}
	if err := writeAudit(pool, actor, event); err != nil {
		t.Fatal(err)
	}
	var original string
	if err := pool.QueryRow("SELECT row_to_json(audit_events)::text FROM audit_events").Scan(&original); err != nil {
		t.Fatal(err)
	}
	copyMigrations(t, dir, "000003_agents.up.sql", "000003_agents.down.sql")
	if err := Apply(context.Background(), dsn, dir); err != nil {
		t.Fatal(err)
	}
	schemaVersion(t, pool, 3)
	if _, err := pool.Exec("INSERT INTO agents(id,account_id,public_id,name) VALUES ('00000000-0000-4000-8000-000000000003',$1,'upgrade-public','Помощник')", auditAccount); err != nil {
		t.Fatal(db.WrapError("create upgrade agent", err))
	}
	copyMigrations(t, dir, "000004_conversations.up.sql", "000004_conversations.down.sql")
	for range 2 {
		if err := Apply(context.Background(), dsn, dir); err != nil {
			t.Fatal(err)
		}
	}
	schemaVersion(t, pool, 4)
	var count int
	if err := pool.QueryRow("SELECT count(*) FROM agents").Scan(&count); err != nil || count != 1 {
		t.Fatal("upgrade lost agents")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	conn, err := pool.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	driver, err := postgres.WithConnection(ctx, conn, &postgres.Config{StatementTimeout: 10 * time.Second})
	if err != nil {
		t.Fatal(db.WrapError("initialize package migration", errors.Join(err, conn.Close())))
	}
	path, err := filepath.Abs(dir)
	if err != nil {
		t.Fatal(errors.Join(err, driver.Close()))
	}
	runner, err := migrate.NewWithDatabaseInstance((&url.URL{Scheme: "file", Path: path}).String(), "postgres", driver)
	if err != nil {
		t.Fatal(db.WrapError("create package runner", errors.Join(err, driver.Close())))
	}
	defer func() {
		a, b := runner.Close()
		if err := errors.Join(a, b); err != nil {
			t.Error(db.WrapError("close package runner", err))
		}
	}()
	if err := runner.Steps(-2); err != nil {
		t.Fatal(db.WrapError("package down", err))
	}
	schemaVersion(t, pool, 2)
	for _, table := range []string{"agents", "conversations", "messages", "message_sources", "ai_requests"} {
		var removed bool
		if err := pool.QueryRow("SELECT to_regclass($1) IS NULL", table).Scan(&removed); err != nil || !removed {
			t.Fatalf("table %s remains", table)
		}
	}
	var functionRemoved bool
	if err := pool.QueryRow("SELECT to_regprocedure('check_conversation_member()') IS NULL").Scan(&functionRemoved); err != nil || !functionRemoved {
		t.Fatal("SECURITY DEFINER function survived down")
	}
	if err := runner.Up(); err != nil {
		t.Fatal(db.WrapError("package up", err))
	}
	schemaVersion(t, pool, 4)
	var unchanged string
	if err := pool.QueryRow("SELECT row_to_json(audit_events)::text FROM audit_events").Scan(&unchanged); err != nil || unchanged != original {
		t.Fatal("package migration changed audit")
	}
	if err := pool.QueryRow("SELECT count(*) FROM accounts WHERE id=$1", auditAccount).Scan(&count); err != nil || count != 1 {
		t.Fatal("package migration changed identity")
	}
	var identityAfter string
	if err := pool.QueryRow(identitySQL).Scan(&identityAfter); err != nil || identityBefore != identityAfter {
		t.Fatal("package migration changed users, membership or sessions")
	}
	if _, err := pool.Exec("INSERT INTO agents(id,account_id,public_id,name) VALUES ($1,$2,'up-again','Помощник')", chatID(3), auditAccount); err != nil {
		t.Fatal(db.WrapError("create agent after up", err))
	}
	if os.Getenv("TEST_AUDIT_ROLE") != "" {
		checkConversationMemberRole(t, pool, auditAccount, chatID(3), actor.ID)
	}

}

func TestAgentsSQLNonblankName(t *testing.T) {
	pool := chatDatabase(t)
	for _, name := range []string{" ", "\t\r\n", "\u00a0\u2003\u202f", "\u0085\u1680\u2028\u2029\u205f\u3000"} {
		_, err := pool.Exec("UPDATE agents SET name=$1 WHERE id=$2", name, chatID(6))
		sqlState(t, err, "23514")
	}
	name := "\u2003  Помощник компании  \u00a0"
	if _, err := pool.Exec("UPDATE agents SET name=$1 WHERE id=$2", name, chatID(6)); err != nil {
		t.Fatal(db.WrapError("save agent name whitespace", err))
	}
	var stored string
	if err := pool.QueryRow("SELECT name FROM agents WHERE id=$1", chatID(6)).Scan(&stored); err != nil || stored != name {
		t.Fatal("agent name whitespace was normalized")
	}
}
