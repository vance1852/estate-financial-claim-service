package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/vance1852/estate-financial-claim-service/internal/audit"
	"github.com/vance1852/estate-financial-claim-service/internal/domain"
)

func openTestStore(t *testing.T) *Store {
	t.Helper()
	database, err := Open(context.Background(), filepath.Join(t.TempDir(), "estate-test.db"))
	if err != nil {
		t.Fatalf("open test store: %v", err)
	}
	t.Cleanup(func() {
		if err := database.Close(); err != nil {
			t.Errorf("close test store: %v", err)
		}
	})
	return database
}

func testUser(id, email string, role domain.Role, now time.Time) User {
	return User{ID: id, Email: email, PasswordHash: "hash", DisplayName: id,
		Role: role, Active: true, CreatedAt: now, UpdatedAt: now}
}

func TestMigrationsCreateExpectedRelationalSchema(t *testing.T) {
	database := openTestStore(t)
	ctx := context.Background()
	rows, err := database.db.QueryContext(ctx, `SELECT name FROM sqlite_master
		WHERE type='table' AND name NOT LIKE 'sqlite_%' ORDER BY name`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var tables []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			t.Fatal(err)
		}
		tables = append(tables, name)
	}
	want := []string{"audit_events", "case_parties", "claim_accounts", "claims", "documents",
		"estate_cases", "financial_accounts", "idempotency_keys", "inquiries", "inquiry_results",
		"institutions", "parties", "payouts", "schema_metadata", "schema_migrations",
		"sessions", "users", "worker_jobs"}
	sort.Strings(want)
	if strings.Join(tables, ",") != strings.Join(want, ",") {
		t.Fatalf("tables = %v, want %v", tables, want)
	}
	var versions int
	if err := database.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM schema_migrations").Scan(&versions); err != nil {
		t.Fatal(err)
	}
	if versions != 2 {
		t.Fatalf("migration count = %d, want 2", versions)
	}
	var foreignKeys int
	if err := database.db.QueryRowContext(ctx, "PRAGMA foreign_keys").Scan(&foreignKeys); err != nil {
		t.Fatal(err)
	}
	if foreignKeys != 1 {
		t.Fatalf("foreign_keys = %d, want 1", foreignKeys)
	}
	if err := database.Migrate(ctx); err != nil {
		t.Fatalf("repeated migration must be idempotent: %v", err)
	}
}

func TestOpenRejectsFutureSchemaVersion(t *testing.T) {
	path := filepath.Join(t.TempDir(), "future.db")
	database, err := Open(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := database.db.Exec(`INSERT INTO schema_migrations(version,name,applied_at)
		VALUES(99,'future.sql','2026-08-24T00:00:00Z')`); err != nil {
		t.Fatal(err)
	}
	if err := database.Close(); err != nil {
		t.Fatal(err)
	}
	_, err = Open(context.Background(), path)
	if err == nil || !strings.Contains(err.Error(), "newer than supported") {
		t.Fatalf("future schema error = %v", err)
	}
}

func TestPersistenceSurvivesCloseAndReopen(t *testing.T) {
	path := filepath.Join(t.TempDir(), "restart.db")
	ctx := context.Background()
	first, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 8, 24, 1, 2, 3, 0, time.UTC)
	if err := first.CreateUser(ctx, testUser("user_restart", "restart@example.test", domain.RoleClaimant, now)); err != nil {
		t.Fatal(err)
	}
	if err := first.Close(); err != nil {
		t.Fatal(err)
	}
	second, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer second.Close()
	user, err := second.UserByEmail(ctx, "RESTART@example.test")
	if err != nil {
		t.Fatal(err)
	}
	if user.ID != "user_restart" || user.Role != domain.RoleClaimant || !user.CreatedAt.Equal(now) {
		t.Fatalf("recovered user = %#v", user)
	}
	if err := second.Ping(ctx); err != nil {
		t.Fatalf("reopened store not ready: %v", err)
	}
}

