package cases

import (
	"context"
	"errors"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/vance1852/estate-financial-claim-service/internal/audit"
	"github.com/vance1852/estate-financial-claim-service/internal/clock"
	"github.com/vance1852/estate-financial-claim-service/internal/domain"
	"github.com/vance1852/estate-financial-claim-service/internal/ids"
	"github.com/vance1852/estate-financial-claim-service/internal/store"
)

type caseFixture struct {
	service  *Service
	store    *store.Store
	clock    *clock.Manual
	claimant domain.Principal
	officer  domain.Principal
	other    domain.Principal
}

func newCaseFixture(t *testing.T) caseFixture {
	t.Helper()
	database, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "cases.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	now := time.Date(2026, 8, 24, 8, 0, 0, 0, time.UTC)
	users := []store.User{
		{ID: "claimant_1", Email: "one@example.test", Role: domain.RoleClaimant},
		{ID: "claimant_2", Email: "two@example.test", Role: domain.RoleClaimant},
		{ID: "officer_1", Email: "officer@example.test", Role: domain.RoleOfficer},
	}
	for _, user := range users {
		user.PasswordHash, user.DisplayName, user.Active = "hash", user.ID, true
		user.CreatedAt, user.UpdatedAt = now, now
		if err := database.CreateUser(context.Background(), user); err != nil {
			t.Fatal(err)
		}
	}
	manual := clock.NewManual(now)
	return caseFixture{service: New(database, manual, &ids.Sequence{}), store: database, clock: manual,
		claimant: domain.Principal{UserID: "claimant_1", Role: domain.RoleClaimant},
		officer:  domain.Principal{UserID: "officer_1", Role: domain.RoleOfficer},
		other:    domain.Principal{UserID: "claimant_2", Role: domain.RoleClaimant}}
}

func validSubmit(key string) SubmitInput {
	return SubmitInput{
		Deceased: domain.PersonIdentity{Name: "Deceased Person", IDNumber: "37020019500101001X"},
		Claimant: domain.PersonIdentity{Name: "Claimant Person", IDNumber: "370200198001010019"},
		Relation: domain.RelationChild, Jurisdiction: "Qingdao", IdempotencyKey: key,
	}
}

func TestSubmitCreatesRelatedRecordsAtomically(t *testing.T) {
	fixture := newCaseFixture(t)
	ctx := audit.WithRequestID(context.Background(), "req_submit")
	result, err := fixture.service.Submit(ctx, fixture.claimant, validSubmit("submit-key-0001"))
	if err != nil {
		t.Fatal(err)
	}
	if result.Replayed || result.Case.Status != domain.CaseSubmitted || result.Case.Version != 1 {
		t.Fatalf("submit result = %#v", result)
	}
	if result.Case.ClaimantUserID != fixture.claimant.UserID || result.Case.DeceasedIDMasked == validSubmit("").Deceased.IDNumber {
		t.Fatalf("case identity/ownership = %#v", result.Case)
	}
	checks := map[string]string{
		"case_parties": "SELECT COUNT(*) FROM case_parties WHERE case_id=?",
		"documents":    "SELECT COUNT(*) FROM documents WHERE case_id=?",
		"idempotency":  "SELECT COUNT(*) FROM idempotency_keys WHERE resource_id=?",
		"audit":        "SELECT COUNT(*) FROM audit_events WHERE object_id=? AND request_id='req_submit'",
	}
	for name, query := range checks {
		var count int
		if err := fixture.store.QueryRowContext(ctx, query, result.Case.ID).Scan(&count); err != nil {
			t.Fatal(err)
		}
		if count != 1 {
			t.Errorf("%s rows = %d, want 1", name, count)
		}
	}
	var auditJSON string
	if err := fixture.store.QueryRowContext(ctx, `SELECT details_json FROM audit_events WHERE object_id=?`, result.Case.ID).Scan(&auditJSON); err != nil {
		t.Fatal(err)
	}
	if auditJSON == "" || contains(auditJSON, validSubmit("").Deceased.IDNumber) {
		t.Fatalf("audit identity leaked or missing: %s", auditJSON)
	}
}

