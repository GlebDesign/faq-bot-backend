package migration

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"faqbot/backend/internal/db"
	"faqbot/backend/internal/models"
	"faqbot/backend/internal/repository"
	"faqbot/backend/internal/service"
)

func chatID(n int) string { return fmt.Sprintf("00000000-0000-4000-8000-%012d", n) }

func chatDatabase(t *testing.T) *sql.DB {
	t.Helper()
	dsn, pool := database(t)
	if err := Apply(context.Background(), dsn, "../../migrations"); err != nil {
		t.Fatal(err)
	}
	schemaVersion(t, pool, 7)
	for _, n := range []int{1, 2} {
		if _, err := pool.Exec("INSERT INTO accounts(id) VALUES ($1)", chatID(n)); err != nil {
			t.Fatal(db.WrapError("create chat account", err))
		}
	}
	for _, row := range []struct{ user, account int }{{3, 1}, {4, 1}, {5, 2}} {
		if _, err := pool.Exec("INSERT INTO users(id,auth_subject) VALUES ($1,$2)", chatID(row.user), chatID(row.user)); err != nil {
			t.Fatal(db.WrapError("create chat user", err))
		}
		if _, err := pool.Exec("INSERT INTO account_members(user_id,account_id,role) VALUES ($1,$2,'owner')", chatID(row.user), chatID(row.account)); err != nil {
			t.Fatal(db.WrapError("create chat member", err))
		}
	}
	for _, row := range []struct{ agent, account int }{{6, 1}, {7, 1}, {8, 2}} {
		if _, err := pool.Exec("INSERT INTO agents(id,account_id,public_id,name) VALUES ($1,$2,$3,'Помощник')", chatID(row.agent), chatID(row.account), chatID(row.agent)); err != nil {
			t.Fatal(db.WrapError("create chat agent", err))
		}
	}
	for _, row := range []struct {
		conv, account, agent, member int
		visitor                      string
	}{{10, 1, 6, 0, "visitor-one"}, {11, 1, 6, 0, "visitor-two"}, {12, 1, 7, 0, "visitor-one"}, {13, 2, 8, 0, "visitor-one"}, {14, 1, 6, 3, ""}, {15, 1, 6, 4, ""}, {16, 1, 6, 0, "visitor-one"}} {
		var member, visitor any
		if row.member != 0 {
			member = chatID(row.member)
		} else {
			visitor = row.visitor
		}
		if _, err := pool.Exec("INSERT INTO conversations(id,account_id,agent_id,visitor_id,member_id) VALUES ($1,$2,$3,$4,$5)", chatID(row.conv), chatID(row.account), chatID(row.agent), visitor, member); err != nil {
			t.Fatal(db.WrapError("create chat conversation", err))
		}
		if _, err := pool.Exec("INSERT INTO messages(id,account_id,agent_id,conversation_id,question) VALUES ($1,$2,$3,$4,'Вопрос')", chatID(row.conv+10), chatID(row.account), chatID(row.agent), chatID(row.conv)); err != nil {
			t.Fatal(db.WrapError("create chat message", err))
		}
	}
	return pool
}

func chatInput() models.AIRequestInput {
	return models.AIRequestInput{ID: chatID(40), AccountID: chatID(1), AgentID: chatID(6), VisitorID: "visitor-one", Operation: "widget_message", IdempotencyKey: "same-key", ConversationID: chatID(10), MessageID: chatID(20), Message: "Вопрос"}
}

