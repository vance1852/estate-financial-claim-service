package httpapi

import (
	"context"
	"log/slog"
	"net/http"
	"time"

	"github.com/vance1852/estate-financial-claim-service/internal/auth"
	"github.com/vance1852/estate-financial-claim-service/internal/cases"
	"github.com/vance1852/estate-financial-claim-service/internal/claims"
	"github.com/vance1852/estate-financial-claim-service/internal/ids"
	"github.com/vance1852/estate-financial-claim-service/internal/inquiry"
	"github.com/vance1852/estate-financial-claim-service/internal/store"
)

type Dependencies struct {
	Store     *store.Store
	Auth      *auth.Service
	Cases     *cases.Service
	Inquiries *inquiry.Service
	Claims    *claims.Service
	IDs       ids.Generator
	Logger    *slog.Logger
}

func New(deps Dependencies) http.Handler {
	middleware := Middleware{auth: deps.Auth, ids: deps.IDs, logger: deps.Logger}
	authHandlers := AuthHandlers{service: deps.Auth}
	caseHandlers := CaseHandlers{cases: deps.Cases, inquiries: deps.Inquiries}
	claimHandlers := ClaimHandlers{claims: deps.Claims}
	public := http.NewServeMux()
	public.HandleFunc("POST /v1/auth/login", authHandlers.Login)
	public.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, map[string]string{"status": "alive"})
	})
	public.HandleFunc("GET /readyz", func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := contextWithTimeout(r, 2*time.Second)
		defer cancel()
		if err := deps.Store.Ping(ctx); err != nil {
			writeError(w, r, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]string{"status": "ready"})
	})
	protected := http.NewServeMux()
	protected.HandleFunc("POST /v1/auth/logout", authHandlers.Logout)
	protected.HandleFunc("POST /v1/cases", caseHandlers.Submit)
	protected.HandleFunc("GET /v1/cases", caseHandlers.List)
	protected.HandleFunc("GET /v1/cases/{case_id}", caseHandlers.Get)
	protected.HandleFunc("POST /v1/cases/{case_id}/review", caseHandlers.StartReview)
	protected.HandleFunc("POST /v1/cases/{case_id}/inquiries", caseHandlers.DispatchInquiries)
	protected.HandleFunc("POST /v1/claims", claimHandlers.Create)
	protected.HandleFunc("POST /v1/claims/{claim_id}/approve", claimHandlers.Approve)
	root := http.NewServeMux()
	root.Handle("/healthz", middleware.Public(public))
	root.Handle("/readyz", middleware.Public(public))
	root.Handle("/v1/auth/login", middleware.Public(public))
	root.Handle("/v1/", middleware.Protected(protected))
	return root
}

func contextWithTimeout(r *http.Request, timeout time.Duration) (context.Context, context.CancelFunc) {
	return context.WithTimeout(r.Context(), timeout)
}