func TestSubmitRollsBackWhenAuditWriteFails(t *testing.T) {
	fixture := newCaseFixture(t)
	restore := fixture.store.InjectFailure("audit", errors.New("audit storage unavailable"))
	defer restore()
	_, err := fixture.service.Submit(context.Background(), fixture.claimant, validSubmit("submit-key-0002"))
	if err == nil || !contains(err.Error(), "audit storage unavailable") {
		t.Fatalf("submit error = %v", err)
	}
	for _, table := range []string{"estate_cases", "parties", "case_parties", "documents", "idempotency_keys", "audit_events"} {
		var count int
		if err := fixture.store.QueryRowContext(context.Background(), "SELECT COUNT(*) FROM "+table).Scan(&count); err != nil {
			t.Fatal(err)
		}
		if count != 0 {
			t.Errorf("%s retained %d rows after rollback", table, count)
		}
	}
}

func TestSubmitReplaysPersistedResultAndRejectsChangedPayload(t *testing.T) {
	fixture := newCaseFixture(t)
	input := validSubmit("submit-key-replay")
	first, err := fixture.service.Submit(context.Background(), fixture.claimant, input)
	if err != nil {
		t.Fatal(err)
	}
	fixture.clock.Advance(time.Hour)
	second, err := fixture.service.Submit(context.Background(), fixture.claimant, input)
	if err != nil {
		t.Fatal(err)
	}
	if !second.Replayed || second.Case.ID != first.Case.ID || second.Case.Reference != first.Case.Reference {
		t.Fatalf("replay = %#v, first = %#v", second, first)
	}
	changed := input
	changed.Jurisdiction = "Shinan District"
	if _, err := fixture.service.Submit(context.Background(), fixture.claimant, changed); !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("changed payload error = %v", err)
	}
	if _, err := fixture.service.Submit(context.Background(), fixture.other, input); !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("cross-actor key reuse error = %v", err)
	}
	var cases int
	if err := fixture.store.QueryRowContext(context.Background(), "SELECT COUNT(*) FROM estate_cases").Scan(&cases); err != nil {
		t.Fatal(err)
	}
	if cases != 1 {
		t.Fatalf("case count = %d, want 1", cases)
	}
}

func TestSamePartyCanBeLinkedToMultipleCases(t *testing.T) {
	fixture := newCaseFixture(t)
	first, err := fixture.service.Submit(context.Background(), fixture.claimant, validSubmit("party-reuse-0001"))
	if err != nil {
		t.Fatal(err)
	}
	secondInput := validSubmit("party-reuse-0002")
	secondInput.Deceased = domain.PersonIdentity{Name: "Another Deceased", IDNumber: "370200194001010011"}
	second, err := fixture.service.Submit(context.Background(), fixture.claimant, secondInput)
	if err != nil {
		t.Fatal(err)
	}
	if first.Case.ID == second.Case.ID {
		t.Fatal("separate submissions returned same case")
	}
	var parties, links int
	if err := fixture.store.QueryRowContext(context.Background(), "SELECT COUNT(*) FROM parties").Scan(&parties); err != nil {
		t.Fatal(err)
	}
	if err := fixture.store.QueryRowContext(context.Background(), "SELECT COUNT(*) FROM case_parties").Scan(&links); err != nil {
		t.Fatal(err)
	}
	if parties != 1 || links != 2 {
		t.Fatalf("parties=%d links=%d", parties, links)
	}
}

func TestSubmitValidationAndRoleBoundary(t *testing.T) {
	fixture := newCaseFixture(t)
	tests := []struct {
		name      string
		principal domain.Principal
		mutate    func(*SubmitInput)
		want      error
	}{
		{"officer cannot submit", fixture.officer, func(*SubmitInput) {}, domain.ErrForbidden},
		{"invalid deceased", fixture.claimant, func(i *SubmitInput) { i.Deceased.IDNumber = "bad" }, domain.ErrValidation},
		{"invalid claimant", fixture.claimant, func(i *SubmitInput) { i.Claimant.Name = "X" }, domain.ErrValidation},
		{"invalid relation", fixture.claimant, func(i *SubmitInput) { i.Relation = "friend" }, domain.ErrValidation},
		{"missing jurisdiction", fixture.claimant, func(i *SubmitInput) { i.Jurisdiction = "" }, domain.ErrValidation},
		{"short key", fixture.claimant, func(i *SubmitInput) { i.IdempotencyKey = "short" }, domain.ErrValidation},
	}
	for index, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			input := validSubmit("validation-key-" + string(rune('a'+index)))
			test.mutate(&input)
			_, err := fixture.service.Submit(context.Background(), test.principal, input)
			if !errors.Is(err, test.want) {
				t.Fatalf("error = %v, want %v", err, test.want)
			}
		})
	}
}

