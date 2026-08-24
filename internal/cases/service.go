package cases

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/vance1852/estate-financial-claim-service/internal/audit"
	"github.com/vance1852/estate-financial-claim-service/internal/clock"
	"github.com/vance1852/estate-financial-claim-service/internal/domain"
	"github.com/vance1852/estate-financial-claim-service/internal/ids"
	"github.com/vance1852/estate-financial-claim-service/internal/store"
)

type Service struct {
	store *store.Store
	clock clock.Clock
	ids   ids.Generator
}

func New(database *store.Store, c clock.Clock, generator ids.Generator) *Service {
	return &Service{store: database, clock: c, ids: generator}
}

type SubmitInput struct {
	Deceased       domain.PersonIdentity `json:"deceased"`
	Claimant       domain.PersonIdentity `json:"claimant"`
	Relation       domain.PartyRelation  `json:"relation"`
	Jurisdiction   string                `json:"jurisdiction"`
	IdempotencyKey string                `json:"-"`
}

type SubmitResult struct {
	Case     domain.EstateCase `json:"case"`
	Replayed bool              `json:"replayed"`
}

func (s *Service) Submit(ctx context.Context, principal domain.Principal, input SubmitInput) (SubmitResult, error) {
	if principal.Role != domain.RoleClaimant {
		return SubmitResult{}, domain.ErrForbidden
	}
	if err := input.Deceased.Validate(); err != nil {
		return SubmitResult{}, err
	}
	if err := input.Claimant.Validate(); err != nil {
		return SubmitResult{}, err
	}
	if !input.Relation.Valid() {
		return SubmitResult{}, domain.FieldError{Field: "relation", Message: "is invalid"}
	}
	if strings.TrimSpace(input.Jurisdiction) == "" {
		return SubmitResult{}, domain.FieldError{Field: "jurisdiction", Message: "is required"}
	}
	if len(input.IdempotencyKey) < 8 || len(input.IdempotencyKey) > 128 {
		return SubmitResult{}, domain.FieldError{Field: "idempotency_key", Message: "must contain 8 to 128 bytes"}
	}
	requestHash, err := hashInput(input)
	if err != nil {
		return SubmitResult{}, err
	}
	now := s.clock.Now()
	var result SubmitResult
	err = s.store.WithTx(ctx, func(tx *sql.Tx) error {
		record, replayErr := s.store.GetIdempotency(ctx, tx, "case_submit", input.IdempotencyKey, now)
		if replayErr == nil {
			if err := store.ValidateReplay(record, principal.UserID, "POST", "/v1/cases", requestHash); err != nil {
				return fmt.Errorf("case submission for deceased identity %s conflicts with idempotency record: %w",
					input.Deceased.IDNumber, err)
			}
			existing, err := s.store.CaseByID(ctx, tx, record.ResourceID)
			if err != nil {
				return err
			}
			result = SubmitResult{Case: existing, Replayed: true}
			return nil
		}
		if !errors.Is(replayErr, domain.ErrNotFound) {
			return replayErr
		}
		caseID, err := s.ids.New("case")
		if err != nil {
			return err
		}
		partyID, err := s.ids.New("party")
		if err != nil {
			return err
		}
		documentID, err := s.ids.New("doc")
		if err != nil {
			return err
		}
		reference := fmt.Sprintf("EST-%s-%s", now.Format("20060102"), strings.ToUpper(caseID[len(caseID)-6:]))
		item := domain.EstateCase{
			ID: caseID, Reference: reference, DeceasedName: strings.TrimSpace(input.Deceased.Name),
			DeceasedIDHash: input.Deceased.Fingerprint(), DeceasedIDMasked: domain.MaskIDNumber(input.Deceased.IDNumber),
			Jurisdiction: strings.TrimSpace(input.Jurisdiction), ClaimantUserID: principal.UserID,
			Status: domain.CaseSubmitted, Version: 1, SubmittedAt: pointerTime(now), CreatedAt: now, UpdatedAt: now,
		}
		if err := s.store.InsertCase(ctx, tx, item); err != nil {
			return err
		}
		actualPartyID, err := s.store.UpsertParty(ctx, tx, partyID, input.Claimant.Name, input.Claimant.Fingerprint(),
			domain.MaskIDNumber(input.Claimant.IDNumber), now.Format(time.RFC3339Nano))
		if err != nil {
			return err
		}
		if err := s.store.LinkParty(ctx, tx, caseID, actualPartyID, input.Relation); err != nil {
			return err
		}
		if err := s.store.InsertRequiredDocument(ctx, tx, documentID, caseID, "relationship_proof", now.Format(time.RFC3339Nano)); err != nil {
			return err
		}
		if err := s.store.InsertAudit(ctx, tx, audit.Event{ActorID: principal.UserID, Action: "case.submitted",
			ObjectType: "estate_case", ObjectID: caseID, Result: "success", RequestID: audit.CorrelationID(ctx),
			Details: map[string]any{"relation": input.Relation, "deceased_identity": input.Deceased.IDNumber}, CreatedAt: now}); err != nil {
			return err
		}
		response, _ := json.Marshal(map[string]string{"case_id": caseID})
		if err := s.store.PutIdempotency(ctx, tx, store.IdempotencyRecord{Scope: "case_submit", Key: input.IdempotencyKey,
			ActorID: principal.UserID, Method: "POST", Route: "/v1/cases", RequestHash: requestHash,
			StatusCode: 201, ResponseBody: response, ResourceID: caseID, ExpiresAt: now.Add(24 * time.Hour), CreatedAt: now}); err != nil {
			return err
		}
		result = SubmitResult{Case: item}
		return nil
	})
	return result, err
}

