package store

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/vance1852/estate-financial-claim-service/internal/domain"
)

func TestPayoutConfirmationReplayIsIdempotent(t *testing.T) {
	db := openTestStore(t)
	ctx := context.Background()
	now := time.Date(2026, 8, 24, 12, 0, 0, 0, time.UTC)
	if err := db.CreateUser(ctx, testUser("claimant", "claimant@replay.test", domain.RoleClaimant, now)); err != nil {
		t.Fatal(err)
	}
	caseItem := domain.EstateCase{ID: "case", Reference: "EST-R", DeceasedName: "Person", DeceasedIDHash: "hash", DeceasedIDMasked: "***", Jurisdiction: "Qingdao", ClaimantUserID: "claimant", Status: domain.CaseClaiming, Version: 1, CreatedAt: now, UpdatedAt: now}
	if err := db.InsertCase(ctx, db, caseItem); err != nil {
		t.Fatal(err)
	}
	claim := domain.Claim{ID: "claim", CaseID: "case", ClaimantUserID: "claimant", Status: domain.ClaimApproved, TotalMinor: 100, Currency: "CNY", Version: 1, CreatedAt: now, UpdatedAt: now}
	if err := db.InsertClaim(ctx, db, claim); err != nil {
		t.Fatal(err)
	}
	payout := domain.Payout{ID: "payout", ClaimID: "claim", IdempotencyKey: "payout-key", Status: domain.PayoutPending, AmountMinor: 100, Currency: "CNY", CreatedAt: now, UpdatedAt: now}
	if err := db.InsertPayout(ctx, db, payout); err != nil {
		t.Fatal(err)
	}
	if err := db.MarkPayoutSubmitted(ctx, payout.ID, now); err != nil {
		t.Fatal(err)
	}
	if err := db.ConfirmPayout(ctx, payout.ID, "provider-42", now.Format(time.RFC3339Nano)); err != nil {
		t.Fatal(err)
	}
	if err := db.ConfirmPayout(ctx, payout.ID, "provider-42", now.Format(time.RFC3339Nano)); err != nil {
		t.Fatalf("same provider confirmation replay failed: %v", err)
	}
	if err := db.ConfirmPayout(ctx, payout.ID, "provider-other", now.Format(time.RFC3339Nano)); !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("changed provider reference error = %v", err)
	}
}
