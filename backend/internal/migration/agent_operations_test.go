package migration

import (
	"context"
	"errors"
	"net/url"
	"path/filepath"
	"testing"
	"time"

	"github.com/golang-migrate/migrate/v4"
	"github.com/golang-migrate/migrate/v4/database/postgres"

	"faqbot/backend/internal/db"
)

func TestAgentOperationsUpgradeRepeat(t *testing.T) {
	dsn, pool := database(t)
	dir := t.TempDir()
	copyMigrations(t, dir, "000001_identity.up.sql", "000001_identity.down.sql", "000002_audit.up.sql", "000002_audit.down.sql", "000003_agents.up.sql", "000003_agents.down.sql", "000004_conversations.up.sql", "000004_conversations.down.sql")
	if err := Apply(context.Background(), dsn, dir); err != nil {
		t.Fatal(err)
	}
	schemaVersion(t, pool, 4)
	if _, err := pool.Exec(`INSERT INTO accounts(id) VALUES($1);`, chatID(1)); err != nil {
		t.Fatal(db.WrapError("create upgrade account", err))
	}
	if _, err := pool.Exec(`INSERT INTO users(id,auth_subject) VALUES($1,'operations-upgrade')`, chatID(2)); err != nil {
		t.Fatal(db.WrapError("create upgrade actor", err))
	}
	if _, err := pool.Exec(`INSERT INTO agents(id,account_id,public_id,name) VALUES($1,$2,'upgrade-operations','Помощник')`, chatID(3), chatID(1)); err != nil {
		t.Fatal(db.WrapError("create upgrade agent", err))
	}
	if _, err := pool.Exec(`INSERT INTO conversations(id,account_id,agent_id,visitor_id) VALUES($1,$2,$3,'upgrade-visitor')`, chatID(8), chatID(1), chatID(3)); err != nil {
		t.Fatal(err)
	}
	actor, event := auditEvent()
	actor.ID = chatID(2)
	event.AccountID = chatID(1)
	if err := writeAudit(pool, actor, event); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(`INSERT INTO account_members(user_id,account_id,role) VALUES($1,$2,'owner')`, chatID(2), chatID(1)); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(`INSERT INTO sessions(token_hash,csrf,user_id,account_id,created_at,expires_at,idle_expires_at) VALUES(decode(repeat('ab',32),'hex'),'upgrade-csrf',$1,$2,now(),now()+interval '1 hour',now()+interval '30 minutes')`, chatID(2), chatID(1)); err != nil {
		t.Fatal(err)
	}
	snapshot := `SELECT jsonb_build_object('accounts',(SELECT jsonb_agg(accounts) FROM accounts),'users',(SELECT jsonb_agg(users) FROM users),'members',(SELECT jsonb_agg(account_members) FROM account_members),'sessions',(SELECT jsonb_agg(sessions) FROM sessions),'agents',(SELECT jsonb_agg(agents) FROM agents),'conversations',(SELECT jsonb_agg(conversations) FROM conversations),'audit',(SELECT jsonb_agg(audit_events) FROM audit_events))::text`
	var old string
	if err := pool.QueryRow(snapshot).Scan(&old); err != nil {
		t.Fatal(err)
	}
	var before string
	if err := pool.QueryRow(`SELECT row_to_json(agents)::text FROM agents`).Scan(&before); err != nil {
		t.Fatal(err)
	}
	copyMigrations(t, dir, "000005_agent_operations.up.sql", "000005_agent_operations.down.sql")
	for range 2 {
		if err := Apply(context.Background(), dsn, dir); err != nil {
			t.Fatal(err)
		}
	}
	schemaVersion(t, pool, 5)
	var after string
	if err := pool.QueryRow(`SELECT row_to_json(agents)::text FROM agents`).Scan(&after); err != nil || before != after {
		t.Fatal("upgrade changed agent")
	}
	if _, err := pool.Exec(`INSERT INTO agent_operations(id,account_id,actor_id,operation,resource,key_hash,payload_hash,result) VALUES($1,$2,$3,'delete',$4,decode(repeat('ab',32),'hex'),decode(repeat('cd',32),'hex'),'{}')`, chatID(4), chatID(1), chatID(2), chatID(3)); err != nil {
		t.Fatal(db.WrapError("create upgraded operation", err))
	}
	if _, err := pool.Exec(`INSERT INTO agent_cleanup_requests(id,account_id,agent_id) VALUES($1,$2,$3)`, chatID(4), chatID(1), chatID(3)); err != nil {
		t.Fatal(db.WrapError("create upgraded cleanup", err))
	}
	_, err := pool.Exec(`UPDATE agent_cleanup_requests SET account_id=$1`, chatID(2))
	sqlState(t, err, "23503")
	_, err = pool.Exec(`UPDATE agent_cleanup_requests SET status='completed'`)
	sqlState(t, err, "23514")
	_, err = pool.Exec(`UPDATE agent_operations SET key_hash=decode('ab','hex')`)
	sqlState(t, err, "23514")
	if _, err := pool.Exec(`INSERT INTO accounts(id) VALUES($1)`, chatID(99)); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(`INSERT INTO agents(id,account_id,public_id,name) VALUES($1,$2,'other-upgrade-agent','other')`, chatID(98), chatID(1)); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(`DELETE FROM agent_cleanup_requests`); err != nil {
		t.Fatal(err)
	}
	for i, tc := range []struct{ account, operation, resource string }{
		{chatID(99), "delete", chatID(3)},
		{chatID(1), "delete", chatID(98)},
		{chatID(1), "update", chatID(3)},
		{chatID(1), "create", ""},
	} {
		opID := chatID(100 + i)
		if _, err := pool.Exec(`INSERT INTO agent_operations(id,account_id,actor_id,operation,resource,key_hash,payload_hash) VALUES($1,$2,$3,$4,$5,decode(repeat('ab',32),'hex'),decode(repeat('cd',32),'hex'))`, opID, tc.account, chatID(2), tc.operation, tc.resource); err != nil {
			t.Fatal(err)
		}
		_, err := pool.Exec(`INSERT INTO agent_cleanup_requests(id,account_id,agent_id) VALUES($1,$2,$3)`, opID, chatID(1), chatID(3))
		sqlState(t, err, "23503")
	}
	if _, err := pool.Exec(`DELETE FROM agents WHERE id=$1`, chatID(98)); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(`DELETE FROM agent_operations WHERE account_id=$1`, chatID(99)); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(`DELETE FROM accounts WHERE id=$1`, chatID(99)); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	conn, err := pool.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	driver, err := postgres.WithConnection(ctx, conn, &postgres.Config{})
	if err != nil {
		t.Fatal(errors.Join(err, conn.Close()))
	}
	path, err := filepath.Abs(dir)
	if err != nil {
		t.Fatal(err)
	}
	runner, err := migrate.NewWithDatabaseInstance((&url.URL{Scheme: "file", Path: path}).String(), "postgres", driver)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		a, b := runner.Close()
		if err := errors.Join(a, b); err != nil {
			t.Error(err)
		}
	}()
	if err := runner.Steps(-1); err != nil {
		t.Fatal(err)
	}
	schemaVersion(t, pool, 4)
	if err := runner.Up(); err != nil {
		t.Fatal(err)
	}
	schemaVersion(t, pool, 5)
	var preserved string
	if err := pool.QueryRow(snapshot).Scan(&preserved); err != nil || old != preserved {
		t.Fatal("down5/up5 changed old data")
	}
}
