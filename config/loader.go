// Package config loads agent configuration from /etc/diagnostack/config.yaml
// with environment variable overrides.
//
// Precedence (highest to lowest):
//  1. Environment variables (DIAGNOSTACK_*)
//  2. /etc/diagnostack/config.yaml
//  3. Built-in defaults
package config

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/hoaithuonguit/diagnostack-agent/internal/app"
)

const (
	// DefaultConfigPath is the Linux-standard location for the config file.
	DefaultConfigPath = "/etc/diagnostack/config.yaml"

	// AgentIDFile stores the auto-generated agent UUID between restarts.
	AgentIDFile = "/etc/diagnostack/agent_id"
)

// File mirrors the config.yaml structure exactly.
type File struct {
	AgentID     string
	ServerLabel string

	Redis struct {
		Addr     string
		Password string
		DB       int
	}

	API struct {
		Endpoint string
		APIKey   string
		Timeout  time.Duration
	}

	CollectInterval time.Duration
	CollectTimeout  time.Duration
	BufferCapacity  int

	Retry struct {
		MaxAttempts int
		BaseDelay   time.Duration
		MaxDelay    time.Duration
	}

	Log struct {
		Level  string
		Format string
	}
}

// FullConfig bundles everything the bootstrap layer needs.
type FullConfig struct {
	App   app.AgentConfig
	Redis RedisConfig
	API   APIConfig
	Log   LogConfig
}

// RedisConfig holds Redis connection parameters for the infra layer.
type RedisConfig struct {
	Addr           string
	Password       string
	DB             int
	AgentID        string
	ServerLabel    string
	CollectTimeout time.Duration
}

// APIConfig holds HTTP shipper parameters for the infra layer.
type APIConfig struct {
	Endpoint string
	APIKey   string
	Timeout  time.Duration
}

// LogConfig holds structured logger settings.
type LogConfig struct {
	Level  string
	Format string
}

// Load reads the config file and applies environment overrides.
// configPath may be empty — DefaultConfigPath is used in that case.
func Load(configPath string) (*FullConfig, error) {
	if configPath == "" {
		configPath = DefaultConfigPath
	}

	f := defaults()

	data, err := os.ReadFile(configPath) //nolint:gosec // path is operator-controlled
	if err != nil && !os.IsNotExist(err) {
		return nil, fmt.Errorf("read config file %s: %w", configPath, err)
	}
	if err == nil {
		kv, parseErr := parseYAML(bytes.NewReader(data))
		if parseErr != nil {
			return nil, fmt.Errorf("parse config file %s: %w", configPath, parseErr)
		}
		if applyErr := applyParsed(&f, kv); applyErr != nil {
			return nil, fmt.Errorf("apply config file %s: %w", configPath, applyErr)
		}
	}

	applyEnvOverrides(&f)

	if err := validate(&f); err != nil {
		return nil, fmt.Errorf("invalid config: %w", err)
	}

	agentID, err := resolveAgentID(f.AgentID)
	if err != nil {
		return nil, fmt.Errorf("resolve agent ID: %w", err)
	}
	f.AgentID = agentID

	return &FullConfig{
		App: app.AgentConfig{
			AgentID:         f.AgentID,
			ServerLabel:     f.ServerLabel,
			CollectInterval: f.CollectInterval,
			CollectTimeout:  f.CollectTimeout,
			BufferCapacity:  f.BufferCapacity,
			Retry: app.RetryConfig{
				MaxAttempts: f.Retry.MaxAttempts,
				BaseDelay:   f.Retry.BaseDelay,
				MaxDelay:    f.Retry.MaxDelay,
			},
		},
		Redis: RedisConfig{
			Addr:        f.Redis.Addr,
			Password:    f.Redis.Password,
			DB:          f.Redis.DB,
			AgentID:     f.AgentID,
			ServerLabel: f.ServerLabel,
		},
		API: APIConfig{
			Endpoint: f.API.Endpoint,
			APIKey:   f.API.APIKey,
			Timeout:  f.API.Timeout,
		},
		Log: LogConfig{
			Level:  f.Log.Level,
			Format: f.Log.Format,
		},
	}, nil
}

