package store

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/vance1852/estate-financial-claim-service/internal/domain"
)

func (s *Store) ActiveInstitutions(ctx context.Context) ([]string, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id FROM institutions WHERE active=1 ORDER BY code`)
	if err != nil {
		return nil, fmt.Errorf("list active institutions: %w", err)
	}
	defer rows.Close()
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

func (s *Store) InsertInstitution(ctx context.Context, id, code, name string, kind domain.InstitutionKind, at string) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO institutions(id,code,name,kind,active,created_at,updated_at)
		VALUES(?,?,?,?,1,?,?) ON CONFLICT(code) DO UPDATE SET name=excluded.name,kind=excluded.kind,
		active=1,updated_at=excluded.updated_at`, id, code, name, kind, at, at)
	if err != nil {
		return fmt.Errorf("insert institution: %w", err)
	}
	return nil
}

func (s *Store) InsertInquiry(ctx context.Context, q DBTX, inquiry domain.Inquiry) error {
	_, err := q.ExecContext(ctx, `INSERT INTO inquiries
		(id,case_id,institution_id,request_key,status,external_ref,expected_parts,received_parts,
		version,dispatched_at,completed_at,last_error,created_at,updated_at)
		VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?)`, inquiry.ID, inquiry.CaseID, inquiry.InstitutionID,
		inquiry.RequestKey, inquiry.Status, inquiry.ExternalRef, inquiry.ExpectedParts,
		inquiry.ReceivedParts, inquiry.Version, formatNullableTime(inquiry.DispatchedAt),
		formatNullableTime(inquiry.CompletedAt), inquiry.LastError, formatTime(inquiry.CreatedAt),
		formatTime(inquiry.UpdatedAt))
	if err != nil {
		return fmt.Errorf("insert inquiry: %w", err)
	}
	return nil
}

// PrepareInquiryDispatch inserts the inquiry and dispatch job pairs within the
// caller's transaction so a later failure (for example the dispatch audit write)
// rolls every queued inquiry and task back together with the case transition.
func (s *Store) PrepareInquiryDispatch(ctx context.Context, q DBTX, inquiries []domain.Inquiry, jobs []Job) error {
	if len(inquiries) != len(jobs) {
		return fmt.Errorf("inquiry and dispatch job counts differ: %w", domain.ErrValidation)
	}
	for index := range inquiries {
		if err := s.InsertInquiry(ctx, q, inquiries[index]); err != nil {
			return err
		}
		if err := s.InsertJob(ctx, q, jobs[index]); err != nil {
			return err
		}
	}
	return nil
}

func (s *Store) InquiryByID(ctx context.Context, q DBTX, id string) (domain.Inquiry, error) {
	row := q.QueryRowContext(ctx, `SELECT id,case_id,institution_id,request_key,status,external_ref,
		expected_parts,received_parts,version,dispatched_at,completed_at,last_error,created_at,updated_at
		FROM inquiries WHERE id=?`, id)
	return scanInquiry(row)
}

func scanInquiry(row rowScanner) (domain.Inquiry, error) {
	var item domain.Inquiry
	var status string
	var dispatched, completed sql.NullString
	var created, updated string
	err := row.Scan(&item.ID, &item.CaseID, &item.InstitutionID, &item.RequestKey, &status,
		&item.ExternalRef, &item.ExpectedParts, &item.ReceivedParts, &item.Version,
		&dispatched, &completed, &item.LastError, &created, &updated)
	if err != nil {
		return domain.Inquiry{}, mapNotFound("inquiry", err)
	}
	item.Status = domain.InquiryStatus(status)
	if item.DispatchedAt, err = nullableTime(dispatched); err != nil {
		return domain.Inquiry{}, err
	}
	if item.CompletedAt, err = nullableTime(completed); err != nil {
		return domain.Inquiry{}, err
	}
	if item.CreatedAt, err = parseTime(created); err != nil {
		return domain.Inquiry{}, err
	}
	if item.UpdatedAt, err = parseTime(updated); err != nil {
		return domain.Inquiry{}, err
	}
	return item, nil
}

