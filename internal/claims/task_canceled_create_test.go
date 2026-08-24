package claims

import (
	"context"
	"testing"

	"github.com/vance1852/estate-financial-claim-service/internal/domain"
)

func TestCanceledClaimCreationDoesNotPersist(t *testing.T) {
	f := newClaimFixture(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := f.service.Create(ctx, f.claimant, f.caseID, f.accountIDs); err == nil {
		t.Fatal("canceled claim creation succeeded")
	}
	var claims, reserved int
	if err := f.store.QueryRowContext(context.Background(), "SELECT COUNT(*) FROM claims").Scan(&claims); err != nil {
		t.Fatal(err)
	}
	if err := f.store.QueryRowContext(context.Background(), "SELECT COUNT(*) FROM financial_accounts WHERE reserved_claim_id IS NOT NULL").Scan(&reserved); err != nil {
		t.Fatal(err)
	}
	item, err := f.store.CaseByID(context.Background(), f.store, f.caseID)
	if err != nil {
		t.Fatal(err)
	}
	if claims != 0 || reserved != 0 || item.Status != domain.CaseEligible {
		t.Fatalf("canceled command persisted state: claims=%d reserved=%d case=%s", claims, reserved, item.Status)
	}
}
