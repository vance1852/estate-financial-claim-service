package audit

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func TestEventValidation(t *testing.T) {
	valid := Event{ActorID: "user_1", Action: "case.created", ObjectType: "estate_case",
		ObjectID: "case_1", Result: "success", RequestID: "req_1", CreatedAt: time.Now()}
	if err := valid.Validate(); err != nil {
		t.Fatalf("valid event: %v", err)
	}
	fields := []struct {
		name   string
		mutate func(*Event)
	}{
		{"actor", func(e *Event) { e.ActorID = "" }},
		{"action", func(e *Event) { e.Action = "" }},
		{"type", func(e *Event) { e.ObjectType = "" }},
		{"object", func(e *Event) { e.ObjectID = "" }},
		{"result", func(e *Event) { e.Result = "" }},
		{"request", func(e *Event) { e.RequestID = "" }},
	}
	for _, field := range fields {
		t.Run(field.name, func(t *testing.T) {
			candidate := valid
			field.mutate(&candidate)
			if candidate.Validate() == nil {
				t.Fatal("missing required field unexpectedly passed")
			}
		})
	}
}

func TestMarshalDetailsRedactsSensitiveValues(t *testing.T) {
	long := strings.Repeat("x", 700)
	payload, err := MarshalDetails(map[string]any{
		"password": "top-secret", "session_token": "token", "identity_number": "370200",
		"account_number": "621700", "safe": "visible", "binary": []byte{1, 2, 3},
		"nested": map[string]any{"secret": "hidden", "status": "accepted"}, "long": long,
	})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(payload, "top-secret") || strings.Contains(payload, "621700") || strings.Contains(payload, "hidden") {
		t.Fatalf("sensitive value leaked: %s", payload)
	}
	var decoded map[string]any
	if err := json.Unmarshal([]byte(payload), &decoded); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"password", "session_token", "identity_number", "account_number"} {
		if decoded[key] != "[REDACTED]" {
			t.Errorf("%s = %#v", key, decoded[key])
		}
	}
	if decoded["safe"] != "visible" || decoded["binary"] != "[3 bytes]" {
		t.Fatalf("safe values were not retained: %#v", decoded)
	}
	if len(decoded["long"].(string)) != 500 {
		t.Fatalf("long detail was not bounded: %d", len(decoded["long"].(string)))
	}
}

func TestRequestIDContext(t *testing.T) {
	if got := RequestID(context.Background()); got != "" {
		t.Fatalf("empty context request id = %q", got)
	}
	ctx := WithRequestID(context.Background(), "req_123")
	if got := RequestID(ctx); got != "req_123" {
		t.Fatalf("request id = %q", got)
	}
	child := context.WithValue(ctx, struct{}{}, "unrelated")
	if got := RequestID(child); got != "req_123" {
		t.Fatalf("child request id = %q", got)
	}
	if got := CorrelationID(context.Background()); got != "internal" {
		t.Fatalf("background correlation id = %q", got)
	}
	if got := CorrelationID(ctx); got != "req_123" {
		t.Fatalf("request correlation id = %q", got)
	}
}
