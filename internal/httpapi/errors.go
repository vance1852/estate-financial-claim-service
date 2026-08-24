package httpapi

import (
	"encoding/json"
	"errors"
	"net/http"

	"github.com/vance1852/estate-financial-claim-service/internal/audit"
	"github.com/vance1852/estate-financial-claim-service/internal/domain"
)

type errorBody struct {
	Error apiError `json:"error"`
}

type apiError struct {
	Code      string `json:"code"`
	Message   string `json:"message"`
	RequestID string `json:"request_id"`
}

func writeError(w http.ResponseWriter, r *http.Request, err error) {
	status := http.StatusInternalServerError
	code := "internal_error"
	message := "The request could not be completed."
	switch {
	case errors.Is(err, domain.ErrValidation):
		status, code, message = http.StatusBadRequest, "validation_error", err.Error()
	case errors.Is(err, domain.ErrUnauthorized):
		status, code, message = http.StatusUnauthorized, "unauthorized", "Authentication is required."
	case errors.Is(err, domain.ErrExpired):
		status, code, message = http.StatusUnauthorized, "session_expired", "The session has expired."
	case errors.Is(err, domain.ErrForbidden):
		status, code, message = http.StatusForbidden, "forbidden", "The current role cannot perform this operation."
	case errors.Is(err, domain.ErrNotFound):
		status, code, message = http.StatusNotFound, "not_found", "The requested resource was not found."
	case errors.Is(err, domain.ErrConflict), errors.Is(err, domain.ErrInvalidState):
		status, code, message = http.StatusConflict, "conflict", err.Error()
	case errors.Is(err, domain.ErrDependency):
		status, code, message = http.StatusServiceUnavailable, "dependency_unavailable", "A required service is unavailable."
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(errorBody{Error: apiError{Code: code, Message: message, RequestID: audit.RequestID(r.Context())}})
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}