func TestAIRequestSQLReplayAndConflict(t *testing.T) {
	pool := chatDatabase(t)
	repo := repository.NewAIRequests(pool)
	in := chatInput()
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	original, err := repo.CreateOrRead(ctx, in)
	if err != nil {
		t.Fatal(err)
	}
	if original.ID != in.ID || original.State != "accepted" || len(original.Result) != 0 {
		t.Fatalf("unexpected request: %+v", original)
	}
	for _, state := range []string{"accepted", "generating", "completed", "no_answer", "failed", "cancelled"} {
		t.Run(state, func(t *testing.T) {
			var result any
			if state != "accepted" && state != "generating" {
				result = `{"status":"` + state + `","answer":"Сохранённый результат"}`
			}
			if _, err := pool.Exec("UPDATE ai_requests SET state=$1,result=$2::jsonb WHERE id=$3", state, result, original.ID); err != nil {
				t.Fatal(db.WrapError("set replay result", err))
			}
			var stored []byte
			if err := pool.QueryRow("SELECT result FROM ai_requests WHERE id=$1", original.ID).Scan(&stored); err != nil {
				t.Fatal(db.WrapError("read replay result", err))
			}
			in.ID = chatID(41)
			in.MessageID = chatID(999)
			got, err := repo.CreateOrRead(ctx, in)
			if err != nil {
				t.Fatal(err)
			}
			if got.ID != original.ID || got.MessageID != original.MessageID || got.State != state || !bytes.Equal(got.Result, stored) {
				t.Fatalf("replay changed result: %+v", got)
			}
		})
	}
	for _, field := range []string{"message", "conversation"} {
		t.Run(field, func(t *testing.T) {
			changed := in
			switch field {
			case "message":
				changed.Message = "Другой вопрос"
			case "conversation":
				changed.ConversationID = chatID(16)
			}
			if _, err := repo.CreateOrRead(ctx, changed); !errors.Is(err, repository.ErrAIRequestConflict) {
				t.Fatalf("payload conflict missing: %v", err)
			}
		})
	}
	var count int
	if err := pool.QueryRow("SELECT count(*) FROM ai_requests").Scan(&count); err != nil || count != 1 {
		t.Fatal("repeat created extra operation")
	}
	got, err := repo.CreateOrRead(ctx, in)
	if err != nil || got.State != "cancelled" {
		t.Fatal("conflict changed original state")
	}
}

func TestAIRequestSQLIndependentScopes(t *testing.T) {
	pool := chatDatabase(t)
	repo := repository.NewAIRequests(pool)
	cases := []models.AIRequestInput{}
	for _, row := range []struct {
		account, agent, conv, member int
		visitor                      string
	}{{1, 6, 10, 0, "visitor-one"}, {1, 6, 11, 0, "visitor-two"}, {1, 7, 12, 0, "visitor-one"}, {2, 8, 13, 0, "visitor-one"}, {1, 6, 14, 3, ""}, {1, 6, 15, 4, ""}} {
		in := chatInput()
		in.ID = chatID(100 + len(cases))
		in.AccountID = chatID(row.account)
		in.AgentID = chatID(row.agent)
		in.ConversationID = chatID(row.conv)
		in.MessageID = chatID(row.conv + 10)
		in.VisitorID = row.visitor
		if row.member != 0 {
			in.MemberID = chatID(row.member)
			in.Operation = "test_message"
		}
		cases = append(cases, in)
	}
	for _, in := range cases {
		got, err := repo.CreateOrRead(context.Background(), in)
		if err != nil || got.ID != in.ID {
			t.Fatalf("scope collision: %v", err)
		}
	}
	var count int
	if err := pool.QueryRow("SELECT count(*) FROM ai_requests").Scan(&count); err != nil || count != len(cases) {
		t.Fatal("independent scope was lost")
	}
}

