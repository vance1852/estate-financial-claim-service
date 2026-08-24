package httpapi

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/vance1852/estate-financial-claim-service/internal/domain"
	"github.com/vance1852/estate-financial-claim-service/internal/ids"
)

func TestConcurrentProtectedRequestsKeepIndependentPrincipals(t *testing.T) {
	fixture := newHTTPFixture(t)
	ctx := context.Background()
	claimantLogin, err := fixture.auth.Login(ctx, "claimant@example.test", "http-test-password")
	if err != nil {
		t.Fatal(err)
	}
	officerLogin, err := fixture.auth.Login(ctx, "officer@example.test", "http-test-password")
	if err != nil {
		t.Fatal(err)
	}

	firstEntered := make(chan struct{})
	secondRead := make(chan struct{})
	firstPrincipal := make(chan domain.Principal, 1)
	secondPrincipal := make(chan domain.Principal, 1)
	next := http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/first" {
			close(firstEntered)
			<-secondRead
			principal, _ := principalFrom(r.Context())
			firstPrincipal <- principal
			return
		}
		principal, _ := principalFrom(r.Context())
		secondPrincipal <- principal
		close(secondRead)
	})
	middleware := Middleware{auth: fixture.auth, ids: &ids.Sequence{}, logger: slog.New(slog.NewTextHandler(io.Discard, nil))}
	handler := middleware.Protected(next)

	firstDone := make(chan struct{})
	go func() {
		request := httptest.NewRequest(http.MethodGet, "/first", nil)
		request.Header.Set("Authorization", "Bearer "+claimantLogin.Token)
		handler.ServeHTTP(httptest.NewRecorder(), request)
		close(firstDone)
	}()
	<-firstEntered
	request := httptest.NewRequest(http.MethodGet, "/second", nil)
	request.Header.Set("Authorization", "Bearer "+officerLogin.Token)
	handler.ServeHTTP(httptest.NewRecorder(), request)
	<-firstDone

	if principal := <-secondPrincipal; principal.UserID != "officer" || principal.Role != domain.RoleOfficer {
		t.Fatalf("second request principal = %#v", principal)
	}
	if principal := <-firstPrincipal; principal.UserID != "claimant" || principal.Role != domain.RoleClaimant {
		t.Fatalf("first request principal changed while handler was active: %#v", principal)
	}
}
