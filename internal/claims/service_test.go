package claims

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

type claimFixture struct {
	service    *Service
	store      *store.Store
	claimant   domain.Principal
	supervisor domain.Principal
	officer    domain.Principal
	caseID     string
	accountIDs []string
	clock      *clock.Manual
}

func newClaimFixture(t *testing.T) claimFixture {
	t.Helper()
	ctx := context.Background()
	database, err := store.Open(ctx, filepath.Join(t.TempDir(), "claims.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	now := time.Date(2026, 8, 24, 10, 0, 0, 0, time.UTC)
	for _, user := range []store.User{
		{ID: "claimant", Email: "claimant@example.test", Role: domain.RoleClaimant},
		{ID: "supervisor", Email: "supervisor@example.test", Role: domain.RoleSupervisor},
		{ID: "officer", Email: "officer@example.test", Role: domain.RoleOfficer},
	} {
		user.PasswordHash, user.DisplayName, user.Active = "hash", user.ID, true
		user.CreatedAt, user.UpdatedAt = now, now
		if err := database.CreateUser(ctx, user); err != nil {
			t.Fatal(err)
		}
	}
	if err := database.InsertInstitution(ctx, "bank", "BANK", "Bank", domain.InstitutionBank, now.Format(time.RFC3339Nano)); err != nil {
		t.Fatal(err)
	}
	caseItem := domain.EstateCase{ID: "case", Reference: "EST", DeceasedName: "Person", DeceasedIDHash: "hash",
		DeceasedIDMasked: "***", Jurisdiction: "Qingdao", ClaimantUserID: "claimant", Status: domain.CaseEligible,
		Version: 4, SubmittedAt: &now, InquiryCompletedAt: &now, CreatedAt: now, UpdatedAt: now}
	if err := database.InsertCase(ctx, database, caseItem); err != nil {
		t.Fatal(err)
	}
	inquiry := domain.Inquiry{ID: "inquiry", CaseID: caseItem.ID, InstitutionID: "bank", RequestKey: "request",
		Status: domain.InquiryCompleted, ExternalRef: "external", ExpectedParts: 1, ReceivedParts: 1,
		Version: 3, DispatchedAt: &now, CompletedAt: &now, CreatedAt: now, UpdatedAt: now}
	if err := database.InsertInquiry(ctx, database, inquiry); err != nil {
		t.Fatal(err)
	}
	accounts := []domain.FinancialAccount{
		{ID: "acct_1", BalanceMinor: 200_000, Kind: domain.AccountDeposit, Currency: "CNY"},
		{ID: "acct_2", BalanceMinor: 300_000, Kind: domain.AccountDeposit, Currency: "CNY"},
	}
	for _, account := range accounts {
		account.CaseID, account.InstitutionID, account.InquiryID = caseItem.ID, "bank", inquiry.ID
		account.ExternalHash, account.Version = "hash_"+account.ID, 1
		account.CreatedAt, account.UpdatedAt = now, now
		if err := database.UpsertFinancialAccount(ctx, database, account); err != nil {
			t.Fatal(err)
		}
	}
	manual := clock.NewManual(now)
	return claimFixture{service: New(database, manual, &ids.Sequence{}, DefaultSmallClaimLimit, 5), store: database,
		claimant:   domain.Principal{UserID: "claimant", Role: domain.RoleClaimant},
		supervisor: domain.Principal{UserID: "supervisor", Role: domain.RoleSupervisor},
		officer:    domain.Principal{UserID: "officer", Role: domain.RoleOfficer}, caseID: caseItem.ID,
		accountIDs: []string{"acct_1", "acct_2"}, clock: manual}
}

func TestCreateClaimValidatesEligibilityAndTransitionsCase(t *testing.T) {
	fixture := newClaimFixture(t)
	claim, err := fixture.service.Create(context.Background(), fixture.claimant, fixture.caseID, fixture.accountIDs)
	if err != nil {
		t.Fatal(err)
	}
	if claim.Status != domain.ClaimPending || claim.TotalMinor != 500_000 || claim.Currency != "CNY" {
		t.Fatalf("claim = %#v", claim)
	}
	caseItem, err := fixture.store.CaseByID(context.Background(), fixture.store, fixture.caseID)
	if err != nil {
		t.Fatal(err)
	}
	if caseItem.Status != domain.CaseClaiming || caseItem.Version != 5 {
		t.Fatalf("case = %#v", caseItem)
	}
	var links, audits int
	if err := fixture.store.QueryRowContext(context.Background(), "SELECT COUNT(*) FROM claim_accounts WHERE claim_id=?", claim.ID).Scan(&links); err != nil {
		t.Fatal(err)
	}
	if err := fixture.store.QueryRowContext(context.Background(), "SELECT COUNT(*) FROM audit_events WHERE object_id=?", claim.ID).Scan(&audits); err != nil {
		t.Fatal(err)
	}
	if links != 2 || audits != 1 {
		t.Fatalf("links=%d audits=%d", links, audits)
	}
	if _, err := fixture.service.Create(context.Background(), fixture.claimant, fixture.caseID, fixture.accountIDs); !errors.Is(err, domain.ErrInvalidState) {
		t.Fatalf("second claim error = %v", err)
	}
}

func TestCreateClaimRejectsOwnershipRoleSelectionAndLimits(t *testing.T) {
	fixture := newClaimFixture(t)
	if _, err := fixture.service.Create(context.Background(), fixture.officer, fixture.caseID, fixture.accountIDs); !errors.Is(err, domain.ErrForbidden) {
		t.Fatalf("officer create error = %v", err)
	}
	if _, err := fixture.service.Create(context.Background(), fixture.claimant, fixture.caseID, nil); !errors.Is(err, domain.ErrValidation) {
		t.Fatalf("empty accounts error = %v", err)
	}
	if _, err := fixture.service.Create(context.Background(), fixture.claimant, fixture.caseID, []string{"missing"}); !errors.Is(err, domain.ErrValidation) {
		t.Fatalf("foreign account error = %v", err)
	}
	if _, err := fixture.store.ExecContext(context.Background(), "UPDATE financial_accounts SET restricted=1 WHERE id='acct_1'"); err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.service.Create(context.Background(), fixture.claimant, fixture.caseID, []string{"acct_1"}); !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("restricted account error = %v", err)
	}
}

func TestApproveAtomicallyReservesAccountsCreatesPayoutAndJob(t *testing.T) {
	fixture := newClaimFixture(t)
	claim, err := fixture.service.Create(context.Background(), fixture.claimant, fixture.caseID, fixture.accountIDs)
	if err != nil {
		t.Fatal(err)
	}
	payout, err := fixture.service.Approve(context.Background(), fixture.supervisor, claim.ID, "payout-key-0001", 1)
	if err != nil {
		t.Fatal(err)
	}
	if payout.Status != domain.PayoutPending || payout.AmountMinor != claim.TotalMinor || payout.ClaimID != claim.ID {
		t.Fatalf("payout = %#v", payout)
	}
	loaded, err := fixture.store.ClaimByID(context.Background(), fixture.store, claim.ID)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Status != domain.ClaimApproved || loaded.ApprovedBy != fixture.supervisor.UserID || loaded.Version != 2 {
		t.Fatalf("approved claim = %#v", loaded)
	}
	accounts, err := fixture.store.AccountsForCase(context.Background(), fixture.store, fixture.caseID)
	if err != nil {
		t.Fatal(err)
	}
	for _, account := range accounts {
		if account.ReservedClaimID != claim.ID {
			t.Errorf("account reservation = %#v", account)
		}
	}
	var jobs int
	if err := fixture.store.QueryRowContext(context.Background(), "SELECT COUNT(*) FROM worker_jobs WHERE resource_id=?", payout.ID).Scan(&jobs); err != nil {
		t.Fatal(err)
	}
	if jobs != 1 {
		t.Fatalf("payout jobs = %d", jobs)
	}
}

func TestApproveRejectsWrongRoleStaleVersionAndReservationConflict(t *testing.T) {
	fixture := newClaimFixture(t)
	claim, err := fixture.service.Create(context.Background(), fixture.claimant, fixture.caseID, fixture.accountIDs)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.service.Approve(context.Background(), fixture.officer, claim.ID, "payout-key-0002", 1); !errors.Is(err, domain.ErrForbidden) {
		t.Fatalf("officer approve error = %v", err)
	}
	if _, err := fixture.service.Approve(context.Background(), fixture.supervisor, claim.ID, "short", 1); !errors.Is(err, domain.ErrValidation) {
		t.Fatalf("short key error = %v", err)
	}
	if _, err := fixture.service.Approve(context.Background(), fixture.supervisor, claim.ID, "payout-key-0003", 99); !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("stale version error = %v", err)
	}
	if _, err := fixture.store.ExecContext(context.Background(), "UPDATE financial_accounts SET reserved_claim_id='other_claim' WHERE id='acct_1'"); err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.service.Approve(context.Background(), fixture.supervisor, claim.ID, "payout-key-0004", 1); !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("reservation conflict error = %v", err)
	}
	var payouts int
	if err := fixture.store.QueryRowContext(context.Background(), "SELECT COUNT(*) FROM payouts").Scan(&payouts); err != nil {
		t.Fatal(err)
	}
	if payouts != 0 {
		t.Fatalf("partial payout survived conflict: %d", payouts)
	}
}

