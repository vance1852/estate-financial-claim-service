package auth

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/vance1852/estate-financial-claim-service/internal/domain"
	"github.com/vance1852/estate-financial-claim-service/internal/ids"
)

func TestLogoutRevocationIsVisibleAcrossServiceInstances(t *testing.T) {
	instanceA, database, manual := authFixture(t, domain.RoleClaimant, true)
	instanceB := New(database, manual, &ids.Sequence{}, 30*time.Minute)
	ctx := context.Background()

	revoked, err := instanceA.Login(ctx, "person@example.test", "correct-horse-battery-staple")
	if err != nil {
		t.Fatal(err)
	}
	active, err := instanceA.Login(ctx, "person@example.test", "correct-horse-battery-staple")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := instanceA.Authenticate(ctx, revoked.Token); err != nil {
		t.Fatalf("warm revoked-token cache on instance A: %v", err)
	}
	if _, err := instanceA.Authenticate(ctx, active.Token); err != nil {
		t.Fatalf("warm active-token cache on instance A: %v", err)
	}

	if err := instanceB.Logout(ctx, revoked.Token); err != nil {
		t.Fatalf("logout through instance B: %v", err)
	}
	if _, err := instanceB.Authenticate(ctx, revoked.Token); !errors.Is(err, domain.ErrUnauthorized) {
		t.Fatalf("instance B accepted persisted revoked token: %v", err)
	}
	if _, err := instanceA.Authenticate(ctx, active.Token); err != nil {
		t.Fatalf("separate active token was invalidated: %v", err)
	}
	if _, err := instanceA.Authenticate(ctx, revoked.Token); !errors.Is(err, domain.ErrUnauthorized) {
		t.Fatalf("instance A accepted token revoked through instance B: %v", err)
	}
}
