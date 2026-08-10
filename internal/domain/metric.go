// Package domain contains pure domain models and port interfaces.
// This package has zero external dependencies — it must stay that way.
package domain

import "time"

// Snapshot is a single point-in-time observation of a Redis server.
// It is the canonical unit of data that flows through the agent pipeline.
type Snapshot struct {
	AgentID      string          `json:"agent_id"`
	ServerLabel  string          `json:"server_label"`
	CollectedAt  time.Time       `json:"collected_at"`
	RedisVersion string          `json:"redis_version"`
	Metrics      Metrics         `json:"metrics"`
	Slowlog      []SlowlogEntry  `json:"slowlog"`
	Latency      []LatencySample `json:"latency"`

	// DisabledCapabilities lists commands the agent's Redis ACL user is not
	// permitted to run (e.g. "SLOWLOG", "LATENCY"). Populated by the collector
	// at startup so the backend can surface a clear message in the UI instead
	// of silently showing gaps in the data.
	DisabledCapabilities []string `json:"disabled_capabilities,omitempty"`
}

// Metrics holds the key-value fields extracted from Redis INFO.
// Using a typed struct (rather than map[string]string) makes
// downstream processing explicit and avoids silent key misspellings.
type Metrics struct {
	// Memory
	UsedMemoryBytes       int64   `json:"used_memory"`
	UsedMemoryRSSBytes    int64   `json:"used_memory_rss"`
	MaxMemoryBytes        int64   `json:"maxmemory"`
	MemFragmentationRatio float64 `json:"mem_fragmentation_ratio"`

	// Clients
	ConnectedClients int64 `json:"connected_clients"`
	BlockedClients   int64 `json:"blocked_clients"`
	// MaxClients is the configured connection ceiling (INFO clients:
	// maxclients). Needed to compute connection-exhaustion ratio
	// (connected_clients / maxclients).
	MaxClients int64 `json:"maxclients"`

	// Stats
	InstantaneousOpsPerSec int64 `json:"instantaneous_ops_per_sec"`
	KeyspaceHits           int64 `json:"keyspace_hits"`
	KeyspaceMisses         int64 `json:"keyspace_misses"`
	EvictedKeys            int64 `json:"evicted_keys"`
	RejectedConnections    int64 `json:"rejected_connections"`

	// Replication
	// ReplicationLagBytes is the maximum number of bytes any connected
	// replica is behind, computed on the master as master_repl_offset minus
	// each replica's reported offset. Only meaningful when Role == "master";
	// zero otherwise (a single INFO call on a replica cannot see the
	// master's offset, so byte lag isn't computable from that side).
	ReplicationLagBytes int64 `json:"replication_lag_bytes"`
	// ReplicationLagSeconds is how long this instance has gone without
	// hearing from its master (INFO replication: master_last_io_seconds_ago).
	// Only meaningful when Role == "slave"; zero otherwise.
	ReplicationLagSeconds int64  `json:"replication_lag_seconds"`
	ConnectedSlaves       int64  `json:"connected_slaves"`
	Role                  string `json:"role"` // "master" | "slave"

	// Persistence
	RDBLastSaveTime int64 `json:"rdb_last_save_time"`
	// RDBBgsaveInProgress is true while a background RDB save is running
	// (INFO persistence: rdb_bgsave_in_progress == 1). A blocking latency
	// spike that coincides with this being true is the signature of the
	// RDB_SNAPSHOT_BLOCKING rule.
	RDBBgsaveInProgress bool `json:"rdb_bgsave_in_progress"`
	// AppendFsync is the configured AOF fsync policy (CONFIG GET
	// appendfsync): "always", "everysec", or "no". "always" is the
	// signature of the AOF_FSYNC_BLOCKING rule. Empty if the agent's ACL
	// user cannot run CONFIG GET (see Snapshot.DisabledCapabilities).
	AppendFsync string `json:"append_fsync"`

	// Server
	UptimeInSeconds int64 `json:"uptime_in_seconds"`
}

// SlowlogEntry represents a single entry from SLOWLOG GET.
type SlowlogEntry struct {
	// ID is the unique monotonic ID assigned by Redis.
	ID int64 `json:"id"`
	// Timestamp is when the command was executed (UNIX seconds).
	Timestamp int64 `json:"timestamp"`
	// DurationMicros is the execution time in microseconds.
	DurationMicros int64 `json:"duration_micros"`
	// Command is the Redis command and its arguments (first 128 bytes per Redis default).
	Command []string `json:"command"`
	// ClientAddr is the IP:port of the client that issued the command.
	ClientAddr string `json:"client_addr"`
	// ClientName is the optional name set via CLIENT SETNAME.
	ClientName string `json:"client_name"`
}

// LatencySample is a single reading from LATENCY LATEST.
type LatencySample struct {
	// EventName identifies the Redis latency event (e.g. "command", "fast-command").
	EventName string `json:"event_name"`
	// Timestamp is when the latest sample was recorded (UNIX seconds).
	Timestamp int64 `json:"timestamp"`
	// LatencyMillis is the recorded latency for this event in milliseconds.
	LatencyMillis int64 `json:"latency_millis"`
}