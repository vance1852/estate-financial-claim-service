package store

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"github.com/vance1852/estate-financial-claim-service/internal/domain"
)

type Job struct {
	ID          string
	Kind        string
	ResourceID  string
	Payload     []byte
	Status      string
	Attempts    int
	MaxAttempts int
	AvailableAt time.Time
	LeaseOwner  string
	LeaseUntil  *time.Time
	LastError   string
	CreatedAt   time.Time
	UpdatedAt   time.Time
}

func (s *Store) InsertJob(ctx context.Context, q DBTX, job Job) error {
	_, err := q.ExecContext(ctx, `INSERT INTO worker_jobs
		(id,kind,resource_id,payload,status,attempts,max_attempts,available_at,lease_owner,lease_until,
		last_error,created_at,updated_at) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?)`, job.ID, job.Kind,
		job.ResourceID, job.Payload, job.Status, job.Attempts, job.MaxAttempts,
		formatTime(job.AvailableAt), job.LeaseOwner, formatNullableTime(job.LeaseUntil),
		job.LastError, formatTime(job.CreatedAt), formatTime(job.UpdatedAt))
	if err != nil {
		return fmt.Errorf("insert worker job: %w", err)
	}
	return nil
}

func (s *Store) RecoverExpiredJobs(ctx context.Context, now time.Time) (int64, error) {
	result, err := s.db.ExecContext(ctx, `UPDATE worker_jobs SET status='pending',lease_owner='',
		lease_until=NULL,available_at=?,updated_at=? WHERE status='running' AND lease_until<=?`,
		formatTime(now), formatTime(now), formatTime(now))
	if err != nil {
		return 0, fmt.Errorf("recover expired jobs: %w", err)
	}
	return result.RowsAffected()
}

func (s *Store) ClaimJob(ctx context.Context, owner string, now time.Time, lease time.Duration) (Job, error) {
	var claimed Job
	err := s.WithTx(ctx, func(tx *sql.Tx) error {
		row := tx.QueryRowContext(ctx, `SELECT id,kind,resource_id,payload,status,attempts,max_attempts,
			available_at,lease_owner,lease_until,last_error,created_at,updated_at FROM worker_jobs
			WHERE status='pending' AND available_at<=? ORDER BY available_at,id LIMIT 1`, formatTime(now))
		item, err := scanJob(row)
		if err != nil {
			return err
		}
		until := now.Add(lease)
		result, err := tx.ExecContext(ctx, `UPDATE worker_jobs SET status='running',lease_owner=?,
			lease_until=?,attempts=attempts+1,updated_at=? WHERE id=? AND status='pending'`,
			owner, formatTime(until), formatTime(now), item.ID)
		if err != nil {
			return fmt.Errorf("lease worker job: %w", err)
		}
		count, _ := result.RowsAffected()
		if count != 1 {
			return domain.ErrConflict
		}
		item.Status = "running"
		item.Attempts++
		item.LeaseOwner = owner
		item.LeaseUntil = &until
		item.UpdatedAt = now
		claimed = item
		return nil
	})
	return claimed, err
}

func scanJob(row rowScanner) (Job, error) {
	var job Job
	var available, created, updated string
	var lease sql.NullString
	err := row.Scan(&job.ID, &job.Kind, &job.ResourceID, &job.Payload, &job.Status,
		&job.Attempts, &job.MaxAttempts, &available, &job.LeaseOwner, &lease,
		&job.LastError, &created, &updated)
	if err != nil {
		return Job{}, mapNotFound("worker job", err)
	}
	if job.AvailableAt, err = parseTime(available); err != nil {
		return Job{}, err
	}
	if job.LeaseUntil, err = nullableTime(lease); err != nil {
		return Job{}, err
	}
	if job.CreatedAt, err = parseTime(created); err != nil {
		return Job{}, err
	}
	if job.UpdatedAt, err = parseTime(updated); err != nil {
		return Job{}, err
	}
	return job, nil
}

func (s *Store) CompleteJob(ctx context.Context, id, owner string, now time.Time) error {
	result, err := s.db.ExecContext(ctx, `UPDATE worker_jobs SET status='completed',lease_owner='',
		lease_until=NULL,last_error='',updated_at=? WHERE id=? AND status='running' AND lease_owner=?`,
		formatTime(now), id, owner)
	if err != nil {
		return fmt.Errorf("complete job: %w", err)
	}
	count, _ := result.RowsAffected()
	if count != 1 {
		return domain.ErrConflict
	}
	return nil
}

func (s *Store) ReleaseTimedOutJob(ctx context.Context, job Job, owner string, now time.Time, cause error) error {
	status := "pending"
	if job.Attempts >= job.MaxAttempts {
		status = "failed"
	}
	result, err := s.db.ExecContext(ctx, `UPDATE worker_jobs SET status=?,lease_owner='',lease_until=NULL,
		available_at=?,last_error=?,updated_at=? WHERE id=? AND status='running' AND lease_owner=?`,
		status, formatTime(now), cause.Error(), formatTime(now), job.ID, owner)
	if err != nil {
		return fmt.Errorf("release timed out job: %w", err)
	}
	count, _ := result.RowsAffected()
	if count != 1 {
		return domain.ErrConflict
	}
	return nil
}

func (s *Store) FailJob(ctx context.Context, job Job, owner string, now time.Time, retryAt time.Time, cause error) error {
	status := "pending"
	available := retryAt
	if job.Attempts >= job.MaxAttempts {
		status = "failed"
		available = now
	}
	result, err := s.db.ExecContext(ctx, `UPDATE worker_jobs SET status=?,lease_owner='',lease_until=NULL,
		available_at=?,last_error=?,updated_at=? WHERE id=? AND status='running' AND lease_owner=?`,
		status, formatTime(available), cause.Error(), formatTime(now), job.ID, owner)
	if err != nil {
		return fmt.Errorf("fail worker job: %w", err)
	}
	count, _ := result.RowsAffected()
	if count != 1 {
		return domain.ErrConflict
	}
	return nil
}