func TestChatSQLRejectsForeignLinksAndInvalidStates(t *testing.T) {
	pool := chatDatabase(t)
	for _, tc := range []struct{ name, query, code string }{
		{"foreign conversation agent", `INSERT INTO conversations(id,account_id,agent_id,visitor_id) VALUES ('00000000-0000-4000-8000-000000000090','00000000-0000-4000-8000-000000000002','00000000-0000-4000-8000-000000000006','v')`, "23503"},
		{"foreign member", `UPDATE conversations SET visitor_id=NULL,member_id='00000000-0000-4000-8000-000000000005' WHERE id='00000000-0000-4000-8000-000000000010'`, "23503"},
		{"both actors", `UPDATE conversations SET member_id='00000000-0000-4000-8000-000000000003' WHERE id='00000000-0000-4000-8000-000000000010'`, "23514"},
		{"no actor", `UPDATE conversations SET visitor_id=NULL WHERE id='00000000-0000-4000-8000-000000000010'`, "23514"},
		{"foreign message tenant", `UPDATE messages SET account_id='00000000-0000-4000-8000-000000000002' WHERE id='00000000-0000-4000-8000-000000000020'`, "23503"},
		{"foreign message agent", `UPDATE messages SET agent_id='00000000-0000-4000-8000-000000000007' WHERE id='00000000-0000-4000-8000-000000000020'`, "23503"},
		{"invalid message state", `UPDATE messages SET status='unknown'`, "23514"},
		{"missing completed answer", `UPDATE messages SET status='completed'`, "23514"},
		{"answer during generation", `UPDATE messages SET status='generating',answer='Ответ'`, "23514"},
	} {
		t.Run(tc.name, func(t *testing.T) { _, err := pool.Exec(tc.query); sqlState(t, err, tc.code) })
	}
	in := chatInput()
	repo := repository.NewAIRequests(pool)
	if _, err := repo.CreateOrRead(context.Background(), in); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct{ name, set, code string }{
		{"foreign tenant", "account_id='00000000-0000-4000-8000-000000000002'", "23503"},
		{"foreign agent", "agent_id='00000000-0000-4000-8000-000000000007'", "23503"},
		{"foreign visitor", "visitor_id='visitor-two'", "23503"},
		{"other conversation message", "message_id='00000000-0000-4000-8000-000000000021'", "23503"},
		{"foreign member", "visitor_id=NULL,member_id='00000000-0000-4000-8000-000000000004',operation='test_message'", "23503"},
		{"no actor", "visitor_id=NULL", "23514"},
		{"invalid operation", "operation='other'", "23514"},
		{"wrong actor operation", "operation='test_message'", "23514"},
		{"short hash", "payload_hash='x'::bytea", "23514"},
		{"invalid state", "state='unknown'", "23514"},
		{"missing terminal result", "state='completed'", "23514"},
		{"result before completion", `result='{"status":"accepted"}'::jsonb`, "23514"},
		{"result state mismatch", `state='failed',result='{"status":"completed"}'::jsonb`, "23514"},
		{"result without status", `state='failed',result='{}'::jsonb`, "23514"},
		{"scalar result", `state='failed',result='"failed"'::jsonb`, "23514"},
		{"JSON null result", `state='failed',result='null'::jsonb`, "23514"},
	} {
		t.Run(tc.name, func(t *testing.T) { _, err := pool.Exec("UPDATE ai_requests SET " + tc.set); sqlState(t, err, tc.code) })
	}
	for _, row := range []struct {
		account, agent, conv, message int
		code                          string
	}{{1, 6, 10, 20, ""}, {2, 6, 10, 20, "23503"}, {1, 7, 10, 20, "23503"}, {1, 6, 11, 20, "23503"}, {1, 6, 10, 21, "23503"}} {
		_, err := pool.Exec(`INSERT INTO message_sources(account_id,agent_id,conversation_id,message_id,citation_id,display_name,locator,excerpt,document_id,document_version,knowledge_revision) VALUES ($1,$2,$3,$4,$5,'Документ','раздел 1','Фрагмент',$6,1,1)`, chatID(row.account), chatID(row.agent), chatID(row.conv), chatID(row.message), fmt.Sprintf("cite-%d-%d-%d", row.account, row.agent, row.conv), chatID(99))
		if row.code != "" {
			sqlState(t, err, row.code)
		} else if err != nil {
			t.Fatal(db.WrapError("create source", err))
		}
	}
	for _, state := range []string{"accepted", "generating", "completed", "no_answer", "failed", "cancelled"} {
		var answer any
		if state == "completed" || state == "no_answer" {
			answer = "Ответ"
		}
		if _, err := pool.Exec("UPDATE messages SET status=$1,answer=$2", state, answer); err != nil {
			t.Fatal(db.WrapError("valid message state", err))
		}
	}
}

func TestAIRequestSQLConcurrentReplay(t *testing.T) {
	for _, conflict := range []bool{false, true} {
		t.Run(fmt.Sprint(conflict), func(t *testing.T) {
			pool := chatDatabase(t)
			repo := repository.NewAIRequests(pool)
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			type result struct {
				request models.AIRequest
				err     error
			}
			results := make(chan result, 8)
			start := make(chan struct{})
			for i := range 8 {
				go func() {
					select {
					case <-start:
					case <-ctx.Done():
						results <- result{err: ctx.Err()}
						return
					}
					in := chatInput()
					in.ID = chatID(100 + i)
					if conflict && i%2 == 1 {
						in.Message = "Другой вопрос"
					}
					out, err := repo.CreateOrRead(ctx, in)
					results <- result{out, err}
				}()
			}
			close(start)
			var id string
			var successes, conflicts int
			for range 8 {
				r := <-results
				if errors.Is(r.err, repository.ErrAIRequestConflict) {
					conflicts++
					continue
				}
				if r.err != nil {
					t.Error(r.err)
					continue
				}
				successes++
				if id != "" && id != r.request.ID {
					t.Error("concurrent replay returned a different operation")
				}
				id = r.request.ID
			}
			if (!conflict && (successes != 8 || conflicts != 0)) || (conflict && (successes != 4 || conflicts != 4)) {
				t.Fatalf("successes=%d conflicts=%d", successes, conflicts)
			}
			var count int
			if err := pool.QueryRow("SELECT count(*) FROM ai_requests").Scan(&count); err != nil || count != 1 {
				t.Fatal("concurrent replay inserted duplicates")
			}
		})
	}
}

