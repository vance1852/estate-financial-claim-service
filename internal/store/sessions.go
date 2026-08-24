package store

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"github.com/vance1852/estate-financial-claim-service/internal/domain"
)

type Session struct {
	ID         string
	UserID     string
	TokenHash  string
	ExpiresAt  time.Time
	RevokedAt  *time.Time
	CreatedAt  time.Time
	LastSeenAt time.Time
}

func (s *Store) CreateSession(ctx context.Context, session Session) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO sessions
		(id,user_id,token_hash,expires_at,revoked_at,created_at,last_seen_at)
		VALUES(?,?,?,?,?,?,?)`, session.ID, session.UserID, session.TokenHash,
		formatTime(session.ExpiresAt), formatNullableTime(session.RevokedAt),
		formatTime(session.CreatedAt), formatTime(session.LastSeenAt))
	if err != nil {
		return fmt.Errorf("create session: %w", err)
	}
	return nil
}

func (s *Store) SessionPrincipal(ctx context.Context, tokenHash string, now time.Time) (Session, domain.Principal, error) {
	row := s.db.QueryRowContext(ctx, `SELECT s.id,s.user_id,s.token_hash,s.expires_at,s.revoked_at,
		s.created_at,s.last_seen_at,u.role,u.active
		FROM sessions s JOIN users u ON u.id=s.user_id WHERE s.token_hash=?`, tokenHash)
	var session Session
	var expires, created, seen string
	var revoked sql.NullString
	var role string
	var active int
	if err := row.Scan(&session.ID, &session.UserID, &session.TokenHash, &expires, &revoked,
		&created, &seen, &role, &active); err != nil {
		return Session{}, domain.Principal{}, mapNotFound("session", err)
	}
	var err error
	if session.ExpiresAt, err = parseTime(expires); err != nil {
		return Session{}, domain.Principal{}, err
	}
	if session.RevokedAt, err = nullableTime(revoked); err != nil {
		return Session{}, domain.Principal{}, err
	}
	if session.CreatedAt, err = parseTime(created); err != nil {
		return Session{}, domain.Principal{}, err
	}
	if session.LastSeenAt, err = parseTime(seen); err != nil {
		return Session{}, domain.Principal{}, err
	}
	if active != 1 {
		return Session{}, domain.Principal{}, domain.ErrUnauthorized
	}
	if session.RevokedAt != nil {
		return Session{}, domain.Principal{}, domain.ErrUnauthorized
	}
	if !now.Before(session.ExpiresAt) {
		return Session{}, domain.Principal{}, domain.ErrExpired
	}
	return session, domain.Principal{UserID: session.UserID, Role: domain.Role(role)}, nil
}

func (s *Store) TouchSession(ctx context.Context, id string, at time.Time) error {
	result, err := s.db.ExecContext(ctx, `UPDATE sessions SET last_seen_at=?
		WHERE id=? AND revoked_at IS NULL AND expires_at>?`, formatTime(at), id, formatTime(at))
	if err != nil {
		return fmt.Errorf("touch session: %w", err)
	}
	count, _ := result.RowsAffected()
	if count != 1 {
		return domain.ErrUnauthorized
	}
	return nil
}

func (s *Store) RevokeSession(ctx context.Context, tokenHash string, at time.Time) error {
	result, err := s.db.ExecContext(ctx, `UPDATE sessions SET revoked_at=?
		WHERE token_hash=? AND revoked_at IS NULL`, formatTime(at), tokenHash)
	if err != nil {
		return fmt.Errorf("revoke session: %w", err)
	}
	count, _ := result.RowsAffected()
	if count == 0 {
		return domain.ErrNotFound
	}
	return nil
}

func (s *Store) DeleteExpiredSessions(ctx context.Context, before time.Time) (int64, error) {
	result, err := s.db.ExecContext(ctx, `DELETE FROM sessions WHERE expires_at<=? OR revoked_at IS NOT NULL`, formatTime(before))
	if err != nil {
		return 0, fmt.Errorf("delete expired sessions: %w", err)
	}
	return result.RowsAffected()
}
