package store

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"github.com/vance1852/estate-financial-claim-service/internal/domain"
)

func (s *Store) InsertClaim(ctx context.Context, q DBTX, claim domain.Claim) error {
	_, err := q.ExecContext(ctx, `INSERT INTO claims
		(id,case_id,claimant_user_id,status,total_minor,currency,version,approved_by,approved_at,created_at,updated_at)
		VALUES(?,?,?,?,?,?,?,?,?,?,?)`, claim.ID, claim.CaseID, claim.ClaimantUserID, claim.Status,
		claim.TotalMinor, claim.Currency, claim.Version, nullString(claim.ApprovedBy),
		formatNullableTime(claim.ApprovedAt), formatTime(claim.CreatedAt), formatTime(claim.UpdatedAt))
	if err != nil {
		return fmt.Errorf("insert claim: %w", err)
	}
	return nil
}

func (s *Store) ClaimByID(ctx context.Context, q DBTX, id string) (domain.Claim, error) {
	row := q.QueryRowContext(ctx, `SELECT id,case_id,claimant_user_id,status,total_minor,currency,
		version,COALESCE(approved_by,''),approved_at,created_at,updated_at FROM claims WHERE id=?`, id)
	return scanClaim(row)
}

func scanClaim(row rowScanner) (domain.Claim, error) {
	var claim domain.Claim
	var status string
	var approved sql.NullString
	var created, updated string
	err := row.Scan(&claim.ID, &claim.CaseID, &claim.ClaimantUserID, &status, &claim.TotalMinor,
		&claim.Currency, &claim.Version, &claim.ApprovedBy, &approved, &created, &updated)
	if err != nil {
		return domain.Claim{}, mapNotFound("claim", err)
	}
	claim.Status = domain.ClaimStatus(status)
	if claim.ApprovedAt, err = nullableTime(approved); err != nil {
		return domain.Claim{}, err
	}
	if claim.CreatedAt, err = parseTime(created); err != nil {
		return domain.Claim{}, err
	}
	if claim.UpdatedAt, err = parseTime(updated); err != nil {
		return domain.Claim{}, err
	}
	return claim, nil
}

func (s *Store) UpdateClaim(ctx context.Context, q DBTX, claim domain.Claim, expected int64) error {
	result, err := q.ExecContext(ctx, `UPDATE claims SET status=?,total_minor=?,version=?,approved_by=?,
		approved_at=?,updated_at=? WHERE id=? AND version=?`, claim.Status, claim.TotalMinor, claim.Version,
		nullString(claim.ApprovedBy), formatNullableTime(claim.ApprovedAt), formatTime(claim.UpdatedAt),
		claim.ID, expected)
	if err != nil {
		return fmt.Errorf("update claim: %w", err)
	}
	count, _ := result.RowsAffected()
	if count != 1 {
		return domain.VersionConflict{Entity: "claim", ID: claim.ID, Expected: expected}
	}
	return nil
}

func (s *Store) ReserveAccounts(ctx context.Context, q DBTX, claimID string, accountIDs []string) (int64, error) {
	var total int64
	for _, accountID := range accountIDs {
		var amount int64
		result, err := q.ExecContext(ctx, `UPDATE financial_accounts SET reserved_claim_id=?,version=version+1,
			updated_at=strftime('%Y-%m-%dT%H:%M:%fZ','now') WHERE id=? AND restricted=0
			AND (reserved_claim_id IS NULL OR reserved_claim_id='')`, claimID, accountID)
		if err != nil {
			return 0, fmt.Errorf("reserve account %s: %w", accountID, err)
		}
		count, _ := result.RowsAffected()
		if count != 1 {
			return 0, fmt.Errorf("account %s is no longer eligible: %w", accountID, domain.ErrConflict)
		}
		if err := q.QueryRowContext(ctx, `SELECT balance_minor FROM financial_accounts WHERE id=?`, accountID).Scan(&amount); err != nil {
			return 0, fmt.Errorf("read reserved amount: %w", err)
		}
		total += amount
	}
	return total, nil
}

