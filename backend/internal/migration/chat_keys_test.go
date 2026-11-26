package migration

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"faqbot/backend/internal/db"
)

func TestConversationKeysMigrationPreservesHistorySQL(t *testing.T) {
	dsn, pool := database(t)
	ctx := context.Background()
	dir := t.TempDir()
	files, err := filepath.Glob("../../migrations/00000[1-6]_*.sql")
	if err != nil || len(files) != 12 {
		t.Fatalf("version 6 files: %d: %v", len(files), err)
	}
	for _, path := range files {
		copyMigrations(t, dir, filepath.Base(path))
	}
	if err := Apply(ctx, dsn, dir); err != nil {
		t.Fatal(err)
	}
	schemaVersion(t, pool, 6)
	for _, q := range []string{
		"INSERT INTO accounts(id) VALUES ('" + chatID(1) + "')",
		"INSERT INTO agents(id,account_id,public_id,name) VALUES ('" + chatID(2) + "','" + chatID(1) + "','old','Legacy')",
		"INSERT INTO conversations(id,account_id,agent_id,visitor_id) VALUES ('" + chatID(3) + "','" + chatID(1) + "','" + chatID(2) + "','visitor')",
		"INSERT INTO messages(id,account_id,agent_id,conversation_id,question) VALUES ('" + chatID(4) + "','" + chatID(1) + "','" + chatID(2) + "','" + chatID(3) + "',repeat('я',4001))",
		"INSERT INTO ai_requests(id,account_id,agent_id,visitor_id,operation,idempotency_key,conversation_id,message_id,payload_hash) VALUES ('" + chatID(5) + "','" + chatID(1) + "','" + chatID(2) + "','visitor','widget_message','old','" + chatID(3) + "','" + chatID(4) + "',sha256(convert_to('old','UTF8')))",
	} {
		if _, err := pool.ExecContext(ctx, q); err != nil {
			t.Fatal(db.WrapError("seed version 6 chat", err))
		}
	}
	check := func(up bool) {
		t.Helper()
		var key, visitor string
		var question int
		err := pool.QueryRowContext(ctx, `SELECT c.visitor_id,char_length(m.question),r.idempotency_key
		 FROM conversations c JOIN messages m ON m.conversation_id=c.id JOIN ai_requests r ON r.message_id=m.id`).Scan(&visitor, &question, &key)
		if err != nil || visitor != "visitor" || question != 4001 || key != "old" {
			t.Fatal("legacy history changed", db.WrapError("inspect legacy history", err))
		}
		if up {
			var isNull bool
			if err := pool.QueryRowContext(ctx, "SELECT create_key_hash IS NULL FROM conversations").Scan(&isNull); err != nil || !isNull {
				t.Fatal("legacy conversation gained a create key", db.WrapError("inspect legacy create key", err))
			}
		}
	}
	if err := Apply(ctx, dsn, "../../migrations"); err != nil {
		t.Fatal(err)
	}
	schemaVersion(t, pool, 7)
	check(true)
	for _, direction := range []string{"down", "up"} {
		data, err := os.ReadFile("../../migrations/000007_chat_conversation_keys." + direction + ".sql")
		if err != nil {
			t.Fatal(err)
		}
		if _, err := pool.ExecContext(ctx, string(data)); err != nil {
			t.Fatal(db.WrapError("run version 7 "+direction, err))
		}
		check(direction == "up")
	}
}
