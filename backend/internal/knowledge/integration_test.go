package knowledge

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"faqbot/backend/internal/aiclient"
	"faqbot/backend/internal/aipb"
	"faqbot/backend/internal/db"
	"faqbot/backend/internal/messaging"
	"github.com/google/uuid"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func integration(t *testing.T) (context.Context, Store, *Broker, string, string, string) {
	t.Helper()
	dsn := os.Getenv("TEST_KNOWLEDGE_DATABASE_URL")
	if dsn == "" || os.Getenv("AI_AMQP_URL") == "" {
		t.Skip("set isolated knowledge DB and AMQP environment")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	t.Cleanup(cancel)
	pool, err := db.Open(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = pool.Close() })
	var name string
	if err = pool.QueryRow("SELECT current_database()").Scan(&name); err != nil || name != "faq_ai18" {
		t.Fatal("integration tests require dedicated faq_ai18 DB")
	}
	broker, err := Dial(os.Getenv("AI_AMQP_URL"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(broker.Close)
	account, agent := uuid.NewString(), uuid.NewString()
	if _, err = pool.Exec(`INSERT INTO accounts(id) VALUES($1)`, account); err != nil {
		t.Fatal(err)
	}
	if _, err = pool.Exec(`INSERT INTO agents(id,account_id,public_id,name) VALUES($1,$2,$3,'Интеграция')`, agent, account, "integration-"+agent); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		for _, query := range []string{
			`DELETE FROM ai.receipts WHERE job_id IN(SELECT id FROM knowledge.jobs WHERE account_id=$1)`,
			`DELETE FROM ai.generations WHERE account_id=$1`,
			`DELETE FROM knowledge.outbox WHERE job_id IN(SELECT id FROM knowledge.jobs WHERE account_id=$1)`,
			`DELETE FROM knowledge.jobs WHERE account_id=$1`,
			`DELETE FROM knowledge.documents WHERE account_id=$1`,
			`DELETE FROM agents WHERE account_id=$1`,
			`DELETE FROM accounts WHERE id=$1`,
		} {
			if _, err := pool.Exec(query, account); err != nil {
				t.Error(err)
			}
		}
	})
	return ctx, Store{pool}, broker, account, agent, os.Getenv("AI_TEST_SOURCE_ROOT")
}

func submit(t *testing.T, ctx context.Context, store Store, root, account, agent, document, kind, text, format string) messaging.Message {
	t.Helper()
	input := Submission{JobID: uuid.NewString(), OperationID: uuid.NewString(), AccountID: account, AgentID: agent, DocumentID: document, Kind: kind, RequestID: "go-integration", TraceID: "go-integration"}
	if kind != "document.delete" {
		input.Name = "Условия"
		input.Format = format
		input.StorageKey = uuid.NewString() + "." + format
		if err := os.WriteFile(filepath.Join(root, input.StorageKey), []byte(text), 0644); err != nil {
			t.Fatal(err)
		}
		sum := sha256.Sum256([]byte(text))
		input.SHA256 = hex.EncodeToString(sum[:])
		t.Cleanup(func() { _ = os.Remove(filepath.Join(root, input.StorageKey)) })
	}
	out, err := store.Submit(ctx, input)
	if err != nil {
		t.Fatal(err)
	}
	input.JobID = uuid.NewString()
	replay, err := store.Submit(ctx, input)
	if err != nil || replay.JobID != out.JobID {
		t.Fatalf("submission replay: %v", err)
	}
	input.Name = "Changed name"
	if _, err = store.Submit(ctx, input); !errors.Is(err, ErrConflict) {
		t.Fatalf("payload conflict: %v", err)
	}
	return out
}

func wait(t *testing.T, ctx context.Context, store Store, broker *Broker, job messaging.Message, want string) {
	t.Helper()
	for attempt := 0; attempt < 200; attempt++ {
		if err := store.Dispatch(ctx, broker); err != nil {
			t.Fatal(err)
		}
		for {
			handled, err := store.ConsumeResult(ctx, broker)
			if err != nil {
				t.Fatal(err)
			}
			if !handled {
				break
			}
		}
		var state string
		if err := store.DB.QueryRowContext(ctx, `SELECT status FROM knowledge.jobs WHERE id=$1`, job.JobID).Scan(&state); err != nil {
			t.Fatal(err)
		}
		if state == want {
			return
		}
		if state == "completed" || state == "failed" {
			t.Fatalf("state %s want %s", state, want)
		}
		select {
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		case <-time.After(150 * time.Millisecond):
		}
	}
	t.Fatal("queue job did not finish")
}

func ask(t *testing.T, ctx context.Context, client *aiclient.Client, account, agent string) *aipb.GenerateAnswerResponse {
	t.Helper()
	answer, err := client.Generate(ctx, &aipb.GenerateAnswerRequest{AccountId: account, AgentId: agent, RequestId: uuid.NewString(), TraceId: "integration", Question: "Сколько стоит доставка в Москву?"})
	if err != nil {
		t.Fatal(err)
	}
	return answer
}

