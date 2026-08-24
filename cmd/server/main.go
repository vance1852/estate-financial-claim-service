package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/vance1852/estate-financial-claim-service/internal/auth"
	"github.com/vance1852/estate-financial-claim-service/internal/cases"
	"github.com/vance1852/estate-financial-claim-service/internal/claims"
	"github.com/vance1852/estate-financial-claim-service/internal/clock"
	"github.com/vance1852/estate-financial-claim-service/internal/config"
	"github.com/vance1852/estate-financial-claim-service/internal/domain"
	"github.com/vance1852/estate-financial-claim-service/internal/httpapi"
	"github.com/vance1852/estate-financial-claim-service/internal/ids"
	"github.com/vance1852/estate-financial-claim-service/internal/inquiry"
	"github.com/vance1852/estate-financial-claim-service/internal/store"
	"github.com/vance1852/estate-financial-claim-service/internal/worker"
)

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))
	if err := run(context.Background(), logger); err != nil {
		logger.Error("server stopped", "error", err)
		os.Exit(1)
	}
}

func run(parent context.Context, logger *slog.Logger) error {
	cfg, err := config.Load()
	if err != nil {
		return fmt.Errorf("load configuration: %w", err)
	}
	ctx, stop := signal.NotifyContext(parent, os.Interrupt, syscall.SIGTERM)
	defer stop()
	database, err := store.Open(ctx, cfg.DatabasePath)
	if err != nil {
		return err
	}
	defer database.Close()
	now := time.Now().UTC().Format(time.RFC3339Nano)
	institutions := []struct {
		id, code, name string
		kind           domain.InstitutionKind
	}{
		{"inst_qd_bank", "QD-BANK", "Qingdao Banking Inquiry Hub", domain.InstitutionBank},
		{"inst_qd_insurance", "QD-INS", "Qingdao Insurance Inquiry Hub", domain.InstitutionInsurer},
	}
	for _, institution := range institutions {
		if err := database.InsertInstitution(ctx, institution.id, institution.code, institution.name, institution.kind, now); err != nil {
			return err
		}
	}
	if err := bootstrapUsers(ctx, database); err != nil {
		return err
	}
	realClock := clock.Real{}
	generator := ids.Crypto{}
	authService := auth.New(database, realClock, generator, cfg.SessionTTL)
	caseService := cases.New(database, realClock, generator)
	inquiryService := inquiry.New(database, realClock, generator, cfg.WorkerMaxAttempts)
	claimService := claims.New(database, realClock, generator, claims.DefaultSmallClaimLimit, cfg.WorkerMaxAttempts)
	jobWorker := worker.New(database, realClock, "server-worker", cfg.WorkerPoll, cfg.WorkerLease, logger)
	if err := jobWorker.Register("dispatch_inquiry", func(ctx context.Context, job store.Job) error {
		return inquiryService.MarkDispatched(ctx, job.ResourceID, "outbox-"+job.ID)
	}); err != nil {
		return err
	}
	if err := jobWorker.Register("execute_payout", func(ctx context.Context, job store.Job) error {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
			return database.MarkPayoutSubmitted(ctx, job.ResourceID, time.Now().UTC())
		}
	}); err != nil {
		return err
	}
	workerDone := make(chan error, 1)
	go func() { workerDone <- jobWorker.Run(ctx) }()
	handler := httpapi.New(httpapi.Dependencies{Store: database, Auth: authService, Cases: caseService,
		Inquiries: inquiryService, Claims: claimService, IDs: generator, Logger: logger})
	server := &http.Server{
		Addr: cfg.HTTPAddr, Handler: handler, ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout: 15 * time.Second, WriteTimeout: 30 * time.Second, IdleTimeout: 60 * time.Second,
	}
	serverDone := make(chan error, 1)
	go func() {
		logger.Info("http server listening", "address", cfg.HTTPAddr)
		serverDone <- server.ListenAndServe()
	}()
	select {
	case err := <-serverDone:
		if !errors.Is(err, http.ErrServerClosed) {
			return err
		}
	case err := <-workerDone:
		if err != nil && !errors.Is(err, context.Canceled) {
			return err
		}
	case <-ctx.Done():
	}
	shutdownCtx, cancel := context.WithTimeout(context.Background(), cfg.ShutdownTimeout)
	defer cancel()
	if err := server.Shutdown(shutdownCtx); err != nil {
		return fmt.Errorf("shutdown http server: %w", err)
	}
	return nil
}

func bootstrapUsers(ctx context.Context, database *store.Store) error {
	password := strings.TrimSpace(os.Getenv("BOOTSTRAP_PASSWORD"))
	if password == "" {
		return nil
	}
	hash, err := auth.HashPassword(password)
	if err != nil {
		return fmt.Errorf("hash bootstrap password: %w", err)
	}
	now := time.Now().UTC()
	users := []store.User{
		{ID: "bootstrap_claimant", Email: "claimant@example.test", DisplayName: "Bootstrap Claimant", Role: domain.RoleClaimant},
		{ID: "bootstrap_officer", Email: "officer@example.test", DisplayName: "Bootstrap Officer", Role: domain.RoleOfficer},
		{ID: "bootstrap_supervisor", Email: "supervisor@example.test", DisplayName: "Bootstrap Supervisor", Role: domain.RoleSupervisor},
	}
	for _, user := range users {
		if _, err := database.UserByEmail(ctx, user.Email); err == nil {
			continue
		} else if !errors.Is(err, domain.ErrNotFound) {
			return err
		}
		user.PasswordHash = hash
		user.Active = true
		user.CreatedAt, user.UpdatedAt = now, now
		if err := database.CreateUser(ctx, user); err != nil {
			return err
		}
	}
	return nil
}
