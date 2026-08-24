package store

import (
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/vance1852/estate-financial-claim-service/internal/domain"
)

func parseTime(value string) (time.Time, error) {
	parsed, err := time.Parse(time.RFC3339Nano, value)
	if err != nil {
		return time.Time{}, fmt.Errorf("parse persisted time %q: %w", value, err)
	}
	return parsed.UTC(), nil
}

func nullableTime(value sql.NullString) (*time.Time, error) {
	if !value.Valid {
		return nil, nil
	}
	parsed, err := parseTime(value.String)
	if err != nil {
		return nil, err
	}
	return &parsed, nil
}

func mapNotFound(entity string, err error) error {
	if errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("%s: %w", entity, domain.ErrNotFound)
	}
	return err
}

func boolInt(value bool) int {
	if value {
		return 1
	}
	return 0
}

func formatTime(value time.Time) string { return value.UTC().Format(time.RFC3339Nano) }

func formatNullableTime(value *time.Time) any {
	if value == nil {
		return nil
	}
	return formatTime(*value)
}
