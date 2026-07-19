package config_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/keywatch/agent/config"
)

// ── applyParsed via Load ──────────────────────────────────────────────────────
// applyParsed is private but fully exercised through Load() — we write
// a config file with every supported field and assert the values arrive correctly.

func TestLoad_AllFieldsParsed(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")

	content := `
server_label: my-redis
redis:
  addr: "10.0.0.1:6380"
  password: "secret"
  db: 2
api:
  endpoint: "https://custom.api.io/ingest"
  api_key: "kw_live_abc123"
  timeout: 30s
collect_interval: 30s
collect_timeout: 8s
buffer_capacity: 100
retry:
  max_attempts: 3
  base_delay: 2s
  max_delay: 30s
log:
  level: "debug"
  format: "json"
`
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}

	cfg, err := config.Load(path)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	checks := []struct {
		name string
		got  any
		want any
	}{
		{"server_label", cfg.App.ServerLabel, "my-redis"},
		{"redis.addr", cfg.Redis.Addr, "10.0.0.1:6380"},
		{"redis.password", cfg.Redis.Password, "secret"},
		{"redis.db", cfg.Redis.DB, 2},
		{"api.endpoint", cfg.API.Endpoint, "https://custom.api.io/ingest"},
		{"api.api_key", cfg.API.APIKey, "kw_live_abc123"},
		{"api.timeout", cfg.API.Timeout, 30 * time.Second},
		{"collect_interval", cfg.App.CollectInterval, 30 * time.Second},
		{"collect_timeout", cfg.App.CollectTimeout, 8 * time.Second},
		{"buffer_capacity", cfg.App.BufferCapacity, 100},
		{"retry.max_attempts", cfg.App.Retry.MaxAttempts, 3},
		{"retry.base_delay", cfg.App.Retry.BaseDelay, 2 * time.Second},
		{"retry.max_delay", cfg.App.Retry.MaxDelay, 30 * time.Second},
		{"log.level", cfg.Log.Level, "debug"},
		{"log.format", cfg.Log.Format, "json"},
	}

	for _, c := range checks {
		t.Run(c.name, func(t *testing.T) {
			if c.got != c.want {
				t.Errorf("want %v, got %v", c.want, c.got)
			}
		})
	}
}

// ── resolveAgentID ────────────────────────────────────────────────────────────

func TestLoad_AgentIDFromConfigFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	content := "api:\n  api_key: k\nagent_id: pinned-uuid-123\n"
	_ = os.WriteFile(path, []byte(content), 0o644)

	cfg, err := config.Load(path)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.App.AgentID != "pinned-uuid-123" {
		t.Errorf("want agent_id=pinned-uuid-123, got %q", cfg.App.AgentID)
	}
}

func TestLoad_AgentIDIsGeneratedWhenMissing(t *testing.T) {
	// Point AgentIDFile to a temp dir so we don't write to /etc/keywatch.
	// We can't override AgentIDFile easily without export, so we rely on
	// the fact that /etc/keywatch/agent_id won't exist in the test environment
	// and the file write will silently fail (no root), giving us a fresh UUID.
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	_ = os.WriteFile(path, []byte("api:\n  api_key: k\n"), 0o644)

	cfg, err := config.Load(path)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	id := cfg.App.AgentID
	if id == "" {
		t.Fatal("want a generated agent_id, got empty string")
	}
	// UUID v4 format: 8-4-4-4-12 hex chars
	parts := strings.Split(id, "-")
	if len(parts) != 5 {
		t.Errorf("want UUID v4 with 5 parts, got %q (%d parts)", id, len(parts))
	}
}

func TestLoad_AgentIDConsistentAcrossCalls(t *testing.T) {
	// When agent_id is pinned in config it must be identical across calls.
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	_ = os.WriteFile(path, []byte("api:\n  api_key: k\nagent_id: stable-id-xyz\n"), 0o644)

	cfg1, _ := config.Load(path)
	cfg2, _ := config.Load(path)

	if cfg1.App.AgentID != cfg2.App.AgentID {
		t.Errorf("agent_id not stable: %q vs %q", cfg1.App.AgentID, cfg2.App.AgentID)
	}
}

// ── validation ────────────────────────────────────────────────────────────────

func TestLoad_InvalidCollectInterval(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	// collect_interval of 500ms is below the 1s minimum
	_ = os.WriteFile(path, []byte("api:\n  api_key: k\ncollect_interval: 500ms\n"), 0o644)

	_, err := config.Load(path)
	if err == nil {
		t.Fatal("want validation error for collect_interval < 1s, got nil")
	}
}

func TestLoad_InvalidRetryMaxAttempts(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	_ = os.WriteFile(path, []byte("api:\n  api_key: k\nretry:\n  max_attempts: 0\n"), 0o644)

	_, err := config.Load(path)
	if err == nil {
		t.Fatal("want validation error for retry.max_attempts=0, got nil")
	}
}

// ── env overrides (extended) ──────────────────────────────────────────────────

func TestLoad_EnvOverride_RetryMaxAttempts(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	_ = os.WriteFile(path, []byte("api:\n  api_key: k\n"), 0o644)

	t.Setenv("KEYWATCH_RETRY_MAX_ATTEMPTS", "10")

	cfg, err := config.Load(path)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.App.Retry.MaxAttempts != 10 {
		t.Errorf("want retry.max_attempts=10 from env, got %d", cfg.App.Retry.MaxAttempts)
	}
}

func TestLoad_EnvOverride_CollectInterval(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	_ = os.WriteFile(path, []byte("api:\n  api_key: k\n"), 0o644)

	t.Setenv("KEYWATCH_COLLECT_INTERVAL", "30s")

	cfg, err := config.Load(path)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.App.CollectInterval != 30*time.Second {
		t.Errorf("want collect_interval=30s from env, got %v", cfg.App.CollectInterval)
	}
}

func TestLoad_EnvOverride_LogFormat(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	_ = os.WriteFile(path, []byte("api:\n  api_key: k\n"), 0o644)

	t.Setenv("KEYWATCH_LOG_FORMAT", "json")

	cfg, err := config.Load(path)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.Log.Format != "json" {
		t.Errorf("want log.format=json from env, got %q", cfg.Log.Format)
	}
}
