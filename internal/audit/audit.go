package audit

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"time"
)

type Event struct {
	ActorID    string
	Action     string
	ObjectType string
	ObjectID   string
	Result     string
	RequestID  string
	Details    map[string]any
	CreatedAt  time.Time
}

func (e Event) Validate() error {
	fields := map[string]string{
		"actor_id": e.ActorID, "action": e.Action, "object_type": e.ObjectType,
		"object_id": e.ObjectID, "result": e.Result, "request_id": e.RequestID,
	}
	for name, value := range fields {
		if strings.TrimSpace(value) == "" {
			return fmt.Errorf("audit %s is required", name)
		}
	}
	return nil
}

var sensitiveKey = regexp.MustCompile(`(?i)(password|token|identity|id_number|account_number|secret)`)

func MarshalDetails(details map[string]any) (string, error) {
	masked := make(map[string]any, len(details))
	for key, value := range details {
		if sensitiveKey.MatchString(key) {
			masked[key] = "[REDACTED]"
			continue
		}
		masked[key] = sanitize(value)
	}
	payload, err := json.Marshal(masked)
	if err != nil {
		return "", fmt.Errorf("marshal audit details: %w", err)
	}
	return string(payload), nil
}

func sanitize(value any) any {
	switch typed := value.(type) {
	case string:
		if len(typed) > 500 {
			return typed[:500]
		}
		return typed
	case []byte:
		return fmt.Sprintf("[%d bytes]", len(typed))
	case error:
		return typed.Error()
	case map[string]any:
		result := make(map[string]any, len(typed))
		for key, nested := range typed {
			if sensitiveKey.MatchString(key) {
				result[key] = "[REDACTED]"
			} else {
				result[key] = sanitize(nested)
			}
		}
		return result
	default:
		return value
	}
}

type requestIDKey struct{}

func WithRequestID(ctx context.Context, id string) context.Context {
	return context.WithValue(ctx, requestIDKey{}, id)
}

func RequestID(ctx context.Context) string {
	value, _ := ctx.Value(requestIDKey{}).(string)
	return value
}

func CorrelationID(ctx context.Context) string {
	if value := RequestID(ctx); value != "" {
		return value
	}
	return "internal"
}
