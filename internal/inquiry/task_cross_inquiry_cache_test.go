package inquiry

import (
	"context"
	"testing"

	"github.com/vance1852/estate-financial-claim-service/internal/domain"
)

func TestPartialResultCacheIsScopedToInquiry(t *testing.T) {
	f := newInquiryFixture(t)
	items, err := f.service.Dispatch(context.Background(), f.officer, f.caseID, "cache-scope-key", 2)
	if err != nil {
		t.Fatal(err)
	}
	for _, item := range items {
		if err := f.service.MarkDispatched(context.Background(), item.ID, "ref-"+item.ID); err != nil {
			t.Fatal(err)
		}
		if _, err := f.store.ExecContext(context.Background(), "UPDATE inquiries SET expected_parts=2 WHERE id=?", item.ID); err != nil {
			t.Fatal(err)
		}
	}
	input := ResultInput{PartKey: "page-1", Accounts: []AccountResult{{ExternalReference: "shared", Kind: domain.AccountDeposit, Currency: "CNY", BalanceMinor: 10}}}
	first, err := f.service.RecordResult(context.Background(), items[0].ID, input)
	if err != nil {
		t.Fatal(err)
	}
	second, err := f.service.RecordResult(context.Background(), items[1].ID, input)
	if err != nil {
		t.Fatal(err)
	}
	if first.ID == second.ID || second.ID != items[1].ID {
		t.Fatalf("second inquiry received cached result from first: first=%s second=%s want=%s", first.ID, second.ID, items[1].ID)
	}
}