func (s *Store) UpdateInquiry(ctx context.Context, q DBTX, item domain.Inquiry, expected int64) error {
	result, err := q.ExecContext(ctx, `UPDATE inquiries SET status=?,external_ref=?,expected_parts=?,
		received_parts=?,version=?,dispatched_at=?,completed_at=?,last_error=?,updated_at=?
		WHERE id=? AND version=?`, item.Status, item.ExternalRef, item.ExpectedParts,
		item.ReceivedParts, item.Version, formatNullableTime(item.DispatchedAt),
		formatNullableTime(item.CompletedAt), item.LastError, formatTime(item.UpdatedAt), item.ID, expected)
	if err != nil {
		return fmt.Errorf("update inquiry: %w", err)
	}
	count, _ := result.RowsAffected()
	if count != 1 {
		return domain.VersionConflict{Entity: "inquiry", ID: item.ID, Expected: expected}
	}
	return nil
}

func (s *Store) InsertInquiryResult(ctx context.Context, q DBTX, id, inquiryID, partKey, hash, receivedAt string) (bool, error) {
	result, err := q.ExecContext(ctx, `INSERT INTO inquiry_results(id,inquiry_id,part_key,payload_hash,received_at)
		VALUES(?,?,?,?,?) ON CONFLICT(inquiry_id,part_key) DO NOTHING`, id, inquiryID, partKey, hash, receivedAt)
	if err != nil {
		return false, fmt.Errorf("insert inquiry result: %w", err)
	}
	count, _ := result.RowsAffected()
	return count == 1, nil
}

func (s *Store) InquiryResultHash(ctx context.Context, q DBTX, inquiryID, partKey string) (string, error) {
	var hash string
	err := q.QueryRowContext(ctx, `SELECT payload_hash FROM inquiry_results
		WHERE inquiry_id=? AND part_key=?`, inquiryID, partKey).Scan(&hash)
	if err != nil {
		return "", mapNotFound("inquiry result", err)
	}
	return hash, nil
}

func (s *Store) UpsertFinancialAccount(ctx context.Context, q DBTX, account domain.FinancialAccount) error {
	_, err := q.ExecContext(ctx, `INSERT INTO financial_accounts
		(id,case_id,institution_id,inquiry_id,external_hash,kind,currency,balance_minor,restricted,
		restriction_note,reserved_claim_id,version,created_at,updated_at)
		VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?)
		ON CONFLICT(case_id,institution_id,external_hash) DO UPDATE SET
		inquiry_id=excluded.inquiry_id,kind=excluded.kind,currency=excluded.currency,
		balance_minor=excluded.balance_minor,restricted=excluded.restricted,
		restriction_note=excluded.restriction_note,version=financial_accounts.version+1,
		updated_at=excluded.updated_at WHERE financial_accounts.reserved_claim_id IS NULL
		OR financial_accounts.reserved_claim_id=''`, account.ID, account.CaseID, account.InstitutionID,
		account.InquiryID, account.ExternalHash, account.Kind, account.Currency, account.BalanceMinor,
		boolInt(account.Restricted), account.RestrictionNote, nullString(account.ReservedClaimID),
		account.Version, formatTime(account.CreatedAt), formatTime(account.UpdatedAt))
	if err != nil {
		return fmt.Errorf("upsert financial account: %w", err)
	}
	return nil
}

func (s *Store) AccountsForCase(ctx context.Context, q DBTX, caseID string) ([]domain.FinancialAccount, error) {
	rows, err := q.QueryContext(ctx, `SELECT id,case_id,institution_id,inquiry_id,external_hash,kind,
		currency,balance_minor,restricted,restriction_note,COALESCE(reserved_claim_id,''),version,
		created_at,updated_at FROM financial_accounts WHERE case_id=? ORDER BY id`, caseID)
	if err != nil {
		return nil, fmt.Errorf("query financial accounts: %w", err)
	}
	defer rows.Close()
	result := make([]domain.FinancialAccount, 0)
	for rows.Next() {
		var item domain.FinancialAccount
		var kind string
		var restricted int
		var created, updated string
		if err := rows.Scan(&item.ID, &item.CaseID, &item.InstitutionID, &item.InquiryID,
			&item.ExternalHash, &kind, &item.Currency, &item.BalanceMinor, &restricted,
			&item.RestrictionNote, &item.ReservedClaimID, &item.Version, &created, &updated); err != nil {
			return nil, err
		}
		item.Kind = domain.AccountKind(kind)
		item.Restricted = restricted == 1
		item.CreatedAt, err = parseTime(created)
		if err != nil {
			return nil, err
		}
		item.UpdatedAt, err = parseTime(updated)
		if err != nil {
			return nil, err
		}
		result = append(result, item)
	}
	return result, rows.Err()
}

func nullString(value string) any {
	if value == "" {
		return nil
	}
	return value
}
