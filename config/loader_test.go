package config_test

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/keywatch/agent/config"
)

func TestLoad_Defaults(t *testing.T) {
	// Write a minimal valid config (only api_key is required, no default).
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	err := os.WriteFile(path, []byte("api:\n  api_key: test-key-123\n"), 0o644)
	if err != nil {
		t.Fatal(err)
	}

	cfg, err := config.Load(path)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if cfg.App.CollectInterval != 15*time.Second {
		t.Errorf("want default collect_interval=15s, got %v", cfg.App.CollectInterval)
	}
	if cfg.App.BufferCapacity != 200 {
		t.Errorf("want default buffer_capacity=200, got %d", cfg.App.BufferCapacity)
	}
	if cfg.App.Retry.MaxAttempts != 5 {
		t.Errorf("want default retry.max_attempts=5, got %d", cfg.App.Retry.MaxAttempts)
	}
	if cfg.Redis.Addr != "127.0.0.1:6379" {
		t.Errorf("want default redis.addr=127.0.0.1:6379, got %s", cfg.Redis.Addr)
	}
}

func TestLoad_EnvOverridesFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	_ = os.WriteFile(path, []byte("api:\n  api_key: from-file\n"), 0o644)

	t.Setenv("KEYWATCH_API_KEY", "from-env")
	t.Setenv("KEYWATCH_SERVER_LABEL", "env-server")
	t.Setenv("KEYWATCH_REDIS_ADDR", "10.0.0.1:6380")

	cfg, err := config.Load(path)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if cfg.API.APIKey != "from-env" {
		t.Errorf("want api_key=from-env, got %s", cfg.API.APIKey)
	}
	if cfg.App.ServerLabel != "env-server" {
		t.Errorf("want server_label=env-server, got %s", cfg.App.ServerLabel)
	}
	if cfg.Redis.Addr != "10.0.0.1:6380" {
		t.Errorf("want redis.addr=10.0.0.1:6380, got %s", cfg.Redis.Addr)
	}
}

func TestLoad_MissingAPIKey(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	_ = os.WriteFile(path, []byte("server_label: test\n"), 0o644)

	_, err := config.Load(path)
	if err == nil {
		t.Fatal("expected validation error for missing api_key, got nil")
	}
}

func TestLoad_MissingFileUsesDefaults(t *testing.T) {
	t.Setenv("KEYWATCH_API_KEY", "env-key")
	// Point to a path that does not exist.
	cfg, err := config.Load("/tmp/keywatch-nonexistent-config-xyz.yaml")
	if err != nil {
		t.Fatalf("missing file should not error (uses defaults): %v", err)
	}
	if cfg.API.APIKey != "env-key" {
		t.Errorf("want api_key from env, got %s", cfg.API.APIKey)
	}
}
