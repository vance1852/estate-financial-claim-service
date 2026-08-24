package httpapi

import (
	"errors"
	"net/http"
	"strings"

	"github.com/vance1852/estate-financial-claim-service/internal/auth"
	"github.com/vance1852/estate-financial-claim-service/internal/domain"
)

type AuthHandlers struct{ service *auth.Service }

func (h AuthHandlers) Login(w http.ResponseWriter, r *http.Request) {
	var input struct {
		Email    string `json:"email"`
		Password string `json:"password"`
	}
	if err := decodeJSON(w, r, &input); err != nil {
		writeError(w, r, domain.FieldError{Field: "body", Message: err.Error()})
		return
	}
	result, err := h.service.Login(r.Context(), input.Email, input.Password)
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"token": result.Token, "expires_at": result.ExpiresAt,
		"user": map[string]any{"id": result.Principal.UserID, "role": result.Principal.Role},
	})
}

func (h AuthHandlers) Logout(w http.ResponseWriter, r *http.Request) {
	parts := strings.SplitN(strings.TrimSpace(r.Header.Get("Authorization")), " ", 2)
	if len(parts) != 2 {
		writeError(w, r, domain.ErrUnauthorized)
		return
	}
	if err := h.service.Logout(r.Context(), parts[1]); err != nil && !errors.Is(err, domain.ErrNotFound) {
		writeError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
