package domain

import "time"

type ClaimStatus string

const (
	ClaimDraft     ClaimStatus = "draft"
	ClaimPending   ClaimStatus = "pending_approval"
	ClaimApproved  ClaimStatus = "approved"
	ClaimPaying    ClaimStatus = "paying"
	ClaimPaid      ClaimStatus = "paid"
	ClaimRejected  ClaimStatus = "rejected"
	ClaimCancelled ClaimStatus = "cancelled"
)

var claimTransitions = map[ClaimStatus]map[ClaimStatus]bool{
	ClaimDraft:    {ClaimPending: true, ClaimCancelled: true},
	ClaimPending:  {ClaimApproved: true, ClaimRejected: true, ClaimCancelled: true},
	ClaimApproved: {ClaimPaying: true, ClaimCancelled: true},
	ClaimPaying:   {ClaimPaid: true},
}

func (s ClaimStatus) CanTransition(to ClaimStatus) bool { return claimTransitions[s][to] }

type Claim struct {
	ID             string      `json:"id"`
	CaseID         string      `json:"case_id"`
	ClaimantUserID string      `json:"claimant_user_id"`
	Status         ClaimStatus `json:"status"`
	TotalMinor     int64       `json:"total_minor"`
	Currency       string      `json:"currency"`
	Version        int64       `json:"version"`
	ApprovedBy     string      `json:"approved_by,omitempty"`
	ApprovedAt     *time.Time  `json:"approved_at,omitempty"`
	CreatedAt      time.Time   `json:"created_at"`
	UpdatedAt      time.Time   `json:"updated_at"`
}

func (c *Claim) Transition(to ClaimStatus, actor string, now time.Time) error {
	if !c.Status.CanTransition(to) {
		return StateError{Entity: "claim", From: string(c.Status), To: string(to)}
	}
	c.Status = to
	c.Version++
	c.UpdatedAt = now.UTC()
	if to == ClaimApproved {
		value := now.UTC()
		c.ApprovedAt = &value
		c.ApprovedBy = actor
	}
	return nil
}

type PayoutStatus string

const (
	PayoutPending   PayoutStatus = "pending"
	PayoutSubmitted PayoutStatus = "submitted"
	PayoutConfirmed PayoutStatus = "confirmed"
	PayoutFailed    PayoutStatus = "failed"
)

type Payout struct {
	ID             string       `json:"id"`
	ClaimID        string       `json:"claim_id"`
	IdempotencyKey string       `json:"-"`
	Status         PayoutStatus `json:"status"`
	AmountMinor    int64        `json:"amount_minor"`
	Currency       string       `json:"currency"`
	ProviderRef    string       `json:"provider_ref,omitempty"`
	Attempts       int          `json:"attempts"`
	LastError      string       `json:"last_error,omitempty"`
	CreatedAt      time.Time    `json:"created_at"`
	UpdatedAt      time.Time    `json:"updated_at"`
}