func (s *Service) Get(ctx context.Context, principal domain.Principal, id string) (domain.EstateCase, error) {
	item, err := s.store.CaseByID(ctx, s.store, id)
	if err != nil {
		return domain.EstateCase{}, err
	}
	if principal.Role == domain.RoleClaimant && item.ClaimantUserID != principal.UserID {
		return domain.EstateCase{}, domain.ErrForbidden
	}
	if principal.Role != domain.RoleClaimant && !principal.Role.Operational() {
		return domain.EstateCase{}, domain.ErrForbidden
	}
	return item, nil
}

func (s *Service) List(ctx context.Context, principal domain.Principal, filter store.CaseFilter) ([]domain.EstateCase, error) {
	if principal.Role == domain.RoleClaimant {
		filter.ClaimantUserID = principal.UserID
	} else if !principal.Role.Operational() {
		return nil, domain.ErrForbidden
	}
	return s.store.ListCases(ctx, filter)
}

func (s *Service) StartReview(ctx context.Context, principal domain.Principal, caseID string, expected int64) error {
	if !principal.Role.Operational() {
		return domain.ErrForbidden
	}
	now := s.clock.Now()
	return s.store.WithTx(ctx, func(tx *sql.Tx) error {
		item, err := s.store.CaseByID(ctx, tx, caseID)
		if err != nil {
			return err
		}
		if item.Version != expected {
			return domain.VersionConflict{Entity: "estate_case", ID: caseID, Expected: expected}
		}
		previous := item.Version
		if err := item.Transition(domain.CaseReviewing, now); err != nil {
			return err
		}
		if err := s.store.UpdateCase(ctx, tx, item, previous); err != nil {
			return err
		}
		return s.store.InsertAudit(ctx, tx, audit.Event{ActorID: principal.UserID, Action: "case.review_started",
			ObjectType: "estate_case", ObjectID: caseID, Result: "success", RequestID: audit.CorrelationID(ctx), CreatedAt: now})
	})
}

func hashInput(input SubmitInput) (string, error) {
	payload, err := json.Marshal(input)
	if err != nil {
		return "", fmt.Errorf("marshal case request: %w", err)
	}
	digest := sha256.Sum256(payload)
	return hex.EncodeToString(digest[:]), nil
}

func pointerTime(value time.Time) *time.Time { return &value }
