// Package app contains the application use cases.
// It depends only on the domain package — no Redis, no HTTP, no YAML.
package app

import "time"

// AgentConfig holds all runtime configuration for the agent.
// It is populated by the config loader in the bootstrap layer and
// injected into each use case at construction time.
type AgentConfig struct {
	// AgentID is the stable UUID identifying this agent installation.
	// Auto-generated on first run and persisted to /etc/diagnostack/agent_id.
	AgentID string

	// ServerLabel is the human-readable name shown in the Diagnostack UI.
	ServerLabel string

	// CollectInterval controls how often the agent polls Redis.
	CollectInterval time.Duration

	// CollectTimeout is the maximum wall-clock time for a single Collect() call
	// (INFO + LATENCY LATEST + SLOWLOG GET combined). Must be less than CollectInterval
	// so a slow Redis cannot block the scheduler. Defaults to 10s if zero.
	CollectTimeout time.Duration

	// BufferCapacity is the maximum number of Snapshots the ring buffer holds.
	// When full, the oldest entry is dropped to make room.
	BufferCapacity int

	// Retry controls how the ShipUseCase retries failed deliveries.
	Retry RetryConfig
}

// RetryConfig defines the exponential backoff policy for failed shipments.
type RetryConfig struct {
	// MaxAttempts is the total number of attempts (1 = no retry).
	MaxAttempts int

	// BaseDelay is the wait time before the first retry.
	BaseDelay time.Duration

	// MaxDelay caps the backoff so it does not grow indefinitely.
	MaxDelay time.Duration
}
