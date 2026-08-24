package httpapi

import (
	"net/http"

	"github.com/vance1852/estate-financial-claim-service/internal/claims"
	"github.com/vance1852/estate-financial-claim-service/internal/domain"
)

type ClaimHandlers struct{ claims *claims.Service }

func (h ClaimHandlers) Create(w http.ResponseWriter, r *http.Request) {
	principal, _ := principalFrom(r.Context())
	var input struct {
		CaseID     string   `json:"case_id"`
		AccountIDs []string `json:"account_ids"`
	}
	if err := decodeJSON(w, r, &input); err != nil {
		writeError(w, r, domain.FieldError{Field: "body", Message: err.Error()})
		return
	}
	claim, err := h.claims.Create(r.Context(), principal, input.CaseID, input.AccountIDs)
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, claim)
}

func (h ClaimHandlers) Approve(w http.ResponseWriter, r *http.Request) {
	principal, _ := principalFrom(r.Context())
	version, err := parseVersion(r)
	if err != nil {
		writeError(w, r, domain.FieldError{Field: "If-Match", Message: err.Error()})
		return
	}
	var input struct {
		PayoutKey string `json:"payout_key"`
	}
	if err := decodeJSON(w, r, &input); err != nil {
		writeError(w, r, domain.FieldError{Field: "body", Message: err.Error()})
		return
	}
	payout, err := h.claims.Approve(r.Context(), principal, r.PathValue("claim_id"), input.PayoutKey, version)
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusAccepted, payout)
}
