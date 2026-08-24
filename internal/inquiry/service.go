package inquiry

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/vance1852/estate-financial-claim-service/internal/audit"
	"github.com/vance1852/estate-financial-claim-service/internal/clock"
	"github.com/vance1852/estate-financial-claim-service/internal/domain"
	"github.com/vance1852/estate-financial-claim-service/internal/ids"
	"github.com/vance1852/estate-financial-claim-service/internal/store"
)

type Service struct {
	store       *store.Store
	clock       clock.Clock
	ids         ids.Generator
	maxAttempts int
	cacheMu     sync.RWMutex
	partial     map[string]cachedResult
}

type cachedResult struct {
	hash string
	item domain.Inquiry
}

func New(database *store.Store, c clock.Clock, generator ids.Generator, maxAttempts int) *Service {
	return &Service{store: database, clock: c, ids: generator, maxAttempts: maxAttempts,
		partial: make(map[string]cachedResult)}
}

func (s *Service) Dispatch(ctx context.Context, principal domain.Principal, caseID, requestKey string, expectedVersion int64) ([]domain.Inquiry, error) {
	if !principal.Role.Operational() {
		return nil, domain.ErrForbidden
	}
	if len(requestKey) < 8 || len(requestKey) > 128 {
		return nil, domain.FieldError{Field: "request_key", Message: "must contain 8 to 128 bytes"}
	}
	institutions, err := s.store.ActiveInstitutions(ctx)
	if err != nil {
		return nil, err
	}
	if len(institutions) == 0 {
		return nil, fmt.Errorf("no active institutions: %w", domain.ErrDependency)
	}
	now := s.clock.Now()
	created := make([]domain.Inquiry, 0, len(institutions))
	err = s.store.WithTx(ctx, func(tx *sql.Tx) error {
		item, err := s.store.CaseByID(ctx, tx, caseID)
		if err != nil {
			return err
		}
		if item.Status != domain.CaseReviewing {
			return domain.StateError{Entity: "estate_case", From: string(item.Status), To: string(domain.CaseInquiring)}
		}
		if item.Version != expectedVersion {
			return domain.VersionConflict{Entity: "estate_case", ID: item.ID, Expected: expectedVersion}
		}
		for _, institutionID := range institutions {
			inquiryID, err := s.ids.New("inq")
			if err != nil {
				return err
			}
			jobID, err := s.ids.New("job")
			if err != nil {
				return err
			}
			entry := domain.Inquiry{ID: inquiryID, CaseID: caseID, InstitutionID: institutionID,
				RequestKey: requestKey, Status: domain.InquiryPending, ExpectedParts: 1,
				Version: 1, CreatedAt: now, UpdatedAt: now}
			if err := s.store.InsertInquiry(ctx, tx, entry); err != nil {
				return err
			}
			payload, _ := json.Marshal(map[string]string{"inquiry_id": inquiryID})
			if err := s.store.InsertJob(ctx, tx, store.Job{ID: jobID, Kind: "dispatch_inquiry",
				ResourceID: inquiryID, Payload: payload, Status: "pending", MaxAttempts: s.maxAttempts,
				AvailableAt: now, CreatedAt: now, UpdatedAt: now}); err != nil {
				return err
			}
			created = append(created, entry)
		}
		previous := item.Version
		if err := item.Transition(domain.CaseInquiring, now); err != nil {
			return err
		}
		if err := s.store.UpdateCase(ctx, tx, item, previous); err != nil {
			return err
		}
		return s.store.InsertAudit(ctx, tx, audit.Event{ActorID: principal.UserID,
			Action: "inquiries.dispatched", ObjectType: "estate_case", ObjectID: caseID,
			Result: "success", RequestID: audit.CorrelationID(ctx),
			Details: map[string]any{"institution_count": len(created)}, CreatedAt: now})
	})
	return created, err
}

type AccountResult struct {
	ExternalReference string             `json:"external_reference"`
	Kind              domain.AccountKind `json:"kind"`
	Currency          string             `json:"currency"`
	BalanceMinor      int64              `json:"balance_minor"`
	Restricted        bool               `json:"restricted"`
	RestrictionNote   string             `json:"restriction_note"`
}

type ResultInput struct {
	PartKey  string          `json:"part_key"`
	Accounts []AccountResult `json:"accounts"`
}

