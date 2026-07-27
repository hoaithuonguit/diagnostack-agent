package app

import (
	"log/slog"
	"strings"
	"time"
)

// CollectHealth tracks the health of the collect pipeline across ticks.
// It escalates log severity after consecutive failures so operators notice
// a sustained Redis problem rather than a single transient blip.
//
// Thresholds (configurable via CollectHealthConfig):
//   - 1–2 consecutive failures  → WARN  (transient, self-resolving)
//   - 3+  consecutive failures  → ERROR (operator action needed)
//   - After ErrorThreshold      → one actionable message with root-cause hints
//   - On recovery               → INFO  (so operators know it resolved)
type CollectHealth struct {
	cfg    CollectHealthConfig
	logger *slog.Logger

	consecutiveFailures int
	firstFailureAt      time.Time
	lastErrMsg          string
	// escalated is set to true once we have emitted the "action needed" message
	// so we don't spam it on every tick.
	escalated bool
}

// CollectHealthConfig controls escalation thresholds.
type CollectHealthConfig struct {
	// WarnThreshold is the number of consecutive failures before escalating
	// from DEBUG to WARN. Default: 1 (warn on first failure).
	WarnThreshold int
	// ErrorThreshold is the number of consecutive failures before escalating
	// to ERROR and emitting the actionable hint message. Default: 3.
	ErrorThreshold int
}

func defaultCollectHealthConfig() CollectHealthConfig {
	return CollectHealthConfig{
		WarnThreshold:  1,
		ErrorThreshold: 3,
	}
}

// NewCollectHealth creates a CollectHealth with default thresholds.
func NewCollectHealth(logger *slog.Logger) *CollectHealth {
	return NewCollectHealthWithConfig(defaultCollectHealthConfig(), logger)
}

// NewCollectHealthWithConfig creates a CollectHealth with custom thresholds.
func NewCollectHealthWithConfig(cfg CollectHealthConfig, logger *slog.Logger) *CollectHealth {
	return &CollectHealth{cfg: cfg, logger: logger}
}

// RecordError is called by CollectUseCase when Collect() returns an error.
// It increments the failure counter and logs at the appropriate level.
func (h *CollectHealth) RecordError(err error) {
	if h.consecutiveFailures == 0 {
		h.firstFailureAt = time.Now()
		h.escalated = false
	}
	h.consecutiveFailures++
	h.lastErrMsg = err.Error()

	switch {
	case h.consecutiveFailures >= h.cfg.ErrorThreshold:
		h.logger.Error("redis collect failing repeatedly",
			slog.Int("consecutive_failures", h.consecutiveFailures),
			slog.Duration("failing_for", time.Since(h.firstFailureAt).Round(time.Second)),
			slog.String("error", h.lastErrMsg),
		)
		// Emit the actionable hint once so the operator knows what to check.
		if !h.escalated {
			h.escalated = true
			h.emitActionableHint(err)
		}

	case h.consecutiveFailures >= h.cfg.WarnThreshold:
		h.logger.Warn("redis collect failed",
			slog.Int("consecutive_failures", h.consecutiveFailures),
			slog.String("error", h.lastErrMsg),
		)
	}
}

// RecordSuccess is called by CollectUseCase when Collect() succeeds.
// It resets the counter and logs a recovery message if we were escalated.
func (h *CollectHealth) RecordSuccess() {
	if h.consecutiveFailures == 0 {
		return // nothing to reset
	}

	wasEscalated := h.consecutiveFailures >= h.cfg.ErrorThreshold
	duration := time.Since(h.firstFailureAt).Round(time.Second)
	failures := h.consecutiveFailures

	h.consecutiveFailures = 0
	h.lastErrMsg = ""
	h.escalated = false

	if wasEscalated {
		h.logger.Info("redis collect recovered",
			slog.Int("failures_before_recovery", failures),
			slog.Duration("outage_duration", duration),
		)
	}
}

// ConsecutiveFailures returns the current failure streak (for tests).
func (h *CollectHealth) ConsecutiveFailures() int {
	return h.consecutiveFailures
}

// emitActionableHint logs a single ERROR message with concrete remediation
// steps based on what the error looks like. This is the message an operator
// will see in journalctl after a sustained failure.
func (h *CollectHealth) emitActionableHint(err error) {
	msg := err.Error()

	switch {
	case isAuthError(msg):
		h.logger.Error("ACTION NEEDED — Redis authentication failure",
			slog.String("check", "redis.password in /etc/diagnostack/config.yaml or DIAGNOSTACK_REDIS_PASSWORD env var"),
			slog.String("verify", "redis-cli -h <addr> AUTH <password>"),
		)

	case isConnectionRefused(msg):
		h.logger.Error("ACTION NEEDED — cannot connect to Redis",
			slog.String("check_1", "redis.addr in /etc/diagnostack/config.yaml"),
			slog.String("check_2", "Redis process is running: systemctl status redis"),
			slog.String("check_3", "firewall allows agent → Redis on the configured port"),
		)

	case isTimeout(msg):
		h.logger.Error("ACTION NEEDED — Redis collect timed out repeatedly",
			slog.String("check_1", "Redis may be overloaded — check INFO commandstats"),
			slog.String("check_2", "consider increasing collect_timeout in config"),
			slog.String("check_3", "check network latency between agent and Redis"),
		)

	default:
		h.logger.Error("ACTION NEEDED — Redis collect failing, manual investigation required",
			slog.String("last_error", msg),
			slog.String("docs", "https://docs.diagnostack.io/agent/troubleshooting"),
		)
	}
}

func isAuthError(msg string) bool {
	return containsAny(msg, "NOAUTH", "WRONGPASS", "ERR AUTH", "ERR invalid password", "NOPERM")
}

func isConnectionRefused(msg string) bool {
	return containsAny(msg, "connection refused", "no such host", "no route to host", "i/o timeout", "dial tcp")
}

func isTimeout(msg string) bool {
	return containsAny(msg, "context deadline exceeded", "deadline exceeded", "timeout")
}

func containsAny(s string, subs ...string) bool {
	for _, sub := range subs {
		if strings.Contains(s, sub) {
			return true
		}
	}
	return false
}
