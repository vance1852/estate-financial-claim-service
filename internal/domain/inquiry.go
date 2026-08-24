package domain

import "time"

type InquiryStatus string

const (
	InquiryPending    InquiryStatus = "pending"
	InquiryDispatched InquiryStatus = "dispatched"
	InquiryPartial    InquiryStatus = "partial"
	InquiryCompleted  InquiryStatus = "completed"
	InquiryFailed     InquiryStatus = "failed"
)

var inquiryTransitions = map[InquiryStatus]map[InquiryStatus]bool{
	InquiryPending:    {InquiryDispatched: true, InquiryFailed: true},
	InquiryDispatched: {InquiryPartial: true, InquiryCompleted: true, InquiryFailed: true},
	InquiryPartial:    {InquiryPartial: true, InquiryCompleted: true, InquiryFailed: true},
}

func (s InquiryStatus) CanTransition(to InquiryStatus) bool { return inquiryTransitions[s][to] }

type InstitutionKind string

const (
	InstitutionBank    InstitutionKind = "bank"
	InstitutionInsurer InstitutionKind = "insurer"
)

type Inquiry struct {
	ID            string        `json:"id"`
	CaseID        string        `json:"case_id"`
	InstitutionID string        `json:"institution_id"`
	RequestKey    string        `json:"request_key"`
	Status        InquiryStatus `json:"status"`
	ExternalRef   string        `json:"external_ref,omitempty"`
	ExpectedParts int           `json:"expected_parts"`
	ReceivedParts int           `json:"received_parts"`
	Version       int64         `json:"version"`
	DispatchedAt  *time.Time    `json:"dispatched_at,omitempty"`
	CompletedAt   *time.Time    `json:"completed_at,omitempty"`
	LastError     string        `json:"last_error,omitempty"`
	CreatedAt     time.Time     `json:"created_at"`
	UpdatedAt     time.Time     `json:"updated_at"`
}

func (q *Inquiry) Transition(to InquiryStatus, now time.Time) error {
	if !q.Status.CanTransition(to) {
		return StateError{Entity: "inquiry", From: string(q.Status), To: string(to)}
	}
	q.Status = to
	q.Version++
	q.UpdatedAt = now.UTC()
	if to == InquiryDispatched {
		value := now.UTC()
		q.DispatchedAt = &value
	}
	if to == InquiryCompleted {
		value := now.UTC()
		q.CompletedAt = &value
	}
	return nil
}

type AccountKind string

const (
	AccountDeposit    AccountKind = "deposit"
	AccountPolicy     AccountKind = "insurance_policy"
	AccountInvestment AccountKind = "investment"
)

func (k AccountKind) Valid() bool {
	return k == AccountDeposit || k == AccountPolicy || k == AccountInvestment
}

type FinancialAccount struct {
	ID              string      `json:"id"`
	CaseID          string      `json:"case_id"`
	InstitutionID   string      `json:"institution_id"`
	InquiryID       string      `json:"inquiry_id"`
	ExternalHash    string      `json:"-"`
	Kind            AccountKind `json:"kind"`
	Currency        string      `json:"currency"`
	BalanceMinor    int64       `json:"balance_minor"`
	Restricted      bool        `json:"restricted"`
	RestrictionNote string      `json:"restriction_note,omitempty"`
	ReservedClaimID string      `json:"reserved_claim_id,omitempty"`
	Version         int64       `json:"version"`
	CreatedAt       time.Time   `json:"created_at"`
	UpdatedAt       time.Time   `json:"updated_at"`
}

func (a FinancialAccount) EligibleForSmallClaim(limitMinor int64) bool {
	return a.Kind == AccountDeposit && a.Currency == "CNY" && a.BalanceMinor > 0 &&
		a.BalanceMinor <= limitMinor && !a.Restricted && a.ReservedClaimID == ""
}
