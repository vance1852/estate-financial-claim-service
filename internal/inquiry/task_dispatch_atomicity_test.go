package inquiry

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/vance1852/estate-financial-claim-service/internal/clock"
	"github.com/vance1852/estate-financial-claim-service/internal/domain"
	"github.com/vance1852/estate-financial-claim-service/internal/ids"
	"github.com/vance1852/estate-financial-claim-service/internal/store"
)

func TestFailedInquiryDispatchLeavesNoPreparedWork(t *testing.T) {
	ctx := context.Background()
	database, err := store.Open(ctx, filepath.Join(t.TempDir(), "dispatch-atomicity.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	now := time.Date(2026, 8, 24, 12, 0, 0, 0, time.UTC)

	for _, user := range []store.User{
		{ID: "claimant-atomic", Email: "claimant-atomic@example.test", Role: domain.RoleClaimant},
		{ID: "officer-atomic", Email: "officer-atomic@example.test", Role: domain.RoleOfficer},
	} {
		user.PasswordHash = "hash"
		user.DisplayName = user.ID
		user.Active = true
		user.CreatedAt, user.UpdatedAt = now, now
		if err := database.CreateUser(ctx, user); err != nil {
			t.Fatal(err)
		}
	}
	for _, institution := range []struct {
		id   string
		kind domain.InstitutionKind
	}{
		{id: "bank-atomic", kind: domain.InstitutionBank},
		{id: "insurer-atomic", kind: domain.InstitutionInsurer},
	} {
		if err := database.InsertInstitution(ctx, institution.id, institution.id, institution.id,
			institution.kind, now.Format(time.RFC3339Nano)); err != nil {
			t.Fatal(err)
		}
	}
	caseItem := domain.EstateCase{
		ID: "case-atomic", Reference: "EST-ATOMIC", DeceasedName: "Person",
		DeceasedIDHash: "hash", DeceasedIDMasked: "***", Jurisdiction: "Qingdao",
		ClaimantUserID: "claimant-atomic", Status: domain.CaseReviewing, Version: 2,
		SubmittedAt: &now, CreatedAt: now, UpdatedAt: now,
	}
	if err := database.InsertCase(ctx, database, caseItem); err != nil {
		t.Fatal(err)
	}
	service := New(database, clock.NewManual(now), &ids.Sequence{}, 4)
	principal := domain.Principal{UserID: "officer-atomic", Role: domain.RoleOfficer}
	database.InjectFailure("audit", errors.New("audit disk unavailable"))

	created, err := service.Dispatch(ctx, principal, caseItem.ID, "dispatch-atomic-failure", caseItem.Version)
	if err == nil || !strings.Contains(err.Error(), "audit disk unavailable") {
		t.Fatalf("failed dispatch result=%#v error=%v, want audit failure", created, err)
	}
	for _, table := range []string{"inquiries", "worker_jobs", "audit_events"} {
		var count int
		if err := database.QueryRowContext(ctx, "SELECT COUNT(*) FROM "+table).Scan(&count); err != nil {
			t.Fatal(err)
		}
		if count != 0 {
			t.Errorf("failed dispatch persisted %d row(s) in %s", count, table)
		}
	}
	unchanged, err := database.CaseByID(ctx, database, caseItem.ID)
	if err != nil {
		t.Fatal(err)
	}
	if unchanged.Status != domain.CaseReviewing || unchanged.Version != caseItem.Version {
		t.Errorf("case changed after failed dispatch: %#v", unchanged)
	}

	created, err = service.Dispatch(ctx, principal, caseItem.ID, "dispatch-atomic-success", caseItem.Version)
	if err != nil {
		t.Fatalf("healthy dispatch failed after rollback: %v", err)
	}
	if len(created) != 2 {
		t.Fatalf("healthy dispatch created %d inquiries, want 2", len(created))
	}
	var jobs int
	if err := database.QueryRowContext(ctx, "SELECT COUNT(*) FROM worker_jobs").Scan(&jobs); err != nil {
		t.Fatal(err)
	}
	if jobs != 2 {
		t.Fatalf("healthy dispatch created %d jobs, want 2", jobs)
	}
}
