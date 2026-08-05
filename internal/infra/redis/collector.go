// Package redis provides the concrete inbound adapter that polls a Redis
// server via INFO, SLOWLOG GET, and LATENCY LATEST and turns the results
// into a domain.Snapshot.
//
// It implements the domain.Collector port defined in internal/domain.
// The port interface itself must NEVER be redeclared here — only the
// concrete RedisCollector struct belongs in this package.
package redis

import (
	"context"
	"fmt"
	"log/slog"
	"strconv"
	"strings"
	"time"

	goredis "github.com/redis/go-redis/v9"

	"github.com/hoaithuonguit/diagnostack-agent/internal/domain"
)

// slowlogFetchWindow is how many slowlog entries we request per collect
// cycle. 128 comfortably covers a 15s interval even on a busy server
// without pulling an unbounded amount of data on every tick.
const slowlogFetchWindow = 128

// SlowlogRolloverThreshold is exposed so callers/tests can sanity-check the
// relationship between the fetch window and rollover detection. It is not
// currently used as a numeric gate itself — rollover is detected whenever
// the newest fetched ID is lower than the last ID we shipped, which can only
// happen after the Redis process restarts (SLOWLOG IDs are monotonic for
// the lifetime of the process). The constant exists so that assumption is
// documented and bounded rather than implicit.
const SlowlogRolloverThreshold int64 = 1000

// CollectorConfig configures the Redis collector adapter.
type CollectorConfig struct {
	Addr           string
	Password       string
	DB             int
	AgentID        string
	ServerLabel    string
	CollectTimeout time.Duration
}

// RedisCollector is the concrete implementation of domain.Collector.
// capabilities.go (probeCapabilities, logCapabilitySummary, IsACLDenied,
// ACLError) is implemented as methods/functions on this same type.
type RedisCollector struct {
	client *goredis.Client
	logger *slog.Logger
	cfg    CollectorConfig

	caps Capabilities

	// lastSlowlogID is the highest slowlog entry ID shipped so far.
	// Used to deduplicate entries across collect cycles.
	lastSlowlogID int64
}

// New connects to Redis, probes ACL capabilities, and returns a ready
// RedisCollector. It returns an error only when INFO is denied (fatal) or
// the initial connection cannot be established — SLOWLOG/LATENCY denial is
// non-fatal and simply recorded in caps.
func New(cfg CollectorConfig, logger *slog.Logger) (*RedisCollector, error) {
	if cfg.CollectTimeout <= 0 {
		cfg.CollectTimeout = 10 * time.Second
	}

	client := goredis.NewClient(&goredis.Options{
		Addr:     cfg.Addr,
		Password: cfg.Password,
		DB:       cfg.DB,
	})

	c := &RedisCollector{
		client: client,
		logger: logger,
		cfg:    cfg,
	}

	probeCtx, cancel := context.WithTimeout(context.Background(), cfg.CollectTimeout)
	defer cancel()

	caps, err := c.probeCapabilities(probeCtx)
	if err != nil {
		_ = client.Close()
		return nil, err
	}
	c.caps = caps
	c.logCapabilitySummary(caps)

	return c, nil
}

// Close releases the underlying Redis connection pool.
func (c *RedisCollector) Close() error {
	return c.client.Close()
}

// Collect polls Redis once and returns a Snapshot.
// It never calls MONITOR. Each sub-command shares the ctx deadline set by
// the caller's CollectTimeout budget.
func (c *RedisCollector) Collect(ctx context.Context) (*domain.Snapshot, error) {
	ctx, cancel := context.WithTimeout(ctx, c.cfg.CollectTimeout)
	defer cancel()

	rawInfo, err := c.client.Info(ctx, "server", "memory", "clients", "stats", "replication", "persistence").Result()
	if err != nil {
		return nil, fmt.Errorf("INFO: %w", err)
	}
	fields := parseInfoOutput(rawInfo)
	metrics := buildMetrics(fields)

	snapshot := &domain.Snapshot{
		AgentID:              c.cfg.AgentID,
		ServerLabel:          c.cfg.ServerLabel,
		CollectedAt:          time.Now().UTC(),
		RedisVersion:         fields["redis_version"],
		Metrics:              metrics,
		DisabledCapabilities: c.caps.Disabled(),
	}

	if c.caps.Slowlog {
		entries, err := c.collectSlowlog(ctx)
		if err != nil {
			// Non-fatal: metrics still ship without slowlog for this cycle.
			c.logger.Warn("slowlog collection failed — shipping metrics without it",
				slog.String("error", err.Error()))
		} else {
			snapshot.Slowlog = entries
		}
	}

	if c.caps.Latency {
		samples, err := c.collectLatency(ctx)
		if err != nil {
			c.logger.Warn("latency collection failed — shipping metrics without it",
				slog.String("error", err.Error()))
		} else {
			snapshot.Latency = samples
		}
	}

	return snapshot, nil
}

