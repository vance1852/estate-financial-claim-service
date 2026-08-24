package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	_ "modernc.org/sqlite"

	"github.com/vance1852/estate-financial-claim-service/migrations"
)

type DBTX interface {
	ExecContext(context.Context, string, ...any) (sql.Result, error)
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

type Store struct {
	db         *sql.DB
	failMu     sync.Mutex
	failpoints map[string]error
}

func Open(ctx context.Context, path string) (*Store, error) {
	if path == "" {
		return nil, errors.New("database path is required")
	}
	if path != ":memory:" && !strings.HasPrefix(path, "file:") {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			return nil, fmt.Errorf("create database directory: %w", err)
		}
	}
	dsn := path
	if path == ":memory:" {
		dsn = "file:estate?mode=memory&cache=shared"
	}
	separator := "?"
	if strings.Contains(dsn, "?") {
		separator = "&"
	}
	dsn += separator + "_pragma=foreign_keys(1)&_pragma=busy_timeout(5000)"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("open sqlite: %w", err)
	}
	db.SetMaxOpenConns(8)
	db.SetMaxIdleConns(8)
	db.SetConnMaxLifetime(0)
	s := &Store{db: db, failpoints: make(map[string]error)}
	if err := s.configure(ctx); err != nil {
		db.Close()
		return nil, err
	}
	if err := s.Migrate(ctx); err != nil {
		db.Close()
		return nil, err
	}
	return s, nil
}

func (s *Store) configure(ctx context.Context) error {
	settings := []string{
		"PRAGMA foreign_keys = ON",
		"PRAGMA busy_timeout = 5000",
		"PRAGMA journal_mode = WAL",
		"PRAGMA synchronous = NORMAL",
	}
	for _, statement := range settings {
		if _, err := s.db.ExecContext(ctx, statement); err != nil {
			return fmt.Errorf("configure sqlite with %q: %w", statement, err)
		}
	}
	return nil
}

func (s *Store) Close() error { return s.db.Close() }

func (s *Store) ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error) {
	return s.db.ExecContext(ctx, query, args...)
}

func (s *Store) QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error) {
	return s.db.QueryContext(ctx, query, args...)
}

func (s *Store) QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row {
	return s.db.QueryRowContext(ctx, query, args...)
}

func (s *Store) Ping(ctx context.Context) error {
	if err := s.db.PingContext(ctx); err != nil {
		return fmt.Errorf("ping database: %w", err)
	}
	var value int
	if err := s.db.QueryRowContext(ctx, "SELECT 1").Scan(&value); err != nil {
		return fmt.Errorf("query database readiness: %w", err)
	}
	if value != 1 {
		return fmt.Errorf("unexpected database readiness value %d", value)
	}
	return nil
}

func (s *Store) WithTx(ctx context.Context, fn func(*sql.Tx) error) error {
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelSerializable})
	if err != nil {
		return fmt.Errorf("begin transaction: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	if err := fn(tx); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit transaction: %w", err)
	}
	return nil
}

func (s *Store) Migrate(ctx context.Context) error {
	if _, err := s.db.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS schema_migrations (
		version INTEGER PRIMARY KEY,
		name TEXT NOT NULL,
		applied_at TEXT NOT NULL
	)`); err != nil {
		return fmt.Errorf("create migration table: %w", err)
	}
	entries, err := fs.Glob(migrations.Files, "*.sql")
	if err != nil {
		return fmt.Errorf("list migrations: %w", err)
	}
	var latest int
	if err := s.db.QueryRowContext(ctx, "SELECT COALESCE(MAX(version),0) FROM schema_migrations").Scan(&latest); err != nil {
		return fmt.Errorf("read migration version: %w", err)
	}
	if latest > len(entries) {
		return fmt.Errorf("database schema version %d is newer than supported version %d", latest, len(entries))
	}
	for index, name := range entries {
		version := index + 1
		if version <= latest {
			continue
		}
		content, err := migrations.Files.ReadFile(name)
		if err != nil {
			return fmt.Errorf("read migration %s: %w", name, err)
		}
		err = s.WithTx(ctx, func(tx *sql.Tx) error {
			if _, err := tx.ExecContext(ctx, string(content)); err != nil {
				return fmt.Errorf("apply migration %s: %w", name, err)
			}
			_, err := tx.ExecContext(ctx,
				"INSERT INTO schema_migrations(version,name,applied_at) VALUES(?,?,?)",
				version, name, time.Now().UTC().Format(time.RFC3339Nano))
			return err
		})
		if err != nil {
			return err
		}
	}
	return nil
}

func (s *Store) InjectFailure(name string, err error) func() {
	s.failMu.Lock()
	s.failpoints[name] = err
	s.failMu.Unlock()
	return func() {
		s.failMu.Lock()
		delete(s.failpoints, name)
		s.failMu.Unlock()
	}
}

func (s *Store) fail(name string) error {
	s.failMu.Lock()
	defer s.failMu.Unlock()
	if err, ok := s.failpoints[name]; ok {
		delete(s.failpoints, name)
		return fmt.Errorf("injected %s failure: %w", name, err)
	}
	return nil
}
