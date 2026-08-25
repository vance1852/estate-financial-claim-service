package worker

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/vance1852/estate-financial-claim-service/internal/clock"
	"github.com/vance1852/estate-financial-claim-service/internal/domain"
	"github.com/vance1852/estate-financial-claim-service/internal/store"
)

func workerFixture(t *testing.T) (*Worker, *store.Store, *clock.Manual) {
	t.Helper()
	database, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "worker.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	manual := clock.NewManual(time.Date(2026, 8, 24, 11, 0, 0, 0, time.UTC))
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	return New(database, manual, "worker-test", time.Millisecond, 100*time.Millisecond, logger), database, manual
}

func insertTestJob(t *testing.T, database *store.Store, now time.Time, id, kind string, max int) {
	t.Helper()
	err := database.InsertJob(context.Background(), database, store.Job{ID: id, Kind: kind,
		ResourceID: "resource_" + id, Payload: []byte(`{}`), Status: "pending", MaxAttempts: max,
		AvailableAt: now, CreatedAt: now, UpdatedAt: now})
	if err != nil {
		t.Fatal(err)
	}
}

func TestRegisterValidatesHandlersAndUniqueness(t *testing.T) {
	worker, _, _ := workerFixture(t)
	if err := worker.Register("", func(context.Context, store.Job) error { return nil }); err == nil {
		t.Fatal("empty kind accepted")
	}
	if err := worker.Register("kind", nil); err == nil {
		t.Fatal("nil handler accepted")
	}
	if err := worker.Register("kind", func(context.Context, store.Job) error { return nil }); err != nil {
		t.Fatal(err)
	}
	if err := worker.Register("kind", func(context.Context, store.Job) error { return nil }); err == nil {
		t.Fatal("duplicate handler accepted")
	}
}

