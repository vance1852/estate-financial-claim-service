package httpapi

import (
	"net/http"
	"strings"
	"testing"
)

func TestIdempotencyConflictDoesNotExposeIdentityNumber(t *testing.T) {
	fixture := newHTTPFixture(t)
	token := loginToken(t, fixture, "claimant@example.test")
	headers := map[string]string{
		"Authorization":   "Bearer " + token,
		"Idempotency-Key": "private-conflict-key",
	}
	body := map[string]any{
		"deceased":     map[string]string{"Name": "Deceased Person", "IDNumber": "37020019500101001X"},
		"claimant":     map[string]string{"Name": "Claimant Person", "IDNumber": "370200198001010019"},
		"relation":     "child",
		"jurisdiction": "Qingdao",
	}
	created := request(t, fixture.handler, http.MethodPost, "/v1/cases", body, headers)
	if created.Code != http.StatusCreated {
		t.Fatalf("initial submission status=%d body=%s", created.Code, created.Body.String())
	}
	changedIdentity := "370200194001010011"
	body["deceased"] = map[string]string{"Name": "Another Person", "IDNumber": changedIdentity}
	conflict := request(t, fixture.handler, http.MethodPost, "/v1/cases", body, headers)
	if conflict.Code != http.StatusConflict || !strings.Contains(conflict.Body.String(), `"code":"conflict"`) {
		t.Fatalf("conflict status=%d body=%s", conflict.Code, conflict.Body.String())
	}
	if strings.Contains(conflict.Body.String(), changedIdentity) {
		t.Fatalf("conflict response exposed deceased identity: %s", conflict.Body.String())
	}
}