func TestConfirmedPayoutIsTerminalAndClosesClaimPayment(t *testing.T) {
	fixture := newClaimFixture(t)
	claim, err := fixture.service.Create(context.Background(), fixture.claimant, fixture.caseID, fixture.accountIDs)
	if err != nil {
		t.Fatal(err)
	}
	payout, err := fixture.service.Approve(context.Background(), fixture.supervisor, claim.ID, "payout-key-confirm", 1)
	if err != nil {
		t.Fatal(err)
	}
	if err := fixture.store.MarkPayoutSubmitted(context.Background(), payout.ID, fixture.clock.Now()); err != nil {
		t.Fatal(err)
	}
	if err := fixture.store.MarkPayoutSubmitted(context.Background(), payout.ID, fixture.clock.Now()); err != nil {
		t.Fatalf("submitted payout replay: %v", err)
	}
	if err := fixture.store.ConfirmPayout(context.Background(), payout.ID, "provider-confirmed", fixture.clock.Now().Format(time.RFC3339Nano)); err != nil {
		t.Fatal(err)
	}
	loadedPayout, err := fixture.store.PayoutByClaim(context.Background(), claim.ID)
	if err != nil {
		t.Fatal(err)
	}
	if loadedPayout.Status != domain.PayoutConfirmed || loadedPayout.ProviderRef != "provider-confirmed" {
		t.Fatalf("confirmed payout = %#v", loadedPayout)
	}
	if err := fixture.store.MarkPayoutSubmitted(context.Background(), payout.ID, fixture.clock.Now()); err != nil {
		t.Fatalf("confirmed payout replay: %v", err)
	}
	loadedClaim, err := fixture.store.ClaimByID(context.Background(), fixture.store, claim.ID)
	if err != nil {
		t.Fatal(err)
	}
	if loadedClaim.Status != domain.ClaimPaid {
		t.Fatalf("claim status = %s", loadedClaim.Status)
	}
	if _, err := fixture.store.ExecContext(context.Background(), "UPDATE payouts SET status='pending' WHERE id=?", payout.ID); err == nil {
		t.Fatal("confirmed payout trigger allowed reopening")
	}
}

