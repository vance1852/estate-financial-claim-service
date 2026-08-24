package worker

import (
	"context"
	"io"
	"log/slog"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/vance1852/estate-financial-claim-service/internal/clock"
	"github.com/vance1852/estate-financial-claim-service/internal/store"
)

func TestTimedOutHandlerIsNotReclaimedWhileStillRunning(t *testing.T) {
	ctx := context.Background()
	database, err := store.Open(ctx, filepath.Join(t.TempDir(), "timeout-overlap.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	now := time.Date(2026, 8, 24, 13, 0, 0, 0, time.UTC)
	if err := database.InsertJob(ctx, database, store.Job{
		ID: "job-timeout-overlap", Kind: "payout", ResourceID: "payout-42", Payload: []byte(`{}`),
		Status: "pending", MaxAttempts: 3, AvailableAt: now, CreatedAt: now, UpdatedAt: now,
	}); err != nil {
		t.Fatal(err)
	}
	manual := clock.NewManual(now)
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	first := New(database, manual, "worker-first", time.Millisecond, 100*time.Millisecond, logger)
	second := New(database, manual, "worker-second", time.Millisecond, 100*time.Millisecond, logger)
	first.attemptLimit = 15 * time.Millisecond
	second.attemptLimit = time.Second

	firstStarted := make(chan struct{})
	releaseFirst := make(chan struct{})
	secondStarted := make(chan struct{})
	var active atomic.Int32
	var overlap atomic.Bool
	if err := first.Register("payout", func(context.Context, store.Job) error {
		active.Add(1)
		close(firstStarted)
		<-releaseFirst
		active.Add(-1)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if err := second.Register("payout", func(context.Context, store.Job) error {
		if active.Add(1) > 1 {
			overlap.Store(true)
		}
		close(secondStarted)
		active.Add(-1)
		return nil
	}); err != nil {
		t.Fatal(err)
	}

	firstDone := make(chan error, 1)
	go func() { firstDone <- first.processOne(ctx) }()
	<-firstStarted
	select {
	case err := <-firstDone:
		if err != nil {
			t.Fatalf("timed-out worker returned error: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("timed-out worker did not release the job")
	}
	var status, owner string
	if err := database.QueryRowContext(ctx, "SELECT status,lease_owner FROM worker_jobs WHERE id='job-timeout-overlap'").Scan(&status, &owner); err != nil {
		t.Fatal(err)
	}
	if status != "running" || owner != "worker-first" {
		t.Errorf("live handler lost its lease: status=%s owner=%s", status, owner)
	}

	secondDone := make(chan error, 1)
	go func() { secondDone <- second.processOne(ctx) }()
	select {
	case <-secondStarted:
		if overlap.Load() {
			t.Error("second worker entered the payout handler before the first attempt exited")
		}
	case <-time.After(100 * time.Millisecond):
	}
	close(releaseFirst)
	select {
	case <-secondDone:
	case <-time.After(time.Second):
		t.Fatal("second worker did not finish")
	}
}
