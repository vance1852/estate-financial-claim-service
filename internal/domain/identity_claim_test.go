package domain

import (
	"errors"
	"testing"
	"time"
)

func TestRolesAndPrincipalValidation(t *testing.T) {
	tests := []struct {
		role        Role
		valid       bool
		operational bool
	}{
		{RoleClaimant, true, false},
		{RoleOfficer, true, true},
		{RoleSupervisor, true, true},
		{"administrator", false, false},
		{"", false, false},
	}
	for _, test := range tests {
		if test.role.Valid() != test.valid {
			t.Errorf("role %q validity = %v, want %v", test.role, test.role.Valid(), test.valid)
		}
		if test.role.Operational() != test.operational {
			t.Errorf("role %q operational = %v, want %v", test.role, test.role.Operational(), test.operational)
		}
	}
	if err := (Principal{UserID: "user_1", Role: RoleClaimant}).Validate(); err != nil {
		t.Fatalf("valid principal: %v", err)
	}
	if !errors.Is((Principal{Role: RoleClaimant}).Validate(), ErrValidation) {
		t.Fatal("missing user id should be validation error")
	}
	if !errors.Is((Principal{UserID: "user_1", Role: "root"}).Validate(), ErrValidation) {
		t.Fatal("unknown role should be validation error")
	}
}

func TestPersonIdentityValidationAndFingerprint(t *testing.T) {
	valid := PersonIdentity{Name: "Li Ming", IDNumber: "37020019800101001X"}
	if err := valid.Validate(); err != nil {
		t.Fatalf("valid identity: %v", err)
	}
	copyValue := PersonIdentity{Name: " li ming ", IDNumber: "37020019800101001x"}
	if valid.Fingerprint() != copyValue.Fingerprint() {
		t.Fatalf("normalization changed fingerprint: %s != %s", valid.Fingerprint(), copyValue.Fingerprint())
	}
	if valid.Fingerprint() == (PersonIdentity{Name: "Li Ming", IDNumber: "370200198001010019"}).Fingerprint() {
		t.Fatal("different identity numbers must not share fingerprint")
	}
	tests := []PersonIdentity{
		{Name: "L", IDNumber: valid.IDNumber},
		{Name: "", IDNumber: valid.IDNumber},
		{Name: valid.Name, IDNumber: "abc"},
		{Name: valid.Name, IDNumber: "123"},
		{Name: valid.Name, IDNumber: "1234567890123456789012345"},
	}
	for index, identity := range tests {
		if !errors.Is(identity.Validate(), ErrValidation) {
			t.Errorf("case %d should fail validation: %#v", index, identity)
		}
	}
}

func TestMaskIDNumber(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"37020019800101001X", "370************01X"},
		{"1234567", "123*567"},
		{"123456", "******"},
		{"12", "**"},
		{"", ""},
		{" 1234567 ", "123*567"},
	}
	for _, test := range tests {
		if got := MaskIDNumber(test.input); got != test.want {
			t.Errorf("MaskIDNumber(%q) = %q, want %q", test.input, got, test.want)
		}
	}
}

func TestInquiryTransitionsAndLifecycle(t *testing.T) {
	now := time.Date(2026, 8, 24, 12, 0, 0, 0, time.UTC)
	item := Inquiry{ID: "inq_1", Status: InquiryPending, Version: 1, CreatedAt: now, UpdatedAt: now}
	if err := item.Transition(InquiryDispatched, now.Add(time.Second)); err != nil {
		t.Fatalf("dispatch: %v", err)
	}
	if item.DispatchedAt == nil || item.Version != 2 {
		t.Fatalf("dispatch fields = %#v", item)
	}
	if err := item.Transition(InquiryPartial, now.Add(2*time.Second)); err != nil {
		t.Fatalf("partial: %v", err)
	}
	if err := item.Transition(InquiryPartial, now.Add(3*time.Second)); err != nil {
		t.Fatalf("additional part: %v", err)
	}
	if err := item.Transition(InquiryCompleted, now.Add(4*time.Second)); err != nil {
		t.Fatalf("complete: %v", err)
	}
	if item.CompletedAt == nil || !item.CompletedAt.Equal(now.Add(4*time.Second)) {
		t.Fatalf("completed_at = %v", item.CompletedAt)
	}
	before := item.Version
	if !errors.Is(item.Transition(InquiryPartial, now.Add(5*time.Second)), ErrInvalidState) {
		t.Fatal("completed inquiry must be terminal")
	}
	if item.Version != before {
		t.Fatal("invalid inquiry transition changed version")
	}
}

func TestFinancialAccountEligibility(t *testing.T) {
	base := FinancialAccount{Kind: AccountDeposit, Currency: "CNY", BalanceMinor: 100_000}
	tests := []struct {
		name     string
		mutate   func(*FinancialAccount)
		limit    int64
		eligible bool
	}{
		{"eligible", func(*FinancialAccount) {}, 5_000_000, true},
		{"zero balance", func(a *FinancialAccount) { a.BalanceMinor = 0 }, 5_000_000, false},
		{"over limit", func(a *FinancialAccount) { a.BalanceMinor = 5_000_001 }, 5_000_000, false},
		{"at limit", func(a *FinancialAccount) { a.BalanceMinor = 5_000_000 }, 5_000_000, true},
		{"restricted", func(a *FinancialAccount) { a.Restricted = true }, 5_000_000, false},
		{"reserved", func(a *FinancialAccount) { a.ReservedClaimID = "claim_1" }, 5_000_000, false},
		{"insurance", func(a *FinancialAccount) { a.Kind = AccountPolicy }, 5_000_000, false},
		{"foreign currency", func(a *FinancialAccount) { a.Currency = "USD" }, 5_000_000, false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			candidate := base
			test.mutate(&candidate)
			if got := candidate.EligibleForSmallClaim(test.limit); got != test.eligible {
				t.Fatalf("eligibility = %v, want %v for %#v", got, test.eligible, candidate)
			}
		})
	}
}

func TestClaimStateMachine(t *testing.T) {
	now := time.Date(2026, 8, 24, 9, 30, 0, 0, time.UTC)
	item := Claim{ID: "claim_1", Status: ClaimDraft, Version: 1, CreatedAt: now, UpdatedAt: now}
	if err := item.Transition(ClaimPending, "", now.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	if err := item.Transition(ClaimApproved, "supervisor_1", now.Add(2*time.Minute)); err != nil {
		t.Fatal(err)
	}
	if item.ApprovedBy != "supervisor_1" || item.ApprovedAt == nil {
		t.Fatalf("approval metadata = %#v", item)
	}
	if err := item.Transition(ClaimPaying, "", now.Add(3*time.Minute)); err != nil {
		t.Fatal(err)
	}
	if err := item.Transition(ClaimPaid, "", now.Add(4*time.Minute)); err != nil {
		t.Fatal(err)
	}
	if !errors.Is(item.Transition(ClaimCancelled, "", now.Add(5*time.Minute)), ErrInvalidState) {
		t.Fatal("paid claim should be terminal")
	}
}
