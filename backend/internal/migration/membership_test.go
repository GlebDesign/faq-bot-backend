package migration

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	"faqbot/backend/internal/db"
	"faqbot/backend/internal/handlers"
	"faqbot/backend/internal/models"
	"faqbot/backend/internal/repository"
	"faqbot/backend/internal/service"

	"github.com/jackc/pgx/v5"
)

func TestChatHistorySurvivesMembershipRevocation(t *testing.T) {
	pool := chatDatabase(t)
	store := repository.NewAuth(pool)
	token := "abcdefghijklmnopqrstuvwxyz"
	hash := sha256.Sum256([]byte(token))
	now := time.Now().UTC()
	identity := &models.Identity{UserID: chatID(3), AccountID: chatID(1), Role: "owner"}
	if err := store.CreateSession(t.Context(), hash[:], "fixture-csrf", identity, now, now.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	input := chatInput()
	input.VisitorID = ""
	input.MemberID = chatID(3)
	input.Operation = "test_message"
	input.ConversationID = chatID(14)
	input.MessageID = chatID(24)
	if _, err := repository.NewAIRequests(pool).CreateOrRead(t.Context(), input); err != nil {
		t.Fatal(err)
	}
	var before string
	if err := pool.QueryRow("SELECT row_to_json(conversations)::text FROM conversations WHERE id=$1", chatID(14)).Scan(&before); err != nil {
		t.Fatal(db.WrapError("snapshot historical conversation", err))
	}
	server := httptest.NewServer(handlers.NewHandler(&handlers.Health{}, handlers.NewAuthHandler(service.New(store, nil), "https://cabinet.example.invalid", false)))
	defer server.Close()
	session := func(want int) {
		req, err := http.NewRequestWithContext(t.Context(), "GET", server.URL+"/api/v1/auth/session", nil)
		if err != nil {
			t.Fatal(err)
		}
		req.AddCookie(&http.Cookie{Name: "faqbot_session", Value: token})
		response, err := server.Client().Do(req)
		if err != nil {
			t.Fatal(err)
		}
		if err := response.Body.Close(); err != nil {
			t.Error(err)
		}
		if response.StatusCode != want {
			t.Fatalf("session status=%d want=%d", response.StatusCode, want)
		}
	}
	session(200)
	if _, err := pool.Exec("DELETE FROM account_members WHERE user_id=$1 AND account_id=$2", identity.UserID, identity.AccountID); err != nil {
		t.Fatal(db.WrapError("revoke membership with history", err))
	}
	session(401)
	var after string
	if err := pool.QueryRow("SELECT row_to_json(conversations)::text FROM conversations WHERE id=$1", chatID(14)).Scan(&after); err != nil || before != after {
		t.Fatal("revocation changed historical actor snapshot")
	}
	for _, query := range []string{"SELECT count(*) FROM sessions", "SELECT count(*) FROM messages WHERE id='00000000-0000-4000-8000-000000000024'", "SELECT count(*) FROM ai_requests"} {
		var count int
		if err := pool.QueryRow(query).Scan(&count); err != nil {
			t.Fatal(db.WrapError("read revocation result", err))
		}
		want := 1
		if query == "SELECT count(*) FROM sessions" {
			want = 0
		}
		if count != want {
			t.Fatalf("revocation row count=%d want=%d", count, want)
		}
	}
	_, err := pool.Exec("INSERT INTO conversations(id,account_id,agent_id,member_id) VALUES ($1,$2,$3,$4)", chatID(90), identity.AccountID, chatID(6), identity.UserID)
	sqlState(t, err, "23503")
	_, err = pool.Exec("UPDATE conversations SET member_id=$1 WHERE id=$2", identity.UserID, chatID(15))
	sqlState(t, err, "23503")
	if _, err := pool.Exec("UPDATE conversations SET created_at=created_at WHERE id=$1", chatID(14)); err != nil {
		t.Fatal(db.WrapError("retain historical conversation", err))
	}
}

func checkConversationMemberRole(t *testing.T, pool *sql.DB, accountID, agentID, memberID string) {
	t.Helper()
	role := os.Getenv("TEST_AUDIT_ROLE")
	if role == "" {
		t.Fatal("restricted conversation helper requires TEST_AUDIT_ROLE")
	}
	var schema string
	if err := pool.QueryRow("SELECT current_schema()").Scan(&schema); err != nil {
		t.Fatal(db.WrapError("read restricted conversation schema", err))
	}
	quotedSchema, quotedRole := pgx.Identifier{schema}.Sanitize(), pgx.Identifier{role}.Sanitize()
	for _, query := range []string{"GRANT USAGE ON SCHEMA " + quotedSchema + " TO " + quotedRole, "GRANT SELECT ON " + quotedSchema + ".account_members TO " + quotedRole, "GRANT INSERT ON " + quotedSchema + ".conversations TO " + quotedRole} {
		if _, err := pool.Exec(query); err != nil {
			t.Fatal(db.WrapError("grant restricted conversation writer", err))
		}
	}
	err := db.WithTx(t.Context(), pool, func(ctx context.Context, tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, "SET LOCAL ROLE "+quotedRole); err != nil {
			return db.WrapError("select restricted conversation role", err)
		}
		var update, execute bool
		if err := tx.QueryRowContext(ctx, "SELECT has_table_privilege(current_user,$1,'UPDATE'),has_function_privilege(current_user,$2,'EXECUTE')", quotedSchema+".account_members", quotedSchema+".check_conversation_member()").Scan(&update, &execute); err != nil {
			return db.WrapError("inspect conversation writer privileges", err)
		}
		if update || execute {
			return errors.New("conversation writer has membership UPDATE or direct trigger EXECUTE")
		}
		_, err := tx.ExecContext(ctx, "INSERT INTO "+quotedSchema+".conversations(id,account_id,agent_id,member_id) VALUES ($1,$2,$3,$4)", chatID(90), accountID, agentID, memberID)
		return db.WrapError("restricted member conversation", err)
	})
	if err != nil {
		t.Fatal(err)
	}
	err = db.WithTx(t.Context(), pool, func(ctx context.Context, tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, "SET LOCAL ROLE "+quotedRole); err != nil {
			return db.WrapError("select spoof test role", err)
		}
		if _, err := tx.ExecContext(ctx, "CREATE TEMP TABLE account_members(user_id uuid,account_id uuid) ON COMMIT DROP"); err != nil {
			return db.WrapError("create temporary membership shadow", err)
		}
		absent := chatID(95)
		if _, err := tx.ExecContext(ctx, "INSERT INTO pg_temp.account_members(user_id,account_id) VALUES ($1,$2)", absent, accountID); err != nil {
			return db.WrapError("fill temporary shadow", err)
		}
		_, err := tx.ExecContext(ctx, "INSERT INTO "+quotedSchema+".conversations(id,account_id,agent_id,member_id) VALUES ($1,$2,$3,$4)", chatID(91), accountID, agentID, absent)
		return db.WrapError("reject shadow membership", err)
	})
	sqlState(t, err, "23503")
}

func TestConversationMembershipTriggerRestrictedRole(t *testing.T) {
	if os.Getenv("TEST_AUDIT_ROLE") == "" {
		t.Skip("set TEST_AUDIT_ROLE for restricted conversation writer")
	}
	pool := chatDatabase(t)
	checkConversationMemberRole(t, pool, chatID(1), chatID(6), chatID(3))
}
