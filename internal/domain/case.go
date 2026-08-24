package domain

import (
	"strings"
	"time"
)

type CaseStatus string

const (
	CaseDraft     CaseStatus = "draft"
	CaseSubmitted CaseStatus = "submitted"
	CaseReviewing CaseStatus = "reviewing"
	CaseInquiring CaseStatus = "inquiring"
	CaseEligible  CaseStatus = "eligible"
	CaseClaiming  CaseStatus = "claiming"
	CaseClosed    CaseStatus = "closed"
	CaseRejected  CaseStatus = "rejected"
)

var caseTransitions = map[CaseStatus]map[CaseStatus]bool{
	CaseDraft:     {CaseSubmitted: true},
	CaseSubmitted: {CaseReviewing: true, CaseRejected: true},
	CaseReviewing: {CaseInquiring: true, CaseRejected: true},
	CaseInquiring: {CaseEligible: true, CaseRejected: true},
	CaseEligible:  {CaseClaiming: true, CaseClosed: true},
	CaseClaiming:  {CaseClosed: true},
}

func (s CaseStatus) Valid() bool {
	_, ok := caseTransitions[s]
	return ok || s == CaseClosed || s == CaseRejected
}

func (s CaseStatus) CanTransition(to CaseStatus) bool {
	return caseTransitions[s][to]
}

type EstateCase struct {
	ID                 string     `json:"id"`
	Reference          string     `json:"reference"`
	DeceasedName       string     `json:"deceased_name"`
	DeceasedIDHash     string     `json:"-"`
	DeceasedIDMasked   string     `json:"deceased_id_masked"`
	Jurisdiction       string     `json:"jurisdiction"`
	ClaimantUserID     string     `json:"claimant_user_id"`
	Status             CaseStatus `json:"status"`
	Version            int64      `json:"version"`
	SubmittedAt        *time.Time `json:"submitted_at,omitempty"`
	InquiryCompletedAt *time.Time `json:"inquiry_completed_at,omitempty"`
	ClosedAt           *time.Time `json:"closed_at,omitempty"`
	CreatedAt          time.Time  `json:"created_at"`
	UpdatedAt          time.Time  `json:"updated_at"`
}

func (c EstateCase) ValidateDraft() error {
	if strings.TrimSpace(c.ID) == "" {
		return FieldError{Field: "id", Message: "is required"}
	}
	if strings.TrimSpace(c.ClaimantUserID) == "" {
		return FieldError{Field: "claimant_user_id", Message: "is required"}
	}
	if strings.TrimSpace(c.Jurisdiction) == "" {
		return FieldError{Field: "jurisdiction", Message: "is required"}
	}
	if c.Status != CaseDraft {
		return StateError{Entity: "estate_case", From: string(c.Status), To: string(CaseDraft)}
	}
	return nil
}

func (c *EstateCase) Transition(to CaseStatus, now time.Time) error {
	if !c.Status.CanTransition(to) {
		return StateError{Entity: "estate_case", From: string(c.Status), To: string(to)}
	}
	c.Status = to
	c.Version++
	c.UpdatedAt = now.UTC()
	switch to {
	case CaseSubmitted:
		value := now.UTC()
		c.SubmittedAt = &value
	case CaseEligible:
		value := now.UTC()
		c.InquiryCompletedAt = &value
	case CaseClosed:
		value := now.UTC()
		c.ClosedAt = &value
	}
	return nil
}

type PartyRelation string

const (
	RelationSpouse PartyRelation = "spouse"
	RelationChild  PartyRelation = "child"
	RelationParent PartyRelation = "parent"
	RelationHeir   PartyRelation = "other_heir"
	RelationAgent  PartyRelation = "authorized_agent"
)

func (r PartyRelation) Valid() bool {
	switch r {
	case RelationSpouse, RelationChild, RelationParent, RelationHeir, RelationAgent:
		return true
	default:
		return false
	}
}