func TestAIRequestSQLWaitsForConcurrentInsert(t *testing.T) {
	for _, conflict := range []bool{false, true} {
		t.Run(fmt.Sprint(conflict), func(t *testing.T) {
			pool := chatDatabase(t)
			writer := chatIsolationPool(t, pool)
			in := chatInput()
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			first, err := pool.BeginTx(ctx, nil)
			if err != nil {
				cancel()
				t.Fatal(db.WrapError("begin first chat writer", err))
			}
			payload, err := json.Marshal(struct {
				ConversationID string `json:"conversation_id"`
				Message        string `json:"message"`
			}{in.ConversationID, in.Message})
			if err != nil {
				t.Fatal(err)
			}
			hash := sha256.Sum256(payload)
			_, err = first.ExecContext(ctx, `INSERT INTO ai_requests(id,account_id,agent_id,visitor_id,operation,idempotency_key,conversation_id,message_id,payload_hash,state,result) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,'completed','{"status":"completed","answer":"original"}')`, in.ID, in.AccountID, in.AgentID, in.VisitorID, in.Operation, in.IdempotencyKey, in.ConversationID, in.MessageID, hash[:])
			if err != nil {
				cancel()
				t.Fatal(db.WrapError("insert first chat writer", errors.Join(err, first.Rollback())))
			}
			var pid int
			if err := first.QueryRowContext(ctx, "SELECT pg_backend_pid()").Scan(&pid); err != nil {
				cancel()
				t.Fatal(db.WrapError("read first chat backend", errors.Join(err, first.Rollback())))
			}
			type result struct {
				request models.AIRequest
				err     error
			}
			results := make(chan result, 1)
			received := false
			defer func() {
				cancel()
				if err := first.Rollback(); err != nil && !errors.Is(err, sql.ErrTxDone) {
					t.Error(db.WrapError("rollback chat writer", err))
				}
				if !received {
					<-results
				}
			}()
			second := in
			second.ID = chatID(41)
			second.MessageID = chatID(999)
			if conflict {
				second.Message = "Другой вопрос"
			}
			go func() {
				out, err := repository.NewAIRequests(writer).CreateOrRead(ctx, second)
				results <- result{out, err}
			}()
			ticker := time.NewTicker(10 * time.Millisecond)
			defer ticker.Stop()
			blocked := false
			for !blocked {
				if err := pool.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM pg_stat_activity WHERE $1::int=ANY(pg_blocking_pids(pid)) AND datname=current_database())`, pid).Scan(&blocked); err != nil {
					t.Fatal(db.WrapError("observe waiting chat insert", err))
				}
				if blocked {
					break
				}
				select {
				case r := <-results:
					received = true
					t.Fatalf("second writer finished before first commit: %v", r.err)
				case <-ticker.C:
				case <-ctx.Done():
					t.Fatal(ctx.Err())
				}
			}
			if err := first.Commit(); err != nil {
				t.Fatal(db.WrapError("commit first chat writer", err))
			}
			r := <-results
			received = true
			if conflict {
				if !errors.Is(r.err, repository.ErrAIRequestConflict) {
					t.Fatalf("missing concurrent conflict: %v", r.err)
				}
			} else if r.err != nil || r.request.ID != in.ID || r.request.MessageID != in.MessageID || r.request.State != "completed" || !bytes.Contains(r.request.Result, []byte("original")) {
				t.Fatalf("concurrent replay changed result: %+v %v", r.request, r.err)
			}
			var count int
			if err := pool.QueryRowContext(ctx, "SELECT count(*) FROM ai_requests").Scan(&count); err != nil || count != 1 {
				t.Fatal("waiting writer changed operation count")
			}
		})
	}
}

func chatIsolationPool(t *testing.T, pool *sql.DB) *sql.DB {
	t.Helper()
	var schema string
	if err := pool.QueryRow("SELECT current_schema()").Scan(&schema); err != nil {
		t.Fatal(db.WrapError("read chat schema", err))
	}
	u, err := url.Parse(os.Getenv("TEST_DATABASE_URL"))
	if err != nil {
		t.Fatal(db.WrapError("parse isolated chat URL", err))
	}
	query := u.Query()
	query.Set("search_path", schema)
	query.Set("default_transaction_isolation", "repeatable read")
	u.RawQuery = query.Encode()
	writer, err := db.Open(t.Context(), u.String())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := writer.Close(); err != nil {
			t.Error(db.WrapError("close repeatable-read chat pool", err))
		}
	})
	var isolation string
	if err := writer.QueryRow("SHOW default_transaction_isolation").Scan(&isolation); err != nil || isolation != "repeatable read" {
		t.Fatal("test did not establish repeatable-read default")
	}
	return writer
}

func TestChatSQLAndRepositoryNonblankMessage(t *testing.T) {
	pool := chatDatabase(t)
	for _, text := range []string{" ", "\t\r\n", "\u00a0\u2003\u202f", "\u0085\u1680\u2028\u2029\u205f\u3000"} {
		_, err := pool.Exec("UPDATE messages SET question=$1 WHERE id=$2", text, chatID(20))
		sqlState(t, err, "23514")
		in := chatInput()
		in.Message = text
		if _, err := repository.NewAIRequests(pool).CreateOrRead(t.Context(), in); !errors.Is(err, repository.ErrInvalidAIRequest) {
			t.Fatalf("blank message accepted: %v", err)
		}
	}
	text := "\u2003  Вопрос\tс пробелом  \u00a0"
	if _, err := pool.Exec("UPDATE messages SET question=$1 WHERE id=$2", text, chatID(20)); err != nil {
		t.Fatal(db.WrapError("save meaningful whitespace", err))
	}
	var stored string
	if err := pool.QueryRow("SELECT question FROM messages WHERE id=$1", chatID(20)).Scan(&stored); err != nil || stored != text {
		t.Fatal("SQL normalized meaningful whitespace")
	}
	in := chatInput()
	in.Message = text
	if _, err := repository.NewAIRequests(pool).CreateOrRead(t.Context(), in); err != nil {
		t.Fatal(err)
	}
	in.Message = "Вопрос с пробелом"
	if _, err := repository.NewAIRequests(pool).CreateOrRead(t.Context(), in); !errors.Is(err, repository.ErrAIRequestConflict) {
		t.Fatal("payload normalization hid a different message")
	}
}

func TestAIRequestSQLRejectsInvalidInputBeforeTransaction(t *testing.T) {
	repo := repository.NewAIRequests(nil)
	for _, in := range []models.AIRequestInput{
		{Message: "Question"},
		{VisitorID: "visitor", MemberID: chatID(3), Message: "Question"},
		{MemberID: chatID(3), Message: "\u00a0"},
	} {
		if _, err := repo.CreateOrRead(t.Context(), in); !errors.Is(err, repository.ErrInvalidAIRequest) {
			t.Fatalf("expected invalid input before transaction: %v", err)
		}
	}
}

func TestChatSQLWriteScopeLocksRestrictedRole(t *testing.T) {
	role := os.Getenv("TEST_AUDIT_ROLE")
	if role == "" {
		t.Skip("set TEST_AUDIT_ROLE for restricted chat writer")
	}
	pool := chatDatabase(t)
	ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
	defer cancel()
	var schema string
	if err := pool.QueryRowContext(ctx, "SELECT current_schema()").Scan(&schema); err != nil {
		t.Fatal(db.WrapError("read chat schema", err))
	}
	roleSQL := pgx.Identifier{role}.Sanitize()
	for _, grant := range []string{
		"GRANT USAGE ON SCHEMA " + pgx.Identifier{schema}.Sanitize(),
		"GRANT SELECT, UPDATE ON agents",
		"GRANT SELECT ON account_members",
		"GRANT SELECT, INSERT ON conversations, messages, ai_requests",
		"GRANT EXECUTE ON FUNCTION lock_chat_write(uuid,uuid,uuid,uuid)",
	} {
		if _, err := pool.ExecContext(ctx, grant+" TO "+roleSQL); err != nil {
			t.Fatal(db.WrapError("grant restricted chat writer", err))
		}
	}
	chat := repository.NewChat(pool)
	in := models.AIRequestInput{
		ID: chatID(40), AccountID: chatID(1), AgentID: chatID(6), MemberID: chatID(3),
		Operation: "test_message", IdempotencyKey: "first", ConversationID: chatID(14), MessageID: chatID(41), Message: "First question",
	}
	next := in
	next.ID, next.IdempotencyKey, next.MessageID = chatID(42), "second", chatID(43)
	first, err := pool.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(db.WrapError("begin restricted chat write", err))
	}
	defer func() { _ = first.Rollback() }()
	if _, err := first.ExecContext(ctx, "SET LOCAL ROLE "+roleSQL); err != nil {
		t.Fatal(db.WrapError("set chat writer role", err))
	}
	var hasUpdate bool
	if err := first.QueryRowContext(ctx, `SELECT has_table_privilege(current_user,'account_members','UPDATE')
		OR has_table_privilege(current_user,'conversations','UPDATE')`).Scan(&hasUpdate); err != nil || hasUpdate {
		t.Fatal("chat writer must not have UPDATE membership or conversation", err)
	}
	if _, err := chat.AcceptMessage(ctx, first, in); err != nil {
		t.Fatal(err)
	}
	for _, mutation := range []struct{ query, id string }{
		{"DELETE FROM account_members WHERE account_id=$1 AND user_id=$2", in.MemberID},
		{"UPDATE agents SET status='deleted',deleted_at=now(),revision=revision+1 WHERE account_id=$1 AND id=$2", in.AgentID},
	} {
		err := db.WithTx(ctx, pool, func(ctx context.Context, tx *sql.Tx) error {
			if _, err := tx.ExecContext(ctx, "SET LOCAL lock_timeout='100ms'"); err != nil {
				return db.WrapError("set chat mutation lock timeout", err)
			}
			_, err := tx.ExecContext(ctx, mutation.query, in.AccountID, mutation.id)
			return db.WrapError("try concurrent chat scope mutation", err)
		})
		sqlState(t, err, "55P03")
	}
	writeAsRole := func() error {
		return db.WithTx(ctx, pool, func(ctx context.Context, tx *sql.Tx) error {
			if _, err := tx.ExecContext(ctx, "SET LOCAL ROLE "+roleSQL); err != nil {
				return db.WrapError("set restricted chat role", err)
			}
			if _, err := tx.ExecContext(ctx, "SET LOCAL lock_timeout='100ms'"); err != nil {
				return db.WrapError("set chat write lock timeout", err)
			}
			_, err := chat.AcceptMessage(ctx, tx, next)
			return err
		})
	}
	sqlState(t, writeAsRole(), "55P03")
	if err := first.Commit(); err != nil {
		t.Fatal(db.WrapError("commit scoped chat write", err))
	}
	if err := writeAsRole(); err != nil {
		t.Fatal(err)
	}
	var firstAt, secondAt time.Time
	if err := pool.QueryRowContext(ctx, `SELECT a.created_at,b.created_at FROM messages a,messages b WHERE a.id=$1 AND b.id=$2`, in.MessageID, next.MessageID).Scan(&firstAt, &secondAt); err != nil || !secondAt.After(firstAt) {
		t.Fatal("messages did not follow serialized acceptance order", err)
	}
	if _, err := pool.ExecContext(ctx, "DELETE FROM account_members WHERE account_id=$1 AND user_id=$2", in.AccountID, in.MemberID); err != nil {
		t.Fatal(db.WrapError("revoke chat membership", err))
	}
	if err := writeAsRole(); !errors.Is(err, service.ErrChatNotFound) {
		t.Fatalf("revoked member replay: %v", err)
	}
	if _, err := pool.ExecContext(ctx, "INSERT INTO account_members(account_id,user_id,role) VALUES ($1,$2,'owner')", in.AccountID, in.MemberID); err != nil {
		t.Fatal(db.WrapError("restore chat membership", err))
	}
	if _, err := pool.ExecContext(ctx, "UPDATE agents SET status='deleted',deleted_at=now(),revision=revision+1 WHERE account_id=$1 AND id=$2", in.AccountID, in.AgentID); err != nil {
		t.Fatal(db.WrapError("delete chat agent", err))
	}
	if err := writeAsRole(); !errors.Is(err, service.ErrChatNotFound) {
		t.Fatalf("deleted agent replay: %v", err)
	}
}