func TestKnowledgeRealQueueLifecycleAndGoRPC(t *testing.T) {
	ctx, store, broker, account, agent, root := integration(t)
	if root == "" || os.Getenv("AI_GRPC_ADDR") == "" {
		t.Fatal("source root and gRPC address required")
	}
	client, err := aiclient.New(os.Getenv("AI_GRPC_ADDR"), os.Getenv("AI_SERVICE_TOKEN"), 180*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	document := uuid.NewString()
	indexed := submit(t, ctx, store, root, account, agent, document, "document.index", "Доставка в Москву стоит 500 рублей.", "txt")
	wait(t, ctx, store, broker, indexed, "completed")
	answer := ask(t, ctx, client, account, agent)
	if !answer.HasAnswer || !strings.Contains(answer.Answer, "500") || len(answer.Sources) != 1 || answer.Sources[0].DocumentVersion != 1 {
		t.Fatalf("RPC indexed answer: %+v", answer)
	}
	other := ask(t, ctx, client, account, uuid.NewString())
	if other.HasAnswer {
		t.Fatal("cross-agent retrieval")
	}
	// Redelivery after activation must replay the receipt without touching the effect.
	raw, _ := json.Marshal(indexed)
	if err = broker.Publish(ctx, "faq.jobs", indexed.EventType, raw, indexed.JobID); err != nil {
		t.Fatal(err)
	}
	time.Sleep(400 * time.Millisecond)
	_, err = store.ConsumeResult(ctx, broker)
	if err != nil {
		t.Fatal(err)
	}
	var count int
	if err = store.DB.QueryRow(`SELECT count(*) FROM ai.generations WHERE document_id=$1`, document).Scan(&count); err != nil || count != 1 {
		t.Fatalf("redelivery count=%d err=%v", count, err)
	}
	broken := submit(t, ctx, store, root, account, agent, document, "document.reindex", "corrupt PDF", "pdf")
	wait(t, ctx, store, broker, broken, "failed")
	answer = ask(t, ctx, client, account, agent)
	if !answer.HasAnswer || !strings.Contains(answer.Answer, "500") {
		t.Fatal("failed reindex lost old ready corpus")
	}
	fresh := submit(t, ctx, store, root, account, agent, document, "document.reindex", "# Условия\n\nДоставка в Москву стоит 600 рублей.", "md")
	wait(t, ctx, store, broker, fresh, "completed")
	answer = ask(t, ctx, client, account, agent)
	if !answer.HasAnswer || !strings.Contains(answer.Answer, "600") || strings.Contains(answer.Answer, "500") || answer.Sources[0].DocumentVersion != 3 {
		t.Fatalf("new version answer: %+v", answer)
	}
	deleted := submit(t, ctx, store, root, account, agent, document, "document.delete", "", "")
	if ask(t, ctx, client, account, agent).HasAnswer {
		t.Fatal("deleting document stayed visible")
	}
	wait(t, ctx, store, broker, deleted, "completed")
	if err = broker.Publish(ctx, "faq.jobs", indexed.EventType, raw, indexed.JobID); err != nil {
		t.Fatal(err)
	}
	if ask(t, ctx, client, account, agent).HasAnswer {
		t.Fatal("late index revived deleted document")
	}
	for i := 0; i < 100; i++ {
		handled, err := store.ConsumeResult(ctx, broker)
		if err != nil {
			t.Fatal(err)
		}
		if handled {
			break
		}
		if i == 99 {
			t.Fatal("late duplicate result not received")
		}
		time.Sleep(100 * time.Millisecond)
	}
	wrong, err := aiclient.New(os.Getenv("AI_GRPC_ADDR"), strings.Repeat("x", 40), 10*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer wrong.Close()
	_, err = wrong.Generate(ctx, &aipb.GenerateAnswerRequest{AccountId: account, AgentId: agent, RequestId: "bad-auth", Question: "Доставка?"})
	if status.Code(err) != codes.Unauthenticated {
		t.Fatalf("service auth: %v", err)
	}
}

func TestKnowledgeSupersededResultsCannotActivate(t *testing.T) {
	ctx, store, _, account, agent, root := integration(t)
	document := uuid.NewString()
	old := submit(t, ctx, store, root, account, agent, document, "document.index", "Первый текст", "txt")
	_ = submit(t, ctx, store, root, account, agent, document, "document.delete", "", "")
	old.EventType += ".completed"
	old.Status = "completed"
	n := int64(1)
	old.ChunksIndexed = &n
	if err := store.ApplyResult(ctx, old); !errors.Is(err, ErrStale) {
		t.Fatalf("stale result: %v", err)
	}
	var active sql.NullString
	if err := store.DB.QueryRow(`SELECT active_generation::text FROM knowledge.documents WHERE id=$1`, document).Scan(&active); err != nil || active.Valid {
		t.Fatalf("stale generation activated: %v", err)
	}
}