func TestProcessOneCompletesSuccessfulJob(t *testing.T) {
	worker, database, manual := workerFixture(t)
	insertTestJob(t, database, manual.Now(), "job_success", "success", 3)
	var calls atomic.Int32
	if err := worker.Register("success", func(ctx context.Context, job store.Job) error {
		calls.Add(1)
		if job.ID != "job_success" || job.Attempts != 1 {
			t.Errorf("job = %#v", job)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if err := worker.processOne(context.Background()); err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 1 {
		t.Fatalf("handler calls = %d", calls.Load())
	}
	var status, owner string
	var lease any
	if err := database.QueryRowContext(context.Background(), "SELECT status,lease_owner,lease_until FROM worker_jobs WHERE id='job_success'").Scan(&status, &owner, &lease); err != nil {
		t.Fatal(err)
	}
	if status != "completed" || owner != "" || lease != nil {
		t.Fatalf("completed job status=%s owner=%s lease=%v", status, owner, lease)
	}
}

func TestProcessOneCompletesSuccessfulRetryAndReleasesLease(t *testing.T) {
	worker, database, manual := workerFixture(t)
	insertTestJob(t, database, manual.Now(), "job_retry_success", "retry_success", 3)
	// Force the attempt deadline to elapse before completion so that the
	// per-attempt context is already cancelled when the job succeeds on retry.
	worker.attemptLimit = time.Millisecond
	var calls atomic.Int32
	if err := worker.Register("retry_success", func(ctx context.Context, job store.Job) error {
		calls.Add(1)
		if calls.Load() == 1 {
			return errors.New("provider unavailable")
		}
		// Second attempt: let the attempt context expire, then succeed.
		<-ctx.Done()
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	// First attempt fails and is scheduled for retry.
	if err := worker.processOne(context.Background()); err != nil {
		t.Fatal(err)
	}
	manual.Advance(2 * time.Second)
	// Second attempt succeeds despite the attempt context being cancelled.
	if err := worker.processOne(context.Background()); err != nil {
		t.Fatalf("successful retry completion: %v", err)
	}
	if calls.Load() != 2 {
		t.Fatalf("handler calls = %d, want 2", calls.Load())
	}
	var status, owner, lastError string
	var lease any
	if err := database.QueryRowContext(context.Background(),
		"SELECT status,lease_owner,lease_until,last_error FROM worker_jobs WHERE id='job_retry_success'").
		Scan(&status, &owner, &lease, &lastError); err != nil {
		t.Fatal(err)
	}
	if status != "completed" || owner != "" || lease != nil || lastError != "" {
		t.Fatalf("retry-completed job status=%s owner=%s lease=%v lastError=%s", status, owner, lease, lastError)
	}
}

func TestProcessOneRetriesThenMarksPermanentFailure(t *testing.T) {
	worker, database, manual := workerFixture(t)
	insertTestJob(t, database, manual.Now(), "job_retry", "retry", 2)
	if err := worker.Register("retry", func(context.Context, store.Job) error { return errors.New("provider unavailable") }); err != nil {
		t.Fatal(err)
	}
	if err := worker.processOne(context.Background()); err != nil {
		t.Fatal(err)
	}
	var status, available string
	var attempts int
	if err := database.QueryRowContext(context.Background(), "SELECT status,attempts,available_at FROM worker_jobs WHERE id='job_retry'").Scan(&status, &attempts, &available); err != nil {
		t.Fatal(err)
	}
	if status != "pending" || attempts != 1 {
		t.Fatalf("first failure status=%s attempts=%d", status, attempts)
	}
	manual.Advance(3 * time.Second)
	if err := worker.processOne(context.Background()); err != nil {
		t.Fatal(err)
	}
	var lastError string
	if err := database.QueryRowContext(context.Background(), "SELECT status,attempts,last_error FROM worker_jobs WHERE id='job_retry'").Scan(&status, &attempts, &lastError); err != nil {
		t.Fatal(err)
	}
	if status != "failed" || attempts != 2 || lastError != "provider unavailable" {
		t.Fatalf("terminal status=%s attempts=%d error=%s", status, attempts, lastError)
	}
}

func TestMissingHandlerIsPersistedAsFailure(t *testing.T) {
	worker, database, manual := workerFixture(t)
	insertTestJob(t, database, manual.Now(), "job_missing", "missing", 1)
	if err := worker.processOne(context.Background()); err != nil {
		t.Fatal(err)
	}
	var status, lastError string
	if err := database.QueryRowContext(context.Background(), "SELECT status,last_error FROM worker_jobs WHERE id='job_missing'").Scan(&status, &lastError); err != nil {
		t.Fatal(err)
	}
	if status != "failed" || lastError == "" {
		t.Fatalf("missing handler status=%s error=%s", status, lastError)
	}
}

func TestAttemptContextIsCancelledAtDeadline(t *testing.T) {
	worker, database, manual := workerFixture(t)
	worker.attemptLimit = 10 * time.Millisecond
	insertTestJob(t, database, manual.Now(), "job_timeout", "slow", 1)
	if err := worker.Register("slow", func(ctx context.Context, _ store.Job) error {
		<-ctx.Done()
		return ctx.Err()
	}); err != nil {
		t.Fatal(err)
	}
	if err := worker.processOne(context.Background()); err != nil {
		t.Fatal(err)
	}
	var status, lastError string
	if err := database.QueryRowContext(context.Background(), "SELECT status,last_error FROM worker_jobs WHERE id='job_timeout'").Scan(&status, &lastError); err != nil {
		t.Fatal(err)
	}
	if status != "failed" || lastError != context.DeadlineExceeded.Error() {
		t.Fatalf("timeout status=%s error=%s", status, lastError)
	}
}

func TestRunStopsPromptlyWhenParentIsCancelled(t *testing.T) {
	worker, _, _ := workerFixture(t)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- worker.Run(ctx) }()
	worker.Notify()
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("run error = %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("worker did not stop after cancellation")
	}
}

func TestNotifyIsNonBlockingAndCoalescesWakeups(t *testing.T) {
	worker, _, _ := workerFixture(t)
	for index := 0; index < 100; index++ {
		worker.Notify()
	}
	if len(worker.wake) != 1 {
		t.Fatalf("wake queue length = %d, want 1", len(worker.wake))
	}
}

func TestProcessOneReportsNoWork(t *testing.T) {
	worker, _, _ := workerFixture(t)
	if err := worker.processOne(context.Background()); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("empty queue error = %v", err)
	}
}
