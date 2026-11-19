package knowledge

import (
	"os"
	"testing"
	"time"

	"github.com/google/uuid"
)

func TestKnowledgeCoordinatorProcess(t *testing.T) {
	if os.Getenv("AI_TEST_COORDINATOR") != "1" {
		t.Skip("start separate restricted Go coordinator and set AI_TEST_COORDINATOR=1")
	}
	ctx, store, _, account, agent, root := integration(t)
	job := submit(t, ctx, store, root, account, agent, uuid.NewString(), "document.index", "Доставка стоит 500 рублей.", "txt")
	// This test never dispatches or consumes: the separate Go process must do both.
	for i := 0; i < 200; i++ {
		var state string
		if err := store.DB.QueryRowContext(ctx, `SELECT status FROM knowledge.jobs WHERE id=$1`, job.JobID).Scan(&state); err != nil {
			t.Fatal(err)
		}
		if state == "completed" {
			return
		}
		if state == "failed" {
			t.Fatal("coordinator job failed")
		}
		time.Sleep(150 * time.Millisecond)
	}
	t.Fatal("independent coordinator did not complete the job")
}
