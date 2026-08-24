package httpapi

import (
	"net/http"
	"strconv"

	"github.com/vance1852/estate-financial-claim-service/internal/cases"
	"github.com/vance1852/estate-financial-claim-service/internal/domain"
	"github.com/vance1852/estate-financial-claim-service/internal/inquiry"
	"github.com/vance1852/estate-financial-claim-service/internal/store"
)

type CaseHandlers struct {
	cases     *cases.Service
	inquiries *inquiry.Service
}

func (h CaseHandlers) Submit(w http.ResponseWriter, r *http.Request) {
	principal, _ := principalFrom(r.Context())
	var input cases.SubmitInput
	if err := decodeJSON(w, r, &input); err != nil {
		writeError(w, r, domain.FieldError{Field: "body", Message: err.Error()})
		return
	}
	input.IdempotencyKey = r.Header.Get("Idempotency-Key")
	result, err := h.cases.Submit(r.Context(), principal, input)
	if err != nil {
		writeError(w, r, err)
		return
	}
	status := http.StatusCreated
	if result.Replayed {
		status = http.StatusOK
	}
	writeJSON(w, status, result)
}

func (h CaseHandlers) Get(w http.ResponseWriter, r *http.Request) {
	principal, _ := principalFrom(r.Context())
	item, err := h.cases.Get(r.Context(), principal, r.PathValue("case_id"))
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, item)
}

func (h CaseHandlers) List(w http.ResponseWriter, r *http.Request) {
	principal, _ := principalFrom(r.Context())
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	items, err := h.cases.List(r.Context(), principal, store.CaseFilter{
		Status: domain.CaseStatus(r.URL.Query().Get("status")), Cursor: r.URL.Query().Get("cursor"), Limit: limit,
	})
	if err != nil {
		writeError(w, r, err)
		return
	}
	response := map[string]any{"items": items, "next_cursor": ""}
	if len(items) > 0 {
		response["next_cursor"] = items[len(items)-1].ID
	}
	writeJSON(w, http.StatusOK, response)
}

func (h CaseHandlers) StartReview(w http.ResponseWriter, r *http.Request) {
	principal, _ := principalFrom(r.Context())
	version, err := parseVersion(r)
	if err != nil {
		writeError(w, r, domain.FieldError{Field: "If-Match", Message: err.Error()})
		return
	}
	if err := h.cases.StartReview(r.Context(), principal, r.PathValue("case_id"), version); err != nil {
		writeError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h CaseHandlers) DispatchInquiries(w http.ResponseWriter, r *http.Request) {
	principal, _ := principalFrom(r.Context())
	version, err := parseVersion(r)
	if err != nil {
		writeError(w, r, domain.FieldError{Field: "If-Match", Message: err.Error()})
		return
	}
	var input struct {
		RequestKey string `json:"request_key"`
	}
	if err := decodeJSON(w, r, &input); err != nil {
		writeError(w, r, domain.FieldError{Field: "body", Message: err.Error()})
		return
	}
	items, err := h.inquiries.Dispatch(r.Context(), principal, r.PathValue("case_id"), input.RequestKey, version)
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]any{"items": items})
}