func TestConfirmPayoutReplaysSameReferenceAndRejectsDifferentReference(t *testing.T) {
	fixture := newClaimFixture(t)
	claim, err := fixture.service.Create(context.Background(), fixture.claimant, fixture.caseID, fixture.accountIDs)
	if err != nil {
		t.Fatal(err)
	}
	payout, err := fixture.service.Approve(context.Background(), fixture.supervisor, claim.ID, "payout-key-replay", 1)
	if err != nil {
		t.Fatal(err)
	}
	if err := fixture.store.MarkPayoutSubmitted(context.Background(), payout.ID, fixture.clock.Now()); err != nil {
		t.Fatal(err)
	}
	originalAt := fixture.clock.Now().Format(time.RFC3339Nano)
	if err := fixture.store.ConfirmPayout(context.Background(), payout.ID, "provider-42", originalAt); err != nil {
		t.Fatalf("first confirm: %v", err)
	}
	// A replay with the same provider reference must succeed even though the
	// payout and claim are already completed (e.g. after a network timeout).
	if err := fixture.store.ConfirmPayout(context.Background(), payout.ID, "provider-42", originalAt); err != nil {
		t.Fatalf("same-reference replay: %v", err)
	}
	// A replay carrying a different provider reference must still be rejected so
	// an unrelated confirmation is never attributed to this payout.
	if err := fixture.store.ConfirmPayout(context.Background(), payout.ID, "provider-99", originalAt); !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("different-reference replay error = %v", err)
	}
	loadedPayout, err := fixture.store.PayoutByClaim(context.Background(), claim.ID)
	if err != nil {
		t.Fatal(err)
	}
	if loadedPayout.Status != domain.PayoutConfirmed || loadedPayout.ProviderRef != "provider-42" {
		t.Fatalf("confirmed payout = %#v", loadedPayout)
	}
	loadedClaim, err := fixture.store.ClaimByID(context.Background(), fixture.store, claim.ID)
	if err != nil {
		t.Fatal(err)
	}
	if loadedClaim.Status != domain.ClaimPaid {
		t.Fatalf("claim status = %s", loadedClaim.Status)
	}
}