func (s *Service) RecordResult(ctx context.Context, inquiryID string, input ResultInput) (domain.Inquiry, error) {
	if input.PartKey == "" || len(input.PartKey) > 128 {
		return domain.Inquiry{}, domain.FieldError{Field: "part_key", Message: "is required and limited to 128 bytes"}
	}
	for _, account := range input.Accounts {
		if account.ExternalReference == "" || account.BalanceMinor < 0 || account.Currency == "" || !account.Kind.Valid() {
			return domain.Inquiry{}, domain.FieldError{Field: "accounts", Message: "contains invalid account data"}
		}
	}
	payload, err := json.Marshal(input)
	if err != nil {
		return domain.Inquiry{}, err
	}
	payloadDigest := sha256.Sum256(payload)
	payloadHash := hex.EncodeToString(payloadDigest[:])
	s.cacheMu.RLock()
	cached, ok := s.partial[input.PartKey]
	s.cacheMu.RUnlock()
	if ok {
		if cached.hash != payloadHash {
			return domain.Inquiry{}, fmt.Errorf("inquiry part key reused with different payload: %w", domain.ErrConflict)
		}
		return cached.item, nil
	}
	now := s.clock.Now()
	var result domain.Inquiry
	err = s.store.WithTx(ctx, func(tx *sql.Tx) error {
		item, err := s.store.InquiryByID(ctx, tx, inquiryID)
		if err != nil {
			return err
		}
		existingHash, existingErr := s.store.InquiryResultHash(ctx, tx, inquiryID, input.PartKey)
		if existingErr == nil {
			if existingHash != payloadHash {
				return fmt.Errorf("inquiry part key reused with different payload: %w", domain.ErrConflict)
			}
			result = item
			return nil
		}
		if !errors.Is(existingErr, domain.ErrNotFound) {
			return existingErr
		}
		if item.Status != domain.InquiryDispatched && item.Status != domain.InquiryPartial {
			return domain.StateError{Entity: "inquiry", From: string(item.Status), To: string(domain.InquiryPartial)}
		}
		resultID, err := s.ids.New("res")
		if err != nil {
			return err
		}
		inserted, err := s.store.InsertInquiryResult(ctx, tx, resultID, inquiryID, input.PartKey,
			payloadHash, now.Format(time.RFC3339Nano))
		if err != nil {
			return err
		}
		if !inserted {
			result = item
			return nil
		}
		for _, account := range input.Accounts {
			accountID, err := s.ids.New("acct")
			if err != nil {
				return err
			}
			externalDigest := sha256.Sum256([]byte(account.ExternalReference))
			entry := domain.FinancialAccount{ID: accountID, CaseID: item.CaseID,
				InstitutionID: item.InstitutionID, InquiryID: item.ID,
				ExternalHash: hex.EncodeToString(externalDigest[:]), Kind: account.Kind,
				Currency: account.Currency, BalanceMinor: account.BalanceMinor,
				Restricted: account.Restricted, RestrictionNote: account.RestrictionNote,
				Version: 1, CreatedAt: now, UpdatedAt: now}
			if err := s.store.UpsertFinancialAccount(ctx, tx, entry); err != nil {
				return err
			}
		}
		previous := item.Version
		item.ReceivedParts++
		next := domain.InquiryPartial
		if item.ReceivedParts >= item.ExpectedParts {
			next = domain.InquiryCompleted
		}
		if err := item.Transition(next, now); err != nil {
			return err
		}
		if err := s.store.UpdateInquiry(ctx, tx, item, previous); err != nil {
			return err
		}
		var incomplete int
		if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM inquiries
			WHERE case_id=? AND status!='completed'`, item.CaseID).Scan(&incomplete); err != nil {
			return fmt.Errorf("count incomplete inquiries: %w", err)
		}
		if incomplete == 0 {
			caseItem, err := s.store.CaseByID(ctx, tx, item.CaseID)
			if err != nil {
				return err
			}
			caseVersion := caseItem.Version
			if err := caseItem.Transition(domain.CaseEligible, now); err != nil {
				return err
			}
			if err := s.store.UpdateCase(ctx, tx, caseItem, caseVersion); err != nil {
				return err
			}
		}
		if err := s.store.InsertAudit(ctx, tx, audit.Event{ActorID: "system", Action: "inquiry.result_recorded",
			ObjectType: "inquiry", ObjectID: item.ID, Result: "success", RequestID: audit.CorrelationID(ctx),
			Details: map[string]any{"part_key": input.PartKey, "accounts": len(input.Accounts)}, CreatedAt: now}); err != nil {
			return err
		}
		result = item
		return nil
	})
	if err == nil && result.Status == domain.InquiryPartial {
		s.cacheMu.Lock()
		s.partial[input.PartKey] = cachedResult{hash: payloadHash, item: result}
		s.cacheMu.Unlock()
	}
	return result, err
}

func (s *Service) MarkDispatched(ctx context.Context, inquiryID, externalRef string) error {
	now := s.clock.Now()
	return s.store.WithTx(ctx, func(tx *sql.Tx) error {
		item, err := s.store.InquiryByID(ctx, tx, inquiryID)
		if err != nil {
			return err
		}
		if item.Status == domain.InquiryDispatched || item.Status == domain.InquiryPartial || item.Status == domain.InquiryCompleted {
			if item.ExternalRef == externalRef {
				return nil
			}
			return fmt.Errorf("inquiry already dispatched with another reference: %w", domain.ErrConflict)
		}
		previous := item.Version
		if err := item.Transition(domain.InquiryDispatched, now); err != nil {
			return err
		}
		item.ExternalRef = externalRef
		return s.store.UpdateInquiry(ctx, tx, item, previous)
	})
}