func (s *Store) InsertPayout(ctx context.Context, q DBTX, payout domain.Payout) error {
	_, err := q.ExecContext(ctx, `INSERT INTO payouts
		(id,claim_id,idempotency_key,status,amount_minor,currency,provider_ref,attempts,last_error,created_at,updated_at)
		VALUES(?,?,?,?,?,?,?,?,?,?,?)`, payout.ID, payout.ClaimID, payout.IdempotencyKey, payout.Status,
		payout.AmountMinor, payout.Currency, payout.ProviderRef, payout.Attempts, payout.LastError,
		formatTime(payout.CreatedAt), formatTime(payout.UpdatedAt))
	if err != nil {
		return fmt.Errorf("insert payout: %w", err)
	}
	return nil
}

func (s *Store) PayoutByClaim(ctx context.Context, claimID string) (domain.Payout, error) {
	row := s.db.QueryRowContext(ctx, `SELECT id,claim_id,idempotency_key,status,amount_minor,currency,
		provider_ref,attempts,last_error,created_at,updated_at FROM payouts WHERE claim_id=?`, claimID)
	var payout domain.Payout
	var status, created, updated string
	if err := row.Scan(&payout.ID, &payout.ClaimID, &payout.IdempotencyKey, &status,
		&payout.AmountMinor, &payout.Currency, &payout.ProviderRef, &payout.Attempts,
		&payout.LastError, &created, &updated); err != nil {
		return domain.Payout{}, mapNotFound("payout", err)
	}
	payout.Status = domain.PayoutStatus(status)
	var err error
	if payout.CreatedAt, err = parseTime(created); err != nil {
		return domain.Payout{}, err
	}
	if payout.UpdatedAt, err = parseTime(updated); err != nil {
		return domain.Payout{}, err
	}
	return payout, nil
}

func (s *Store) MarkPayoutSubmitted(ctx context.Context, id string, at time.Time) error {
	result, err := s.db.ExecContext(ctx, `UPDATE payouts SET status='submitted',attempts=attempts+1,
		updated_at=? WHERE id=? AND status='pending'`, formatTime(at), id)
	if err != nil {
		return fmt.Errorf("mark payout submitted: %w", err)
	}
	count, _ := result.RowsAffected()
	if count == 1 {
		return nil
	}
	var status string
	if err := s.db.QueryRowContext(ctx, `SELECT status FROM payouts WHERE id=?`, id).Scan(&status); err != nil {
		return mapNotFound("payout", err)
	}
	if status == string(domain.PayoutSubmitted) || status == string(domain.PayoutConfirmed) {
		return nil
	}
	return domain.ErrConflict
}

func (s *Store) ConfirmPayout(ctx context.Context, id, providerRef string, at string) error {
	return s.WithTx(ctx, func(tx *sql.Tx) error {
		result, err := tx.ExecContext(ctx, `UPDATE payouts SET status='confirmed',provider_ref=?,
			attempts=attempts+1,updated_at=? WHERE id=? AND status IN ('pending','submitted')`, providerRef, at, id)
		if err != nil {
			return fmt.Errorf("confirm payout: %w", err)
		}
		if count, _ := result.RowsAffected(); count != 1 {
			var status, existingRef string
			if err := tx.QueryRowContext(ctx, `SELECT status,provider_ref FROM payouts WHERE id=?`, id).
				Scan(&status, &existingRef); err != nil {
				return mapNotFound("payout", err)
			}
			// A replayed confirmation is only valid when the provider reference
			// matches the one already recorded; a different reference must still be
			// rejected so an unrelated confirmation is not attributed to this payout.
			if status != string(domain.PayoutConfirmed) || existingRef != providerRef {
				return domain.ErrConflict
			}
		}
		var claimID string
		if err := tx.QueryRowContext(ctx, `SELECT claim_id FROM payouts WHERE id=?`, id).Scan(&claimID); err != nil {
			return err
		}
		_, err = tx.ExecContext(ctx, `UPDATE claims SET status='paid',version=version+1,updated_at=?
			WHERE id=? AND status IN ('approved','paying')`, at, claimID)
		return err
	})
}
