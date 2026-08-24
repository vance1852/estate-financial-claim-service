package domain

import (
	"errors"
	"testing"
	"time"
)

func TestCaseStatusTransitionMatrix(t *testing.T) {
	tests := []struct {
		name    string
		from    CaseStatus
		to      CaseStatus
		allowed bool
	}{
		{"draft can submit", CaseDraft, CaseSubmitted, true},
		{"draft cannot review", CaseDraft, CaseReviewing, false},
		{"submitted can review", CaseSubmitted, CaseReviewing, true},
		{"submitted can reject", CaseSubmitted, CaseRejected, true},
		{"reviewing can inquire", CaseReviewing, CaseInquiring, true},
		{"reviewing can reject", CaseReviewing, CaseRejected, true},
		{"inquiring can become eligible", CaseInquiring, CaseEligible, true},
		{"inquiring can reject", CaseInquiring, CaseRejected, true},
		{"eligible can claim", CaseEligible, CaseClaiming, true},
		{"eligible can close", CaseEligible, CaseClosed, true},
		{"claiming can close", CaseClaiming, CaseClosed, true},
		{"closed is terminal", CaseClosed, CaseClaiming, false},
		{"rejected is terminal", CaseRejected, CaseReviewing, false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := test.from.CanTransition(test.to); got != test.allowed {
				t.Fatalf("CanTransition(%s, %s) = %v, want %v", test.from, test.to, got, test.allowed)
			}
		})
	}
}

func TestEstateCaseTransitionUpdatesLifecycleFields(t *testing.T) {
	base := time.Date(2026, 8, 24, 10, 0, 0, 0, time.FixedZone("CST", 8*60*60))
	item := EstateCase{ID: "case_1", Status: CaseDraft, Version: 1, CreatedAt: base, UpdatedAt: base}
	if err := item.Transition(CaseSubmitted, base.Add(time.Minute)); err != nil {
		t.Fatalf("submit transition: %v", err)
	}
	if item.Version != 2 {
		t.Fatalf("version = %d, want 2", item.Version)
	}
	if item.SubmittedAt == nil || !item.SubmittedAt.Equal(base.Add(time.Minute).UTC()) {
		t.Fatalf("submitted_at = %v", item.SubmittedAt)
	}
	if item.UpdatedAt.Location() != time.UTC {
		t.Fatalf("updated time must be UTC: %v", item.UpdatedAt.Location())
	}
	steps := []CaseStatus{CaseReviewing, CaseInquiring, CaseEligible, CaseClaiming, CaseClosed}
	for index, status := range steps {
		at := base.Add(time.Duration(index+2) * time.Minute)
		if err := item.Transition(status, at); err != nil {
			t.Fatalf("transition to %s: %v", status, err)
		}
	}
	if item.InquiryCompletedAt == nil {
		t.Fatal("eligible transition did not record inquiry completion")
	}
	if item.ClosedAt == nil {
		t.Fatal("closed transition did not record close time")
	}
	if item.Version != 7 {
		t.Fatalf("version = %d, want 7", item.Version)
	}
}

func TestEstateCaseInvalidTransitionDoesNotMutate(t *testing.T) {
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	item := EstateCase{ID: "case_1", Status: CaseSubmitted, Version: 4, UpdatedAt: now}
	err := item.Transition(CaseClosed, now.Add(time.Hour))
	if !errors.Is(err, ErrInvalidState) {
		t.Fatalf("error = %v, want ErrInvalidState", err)
	}
	var stateErr StateError
	if !errors.As(err, &stateErr) {
		t.Fatalf("error does not retain StateError: %T", err)
	}
	if stateErr.Entity != "estate_case" || stateErr.From != "submitted" || stateErr.To != "closed" {
		t.Fatalf("state error = %#v", stateErr)
	}
	if item.Status != CaseSubmitted || item.Version != 4 || !item.UpdatedAt.Equal(now) {
		t.Fatalf("invalid transition mutated case: %#v", item)
	}
}

func TestEstateCaseDraftValidation(t *testing.T) {
	valid := EstateCase{ID: "case_1", ClaimantUserID: "user_1", Jurisdiction: "Qingdao", Status: CaseDraft}
	if err := valid.ValidateDraft(); err != nil {
		t.Fatalf("valid draft: %v", err)
	}
	tests := []struct {
		name   string
		mutate func(*EstateCase)
		field  string
	}{
		{"missing id", func(c *EstateCase) { c.ID = "" }, "id"},
		{"missing claimant", func(c *EstateCase) { c.ClaimantUserID = " " }, "claimant_user_id"},
		{"missing jurisdiction", func(c *EstateCase) { c.Jurisdiction = "" }, "jurisdiction"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			candidate := valid
			test.mutate(&candidate)
			err := candidate.ValidateDraft()
			if !errors.Is(err, ErrValidation) {
				t.Fatalf("error = %v, want validation", err)
			}
			var fieldErr FieldError
			if !errors.As(err, &fieldErr) || fieldErr.Field != test.field {
				t.Fatalf("field error = %#v, want %s", fieldErr, test.field)
			}
		})
	}
	candidate := valid
	candidate.Status = CaseSubmitted
	if !errors.Is(candidate.ValidateDraft(), ErrInvalidState) {
		t.Fatal("non-draft case should fail draft validation")
	}
}

func TestPartyRelations(t *testing.T) {
	valid := []PartyRelation{RelationSpouse, RelationChild, RelationParent, RelationHeir, RelationAgent}
	for _, relation := range valid {
		if !relation.Valid() {
			t.Errorf("relation %s should be valid", relation)
		}
	}
	for _, relation := range []PartyRelation{"", "friend", "executor"} {
		if relation.Valid() {
			t.Errorf("relation %q should be invalid", relation)
		}
	}
}

func TestVersionConflictRetainsConflictSentinel(t *testing.T) {
	err := VersionConflict{Entity: "estate_case", ID: "case_1", Expected: 8}
	if !errors.Is(err, ErrConflict) {
		t.Fatal("VersionConflict must unwrap to ErrConflict")
	}
	want := "estate_case case_1 version 8 is stale"
	if err.Error() != want {
		t.Fatalf("message = %q, want %q", err.Error(), want)
	}
}
