package cases

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/vance1852/estate-financial-claim-service/internal/clock"
	"github.com/vance1852/estate-financial-claim-service/internal/domain"
	"github.com/vance1852/estate-financial-claim-service/internal/ids"
	"github.com/vance1852/estate-financial-claim-service/internal/store"
)

func TestCanceledCaseSubmissionDoesNotPersist(t *testing.T) {
	database, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "canceled-submit.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })

	now := time.Date(2026, 8, 24, 14, 0, 0, 0, time.UTC)
	user := store.User{
		ID: "canceled_claimant", Email: "canceled@example.test", PasswordHash: "hash",
		DisplayName: "Canceled Claimant", Role: domain.RoleClaimant, Active: true,
		CreatedAt: now, UpdatedAt: now,
	}
	if err := database.CreateUser(context.Background(), user); err != nil {
		t.Fatal(err)
	}
	service := New(database, clock.NewManual(now), &ids.Sequence{})
	principal := domain.Principal{UserID: user.ID, Role: domain.RoleClaimant}
	input := SubmitInput{
		Deceased: domain.PersonIdentity{Name: "Canceled Deceased", IDNumber: "37020019500101001X"},
		Claimant: domain.PersonIdentity{Name: "Canceled Claimant", IDNumber: "370200198001010019"},
		Relation: domain.RelationChild, Jurisdiction: "Qingdao", IdempotencyKey: "canceled-submit-0001",
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	result, err := service.Submit(ctx, principal, input)
	if !errors.Is(err, context.Canceled) {
		t.Errorf("canceled submission result=%#v error=%v, want context.Canceled", result, err)
	}

	for _, table := range []string{
		"estate_cases", "parties", "case_parties", "documents", "idempotency_keys", "audit_events",
	} {
		var count int
		if err := database.QueryRowContext(context.Background(), "SELECT COUNT(*) FROM "+table).Scan(&count); err != nil {
			t.Fatal(err)
		}
		if count != 0 {
			t.Errorf("canceled submission persisted %d row(s) in %s", count, table)
		}
	}

	input.IdempotencyKey = "active-submit-0001"
	active, err := service.Submit(context.Background(), principal, input)
	if err != nil {
		t.Fatalf("active submission failed after cancellation cleanup: %v", err)
	}
	if active.Case.ID == "" || active.Case.Status != domain.CaseSubmitted {
		t.Fatalf("active submission result=%#v", active)
	}
}