func TestWithTxCommitsOrRollsBackAllWrites(t *testing.T) {
	database := openTestStore(t)
	ctx := context.Background()
	now := time.Now().UTC()
	err := database.WithTx(ctx, func(tx *sql.Tx) error {
		if err := database.SeedUserTx(ctx, tx, testUser("commit", "commit@example.test", domain.RoleOfficer, now)); err != nil {
			return err
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := database.UserByID(ctx, "commit"); err != nil {
		t.Fatalf("committed user missing: %v", err)
	}
	sentinel := errors.New("stop transaction")
	err = database.WithTx(ctx, func(tx *sql.Tx) error {
		if err := database.SeedUserTx(ctx, tx, testUser("rollback", "rollback@example.test", domain.RoleOfficer, now)); err != nil {
			return err
		}
		return sentinel
	})
	if !errors.Is(err, sentinel) {
		t.Fatalf("rollback error = %v", err)
	}
	if _, err := database.UserByID(ctx, "rollback"); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("rolled back user lookup = %v", err)
	}
}

func TestSessionLifecycleQueries(t *testing.T) {
	database := openTestStore(t)
	ctx := context.Background()
	now := time.Date(2026, 8, 24, 10, 0, 0, 0, time.UTC)
	if err := database.CreateUser(ctx, testUser("claimant", "claimant@example.test", domain.RoleClaimant, now)); err != nil {
		t.Fatal(err)
	}
	session := Session{ID: "ses_1", UserID: "claimant", TokenHash: "hash_1",
		ExpiresAt: now.Add(time.Hour), CreatedAt: now, LastSeenAt: now}
	if err := database.CreateSession(ctx, session); err != nil {
		t.Fatal(err)
	}
	loaded, principal, err := database.SessionPrincipal(ctx, "hash_1", now.Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if loaded.ID != session.ID || principal.UserID != "claimant" || principal.Role != domain.RoleClaimant {
		t.Fatalf("loaded session/principal = %#v %#v", loaded, principal)
	}
	if err := database.TouchSession(ctx, session.ID, now.Add(2*time.Minute)); err != nil {
		t.Fatal(err)
	}
	if err := database.RevokeSession(ctx, "hash_1", now.Add(3*time.Minute)); err != nil {
		t.Fatal(err)
	}
	if _, _, err := database.SessionPrincipal(ctx, "hash_1", now.Add(4*time.Minute)); !errors.Is(err, domain.ErrUnauthorized) {
		t.Fatalf("revoked session error = %v", err)
	}
	if err := database.RevokeSession(ctx, "hash_1", now.Add(5*time.Minute)); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("second revoke error = %v", err)
	}
}

func TestExpiredAndInactiveSessionsAreRejected(t *testing.T) {
	database := openTestStore(t)
	ctx := context.Background()
	now := time.Date(2026, 8, 24, 10, 0, 0, 0, time.UTC)
	user := testUser("inactive", "inactive@example.test", domain.RoleOfficer, now)
	if err := database.CreateUser(ctx, user); err != nil {
		t.Fatal(err)
	}
	if err := database.CreateSession(ctx, Session{ID: "expired", UserID: user.ID, TokenHash: "expired_hash",
		ExpiresAt: now, CreatedAt: now.Add(-time.Hour), LastSeenAt: now.Add(-time.Hour)}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := database.SessionPrincipal(ctx, "expired_hash", now); !errors.Is(err, domain.ErrExpired) {
		t.Fatalf("expired session error = %v", err)
	}
	if _, err := database.db.ExecContext(ctx, "UPDATE users SET active=0 WHERE id=?", user.ID); err != nil {
		t.Fatal(err)
	}
	if err := database.CreateSession(ctx, Session{ID: "inactive_session", UserID: user.ID, TokenHash: "inactive_hash",
		ExpiresAt: now.Add(time.Hour), CreatedAt: now, LastSeenAt: now}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := database.SessionPrincipal(ctx, "inactive_hash", now); !errors.Is(err, domain.ErrUnauthorized) {
		t.Fatalf("inactive user session error = %v", err)
	}
	deleted, err := database.DeleteExpiredSessions(ctx, now.Add(time.Second))
	if err != nil {
		t.Fatal(err)
	}
	if deleted != 1 {
		t.Fatalf("deleted sessions = %d, want 1", deleted)
	}
}

func TestOptimisticCaseUpdateAllowsOneWinner(t *testing.T) {
	database := openTestStore(t)
	ctx := context.Background()
	now := time.Now().UTC()
	if err := database.CreateUser(ctx, testUser("owner", "owner@example.test", domain.RoleClaimant, now)); err != nil {
		t.Fatal(err)
	}
	item := domain.EstateCase{ID: "case_optimistic", Reference: "EST-1", DeceasedName: "Li Ming",
		DeceasedIDHash: "hash", DeceasedIDMasked: "***", Jurisdiction: "Qingdao",
		ClaimantUserID: "owner", Status: domain.CaseSubmitted, Version: 1,
		SubmittedAt: &now, CreatedAt: now, UpdatedAt: now}
	if err := database.InsertCase(ctx, database, item); err != nil {
		t.Fatal(err)
	}
	start := make(chan struct{})
	results := make(chan error, 2)
	var group sync.WaitGroup
	for index := 0; index < 2; index++ {
		group.Add(1)
		go func() {
			defer group.Done()
			candidate := item
			candidate.Status = domain.CaseReviewing
			candidate.Version = 2
			candidate.UpdatedAt = now.Add(time.Minute)
			<-start
			results <- database.UpdateCase(ctx, database, candidate, 1)
		}()
	}
	close(start)
	group.Wait()
	close(results)
	var success, conflicts int
	for err := range results {
		switch {
		case err == nil:
			success++
		case errors.Is(err, domain.ErrConflict):
			conflicts++
		default:
			t.Fatalf("unexpected update error: %v", err)
		}
	}
	if success != 1 || conflicts != 1 {
		t.Fatalf("success=%d conflicts=%d", success, conflicts)
	}
}

func TestIdempotencyReplayBindingAndExpiry(t *testing.T) {
	database := openTestStore(t)
	ctx := context.Background()
	now := time.Now().UTC()
	if err := database.CreateUser(ctx, testUser("actor", "actor@example.test", domain.RoleClaimant, now)); err != nil {
		t.Fatal(err)
	}
	record := IdempotencyRecord{Scope: "case_submit", Key: "request-123", ActorID: "actor",
		Method: "POST", Route: "/v1/cases", RequestHash: "payload-hash", StatusCode: 201,
		ResponseBody: []byte(`{"case_id":"case_1"}`), ResourceID: "case_1",
		ExpiresAt: now.Add(time.Hour), CreatedAt: now}
	if err := database.PutIdempotency(ctx, database, record); err != nil {
		t.Fatal(err)
	}
	loaded, err := database.GetIdempotency(ctx, database, record.Scope, record.Key, now)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.ResourceID != "case_1" || string(loaded.ResponseBody) != string(record.ResponseBody) {
		t.Fatalf("loaded record = %#v", loaded)
	}
	if err := ValidateReplay(loaded, "actor", "POST", "/v1/cases", "payload-hash"); err != nil {
		t.Fatalf("valid replay: %v", err)
	}
	for _, mismatch := range []struct{ actor, method, route, hash string }{
		{"other", "POST", "/v1/cases", "payload-hash"},
		{"actor", "PUT", "/v1/cases", "payload-hash"},
		{"actor", "POST", "/v1/claims", "payload-hash"},
		{"actor", "POST", "/v1/cases", "other-hash"},
	} {
		if !errors.Is(ValidateReplay(loaded, mismatch.actor, mismatch.method, mismatch.route, mismatch.hash), domain.ErrConflict) {
			t.Errorf("mismatch unexpectedly accepted: %#v", mismatch)
		}
	}
	if _, err := database.GetIdempotency(ctx, database, record.Scope, record.Key, now.Add(2*time.Hour)); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("expired record error = %v", err)
	}
}

func TestAuditFailureInjectionIsOneShotAndTransactional(t *testing.T) {
	database := openTestStore(t)
	ctx := context.Background()
	now := time.Now().UTC()
	event := audit.Event{ActorID: "actor", Action: "test.action", ObjectType: "test",
		ObjectID: "object", Result: "success", RequestID: "req", CreatedAt: now}
	restore := database.InjectFailure("audit", errors.New("disk full"))
	defer restore()
	err := database.WithTx(ctx, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, `INSERT INTO schema_metadata(key,value,updated_at) VALUES('transient','x',?)`, formatTime(now)); err != nil {
			return err
		}
		return database.InsertAudit(ctx, tx, event)
	})
	if err == nil || !strings.Contains(err.Error(), "disk full") {
		t.Fatalf("injected error = %v", err)
	}
	var count int
	if err := database.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM schema_metadata WHERE key='transient'`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatalf("transactional row survived audit failure: %d", count)
	}
	if err := database.InsertAudit(ctx, database, event); err != nil {
		t.Fatalf("one-shot failure affected next write: %v", err)
	}
	records, err := database.AuditForObject(ctx, "test", "object", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(records) != 1 || records[0].RequestID != "req" {
		t.Fatalf("audit records = %#v", records)
	}
}

func TestWorkerJobLeaseRecoveryAndTerminalFailure(t *testing.T) {
	database := openTestStore(t)
	ctx := context.Background()
	now := time.Date(2026, 8, 24, 10, 0, 0, 0, time.UTC)
	job := Job{ID: "job_1", Kind: "dispatch", ResourceID: "inq_1", Payload: []byte(`{}`),
		Status: "pending", MaxAttempts: 2, AvailableAt: now, CreatedAt: now, UpdatedAt: now}
	if err := database.InsertJob(ctx, database, job); err != nil {
		t.Fatal(err)
	}
	claimed, err := database.ClaimJob(ctx, "worker-a", now, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if claimed.Status != "running" || claimed.Attempts != 1 || claimed.LeaseOwner != "worker-a" {
		t.Fatalf("claimed job = %#v", claimed)
	}
	if _, err := database.ClaimJob(ctx, "worker-b", now, time.Minute); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("second claim error = %v", err)
	}
	recovered, err := database.RecoverExpiredJobs(ctx, now.Add(2*time.Minute))
	if err != nil || recovered != 1 {
		t.Fatalf("recovery count=%d err=%v", recovered, err)
	}
	claimed, err = database.ClaimJob(ctx, "worker-b", now.Add(2*time.Minute), time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if claimed.Attempts != 2 {
		t.Fatalf("attempts = %d, want 2", claimed.Attempts)
	}
	if err := database.FailJob(ctx, claimed, "worker-b", now.Add(2*time.Minute), now.Add(3*time.Minute), errors.New("permanent")); err != nil {
		t.Fatal(err)
	}
	var status, lastError string
	if err := database.db.QueryRowContext(ctx, "SELECT status,last_error FROM worker_jobs WHERE id=?", job.ID).Scan(&status, &lastError); err != nil {
		t.Fatal(err)
	}
	if status != "failed" || lastError != "permanent" {
		t.Fatalf("terminal job status=%s error=%s", status, lastError)
	}
}

func TestForeignKeysRejectOrphanRecords(t *testing.T) {
	database := openTestStore(t)
	_, err := database.db.Exec(`INSERT INTO sessions
		(id,user_id,token_hash,expires_at,created_at,last_seen_at)
		VALUES('orphan','missing','hash','2026-08-25T00:00:00Z','2026-08-24T00:00:00Z','2026-08-24T00:00:00Z')`)
	if err == nil || !strings.Contains(strings.ToLower(err.Error()), "foreign key") {
		t.Fatalf("orphan insert error = %v", err)
	}
}

func TestUnknownUserAndCaseMapToDomainNotFound(t *testing.T) {
	database := openTestStore(t)
	ctx := context.Background()
	checks := []struct {
		name string
		call func() error
	}{
		{"user by id", func() error { _, err := database.UserByID(ctx, "missing"); return err }},
		{"user by email", func() error { _, err := database.UserByEmail(ctx, "missing@example.test"); return err }},
		{"case by id", func() error { _, err := database.CaseByID(ctx, database, "missing"); return err }},
		{"claim by id", func() error { _, err := database.ClaimByID(ctx, database, "missing"); return err }},
		{"inquiry by id", func() error { _, err := database.InquiryByID(ctx, database, "missing"); return err }},
	}
	for _, check := range checks {
		t.Run(check.name, func(t *testing.T) {
			if err := check.call(); !errors.Is(err, domain.ErrNotFound) {
				t.Fatalf("error = %v", err)
			}
		})
	}
}

func ExampleValidateReplay() {
	record := IdempotencyRecord{ActorID: "user_1", Method: "POST", Route: "/v1/cases", RequestHash: "abc"}
	fmt.Println(ValidateReplay(record, "user_1", "POST", "/v1/cases", "abc"))
	fmt.Println(errors.Is(ValidateReplay(record, "user_2", "POST", "/v1/cases", "abc"), domain.ErrConflict))
	// Output:
	// <nil>
	// true
}
