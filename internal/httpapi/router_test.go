package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/vance1852/estate-financial-claim-service/internal/auth"
	"github.com/vance1852/estate-financial-claim-service/internal/cases"
	"github.com/vance1852/estate-financial-claim-service/internal/claims"
	"github.com/vance1852/estate-financial-claim-service/internal/clock"
	"github.com/vance1852/estate-financial-claim-service/internal/domain"
	"github.com/vance1852/estate-financial-claim-service/internal/ids"
	"github.com/vance1852/estate-financial-claim-service/internal/inquiry"
	"github.com/vance1852/estate-financial-claim-service/internal/store"
)

type httpFixture struct {
	handler http.Handler
	auth    *auth.Service
	store   *store.Store
	clock   *clock.Manual
}

func newHTTPFixture(t *testing.T) httpFixture {
	t.Helper()
	ctx := context.Background()
	database, err := store.Open(ctx, filepath.Join(t.TempDir(), "http.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	now := time.Date(2026, 8, 24, 12, 0, 0, 0, time.UTC)
	hash, err := auth.HashPassword("http-test-password")
	if err != nil {
		t.Fatal(err)
	}
	for _, user := range []store.User{
		{ID: "claimant", Email: "claimant@example.test", Role: domain.RoleClaimant},
		{ID: "officer", Email: "officer@example.test", Role: domain.RoleOfficer},
		{ID: "supervisor", Email: "supervisor@example.test", Role: domain.RoleSupervisor},
	} {
		user.PasswordHash, user.DisplayName, user.Active = hash, user.ID, true
		user.CreatedAt, user.UpdatedAt = now, now
		if err := database.CreateUser(ctx, user); err != nil {
			t.Fatal(err)
		}
	}
	manual := clock.NewManual(now)
	generator := &ids.Sequence{}
	authService := auth.New(database, manual, generator, time.Hour)
	caseService := cases.New(database, manual, generator)
	inquiryService := inquiry.New(database, manual, generator, 3)
	claimService := claims.New(database, manual, generator, claims.DefaultSmallClaimLimit, 3)
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	handler := New(Dependencies{Store: database, Auth: authService, Cases: caseService,
		Inquiries: inquiryService, Claims: claimService, IDs: generator, Logger: logger})
	return httpFixture{handler: handler, auth: authService, store: database, clock: manual}
}

func request(t *testing.T, handler http.Handler, method, path string, body any, headers map[string]string) *httptest.ResponseRecorder {
	t.Helper()
	var reader io.Reader
	if body != nil {
		payload, err := json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
		reader = bytes.NewReader(payload)
	}
	req := httptest.NewRequest(method, path, reader)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	for key, value := range headers {
		req.Header.Set(key, value)
	}
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, req)
	return recorder
}

func loginToken(t *testing.T, fixture httpFixture, email string) string {
	t.Helper()
	response := request(t, fixture.handler, http.MethodPost, "/v1/auth/login",
		map[string]string{"email": email, "password": "http-test-password"}, nil)
	if response.Code != http.StatusOK {
		t.Fatalf("login status=%d body=%s", response.Code, response.Body.String())
	}
	var payload struct {
		Token string `json:"token"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	if payload.Token == "" {
		t.Fatal("login response omitted token")
	}
	return payload.Token
}

func TestHealthAndReadinessArePublicAndCarryRequestIDs(t *testing.T) {
	fixture := newHTTPFixture(t)
	for _, path := range []string{"/healthz", "/readyz"} {
		response := request(t, fixture.handler, http.MethodGet, path, nil, nil)
		if response.Code != http.StatusOK {
			t.Fatalf("%s status=%d body=%s", path, response.Code, response.Body.String())
		}
		if response.Header().Get("X-Request-ID") == "" {
			t.Fatalf("%s omitted request id", path)
		}
		if !strings.Contains(response.Body.String(), "status") {
			t.Fatalf("%s body=%s", path, response.Body.String())
		}
	}
	custom := request(t, fixture.handler, http.MethodGet, "/healthz", nil, map[string]string{"X-Request-ID": "caller-request"})
	if custom.Header().Get("X-Request-ID") != "caller-request" {
		t.Fatalf("custom request id = %q", custom.Header().Get("X-Request-ID"))
	}
}

func TestLoginMapsInvalidCredentialsAndStrictJSON(t *testing.T) {
	fixture := newHTTPFixture(t)
	wrong := request(t, fixture.handler, http.MethodPost, "/v1/auth/login",
		map[string]string{"email": "claimant@example.test", "password": "wrong-password"}, nil)
	if wrong.Code != http.StatusUnauthorized || !strings.Contains(wrong.Body.String(), "unauthorized") {
		t.Fatalf("wrong login status=%d body=%s", wrong.Code, wrong.Body.String())
	}
	unknown := request(t, fixture.handler, http.MethodPost, "/v1/auth/login",
		map[string]any{"email": "claimant@example.test", "password": "http-test-password", "extra": true}, nil)
	if unknown.Code != http.StatusBadRequest || !strings.Contains(unknown.Body.String(), "unknown field") {
		t.Fatalf("unknown field status=%d body=%s", unknown.Code, unknown.Body.String())
	}
	req := httptest.NewRequest(http.MethodPost, "/v1/auth/login", strings.NewReader(`{"email":`))
	recorder := httptest.NewRecorder()
	fixture.handler.ServeHTTP(recorder, req)
	if recorder.Code != http.StatusBadRequest || !strings.Contains(recorder.Body.String(), "malformed JSON") {
		t.Fatalf("malformed status=%d body=%s", recorder.Code, recorder.Body.String())
	}
}

func TestProtectedRoutesRejectMissingMalformedAndExpiredTokens(t *testing.T) {
	fixture := newHTTPFixture(t)
	for _, authorization := range []string{"", "Basic credentials", "Bearer small"} {
		response := request(t, fixture.handler, http.MethodGet, "/v1/cases", nil,
			map[string]string{"Authorization": authorization})
		if response.Code != http.StatusUnauthorized {
			t.Errorf("authorization %q status=%d body=%s", authorization, response.Code, response.Body.String())
		}
		if !strings.Contains(response.Body.String(), "request_id") {
			t.Errorf("error omitted request id: %s", response.Body.String())
		}
	}
	token := loginToken(t, fixture, "claimant@example.test")
	fixture.clock.Advance(time.Hour)
	expired := request(t, fixture.handler, http.MethodGet, "/v1/cases", nil,
		map[string]string{"Authorization": "Bearer " + token})
	if expired.Code != http.StatusUnauthorized || !strings.Contains(expired.Body.String(), "session_expired") {
		t.Fatalf("expired status=%d body=%s", expired.Code, expired.Body.String())
	}
}

func TestClaimantCanSubmitReplayListAndReadOwnCase(t *testing.T) {
	fixture := newHTTPFixture(t)
	token := loginToken(t, fixture, "claimant@example.test")
	body := map[string]any{
		"deceased": map[string]string{"Name": "Deceased Person", "IDNumber": "37020019500101001X"},
		"claimant": map[string]string{"Name": "Claimant Person", "IDNumber": "370200198001010019"},
		"relation": "child", "jurisdiction": "Qingdao",
	}
	headers := map[string]string{"Authorization": "Bearer " + token, "Idempotency-Key": "http-submit-key"}
	created := request(t, fixture.handler, http.MethodPost, "/v1/cases", body, headers)
	if created.Code != http.StatusCreated {
		t.Fatalf("create status=%d body=%s", created.Code, created.Body.String())
	}
	var response struct {
		Case struct {
			ID string `json:"id"`
		} `json:"case"`
		Replayed bool `json:"replayed"`
	}
	if err := json.Unmarshal(created.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if response.Case.ID == "" || response.Replayed {
		t.Fatalf("created response = %#v", response)
	}
	if strings.Contains(created.Body.String(), "deceased_id_hash") || strings.Contains(created.Body.String(), "IdempotencyKey") {
		t.Fatalf("sensitive internal field leaked: %s", created.Body.String())
	}
	replayed := request(t, fixture.handler, http.MethodPost, "/v1/cases", body, headers)
	if replayed.Code != http.StatusOK || !strings.Contains(replayed.Body.String(), `"replayed":true`) {
		t.Fatalf("replay status=%d body=%s", replayed.Code, replayed.Body.String())
	}
	listed := request(t, fixture.handler, http.MethodGet, "/v1/cases?limit=10", nil,
		map[string]string{"Authorization": "Bearer " + token})
	if listed.Code != http.StatusOK || !strings.Contains(listed.Body.String(), response.Case.ID) {
		t.Fatalf("list status=%d body=%s", listed.Code, listed.Body.String())
	}
	loaded := request(t, fixture.handler, http.MethodGet, "/v1/cases/"+response.Case.ID, nil,
		map[string]string{"Authorization": "Bearer " + token})
	if loaded.Code != http.StatusOK || !strings.Contains(loaded.Body.String(), response.Case.ID) {
		t.Fatalf("get status=%d body=%s", loaded.Code, loaded.Body.String())
	}
}

func TestRoleAndVersionErrorsMapToStableHTTPResponses(t *testing.T) {
	fixture := newHTTPFixture(t)
	claimantToken := loginToken(t, fixture, "claimant@example.test")
	officerToken := loginToken(t, fixture, "officer@example.test")
	body := map[string]any{
		"deceased": map[string]string{"Name": "Deceased Person", "IDNumber": "37020019500101001X"},
		"claimant": map[string]string{"Name": "Claimant Person", "IDNumber": "370200198001010019"},
		"relation": "child", "jurisdiction": "Qingdao",
	}
	created := request(t, fixture.handler, http.MethodPost, "/v1/cases", body,
		map[string]string{"Authorization": "Bearer " + claimantToken, "Idempotency-Key": "role-version-key"})
	if created.Code != http.StatusCreated {
		t.Fatal(created.Body.String())
	}
	var decoded map[string]any
	if err := json.Unmarshal(created.Body.Bytes(), &decoded); err != nil {
		t.Fatal(err)
	}
	caseID := decoded["case"].(map[string]any)["id"].(string)
	forbidden := request(t, fixture.handler, http.MethodPost, "/v1/cases/"+caseID+"/review", nil,
		map[string]string{"Authorization": "Bearer " + claimantToken, "If-Match": "1"})
	if forbidden.Code != http.StatusForbidden || !strings.Contains(forbidden.Body.String(), "forbidden") {
		t.Fatalf("forbidden status=%d body=%s", forbidden.Code, forbidden.Body.String())
	}
	missingVersion := request(t, fixture.handler, http.MethodPost, "/v1/cases/"+caseID+"/review", nil,
		map[string]string{"Authorization": "Bearer " + officerToken})
	if missingVersion.Code != http.StatusBadRequest {
		t.Fatalf("missing version status=%d body=%s", missingVersion.Code, missingVersion.Body.String())
	}
	stale := request(t, fixture.handler, http.MethodPost, "/v1/cases/"+caseID+"/review", nil,
		map[string]string{"Authorization": "Bearer " + officerToken, "If-Match": "99"})
	if stale.Code != http.StatusConflict || !strings.Contains(stale.Body.String(), "conflict") {
		t.Fatalf("stale status=%d body=%s", stale.Code, stale.Body.String())
	}
	ok := request(t, fixture.handler, http.MethodPost, "/v1/cases/"+caseID+"/review", nil,
		map[string]string{"Authorization": "Bearer " + officerToken, "If-Match": "1"})
	if ok.Code != http.StatusNoContent {
		t.Fatalf("review status=%d body=%s", ok.Code, ok.Body.String())
	}
}

func TestLogoutRevokesOnlyPresentedSession(t *testing.T) {
	fixture := newHTTPFixture(t)
	first := loginToken(t, fixture, "claimant@example.test")
	second := loginToken(t, fixture, "claimant@example.test")
	logout := request(t, fixture.handler, http.MethodPost, "/v1/auth/logout", nil,
		map[string]string{"Authorization": "Bearer " + first})
	if logout.Code != http.StatusNoContent {
		t.Fatalf("logout status=%d body=%s", logout.Code, logout.Body.String())
	}
	revoked := request(t, fixture.handler, http.MethodGet, "/v1/cases", nil,
		map[string]string{"Authorization": "Bearer " + first})
	if revoked.Code != http.StatusUnauthorized {
		t.Fatalf("revoked status=%d", revoked.Code)
	}
	active := request(t, fixture.handler, http.MethodGet, "/v1/cases", nil,
		map[string]string{"Authorization": "Bearer " + second})
	if active.Code != http.StatusOK {
		t.Fatalf("second session status=%d body=%s", active.Code, active.Body.String())
	}
}

func TestDecodeJSONRejectsOversizedBodiesAndMultipleValues(t *testing.T) {
	fixture := newHTTPFixture(t)
	large := `{"email":"` + strings.Repeat("x", maxJSONBody) + `","password":"password"}`
	req := httptest.NewRequest(http.MethodPost, "/v1/auth/login", strings.NewReader(large))
	recorder := httptest.NewRecorder()
	fixture.handler.ServeHTTP(recorder, req)
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("large body status=%d", recorder.Code)
	}
	req = httptest.NewRequest(http.MethodPost, "/v1/auth/login",
		strings.NewReader(`{"email":"a","password":"b"} {"second":true}`))
	recorder = httptest.NewRecorder()
	fixture.handler.ServeHTTP(recorder, req)
	if recorder.Code != http.StatusBadRequest || !strings.Contains(recorder.Body.String(), "one JSON value") {
		t.Fatalf("multiple values status=%d body=%s", recorder.Code, recorder.Body.String())
	}
}

func TestNotFoundHasStableShape(t *testing.T) {
	fixture := newHTTPFixture(t)
	token := loginToken(t, fixture, "officer@example.test")
	response := request(t, fixture.handler, http.MethodGet, "/v1/cases/missing", nil,
		map[string]string{"Authorization": "Bearer " + token})
	if response.Code != http.StatusNotFound {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
	var body errorBody
	if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body.Error.Code != "not_found" || body.Error.RequestID == "" || body.Error.Message == "" {
		t.Fatalf("error body = %#v", body)
	}
}