func TestGetAndListEnforceOwnershipAndPagination(t *testing.T) {
	fixture := newCaseFixture(t)
	var created []domain.EstateCase
	for index := 0; index < 3; index++ {
		input := validSubmit("list-case-key-" + string(rune('a'+index)))
		input.Deceased.IDNumber = "3702001950010100" + string(rune('1'+index))
		result, err := fixture.service.Submit(context.Background(), fixture.claimant, input)
		if err != nil {
			t.Fatal(err)
		}
		created = append(created, result.Case)
	}
	if _, err := fixture.service.Get(context.Background(), fixture.other, created[0].ID); !errors.Is(err, domain.ErrForbidden) {
		t.Fatalf("other claimant get error = %v", err)
	}
	if _, err := fixture.service.Get(context.Background(), fixture.officer, created[0].ID); err != nil {
		t.Fatalf("officer get: %v", err)
	}
	firstPage, err := fixture.service.List(context.Background(), fixture.claimant, store.CaseFilter{Limit: 2})
	if err != nil {
		t.Fatal(err)
	}
	if len(firstPage) != 2 {
		t.Fatalf("first page length = %d", len(firstPage))
	}
	secondPage, err := fixture.service.List(context.Background(), fixture.claimant,
		store.CaseFilter{Cursor: firstPage[1].ID, Limit: 2})
	if err != nil {
		t.Fatal(err)
	}
	if len(secondPage) != 1 || secondPage[0].ID == firstPage[0].ID {
		t.Fatalf("second page = %#v", secondPage)
	}
	otherItems, err := fixture.service.List(context.Background(), fixture.other, store.CaseFilter{Limit: 10})
	if err != nil || len(otherItems) != 0 {
		t.Fatalf("other claimant list=%v err=%v", otherItems, err)
	}
}

func TestStartReviewUsesRoleStateAndVersion(t *testing.T) {
	fixture := newCaseFixture(t)
	result, err := fixture.service.Submit(context.Background(), fixture.claimant, validSubmit("review-key-0001"))
	if err != nil {
		t.Fatal(err)
	}
	if err := fixture.service.StartReview(context.Background(), fixture.claimant, result.Case.ID, 1); !errors.Is(err, domain.ErrForbidden) {
		t.Fatalf("claimant review error = %v", err)
	}
	if err := fixture.service.StartReview(context.Background(), fixture.officer, result.Case.ID, 9); !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("stale review error = %v", err)
	}
	ctx := audit.WithRequestID(context.Background(), "review_request")
	if err := fixture.service.StartReview(ctx, fixture.officer, result.Case.ID, 1); err != nil {
		t.Fatal(err)
	}
	loaded, err := fixture.service.Get(context.Background(), fixture.officer, result.Case.ID)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Status != domain.CaseReviewing || loaded.Version != 2 {
		t.Fatalf("reviewed case = %#v", loaded)
	}
	if err := fixture.service.StartReview(ctx, fixture.officer, result.Case.ID, 2); !errors.Is(err, domain.ErrInvalidState) {
		t.Fatalf("repeat review error = %v", err)
	}
}

func TestConcurrentStartReviewHasOneWinner(t *testing.T) {
	fixture := newCaseFixture(t)
	result, err := fixture.service.Submit(context.Background(), fixture.claimant, validSubmit("concurrent-review"))
	if err != nil {
		t.Fatal(err)
	}
	start := make(chan struct{})
	results := make(chan error, 2)
	var group sync.WaitGroup
	for index := 0; index < 2; index++ {
		group.Add(1)
		go func() {
			defer group.Done()
			<-start
			results <- fixture.service.StartReview(context.Background(), fixture.officer, result.Case.ID, 1)
		}()
	}
	close(start)
	group.Wait()
	close(results)
	var success, rejected int
	for err := range results {
		if err == nil {
			success++
		} else if errors.Is(err, domain.ErrConflict) || errors.Is(err, domain.ErrInvalidState) {
			rejected++
		} else {
			t.Fatalf("unexpected concurrent error: %v", err)
		}
	}
	if success != 1 || rejected != 1 {
		t.Fatalf("success=%d rejected=%d", success, rejected)
	}
}

func contains(value, substring string) bool {
	for index := 0; index+len(substring) <= len(value); index++ {
		if value[index:index+len(substring)] == substring {
			return true
		}
	}
	return false
}
