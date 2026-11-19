package knowledge

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/google/uuid"
)

func TestKnowledgeFormatsThroughRealQueue(t *testing.T) {
	for _, format := range []string{"pdf", "docx", "text"} {
		t.Run(format, func(t *testing.T) {
			ctx, store, broker, account, agent, root := integration(t)
			data := []byte("Доставка в Москву стоит 500 рублей.")
			if format != "text" {
				var err error
				data, err = os.ReadFile(filepath.Join("testdata", "sample."+format))
				if err != nil {
					t.Fatal(err)
				}
			}
			job := submit(t, ctx, store, root, account, agent, uuid.NewString(), "document.index", string(data), format)
			wait(t, ctx, store, broker, job, "completed")
			var count int
			if err := store.DB.QueryRow(`SELECT count(*) FROM ai.chunks WHERE generation_id=$1 AND content LIKE '%500%'`, job.JobID).Scan(&count); err != nil || count < 1 {
				t.Fatalf("format content: %d %v", count, err)
			}
			if format == "pdf" {
				if err := store.DB.QueryRow(`SELECT count(*) FROM ai.chunks WHERE generation_id=$1 AND page=1`, job.JobID).Scan(&count); err != nil || count < 1 {
					t.Fatalf("PDF page provenance: %d %v", count, err)
				}
			}
			if format == "docx" {
				if err := store.DB.QueryRow(`SELECT count(*) FROM ai.chunks WHERE generation_id=$1 AND heading='Условия'`, job.JobID).Scan(&count); err != nil || count < 1 {
					t.Fatalf("DOCX table heading: %d %v", count, err)
				}
			}
		})
	}
}

func TestKnowledgeLostResultPublicationReplaysReceipt(t *testing.T) {
	ctx, store, broker, account, agent, root := integration(t)
	// Only this dedicated vhost is affected; the worker re-declares its binding on reconnect.
	if err := broker.Channel.QueueUnbind("faq.go.results", "jobs.*", "faq.job-results", nil); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = broker.Channel.QueueBind("faq.go.results", "jobs.*", "faq.job-results", false, nil) })
	job := submit(t, ctx, store, root, account, agent, uuid.NewString(), "document.index", "Доставка стоит 500 рублей.", "txt")
	if err := store.Dispatch(ctx, broker); err != nil {
		t.Fatal(err)
	}
	observed := false
	for i := 0; i < 400; i++ {
		var saved bool
		err := store.DB.QueryRow(`SELECT result IS NOT NULL AND published_at IS NULL FROM ai.receipts WHERE job_id=$1`, job.JobID).Scan(&saved)
		if err == nil && saved {
			observed = true
			break
		}
		time.Sleep(25 * time.Millisecond)
	}
	if !observed {
		t.Fatal("did not observe committed effect before missing result publication")
	}
	wait(t, ctx, store, broker, job, "completed")
	var count int
	if err := store.DB.QueryRow(`SELECT count(*) FROM ai.generations WHERE document_id=$1`, job.DocumentID).Scan(&count); err != nil || count != 1 {
		t.Fatalf("publish recovery duplicated effect: %d %v", count, err)
	}
}

func TestKnowledgeDelayedRetryAfterSourceUnavailable(t *testing.T) {
	ctx, store, broker, account, agent, root := integration(t)
	job := submit(t, ctx, store, root, account, agent, uuid.NewString(), "document.index", "Доставка стоит 500 рублей.", "txt")
	var key string
	if err := store.DB.QueryRow(`SELECT storage_key FROM knowledge.documents WHERE id=$1`, job.DocumentID).Scan(&key); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, key)
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err = store.Dispatch(ctx, broker); err != nil {
		t.Fatal(err)
	}
	retryObserved := false
	for i := 0; i < 100; i++ {
		queue, err := broker.Channel.QueueInspect("faq.ai.retry")
		if err != nil {
			t.Fatal(err)
		}
		if queue.Messages > 0 {
			retryObserved = true
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if !retryObserved {
		t.Fatal("transient failure did not enter delayed retry")
	}
	if err = os.WriteFile(path, data, 0644); err != nil {
		t.Fatal(err)
	}
	wait(t, ctx, store, broker, job, "completed")
}
