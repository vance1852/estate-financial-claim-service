package store

import (
	"context"
	"database/sql"
	"fmt"
	"strings"

	"github.com/vance1852/estate-financial-claim-service/internal/domain"
)

type CaseFilter struct {
	ClaimantUserID string
	Status         domain.CaseStatus
	Cursor         string
	Limit          int
}

func (s *Store) InsertCase(ctx context.Context, q DBTX, c domain.EstateCase) error {
	_, err := q.ExecContext(ctx, `INSERT INTO estate_cases
		(id,reference,deceased_name,deceased_id_hash,deceased_id_masked,jurisdiction,
		claimant_user_id,status,version,submitted_at,inquiry_completed_at,closed_at,created_at,updated_at)
		VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?)`, c.ID, c.Reference, c.DeceasedName, c.DeceasedIDHash,
		c.DeceasedIDMasked, c.Jurisdiction, c.ClaimantUserID, c.Status, c.Version,
		formatNullableTime(c.SubmittedAt), formatNullableTime(c.InquiryCompletedAt),
		formatNullableTime(c.ClosedAt), formatTime(c.CreatedAt), formatTime(c.UpdatedAt))
	if err != nil {
		return fmt.Errorf("insert estate case: %w", err)
	}
	return nil
}

func (s *Store) CaseByID(ctx context.Context, q DBTX, id string) (domain.EstateCase, error) {
	row := q.QueryRowContext(ctx, `SELECT id,reference,deceased_name,deceased_id_hash,deceased_id_masked,
		jurisdiction,claimant_user_id,status,version,submitted_at,inquiry_completed_at,closed_at,
		created_at,updated_at FROM estate_cases WHERE id=?`, id)
	return scanCase(row)
}

func scanCase(row rowScanner) (domain.EstateCase, error) {
	var c domain.EstateCase
	var status string
	var submitted, completed, closed sql.NullString
	var created, updated string
	err := row.Scan(&c.ID, &c.Reference, &c.DeceasedName, &c.DeceasedIDHash, &c.DeceasedIDMasked,
		&c.Jurisdiction, &c.ClaimantUserID, &status, &c.Version, &submitted, &completed,
		&closed, &created, &updated)
	if err != nil {
		return domain.EstateCase{}, mapNotFound("estate case", err)
	}
	c.Status = domain.CaseStatus(status)
	if c.SubmittedAt, err = nullableTime(submitted); err != nil {
		return domain.EstateCase{}, err
	}
	if c.InquiryCompletedAt, err = nullableTime(completed); err != nil {
		return domain.EstateCase{}, err
	}
	if c.ClosedAt, err = nullableTime(closed); err != nil {
		return domain.EstateCase{}, err
	}
	if c.CreatedAt, err = parseTime(created); err != nil {
		return domain.EstateCase{}, err
	}
	if c.UpdatedAt, err = parseTime(updated); err != nil {
		return domain.EstateCase{}, err
	}
	return c, nil
}

func (s *Store) UpdateCase(ctx context.Context, q DBTX, c domain.EstateCase, expectedVersion int64) error {
	result, err := q.ExecContext(ctx, `UPDATE estate_cases SET status=?,version=?,submitted_at=?,
		inquiry_completed_at=?,closed_at=?,updated_at=? WHERE id=? AND version=?`, c.Status, c.Version,
		formatNullableTime(c.SubmittedAt), formatNullableTime(c.InquiryCompletedAt),
		formatNullableTime(c.ClosedAt), formatTime(c.UpdatedAt), c.ID, expectedVersion)
	if err != nil {
		return fmt.Errorf("update estate case: %w", err)
	}
	count, _ := result.RowsAffected()
	if count != 1 {
		return domain.VersionConflict{Entity: "estate_case", ID: c.ID, Expected: expectedVersion}
	}
	return nil
}

func (s *Store) ListCases(ctx context.Context, filter CaseFilter) ([]domain.EstateCase, error) {
	limit := filter.Limit
	if limit < 1 || limit > 100 {
		limit = 25
	}
	query := `SELECT id,reference,deceased_name,deceased_id_hash,deceased_id_masked,
		jurisdiction,claimant_user_id,status,version,submitted_at,inquiry_completed_at,closed_at,
		created_at,updated_at FROM estate_cases WHERE 1=1`
	args := make([]any, 0, 4)
	if filter.ClaimantUserID != "" {
		query += " AND claimant_user_id=?"
		args = append(args, filter.ClaimantUserID)
	}
	if filter.Status != "" {
		query += " AND status=?"
		args = append(args, filter.Status)
	}
	if filter.Cursor != "" {
		query += " AND id>?"
		args = append(args, filter.Cursor)
	}
	query += " ORDER BY id LIMIT ?"
	args = append(args, limit)
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("list estate cases: %w", err)
	}
	defer rows.Close()
	result := make([]domain.EstateCase, 0, limit)
	for rows.Next() {
		item, err := scanCase(rows)
		if err != nil {
			return nil, err
		}
		result = append(result, item)
	}
	return result, rows.Err()
}

func (s *Store) UpsertParty(ctx context.Context, q DBTX, id, name, identityHash, masked string, createdAt string) (string, error) {
	_, err := q.ExecContext(ctx, `INSERT INTO parties(id,name,identity_hash,identity_masked,created_at)
		VALUES(?,?,?,?,?) ON CONFLICT(identity_hash) DO UPDATE SET name=excluded.name,
		identity_masked=excluded.identity_masked`, id, strings.TrimSpace(name), identityHash, masked, createdAt)
	if err != nil {
		return "", fmt.Errorf("upsert party: %w", err)
	}
	var actualID string
	if err := q.QueryRowContext(ctx, `SELECT id FROM parties WHERE identity_hash=?`, identityHash).Scan(&actualID); err != nil {
		return "", fmt.Errorf("read upserted party: %w", err)
	}
	return actualID, nil
}

func (s *Store) LinkParty(ctx context.Context, q DBTX, caseID, partyID string, relation domain.PartyRelation) error {
	if !relation.Valid() {
		return domain.FieldError{Field: "relation", Message: "is invalid"}
	}
	_, err := q.ExecContext(ctx, `INSERT INTO case_parties(case_id,party_id,relation,verified)
		VALUES(?,?,?,0)`, caseID, partyID, relation)
	if err != nil {
		return fmt.Errorf("link case party: %w", err)
	}
	return nil
}

func (s *Store) InsertRequiredDocument(ctx context.Context, q DBTX, id, caseID, kind, createdAt string) error {
	_, err := q.ExecContext(ctx, `INSERT INTO documents
		(id,case_id,kind,storage_key,checksum,status,version,created_at,updated_at)
		VALUES(?,?,?,?,'pending','pending',1,?,?)`, id, caseID, kind, "required:"+id, createdAt, createdAt)
	if err != nil {
		return fmt.Errorf("insert required document: %w", err)
	}
	return nil
}