// collectSlowlog fetches recent slowlog entries and deduplicates against
// the last shipped ID. It also detects process restarts (which reset
// Redis's internal SLOWLOG ID counter to zero) and, on detection, treats
// every fetched entry as new rather than dropping them as "already seen".
func (c *RedisCollector) collectSlowlog(ctx context.Context) ([]domain.SlowlogEntry, error) {
	raw, err := c.client.SlowLogGet(ctx, slowlogFetchWindow).Result()
	if err != nil {
		return nil, err
	}
	if len(raw) == 0 {
		return nil, nil
	}

	// SLOWLOG GET returns entries newest-first.
	newestID := raw[0].ID

	rollover := newestID < c.lastSlowlogID
	if rollover {
		c.logger.Warn("slowlog ID rollover detected — Redis likely restarted; shipping all fetched entries",
			slog.Int64("previous_last_id", c.lastSlowlogID),
			slog.Int64("newest_id", newestID),
		)
	}

	entries := make([]domain.SlowlogEntry, 0, len(raw))
	for _, e := range raw {
		if !rollover && e.ID <= c.lastSlowlogID {
			continue
		}
		entries = append(entries, domain.SlowlogEntry{
			ID:             e.ID,
			Timestamp:      e.Time.Unix(),
			DurationMicros: e.Duration.Microseconds(),
			Command:        e.Args,
			ClientAddr:     e.ClientAddr,
			ClientName:     e.ClientName,
		})
	}

	c.lastSlowlogID = newestID

	return entries, nil
}

// collectLatency fetches LATENCY LATEST and parses the RESP reply.
// go-redis has no typed helper for this command, so we parse the raw
// []interface{} response: each event is
// [event-name, latest-unix-time, latest-latency-ms, max-latency-ms].
func (c *RedisCollector) collectLatency(ctx context.Context) ([]domain.LatencySample, error) {
	raw, err := c.client.Do(ctx, "LATENCY", "LATEST").Result()
	if err != nil {
		return nil, err
	}

	events, ok := raw.([]interface{})
	if !ok {
		return nil, fmt.Errorf("unexpected LATENCY LATEST reply type %T", raw)
	}

	samples := make([]domain.LatencySample, 0, len(events))
	for _, ev := range events {
		fields, ok := ev.([]interface{})
		if !ok || len(fields) < 3 {
			continue
		}

		name, _ := fields[0].(string)
		ts := toInt64(fields[1])
		latency := toInt64(fields[2])

		samples = append(samples, domain.LatencySample{
			EventName:     name,
			Timestamp:     ts,
			LatencyMillis: latency,
		})
	}

	return samples, nil
}

// toInt64 handles the fact that RESP integers may arrive as int64 already
// (RESP2 via go-redis) depending on client/protocol negotiation.
func toInt64(v interface{}) int64 {
	switch n := v.(type) {
	case int64:
		return n
	case int:
		return int64(n)
	case string:
		i, _ := strconv.ParseInt(n, 10, 64)
		return i
	default:
		return 0
	}
}

// buildMetrics maps parsed INFO fields onto the typed domain.Metrics struct.
// Missing or unparsable fields are left at their zero value rather than
// failing the whole collect cycle — a partial snapshot is more useful than
// none.
func buildMetrics(f map[string]string) domain.Metrics {
	return domain.Metrics{
		UsedMemoryBytes:        parseInt(f["used_memory"]),
		UsedMemoryRSSBytes:     parseInt(f["used_memory_rss"]),
		MaxMemoryBytes:         parseInt(f["maxmemory"]),
		MemFragmentationRatio:  parseFloat(f["mem_fragmentation_ratio"]),
		ConnectedClients:       parseInt(f["connected_clients"]),
		BlockedClients:         parseInt(f["blocked_clients"]),
		InstantaneousOpsPerSec: parseInt(f["instantaneous_ops_per_sec"]),
		KeyspaceHits:           parseInt(f["keyspace_hits"]),
		KeyspaceMisses:         parseInt(f["keyspace_misses"]),
		EvictedKeys:            parseInt(f["evicted_keys"]),
		RejectedConnections:    parseInt(f["rejected_connections"]),
		ReplicationLagBytes:    parseInt(f["master_repl_offset"]),
		Role:                   f["role"],
		RDBLastSaveTime:        parseInt(f["rdb_last_save_time"]),
		UptimeInSeconds:        parseInt(f["uptime_in_seconds"]),
	}
}

func parseInt(s string) int64 {
	i, _ := strconv.ParseInt(strings.TrimSpace(s), 10, 64)
	return i
}

func parseFloat(s string) float64 {
	f, _ := strconv.ParseFloat(strings.TrimSpace(s), 64)
	return f
}

// parseInfoOutput parses the raw text returned by the Redis INFO command
// into a flat key/value map. INFO output uses "# Section" headers and
// "key:value" lines separated by CRLF (or LF, depending on platform).
// Values may themselves contain colons (e.g. IP:port pairs), so we split
// only on the first colon.
func parseInfoOutput(raw string) map[string]string {
	fields := make(map[string]string)
	if raw == "" {
		return fields
	}

	lines := strings.Split(raw, "\n")
	for _, line := range lines {
		line = strings.TrimRight(line, "\r")
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		idx := strings.Index(line, ":")
		if idx < 0 {
			continue
		}
		key := line[:idx]
		value := line[idx+1:]
		fields[key] = value
	}

	return fields
}
