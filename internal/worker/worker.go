package worker

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"sync"
	"time"

	"github.com/vance1852/estate-financial-claim-service/internal/clock"
	"github.com/vance1852/estate-financial-claim-service/internal/domain"
	"github.com/vance1852/estate-financial-claim-service/internal/store"
)

type Handler func(context.Context, store.Job) error

type Worker struct {
	store        *store.Store
	clock        clock.Clock
	owner        string
	poll         time.Duration
	lease        time.Duration
	attemptLimit time.Duration
	logger       *slog.Logger
	handlers     map[string]Handler
	wake         chan struct{}
	mu           sync.RWMutex
}

func New(database *store.Store, c clock.Clock, owner string, poll, lease time.Duration, logger *slog.Logger) *Worker {
	return &Worker{store: database, clock: c, owner: owner, poll: poll, lease: lease,
		attemptLimit: lease / 2, logger: logger, handlers: make(map[string]Handler), wake: make(chan struct{}, 1)}
}

func (w *Worker) Register(kind string, handler Handler) error {
	if kind == "" || handler == nil {
		return fmt.Errorf("worker handler kind and function are required")
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	if _, exists := w.handlers[kind]; exists {
		return fmt.Errorf("worker handler %s already registered", kind)
	}
	w.handlers[kind] = handler
	return nil
}

func (w *Worker) Notify() {
	select {
	case w.wake <- struct{}{}:
	default:
	}
}

func (w *Worker) Run(ctx context.Context) error {
	if _, err := w.store.RecoverExpiredJobs(ctx, w.clock.Now()); err != nil {
		return err
	}
	ticker := time.NewTicker(w.poll)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		case <-w.wake:
		}
		if err := w.processOne(ctx); err != nil && !errors.Is(err, domain.ErrNotFound) {
			w.logger.ErrorContext(ctx, "worker attempt failed", "error", err)
		}
	}
}

func (w *Worker) processOne(ctx context.Context) error {
	job, err := w.store.ClaimJob(ctx, w.owner, w.clock.Now(), w.lease)
	if err != nil {
		return err
	}
	w.mu.RLock()
	handler := w.handlers[job.Kind]
	w.mu.RUnlock()
	if handler == nil {
		err := fmt.Errorf("no handler for job kind %s", job.Kind)
		return w.store.FailJob(ctx, job, w.owner, w.clock.Now(), w.clock.Now(), err)
	}
	attemptCtx, cancel := context.WithTimeout(ctx, w.attemptLimit)
	result := make(chan error, 1)
	go func() {
		result <- handler(attemptCtx, job)
	}()
	select {
	case err = <-result:
	case <-attemptCtx.Done():
		timer := time.NewTimer(5 * time.Millisecond)
		select {
		case err = <-result:
			timer.Stop()
		case <-timer.C:
			cancel()
			return w.store.ReleaseTimedOutJob(ctx, job, w.owner, w.clock.Now(), attemptCtx.Err())
		}
	}
	cancel()
	now := w.clock.Now()
	if err == nil {
		return w.store.CompleteJob(ctx, job.ID, w.owner, now)
	}
	backoff := time.Duration(math.Pow(2, float64(min(job.Attempts, 6)))) * time.Second
	return w.store.FailJob(ctx, job, w.owner, now, now.Add(backoff), err)
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
