package auth

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/vance1852/estate-financial-claim-service/internal/clock"
	"github.com/vance1852/estate-financial-claim-service/internal/domain"
	"github.com/vance1852/estate-financial-claim-service/internal/ids"
	"github.com/vance1852/estate-financial-claim-service/internal/store"
)

func authFixture(t *testing.T, role domain.Role, active bool) (*Service, *store.Store, *clock.Manual) {
	t.Helper()
	ctx := context.Background()
	database, err := store.Open(ctx, filepath.Join(t.TempDir(), "auth.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	now := time.Date(2026, 8, 24, 8, 0, 0, 0, time.UTC)
	hash, err := HashPassword("correct-horse-battery-staple")
	if err != nil {
		t.Fatal(err)
	}
	if err := database.CreateUser(ctx, store.User{ID: "user_1", Email: "person@example.test",
		PasswordHash: hash, DisplayName: "Person", Role: role, Active: active,
		CreatedAt: now, UpdatedAt: now}); err != nil {
		t.Fatal(err)
	}
	manual := clock.NewManual(now)
	return New(database, manual, &ids.Sequence{}, 30*time.Minute), database, manual
}

func TestHashPasswordValidatesLengthAndUsesBcrypt(t *testing.T) {
	for _, password := range []string{"short", "", string(make([]byte, 129))} {
		if _, err := HashPassword(password); !errors.Is(err, domain.ErrValidation) {
			t.Errorf("password length %d error = %v", len(password), err)
		}
	}
	hash, err := HashPassword("a-secure-password")
	if err != nil {
		t.Fatal(err)
	}
	if hash == "a-secure-password" || len(hash) < 50 {
		t.Fatalf("unexpected password hash %q", hash)
	}
	second, err := HashPassword("a-secure-password")
	if err != nil {
		t.Fatal(err)
	}
	if second == hash {
		t.Fatal("bcrypt hashes must use independent salts")
	}
}

func TestLoginAuthenticateAndLogoutLifecycle(t *testing.T) {
	service, _, manual := authFixture(t, domain.RoleClaimant, true)
	ctx := context.Background()
	result, err := service.Login(ctx, " PERSON@example.test ", "correct-horse-battery-staple")
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Token) < 32 || result.Principal.UserID != "user_1" || result.Principal.Role != domain.RoleClaimant {
		t.Fatalf("login result = %#v", result)
	}
	if !result.ExpiresAt.Equal(manual.Now().Add(30 * time.Minute)) {
		t.Fatalf("expires = %v", result.ExpiresAt)
	}
	principal, err := service.Authenticate(ctx, result.Token)
	if err != nil {
		t.Fatal(err)
	}
	if principal != result.Principal {
		t.Fatalf("principal = %#v, want %#v", principal, result.Principal)
	}
	manual.Advance(time.Minute)
	if _, err := service.Authenticate(ctx, result.Token); err != nil {
		t.Fatalf("active session after touch: %v", err)
	}
	if err := service.Logout(ctx, result.Token); err != nil {
		t.Fatal(err)
	}
	if _, err := service.Authenticate(ctx, result.Token); !errors.Is(err, domain.ErrUnauthorized) {
		t.Fatalf("revoked authentication error = %v", err)
	}
	if err := service.Logout(ctx, result.Token); !errors.Is(err, domain.ErrUnauthorized) {
		t.Fatalf("second logout error = %v", err)
	}
}

func TestLoginRejectsUnknownInactiveAndWrongPassword(t *testing.T) {
	service, _, _ := authFixture(t, domain.RoleOfficer, true)
	ctx := context.Background()
	tests := []struct{ email, password string }{
		{"missing@example.test", "correct-horse-battery-staple"},
		{"person@example.test", "wrong-password"},
		{"", "correct-horse-battery-staple"},
		{"person@example.test", ""},
	}
	for _, test := range tests {
		if _, err := service.Login(ctx, test.email, test.password); !errors.Is(err, domain.ErrUnauthorized) {
			t.Errorf("login(%q) error = %v", test.email, err)
		}
	}
	inactive, _, _ := authFixture(t, domain.RoleOfficer, false)
	if _, err := inactive.Login(ctx, "person@example.test", "correct-horse-battery-staple"); !errors.Is(err, domain.ErrUnauthorized) {
		t.Fatalf("inactive login error = %v", err)
	}
}

func TestSessionExpiresAtBoundaryAndCanBePurged(t *testing.T) {
	service, database, manual := authFixture(t, domain.RoleSupervisor, true)
	ctx := context.Background()
	result, err := service.Login(ctx, "person@example.test", "correct-horse-battery-staple")
	if err != nil {
		t.Fatal(err)
	}
	manual.Set(result.ExpiresAt.Add(-time.Nanosecond))
	if _, err := service.Authenticate(ctx, result.Token); err != nil {
		t.Fatalf("session before boundary: %v", err)
	}
	manual.Set(result.ExpiresAt)
	if _, err := service.Authenticate(ctx, result.Token); !errors.Is(err, domain.ErrExpired) {
		t.Fatalf("boundary error = %v", err)
	}
	deleted, err := service.PurgeExpired(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if deleted != 1 {
		t.Fatalf("purged = %d, want 1", deleted)
	}
	var count int
	if err := database.QueryRowContext(ctx, "SELECT COUNT(*) FROM sessions").Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatalf("remaining sessions = %d", count)
	}
}

func TestAuthenticationRejectsMalformedTokens(t *testing.T) {
	service, _, _ := authFixture(t, domain.RoleClaimant, true)
	for _, token := range []string{"", "small", "bearer token with spaces but still invalid"} {
		if _, err := service.Authenticate(context.Background(), token); !errors.Is(err, domain.ErrUnauthorized) {
			t.Errorf("token %q error = %v", token, err)
		}
	}
	if err := service.Logout(context.Background(), ""); !errors.Is(err, domain.ErrUnauthorized) {
		t.Fatalf("empty logout error = %v", err)
	}
}

func TestSeparateLoginsRemainIndependentlyRevocable(t *testing.T) {
	service, _, _ := authFixture(t, domain.RoleClaimant, true)
	ctx := context.Background()
	first, err := service.Login(ctx, "person@example.test", "correct-horse-battery-staple")
	if err != nil {
		t.Fatal(err)
	}
	second, err := service.Login(ctx, "person@example.test", "correct-horse-battery-staple")
	if err != nil {
		t.Fatal(err)
	}
	if first.Token == second.Token {
		t.Fatal("separate logins returned the same token")
	}
	if err := service.Logout(ctx, first.Token); err != nil {
		t.Fatal(err)
	}
	if _, err := service.Authenticate(ctx, first.Token); !errors.Is(err, domain.ErrUnauthorized) {
		t.Fatalf("first token remains active: %v", err)
	}
	if _, err := service.Authenticate(ctx, second.Token); err != nil {
		t.Fatalf("second token was incorrectly revoked: %v", err)
	}
}
