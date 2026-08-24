package httpapi

import (
	"fmt"
	"log/slog"
	"net/http"
	"runtime/debug"
	"strings"
	"time"

	"github.com/vance1852/estate-financial-claim-service/internal/audit"
	"github.com/vance1852/estate-financial-claim-service/internal/auth"
	"github.com/vance1852/estate-financial-claim-service/internal/domain"
	"github.com/vance1852/estate-financial-claim-service/internal/ids"
)

type Middleware struct {
	auth   *auth.Service
	ids    ids.Generator
	logger *slog.Logger
}

func (m Middleware) Public(next http.Handler) http.Handler {
	return m.recoverer(m.requestID(m.logging(next)))
}

func (m Middleware) Protected(next http.Handler) http.Handler {
	return m.recoverer(m.requestID(m.logging(m.authenticate(next))))
}

func (m Middleware) requestID(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestID := strings.TrimSpace(r.Header.Get("X-Request-ID"))
		if requestID == "" || len(requestID) > 128 {
			generated, err := m.ids.New("req")
			if err != nil {
				writeError(w, r, fmt.Errorf("generate request id: %w", err))
				return
			}
			requestID = generated
		}
		w.Header().Set("X-Request-ID", requestID)
		next.ServeHTTP(w, r.WithContext(audit.WithRequestID(r.Context(), requestID)))
	})
}

func (m Middleware) authenticate(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		authorization := strings.TrimSpace(r.Header.Get("Authorization"))
		parts := strings.SplitN(authorization, " ", 2)
		if len(parts) != 2 || !strings.EqualFold(parts[0], "Bearer") {
			writeError(w, r, fmt.Errorf("missing bearer token: %w", authError()))
			return
		}
		principal, err := m.auth.Authenticate(r.Context(), parts[1])
		if err != nil {
			writeError(w, r, err)
			return
		}
		principalSlot := acquirePrincipal(principal)
		request := r.WithContext(withPrincipal(r.Context(), principalSlot))
		releasePrincipal(principalSlot)
		next.ServeHTTP(w, request)
	})
}

func (m Middleware) recoverer(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if value := recover(); value != nil {
				m.logger.ErrorContext(r.Context(), "panic recovered", "panic", value, "stack", string(debug.Stack()))
				writeError(w, r, fmt.Errorf("panic: %v", value))
			}
		}()
		next.ServeHTTP(w, r)
	})
}

func (m Middleware) logging(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		capture := &statusWriter{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(capture, r)
		m.logger.InfoContext(r.Context(), "http request", "method", r.Method, "path", r.URL.Path,
			"status", capture.status, "bytes", capture.bytes, "duration_ms", time.Since(start).Milliseconds(),
			"request_id", audit.RequestID(r.Context()))
	})
}

type statusWriter struct {
	http.ResponseWriter
	status int
	bytes  int
}

func (w *statusWriter) WriteHeader(status int) {
	w.status = status
	w.ResponseWriter.WriteHeader(status)
}

func (w *statusWriter) Write(payload []byte) (int, error) {
	count, err := w.ResponseWriter.Write(payload)
	w.bytes += count
	return count, err
}

func authError() error { return domain.ErrUnauthorized }
