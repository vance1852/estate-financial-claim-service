package config

import (
	"strings"
	"testing"
	"time"
)

func TestLoadDefaults(t *testing.T) {
	keys := []string{"HTTP_ADDR", "DATABASE_PATH", "SESSION_TTL", "WORKER_POLL_INTERVAL", "WORKER_LEASE_DURATION", "WORKER_MAX_ATTEMPTS", "SHUTDOWN_TIMEOUT"}
	for _, key := range keys {
		t.Setenv(key, "")
	}
	// Empty explicitly configured values are invalid, so remove variables in a subprocess-style helper.
	for _, key := range keys {
		t.Setenv(key, defaultEnvironmentValue(key))
	}
	cfg, err := Load()
	if err != nil {
		t.Fatalf("load configured defaults: %v", err)
	}
	if cfg.HTTPAddr != ":8080" || cfg.DatabasePath != "data/estate.db" {
		t.Fatalf("unexpected addresses: %#v", cfg)
	}
	if cfg.SessionTTL != 8*time.Hour || cfg.WorkerMaxAttempts != 5 {
		t.Fatalf("unexpected lifecycle defaults: %#v", cfg)
	}
}

func defaultEnvironmentValue(key string) string {
	values := map[string]string{
		"HTTP_ADDR": ":8080", "DATABASE_PATH": "data/estate.db", "SESSION_TTL": "8h",
		"WORKER_POLL_INTERVAL": "500ms", "WORKER_LEASE_DURATION": "30s",
		"WORKER_MAX_ATTEMPTS": "5", "SHUTDOWN_TIMEOUT": "10s",
	}
	return values[key]
}

func TestLoadOverrides(t *testing.T) {
	t.Setenv("HTTP_ADDR", "127.0.0.1:9000")
	t.Setenv("DATABASE_PATH", "var/test.db")
	t.Setenv("SESSION_TTL", "2h30m")
	t.Setenv("WORKER_POLL_INTERVAL", "2s")
	t.Setenv("WORKER_LEASE_DURATION", "45s")
	t.Setenv("WORKER_MAX_ATTEMPTS", "11")
	t.Setenv("SHUTDOWN_TIMEOUT", "3s")
	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.HTTPAddr != "127.0.0.1:9000" || cfg.DatabasePath != "var/test.db" {
		t.Fatalf("string overrides not applied: %#v", cfg)
	}
	if cfg.SessionTTL != 150*time.Minute || cfg.WorkerPoll != 2*time.Second || cfg.WorkerLease != 45*time.Second {
		t.Fatalf("duration overrides not applied: %#v", cfg)
	}
	if cfg.WorkerMaxAttempts != 11 || cfg.ShutdownTimeout != 3*time.Second {
		t.Fatalf("numeric overrides not applied: %#v", cfg)
	}
}

func TestLoadRejectsMalformedEnvironment(t *testing.T) {
	tests := []struct {
		name, key, value, contains string
	}{
		{"bad session duration", "SESSION_TTL", "tomorrow", "parse SESSION_TTL"},
		{"bad poll duration", "WORKER_POLL_INTERVAL", "fast", "parse WORKER_POLL_INTERVAL"},
		{"bad lease duration", "WORKER_LEASE_DURATION", "thirty", "parse WORKER_LEASE_DURATION"},
		{"bad shutdown duration", "SHUTDOWN_TIMEOUT", "later", "parse SHUTDOWN_TIMEOUT"},
		{"bad attempts", "WORKER_MAX_ATTEMPTS", "many", "parse WORKER_MAX_ATTEMPTS"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			for _, key := range []string{"HTTP_ADDR", "DATABASE_PATH", "SESSION_TTL", "WORKER_POLL_INTERVAL", "WORKER_LEASE_DURATION", "WORKER_MAX_ATTEMPTS", "SHUTDOWN_TIMEOUT"} {
				t.Setenv(key, defaultEnvironmentValue(key))
			}
			t.Setenv(test.key, test.value)
			_, err := Load()
			if err == nil || !strings.Contains(err.Error(), test.contains) {
				t.Fatalf("error = %v, want containing %q", err, test.contains)
			}
		})
	}
}

func TestValidateCollectsIndependentProblems(t *testing.T) {
	cfg := Config{HTTPAddr: "", DatabasePath: "", SessionTTL: time.Second,
		WorkerPoll: 0, WorkerLease: 0, WorkerMaxAttempts: 0, ShutdownTimeout: 0}
	err := cfg.Validate()
	if err == nil {
		t.Fatal("invalid config unexpectedly passed")
	}
	for _, phrase := range []string{"HTTP_ADDR", "DATABASE_PATH", "SESSION_TTL", "WORKER_POLL_INTERVAL", "WORKER_LEASE_DURATION", "WORKER_MAX_ATTEMPTS", "SHUTDOWN_TIMEOUT"} {
		if !strings.Contains(err.Error(), phrase) {
			t.Errorf("combined error %q is missing %s", err, phrase)
		}
	}
}