// defaults returns a File with safe production defaults.
func defaults() File {
	var f File
	f.Redis.Addr = "127.0.0.1:6379"
	f.Redis.DB = 0
	f.API.Endpoint = "https://ingest.diagnostack.io/v1/ingest"
	f.API.Timeout = 10 * time.Second
	f.CollectInterval = 15 * time.Second
	f.CollectTimeout = 10 * time.Second
	f.BufferCapacity = 200
	f.Retry.MaxAttempts = 5
	f.Retry.BaseDelay = 1 * time.Second
	f.Retry.MaxDelay = 60 * time.Second
	f.Log.Level = "info"
	f.Log.Format = "text"
	f.ServerLabel = hostname()
	return f
}

// applyEnvOverrides allows operators to override any config value via
// environment variables without editing the file — useful for containers.
func applyEnvOverrides(f *File) {
	if v := os.Getenv("DIAGNOSTACK_API_KEY"); v != "" {
		f.API.APIKey = v
	}
	if v := os.Getenv("DIAGNOSTACK_API_ENDPOINT"); v != "" {
		f.API.Endpoint = v
	}
	if v := os.Getenv("DIAGNOSTACK_SERVER_LABEL"); v != "" {
		f.ServerLabel = v
	}
	if v := os.Getenv("DIAGNOSTACK_REDIS_ADDR"); v != "" {
		f.Redis.Addr = v
	}
	if v := os.Getenv("DIAGNOSTACK_REDIS_PASSWORD"); v != "" {
		f.Redis.Password = v
	}
	if v := os.Getenv("DIAGNOSTACK_COLLECT_INTERVAL"); v != "" {
		if d, err := time.ParseDuration(v); err == nil {
			f.CollectInterval = d
		}
	}
	if v := os.Getenv("DIAGNOSTACK_LOG_LEVEL"); v != "" {
		f.Log.Level = v
	}
	if v := os.Getenv("DIAGNOSTACK_LOG_FORMAT"); v != "" {
		f.Log.Format = v
	}
	if v := os.Getenv("DIAGNOSTACK_RETRY_MAX_ATTEMPTS"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			f.Retry.MaxAttempts = n
		}
	}
}

// validate returns an error if required fields are missing.
func validate(f *File) error {
	if strings.TrimSpace(f.API.APIKey) == "" {
		return fmt.Errorf("api.api_key is required (or set DIAGNOSTACK_API_KEY)")
	}
	if strings.TrimSpace(f.Redis.Addr) == "" {
		return fmt.Errorf("redis.addr is required")
	}
	if f.CollectInterval < time.Second {
		return fmt.Errorf("collect_interval must be at least 1s")
	}
	if f.Retry.MaxAttempts < 1 {
		return fmt.Errorf("retry.max_attempts must be >= 1")
	}
	return nil
}

// resolveAgentID returns the stored agent ID or generates and persists a new one.
func resolveAgentID(fromConfig string) (string, error) {
	if fromConfig != "" {
		return fromConfig, nil
	}

	// Try to read existing persisted ID.
	if data, err := os.ReadFile(AgentIDFile); err == nil {
		id := strings.TrimSpace(string(data))
		if id != "" {
			return id, nil
		}
	}

	// Generate a new UUID (stdlib only — no external deps).
	id, err := generateUUID()
	if err != nil {
		return "", err
	}

	// Persist so it survives restarts.
	if err := os.MkdirAll(filepath.Dir(AgentIDFile), 0o755); err != nil {
		// Not fatal — we still return a valid ID; it just won't persist.
		return id, nil
	}
	_ = os.WriteFile(AgentIDFile, []byte(id), 0o644)

	return id, nil
}

func hostname() string {
	h, err := os.Hostname()
	if err != nil {
		return "unknown"
	}
	return h
}
