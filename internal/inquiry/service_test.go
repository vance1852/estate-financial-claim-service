package inquiry

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

type inquiryFixture struct {
	service *Service
	store   *store.Store
	clock   *clock.Manual
	officer domain.Principal
	caseID  string
}

func newInquiryFixture(t *testing.T) inquiryFixture {
	t.Helper()
	ctx := context.Background()
	database, err := store.Open(ctx, filepath.Join(t.TempDir(), "inquiry.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	now := time.Date(2026, 8, 24, 9, 0, 0, 0, time.UTC)
	for _, user := range []store.User{
		{ID: "claimant", Email: "claimant@example.test", Role: domain.RoleClaimant},
		{ID: "officer", Email: "officer@example.test", Role: domain.RoleOfficer},
	} {
		user.PasswordHash, user.DisplayName, user.Active = "hash", user.ID, true
		user.CreatedAt, user.UpdatedAt = now, now
		if err := database.CreateUser(ctx, user); err != nil {
			t.Fatal(err)
		}
	}
	for _, institution := range []struct {
		id, code string
		kind     domain.InstitutionKind
	}{
		{"bank_1", "BANK-1", domain.InstitutionBank}, {"insurer_1", "INS-1", domain.InstitutionInsurer},
	} {
		if err := database.InsertInstitution(ctx, institution.id, institution.code, institution.code, institution.kind, now.Format(time.RFC3339Nano)); err != nil {
			t.Fatal(err)
		}
	}
	caseItem := domain.EstateCase{ID: "case_1", Reference: "EST-1", DeceasedName: "Person",
		DeceasedIDHash: "hash", DeceasedIDMasked: "***", Jurisdiction: "Qingdao", ClaimantUserID: "claimant",
		Status: domain.CaseReviewing, Version: 2, SubmittedAt: &now, CreatedAt: now, UpdatedAt: now}
	if err := database.InsertCase(ctx, database, caseItem); err != nil {
		t.Fatal(err)
	}
	manual := clock.NewManual(now)
	return inquiryFixture{service: New(database, manual, &ids.Sequence{}, 4), store: database,
		clock: manual, officer: domain.Principal{UserID: "officer", Role: domain.RoleOfficer}, caseID: caseItem.ID}
}

func TestDispatchCreatesInquiryJobPairsAndTransitionsCase(t *testing.T) {
	fixture := newInquiryFixture(t)
	items, err := fixture.service.Dispatch(context.Background(), fixture.officer, fixture.caseID, "request-key-0001", 2)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 2 {
		t.Fatalf("inquiries = %d, want 2", len(items))
	}
	for _, item := range items {
		if item.Status != domain.InquiryPending || item.CaseID != fixture.caseID || item.Version != 1 {
			t.Fatalf("inquiry = %#v", item)
		}
	}
	var jobs, audits int
	if err := fixture.store.QueryRowContext(context.Background(), "SELECT COUNT(*) FROM worker_jobs").Scan(&jobs); err != nil {
		t.Fatal(err)
	}
	if err := fixture.store.QueryRowContext(context.Background(), "SELECT COUNT(*) FROM audit_events WHERE object_id=?", fixture.caseID).Scan(&audits); err != nil {
		t.Fatal(err)
	}
	if jobs != 2 || audits != 1 {
		t.Fatalf("jobs=%d audits=%d", jobs, audits)
	}
	caseItem, err := fixture.store.CaseByID(context.Background(), fixture.store, fixture.caseID)
	if err != nil {
		t.Fatal(err)
	}
	if caseItem.Status != domain.CaseInquiring || caseItem.Version != 3 {
		t.Fatalf("case = %#v", caseItem)
	}
}

func TestDispatchRejectsRoleStateVersionAndMissingInstitutions(t *testing.T) {
	fixture := newInquiryFixture(t)
	claimant := domain.Principal{UserID: "claimant", Role: domain.RoleClaimant}
	if _, err := fixture.service.Dispatch(context.Background(), claimant, fixture.caseID, "request-key-0002", 2); !errors.Is(err, domain.ErrForbidden) {
		t.Fatalf("claimant dispatch error = %v", err)
	}
	if _, err := fixture.service.Dispatch(context.Background(), fixture.officer, fixture.caseID, "short", 2); !errors.Is(err, domain.ErrValidation) {
		t.Fatalf("short key error = %v", err)
	}
	if _, err := fixture.service.Dispatch(context.Background(), fixture.officer, fixture.caseID, "request-key-0003", 9); !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("stale version error = %v", err)
	}
	if _, err := fixture.store.ExecContext(context.Background(), "UPDATE institutions SET active=0"); err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.service.Dispatch(context.Background(), fixture.officer, fixture.caseID, "request-key-0004", 2); !errors.Is(err, domain.ErrDependency) {
		t.Fatalf("missing institution error = %v", err)
	}
}

func TestRecordResultsIsIdempotentAndCaseWaitsForEveryInstitution(t *testing.T) {
	fixture := newInquiryFixture(t)
	items, err := fixture.service.Dispatch(context.Background(), fixture.officer, fixture.caseID, "request-key-results", 2)
	if err != nil {
		t.Fatal(err)
	}
	for index := range items {
		if err := fixture.service.MarkDispatched(context.Background(), items[index].ID, "external-"+items[index].ID); err != nil {
			t.Fatal(err)
		}
	}
	input := ResultInput{PartKey: "page-1", Accounts: []AccountResult{{
		ExternalReference: "account-secret-1", Kind: domain.AccountDeposit, Currency: "CNY", BalanceMinor: 300_000,
	}}}
	first, err := fixture.service.RecordResult(context.Background(), items[0].ID, input)
	if err != nil {
		t.Fatal(err)
	}
	if first.Status != domain.InquiryCompleted || first.ReceivedParts != 1 {
		t.Fatalf("first result = %#v", first)
	}
	caseItem, err := fixture.store.CaseByID(context.Background(), fixture.store, fixture.caseID)
	if err != nil {
		t.Fatal(err)
	}
	if caseItem.Status != domain.CaseInquiring {
		t.Fatalf("case advanced before all responses: %s", caseItem.Status)
	}
	replayed, err := fixture.service.RecordResult(context.Background(), items[0].ID, input)
	if err != nil {
		t.Fatal(err)
	}
	if replayed.ReceivedParts != 1 {
		t.Fatalf("duplicate result incremented parts: %#v", replayed)
	}
	changed := input
	changed.Accounts = []AccountResult{{ExternalReference: "different", Kind: domain.AccountDeposit, Currency: "CNY", BalanceMinor: 1}}
	if _, err := fixture.service.RecordResult(context.Background(), items[0].ID, changed); !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("changed duplicate payload error = %v", err)
	}
	if _, err := fixture.service.RecordResult(context.Background(), items[1].ID, ResultInput{
		PartKey: "page-1", Accounts: []AccountResult{{ExternalReference: "policy-1", Kind: domain.AccountPolicy,
			Currency: "CNY", BalanceMinor: 900_000}}}); err != nil {
		t.Fatal(err)
	}
	caseItem, err = fixture.store.CaseByID(context.Background(), fixture.store, fixture.caseID)
	if err != nil {
		t.Fatal(err)
	}
	if caseItem.Status != domain.CaseEligible || caseItem.Version != 4 || caseItem.InquiryCompletedAt == nil {
		t.Fatalf("eligible case = %#v", caseItem)
	}
	accounts, err := fixture.store.AccountsForCase(context.Background(), fixture.store, fixture.caseID)
	if err != nil {
		t.Fatal(err)
	}
	if len(accounts) != 2 {
		t.Fatalf("accounts = %#v", accounts)
	}
	for _, account := range accounts {
		if account.ExternalHash == "account-secret-1" || account.ExternalHash == "policy-1" {
			t.Fatalf("external reference was not hashed: %#v", account)
		}
	}
}

