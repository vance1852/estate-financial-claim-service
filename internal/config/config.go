package config

import (
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
)

type Config struct {
	HTTPAddr          string
	DatabasePath      string
	SessionTTL        time.Duration
	WorkerPoll        time.Duration
	WorkerLease       time.Duration
	WorkerMaxAttempts int
	ShutdownTimeout   time.Duration
}

func Load() (Config, error) {
	cfg := Config{
		HTTPAddr:          env("HTTP_ADDR", ":8080"),
		DatabasePath:      env("DATABASE_PATH", "data/estate.db"),
		SessionTTL:        8 * time.Hour,
		WorkerPoll:        500 * time.Millisecond,
		WorkerLease:       30 * time.Second,
		WorkerMaxAttempts: 5,
		ShutdownTimeout:   10 * time.Second,
	}
	var err error
	if cfg.SessionTTL, err = duration("SESSION_TTL", cfg.SessionTTL); err != nil {
		return Config{}, err
	}
	if cfg.WorkerPoll, err = duration("WORKER_POLL_INTERVAL", cfg.WorkerPoll); err != nil {
		return Config{}, err
	}
	if cfg.WorkerLease, err = duration("WORKER_LEASE_DURATION", cfg.WorkerLease); err != nil {
		return Config{}, err
	}
	if cfg.ShutdownTimeout, err = duration("SHUTDOWN_TIMEOUT", cfg.ShutdownTimeout); err != nil {
		return Config{}, err
	}
	if cfg.WorkerMaxAttempts, err = integer("WORKER_MAX_ATTEMPTS", cfg.WorkerMaxAttempts); err != nil {
		return Config{}, err
	}
	if err := cfg.Validate(); err != nil {
		return Config{}, err
	}
	return cfg, nil
}

func (c Config) Validate() error {
	var problems []string
	if strings.TrimSpace(c.HTTPAddr) == "" {
		problems = append(problems, "HTTP_ADDR is empty")
	}
	if strings.TrimSpace(c.DatabasePath) == "" {
		problems = append(problems, "DATABASE_PATH is empty")
	}
	if c.SessionTTL < time.Minute {
		problems = append(problems, "SESSION_TTL must be at least one minute")
	}
	if c.WorkerPoll <= 0 {
		problems = append(problems, "WORKER_POLL_INTERVAL must be positive")
	}
	if c.WorkerLease <= c.WorkerPoll {
		problems = append(problems, "WORKER_LEASE_DURATION must exceed poll interval")
	}
	if c.WorkerMaxAttempts < 1 || c.WorkerMaxAttempts > 50 {
		problems = append(problems, "WORKER_MAX_ATTEMPTS must be between 1 and 50")
	}
	if c.ShutdownTimeout <= 0 {
		problems = append(problems, "SHUTDOWN_TIMEOUT must be positive")
	}
	return errors.Join(stringsToErrors(problems)...)
}

func env(key, fallback string) string {
	if value, ok := os.LookupEnv(key); ok {
		return value
	}
	return fallback
}

func duration(key string, fallback time.Duration) (time.Duration, error) {
	value, ok := os.LookupEnv(key)
	if !ok {
		return fallback, nil
	}
	parsed, err := time.ParseDuration(value)
	if err != nil {
		return 0, fmt.Errorf("parse %s: %w", key, err)
	}
	return parsed, nil
}

func integer(key string, fallback int) (int, error) {
	value, ok := os.LookupEnv(key)
	if !ok {
		return fallback, nil
	}
	parsed, err := strconv.Atoi(value)
	if err != nil {
		return 0, fmt.Errorf("parse %s: %w", key, err)
	}
	return parsed, nil
}

func stringsToErrors(values []string) []error {
	result := make([]error, 0, len(values))
	for _, value := range values {
		result = append(result, errors.New(value))
	}
	return result
}
