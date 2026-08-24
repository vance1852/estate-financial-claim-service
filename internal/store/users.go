package store

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"

	"github.com/vance1852/estate-financial-claim-service/internal/domain"
)

type User struct {
	ID           string
	Email        string
	PasswordHash string
	DisplayName  string
	Role         domain.Role
	Active       bool
	CreatedAt    time.Time
	UpdatedAt    time.Time
}

func (s *Store) CreateUser(ctx context.Context, user User) error {
	if strings.TrimSpace(user.ID) == "" || strings.TrimSpace(user.Email) == "" {
		return domain.FieldError{Field: "user", Message: "id and email are required"}
	}
	if !user.Role.Valid() {
		return domain.FieldError{Field: "role", Message: "is invalid"}
	}
	_, err := s.db.ExecContext(ctx, `INSERT INTO users
		(id,email,password_hash,display_name,role,active,created_at,updated_at)
		VALUES(?,?,?,?,?,?,?,?)`, user.ID, strings.ToLower(strings.TrimSpace(user.Email)),
		user.PasswordHash, user.DisplayName, user.Role, boolInt(user.Active),
		formatTime(user.CreatedAt), formatTime(user.UpdatedAt))
	if err != nil {
		return fmt.Errorf("create user: %w", err)
	}
	return nil
}

func (s *Store) UserByEmail(ctx context.Context, email string) (User, error) {
	return scanUser(s.db.QueryRowContext(ctx, `SELECT id,email,password_hash,display_name,role,active,created_at,updated_at
		FROM users WHERE email = ? COLLATE NOCASE`, strings.TrimSpace(email)))
}

func (s *Store) UserByID(ctx context.Context, id string) (User, error) {
	return scanUser(s.db.QueryRowContext(ctx, `SELECT id,email,password_hash,display_name,role,active,created_at,updated_at
		FROM users WHERE id = ?`, id))
}

type rowScanner interface{ Scan(...any) error }

func scanUser(row rowScanner) (User, error) {
	var user User
	var role string
	var active int
	var created, updated string
	if err := row.Scan(&user.ID, &user.Email, &user.PasswordHash, &user.DisplayName, &role, &active, &created, &updated); err != nil {
		return User{}, mapNotFound("user", err)
	}
	user.Role = domain.Role(role)
	user.Active = active == 1
	var err error
	if user.CreatedAt, err = parseTime(created); err != nil {
		return User{}, err
	}
	if user.UpdatedAt, err = parseTime(updated); err != nil {
		return User{}, err
	}
	return user, nil
}

func (s *Store) SeedUserTx(ctx context.Context, tx *sql.Tx, user User) error {
	_, err := tx.ExecContext(ctx, `INSERT OR IGNORE INTO users
		(id,email,password_hash,display_name,role,active,created_at,updated_at)
		VALUES(?,?,?,?,?,?,?,?)`, user.ID, user.Email, user.PasswordHash, user.DisplayName,
		user.Role, boolInt(user.Active), formatTime(user.CreatedAt), formatTime(user.UpdatedAt))
	return err
}
