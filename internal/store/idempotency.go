package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/vance1852/estate-financial-claim-service/internal/domain"
)

type IdempotencyRecord struct {
	Scope        string
	Key          string
	ActorID      string
	Method       string
	Route        string
	RequestHash  string
	StatusCode   int
	ResponseBody []byte
	ResourceID   string
	ExpiresAt    time.Time
	CreatedAt    time.Time
}

func (s *Store) GetIdempotency(ctx context.Context, q DBTX, scope, key string, now time.Time) (IdempotencyRecord, error) {
	row := q.QueryRowContext(ctx, `SELECT scope,key,actor_id,method,route,request_hash,status_code,
		response_body,resource_id,expires_at,created_at FROM idempotency_keys
		WHERE scope=? AND key=? AND expires_at>?`, scope, key, formatTime(now))
	var record IdempotencyRecord
	var expires, created string
	if err := row.Scan(&record.Scope, &record.Key, &record.ActorID, &record.Method, &record.Route,
		&record.RequestHash, &record.StatusCode, &record.ResponseBody, &record.ResourceID,
		&expires, &created); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return IdempotencyRecord{}, domain.ErrNotFound
		}
		return IdempotencyRecord{}, fmt.Errorf("get idempotency record: %w", err)
	}
	var err error
	if record.ExpiresAt, err = parseTime(expires); err != nil {
		return IdempotencyRecord{}, err
	}
	if record.CreatedAt, err = parseTime(created); err != nil {
		return IdempotencyRecord{}, err
	}
	return record, nil
}

func (s *Store) PutIdempotency(ctx context.Context, q DBTX, record IdempotencyRecord) error {
	if record.Scope == "" || record.Key == "" || record.RequestHash == "" {
		return domain.FieldError{Field: "idempotency", Message: "scope, key and request hash are required"}
	}
	_, err := q.ExecContext(ctx, `INSERT INTO idempotency_keys
		(scope,key,actor_id,method,route,request_hash,status_code,response_body,resource_id,expires_at,created_at)
		VALUES(?,?,?,?,?,?,?,?,?,?,?)`, record.Scope, record.Key, record.ActorID, record.Method,
		record.Route, record.RequestHash, record.StatusCode, record.ResponseBody, record.ResourceID,
		formatTime(record.ExpiresAt), formatTime(record.CreatedAt))
	if err != nil {
		return fmt.Errorf("put idempotency record: %w", err)
	}
	return nil
}

func ValidateReplay(record IdempotencyRecord, actor, method, route, requestHash string) error {
	if record.ActorID != actor || record.Method != method || record.Route != route || record.RequestHash != requestHash {
		return fmt.Errorf("idempotency key reused for a different request: %w", domain.ErrConflict)
	}
	return nil
}
