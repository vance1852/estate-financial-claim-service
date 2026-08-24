package auth

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"

	"golang.org/x/crypto/bcrypt"

	"github.com/vance1852/estate-financial-claim-service/internal/clock"
	"github.com/vance1852/estate-financial-claim-service/internal/domain"
	"github.com/vance1852/estate-financial-claim-service/internal/ids"
	"github.com/vance1852/estate-financial-claim-service/internal/store"
)

type Service struct {
	store *store.Store
	clock clock.Clock
	ids   ids.Generator
	ttl   time.Duration
}

type LoginResult struct {
	Token     string
	ExpiresAt time.Time
	Principal domain.Principal
}

func New(database *store.Store, c clock.Clock, generator ids.Generator, ttl time.Duration) *Service {
	return &Service{store: database, clock: c, ids: generator, ttl: ttl}
}

func HashPassword(password string) (string, error) {
	if len(password) < 10 || len(password) > 128 {
		return "", domain.FieldError{Field: "password", Message: "must contain 10 to 128 bytes"}
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		return "", fmt.Errorf("hash password: %w", err)
	}
	return string(hash), nil
}

func (s *Service) Login(ctx context.Context, email, password string) (LoginResult, error) {
	if strings.TrimSpace(email) == "" || password == "" {
		return LoginResult{}, domain.ErrUnauthorized
	}
	user, err := s.store.UserByEmail(ctx, email)
	if err != nil {
		if errors.Is(err, domain.ErrNotFound) {
			return LoginResult{}, domain.ErrUnauthorized
		}
		return LoginResult{}, fmt.Errorf("load login user: %w", err)
	}
	if !user.Active || bcrypt.CompareHashAndPassword([]byte(user.PasswordHash), []byte(password)) != nil {
		return LoginResult{}, domain.ErrUnauthorized
	}
	token, hash, err := newToken()
	if err != nil {
		return LoginResult{}, err
	}
	id, err := s.ids.New("ses")
	if err != nil {
		return LoginResult{}, err
	}
	now := s.clock.Now()
	expires := now.Add(s.ttl)
	err = s.store.CreateSession(ctx, store.Session{
		ID: id, UserID: user.ID, TokenHash: hash, ExpiresAt: expires,
		CreatedAt: now, LastSeenAt: now,
	})
	if err != nil {
		return LoginResult{}, err
	}
	return LoginResult{Token: token, ExpiresAt: expires, Principal: domain.Principal{UserID: user.ID, Role: user.Role}}, nil
}

func (s *Service) Authenticate(ctx context.Context, token string) (domain.Principal, error) {
	if len(token) < 32 {
		return domain.Principal{}, domain.ErrUnauthorized
	}
	hash := tokenHash(token)
	session, principal, err := s.store.SessionPrincipal(ctx, hash, s.clock.Now())
	if err != nil {
		if errors.Is(err, domain.ErrExpired) {
			return domain.Principal{}, domain.ErrExpired
		}
		return domain.Principal{}, domain.ErrUnauthorized
	}
	if err := s.store.TouchSession(ctx, session.ID, s.clock.Now()); err != nil {
		return domain.Principal{}, domain.ErrUnauthorized
	}
	return principal, nil
}

func (s *Service) Logout(ctx context.Context, token string) error {
	if token == "" {
		return domain.ErrUnauthorized
	}
	err := s.store.RevokeSession(ctx, tokenHash(token), s.clock.Now())
	if errors.Is(err, domain.ErrNotFound) {
		return domain.ErrUnauthorized
	}
	return err
}

func (s *Service) PurgeExpired(ctx context.Context) (int64, error) {
	return s.store.DeleteExpiredSessions(ctx, s.clock.Now())
}

func newToken() (string, string, error) {
	buffer := make([]byte, 32)
	if _, err := rand.Read(buffer); err != nil {
		return "", "", fmt.Errorf("read session token: %w", err)
	}
	token := base64.RawURLEncoding.EncodeToString(buffer)
	return token, tokenHash(token), nil
}

func tokenHash(token string) string {
	digest := sha256.Sum256([]byte(token))
	return hex.EncodeToString(digest[:])
}
