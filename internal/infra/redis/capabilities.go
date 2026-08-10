package redis

import (
	"context"
	"log/slog"
	"strings"
	"time"
)

// Capabilities records which Redis commands the agent is permitted to run.
// It is populated once at startup by probeCapabilities() and consulted on
// every Collect() call. Commands absent from the ACL are silently skipped —
// no error, no retry — but noted in Snapshot.DisabledCapabilities so the
// backend can surface an informative message in the UI.
type Capabilities struct {
	// Info is true if the agent can run INFO commands.
	// Fatal if false — the agent cannot collect any metrics without INFO.
	Info bool
	// Slowlog is true if the agent can run SLOWLOG GET.
	// Non-fatal if false — metrics still ship, root-cause analysis is degraded.
	Slowlog bool
	// Latency is true if the agent can run LATENCY LATEST.
	// Non-fatal if false — latency samples are omitted from the Snapshot.
	Latency bool
	// Config is true if the agent can run CONFIG GET.
	// Non-fatal if false — AppendFsync is omitted from the Snapshot,
	// degrading the AOF_FSYNC_BLOCKING rule to unavailable.
	Config bool
}

// disabled returns the list of capability names that are false.
// Used to populate Snapshot.DisabledCapabilities.
func (c Capabilities) Disabled() []string {
	var out []string
	if !c.Slowlog {
		out = append(out, "SLOWLOG")
	}
	if !c.Latency {
		out = append(out, "LATENCY")
	}
	if !c.Config {
		out = append(out, "CONFIG")
	}
	return out
}

// probeCapabilities runs a minimal command for each capability and records
// whether it succeeds or is denied by an ACL rule.
//
// Each probe uses the cheapest possible arguments:
//   - INFO server    → smallest INFO section, avoids loading all stats
//   - SLOWLOG GET 0  → fetches zero entries, no data transferred
//   - LATENCY LATEST → read-only, returns current latency events
//
// NOPERM errors are treated as permanent ACL denials.
// All other errors (timeout, network) are treated as transient — we assume
// the command is available and let the normal Collect() error path handle it.
func (c *RedisCollector) probeCapabilities(ctx context.Context) (Capabilities, error) {
	caps := Capabilities{}

	// ── INFO ─────────────────────────────────────────────────────────────────
	// INFO is required — fatal if denied.
	_, infoErr := c.client.Info(ctx, "server").Result()
	if infoErr != nil {
		if IsACLDenied(infoErr) {
			// Return an error so New() can fail fast with a clear message.
			return caps, &ACLError{
				Command: "INFO",
				Hint:    "grant the INFO command: ACL SETUSER diagnostack +info ~* on ><password>",
			}
		}
		// Transient error — assume INFO is available, let Collect() handle it.
		c.logger.Warn("INFO probe returned non-ACL error — assuming available",
			slog.String("error", infoErr.Error()),
		)
	}
	caps.Info = true

	// ── SLOWLOG ──────────────────────────────────────────────────────────────
	probeCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
	_, slowErr := c.client.SlowLogGet(probeCtx, 0).Result()
	cancel()

	if slowErr != nil && IsACLDenied(slowErr) {
		caps.Slowlog = false
		c.logger.Warn("SLOWLOG denied by Redis ACL — slowlog analysis will be unavailable",
			slog.String("acl_hint", "grant: ACL SETUSER diagnostack +slowlog ~* on ><password>"),
		)
	} else {
		caps.Slowlog = true
	}

	// ── LATENCY ──────────────────────────────────────────────────────────────
	probeCtx, cancel = context.WithTimeout(ctx, 3*time.Second)
	_, latErr := c.client.Do(probeCtx, "LATENCY", "LATEST").Result()
	cancel()

	if latErr != nil && IsACLDenied(latErr) {
		caps.Latency = false
		c.logger.Warn("LATENCY LATEST denied by Redis ACL — latency samples will be unavailable",
			slog.String("acl_hint", "grant: ACL SETUSER diagnostack +latency ~* on ><password>"),
		)
	} else {
		caps.Latency = true
	}

	// ── CONFIG ───────────────────────────────────────────────────────────────
	probeCtx, cancel = context.WithTimeout(ctx, 3*time.Second)
	_, cfgErr := c.client.ConfigGet(probeCtx, "appendfsync").Result()
	cancel()

	if cfgErr != nil && IsACLDenied(cfgErr) {
		caps.Config = false
		c.logger.Warn("CONFIG GET denied by Redis ACL — AOF fsync detection will be unavailable",
			slog.String("acl_hint", "grant: ACL SETUSER diagnostack +config|get ~* on ><password>"),
		)
	} else {
		caps.Config = true
	}

	return caps, nil
}

// logCapabilitySummary emits a single INFO-level summary after probing.
// This gives the operator a clear picture of what the agent will and won't
// collect, without requiring them to read through warn lines.
func (c *RedisCollector) logCapabilitySummary(caps Capabilities) {
	disabled := caps.Disabled()
	if len(disabled) == 0 {
		c.logger.Info("redis capability probe passed — all commands available",
			slog.String("addr", c.cfg.Addr),
		)
		return
	}

	c.logger.Warn("redis capability probe: some commands are ACL-restricted",
		slog.String("addr", c.cfg.Addr),
		slog.Any("disabled", disabled),
		slog.String("impact", capabilityImpact(caps)),
		slog.String("docs", "https://docs.diagnostack.io/agent/acl-setup"),
	)
}

// capabilityImpact returns a human-readable description of what the operator
// will lose due to the disabled capabilities.
func capabilityImpact(caps Capabilities) string {
	switch {
	case !caps.Slowlog && !caps.Latency && !caps.Config:
		return "metrics only — root-cause analysis (slowlog + latency) and AOF fsync detection unavailable"
	case !caps.Slowlog && !caps.Latency:
		return "metrics only — root-cause analysis (slowlog + latency) unavailable"
	case !caps.Slowlog:
		return "slowlog analysis unavailable — cannot identify slow commands"
	case !caps.Latency:
		return "latency samples unavailable — tail latency charts will be empty"
	case !caps.Config:
		return "AOF fsync config unavailable — cannot evaluate the AOF_FSYNC_BLOCKING rule"
	default:
		return "full observability available"
	}
}

// isACLDenied returns true when the Redis error is a NOPERM response,
// which means the authenticated user's ACL does not allow this command.
//
// Redis ACL error format (all Redis versions):
//
//	NOPERM this user has no permissions to run the 'info' command
//	NOPERM this user has no permissions to access one of the channels used
//	NOPERM this user has no permissions to access one of the keys
//
// We match only on the prefix "NOPERM" to be version-agnostic.
func IsACLDenied(err error) bool {
	if err == nil {
		return false
	}
	return strings.HasPrefix(err.Error(), "NOPERM")
}

// ACLError is returned by probeCapabilities when a required command is denied.
// It carries a concrete remediation hint for the operator.
type ACLError struct {
	Command string
	Hint    string
}

func (e *ACLError) Error() string {
	return "Redis ACL denied required command " + e.Command + ": " + e.Hint
}