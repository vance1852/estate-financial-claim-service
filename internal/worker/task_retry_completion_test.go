package worker

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/vance1852/estate-financial-claim-service/internal/store"
)

func TestSuccessfulRetryPersistsCompletionBeforeAttemptContextEnds(t *testing.T) {
	worker, database, manual := workerFixture(t)
	insertTestJob(t, database, manual.Now(), "job_recovered", "recovering", 3)
	var calls atomic.Int32
	if err := worker.Register("recovering", func(context.Context, store.Job) error {
		if calls.Add(1) == 1 {
			return errors.New("provider temporarily unavailable")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if err := worker.processOne(context.Background()); err != nil {
		t.Fatal(err)
	}
	manual.Advance(3 * time.Second)
	if err := worker.processOne(context.Background()); err != nil {
		t.Fatalf("successful retry returned error: %v", err)
	}

	var status, owner string
	var lease any
	var attempts int
	if err := database.QueryRowContext(context.Background(), `SELECT status,attempts,lease_owner,lease_until
		FROM worker_jobs WHERE id='job_recovered'`).Scan(&status, &attempts, &owner, &lease); err != nil {
		t.Fatal(err)
	}
	if status != "completed" || attempts != 2 || owner != "" || lease != nil {
		t.Fatalf("successful retry was not completed: status=%s attempts=%d owner=%s lease=%v", status, attempts, owner, lease)
	}
}
