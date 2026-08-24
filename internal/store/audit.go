package store

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/vance1852/estate-financial-claim-service/internal/audit"
)

func (s *Store) InsertAudit(ctx context.Context, q DBTX, event audit.Event) error {
	if err := event.Validate(); err != nil {
		return err
	}
	if err := s.fail("audit"); err != nil {
		return err
	}
	details, err := audit.MarshalDetails(event.Details)
	if err != nil {
		return err
	}
	_, err = q.ExecContext(ctx, `INSERT INTO audit_events
		(actor_id,action,object_type,object_id,result,request_id,details_json,created_at)
		VALUES(?,?,?,?,?,?,?,?)`, event.ActorID, event.Action, event.ObjectType,
		event.ObjectID, event.Result, event.RequestID, details, formatTime(event.CreatedAt))
	if err != nil {
		return fmt.Errorf("insert audit event: %w", err)
	}
	return nil
}

type AuditRecord struct {
	ID          int64
	ActorID     string
	Action      string
	ObjectType  string
	ObjectID    string
	Result      string
	RequestID   string
	DetailsJSON string
	CreatedAt   string
}

func (s *Store) AuditForObject(ctx context.Context, objectType, objectID string, limit int) ([]AuditRecord, error) {
	if limit < 1 || limit > 200 {
		limit = 50
	}
	rows, err := s.db.QueryContext(ctx, `SELECT id,actor_id,action,object_type,object_id,result,
		request_id,details_json,created_at FROM audit_events
		WHERE object_type=? AND object_id=? ORDER BY id DESC LIMIT ?`, objectType, objectID, limit)
	if err != nil {
		return nil, fmt.Errorf("query audit events: %w", err)
	}
	defer rows.Close()
	return scanAudits(rows)
}

func scanAudits(rows *sql.Rows) ([]AuditRecord, error) {
	records := make([]AuditRecord, 0)
	for rows.Next() {
		var record AuditRecord
		if err := rows.Scan(&record.ID, &record.ActorID, &record.Action, &record.ObjectType,
			&record.ObjectID, &record.Result, &record.RequestID, &record.DetailsJSON, &record.CreatedAt); err != nil {
			return nil, fmt.Errorf("scan audit event: %w", err)
		}
		records = append(records, record)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate audit events: %w", err)
	}
	return records, nil
}