func TestRecordResultValidatesInputAndState(t *testing.T) {
	fixture := newInquiryFixture(t)
	items, err := fixture.service.Dispatch(context.Background(), fixture.officer, fixture.caseID, "request-key-invalid", 2)
	if err != nil {
		t.Fatal(err)
	}
	tests := []ResultInput{
		{},
		{PartKey: "part", Accounts: []AccountResult{{ExternalReference: "", Kind: domain.AccountDeposit, Currency: "CNY", BalanceMinor: 1}}},
		{PartKey: "part", Accounts: []AccountResult{{ExternalReference: "a", Kind: "unknown", Currency: "CNY", BalanceMinor: 1}}},
		{PartKey: "part", Accounts: []AccountResult{{ExternalReference: "a", Kind: domain.AccountDeposit, Currency: "", BalanceMinor: 1}}},
		{PartKey: "part", Accounts: []AccountResult{{ExternalReference: "a", Kind: domain.AccountDeposit, Currency: "CNY", BalanceMinor: -1}}},
	}
	for index, input := range tests {
		if _, err := fixture.service.RecordResult(context.Background(), items[0].ID, input); !errors.Is(err, domain.ErrValidation) {
			t.Errorf("case %d error = %v", index, err)
		}
	}
	valid := ResultInput{PartKey: "part", Accounts: []AccountResult{{ExternalReference: "a", Kind: domain.AccountDeposit, Currency: "CNY", BalanceMinor: 1}}}
	if _, err := fixture.service.RecordResult(context.Background(), items[0].ID, valid); !errors.Is(err, domain.ErrInvalidState) {
		t.Fatalf("pending inquiry result error = %v", err)
	}
}

func TestMarkDispatchedUsesOptimisticStateMachine(t *testing.T) {
	fixture := newInquiryFixture(t)
	items, err := fixture.service.Dispatch(context.Background(), fixture.officer, fixture.caseID, "request-key-dispatch", 2)
	if err != nil {
		t.Fatal(err)
	}
	if err := fixture.service.MarkDispatched(context.Background(), items[0].ID, "provider-ref"); err != nil {
		t.Fatal(err)
	}
	loaded, err := fixture.store.InquiryByID(context.Background(), fixture.store, items[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Status != domain.InquiryDispatched || loaded.ExternalRef != "provider-ref" || loaded.DispatchedAt == nil {
		t.Fatalf("dispatched inquiry = %#v", loaded)
	}
	if err := fixture.service.MarkDispatched(context.Background(), items[0].ID, "provider-ref"); err != nil {
		t.Fatalf("same dispatch replay error = %v", err)
	}
	if err := fixture.service.MarkDispatched(context.Background(), items[0].ID, "another-ref"); !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("changed dispatch reference error = %v", err)
	}
}
