package claims

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"github.com/vance1852/estate-financial-claim-service/internal/audit"
	"github.com/vance1852/estate-financial-claim-service/internal/clock"
	"github.com/vance1852/estate-financial-claim-service/internal/domain"
	"github.com/vance1852/estate-financial-claim-service/internal/ids"
	"github.com/vance1852/estate-financial-claim-service/internal/store"
)

const DefaultSmallClaimLimit int64 = 5_000_000

type Service struct {
	store       *store.Store
	clock       clock.Clock
	ids         ids.Generator
	limitMinor  int64
	maxAttempts int
}

func New(database *store.Store, c clock.Clock, generator ids.Generator, limit int64, maxAttempts int) *Service {
	return &Service{store: database, clock: c, ids: generator, limitMinor: limit, maxAttempts: maxAttempts}
}

func (s *Service) Create(ctx context.Context, principal domain.Principal, caseID string, accountIDs []string) (domain.Claim, error) {
	if principal.Role != domain.RoleClaimant {
		return domain.Claim{}, domain.ErrForbidden
	}
	if len(accountIDs) == 0 || len(accountIDs) > 50 {
		return domain.Claim{}, domain.FieldError{Field: "account_ids", Message: "must contain 1 to 50 accounts"}
	}
	now := s.clock.Now()
	claimID, err := s.ids.New("claim")
	if err != nil {
		return domain.Claim{}, err
	}
	var created domain.Claim
	err = s.store.WithTx(ctx, func(tx *sql.Tx) error {
		caseItem, err := s.store.CaseByID(ctx, tx, caseID)
		if err != nil {
			return err
		}
		if caseItem.ClaimantUserID != principal.UserID {
			return domain.ErrForbidden
		}
		if caseItem.Status != domain.CaseEligible {
			return domain.StateError{Entity: "estate_case", From: string(caseItem.Status), To: string(domain.CaseClaiming)}
		}
		accounts, err := s.store.AccountsForCase(ctx, tx, caseID)
		if err != nil {
			return err
		}
		selected := selectAccounts(accounts, accountIDs)
		if len(selected) != len(accountIDs) {
			return fmt.Errorf("one or more accounts do not belong to the case: %w", domain.ErrValidation)
		}
		var total int64
		for _, account := range selected {
			if !account.EligibleForSmallClaim(s.limitMinor) {
				return fmt.Errorf("account %s is not eligible: %w", account.ID, domain.ErrConflict)
			}
			total += account.BalanceMinor
			if total > s.limitMinor {
				return fmt.Errorf("combined claim exceeds small-claim limit: %w", domain.ErrConflict)
			}
		}
		created = domain.Claim{ID: claimID, CaseID: caseID, ClaimantUserID: principal.UserID,
			Status: domain.ClaimPending, TotalMinor: total, Currency: "CNY", Version: 1,
			CreatedAt: now, UpdatedAt: now}
		if err := s.store.InsertClaim(ctx, tx, created); err != nil {
			return err
		}
		for _, account := range selected {
			if _, err := tx.ExecContext(ctx, `INSERT INTO claim_accounts(claim_id,account_id,amount_minor)
				VALUES(?,?,?)`, claimID, account.ID, account.BalanceMinor); err != nil {
				return fmt.Errorf("link pending claim account: %w", err)
			}
		}
		caseVersion := caseItem.Version
		if err := caseItem.Transition(domain.CaseClaiming, now); err != nil {
			return err
		}
		if err := s.store.UpdateCase(ctx, tx, caseItem, caseVersion); err != nil {
			return err
		}
		return s.store.InsertAudit(ctx, tx, audit.Event{ActorID: principal.UserID, Action: "claim.submitted",
			ObjectType: "claim", ObjectID: claimID, Result: "success", RequestID: audit.CorrelationID(ctx),
			Details: map[string]any{"account_count": len(selected), "amount_minor": total}, CreatedAt: now})
	})
	return created, err
}

func (s *Service) Approve(ctx context.Context, principal domain.Principal, claimID, payoutKey string, expected int64) (domain.Payout, error) {
	if principal.Role != domain.RoleSupervisor {
		return domain.Payout{}, domain.ErrForbidden
	}
	if len(payoutKey) < 8 || len(payoutKey) > 128 {
		return domain.Payout{}, domain.FieldError{Field: "payout_key", Message: "must contain 8 to 128 bytes"}
	}
	now := s.clock.Now()
	var payout domain.Payout
	err := s.store.WithTx(ctx, func(tx *sql.Tx) error {
		claim, err := s.store.ClaimByID(ctx, tx, claimID)
		if err != nil {
			return err
		}
		if claim.Version != expected {
			return domain.VersionConflict{Entity: "claim", ID: claimID, Expected: expected}
		}
		rows, err := tx.QueryContext(ctx, `SELECT account_id FROM claim_accounts WHERE claim_id=? ORDER BY account_id`, claimID)
		if err != nil {
			return err
		}
		var accountIDs []string
		for rows.Next() {
			var id string
			if err := rows.Scan(&id); err != nil {
				rows.Close()
				return err
			}
			accountIDs = append(accountIDs, id)
		}
		if err := rows.Close(); err != nil {
			return err
		}
		if len(accountIDs) == 0 {
			return fmt.Errorf("claim has no accounts: %w", domain.ErrConflict)
		}
		if _, err := s.store.ReserveAccounts(ctx, tx, claimID, accountIDs); err != nil {
			return err
		}
		previous := claim.Version
		if err := claim.Transition(domain.ClaimApproved, principal.UserID, now); err != nil {
			return err
		}
		if err := s.store.UpdateClaim(ctx, tx, claim, previous); err != nil {
			return err
		}
		payoutID, err := s.ids.New("pay")
		if err != nil {
			return err
		}
		jobID, err := s.ids.New("job")
		if err != nil {
			return err
		}
		payout = domain.Payout{ID: payoutID, ClaimID: claimID, IdempotencyKey: payoutKey,
			Status: domain.PayoutPending, AmountMinor: claim.TotalMinor, Currency: claim.Currency,
			CreatedAt: now, UpdatedAt: now}
		if err := s.store.InsertPayout(ctx, tx, payout); err != nil {
			return err
		}
		if err := s.store.InsertJob(ctx, tx, store.Job{ID: jobID, Kind: "execute_payout",
			ResourceID: payoutID, Payload: []byte(`{"payout_id":"` + payoutID + `"}`), Status: "pending",
			MaxAttempts: s.maxAttempts, AvailableAt: now, CreatedAt: now, UpdatedAt: now}); err != nil {
			return err
		}
		return s.store.InsertAudit(ctx, tx, audit.Event{ActorID: principal.UserID, Action: "claim.approved",
			ObjectType: "claim", ObjectID: claimID, Result: "success", RequestID: audit.CorrelationID(ctx),
			Details: map[string]any{"payout_id": payoutID, "amount_minor": claim.TotalMinor}, CreatedAt: now})
	})
	return payout, err
}

func selectAccounts(accounts []domain.FinancialAccount, ids []string) []domain.FinancialAccount {
	wanted := make(map[string]struct{}, len(ids))
	for _, id := range ids {
		wanted[id] = struct{}{}
	}
	result := make([]domain.FinancialAccount, 0, len(ids))
	for _, account := range accounts {
		if _, ok := wanted[account.ID]; ok {
			result = append(result, account)
		}
	}
	return result
}

func retryAt(now time.Time, attempts int) time.Time {
	if attempts < 1 {
		attempts = 1
	}
	delay := time.Second << min(attempts-1, 6)
	return now.Add(delay)
}
