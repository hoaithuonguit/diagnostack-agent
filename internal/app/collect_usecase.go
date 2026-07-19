package app

import (
	"context"
	"fmt"
	"log/slog"
	"github.com/keywatch/agent/internal/domain"
)

// CollectUseCase polls Redis via the Collector port and enqueues the
// resulting Snapshot into the ring buffer for the ShipUseCase to drain.
//
// It owns no knowledge of Redis commands, HTTP, or retry logic.
// It orchestrates the collect → buffer pipeline and delegates error
// health tracking to CollectHealth.
type CollectUseCase struct {
	collector       domain.Collector
	buffer          SnapshotEnqueuer
	health          *CollectHealth
	logger          *slog.Logger
}

// SnapshotEnqueuer is the narrow interface the CollectUseCase needs from
// the ring buffer. Keeping it narrow avoids importing the buffer package.
type SnapshotEnqueuer interface {
	Enqueue(s *domain.Snapshot)
}

// NewCollectUseCase constructs a CollectUseCase.
// All dependencies are injected — no globals, no service locator.
func NewCollectUseCase(
	collector domain.Collector,
	buffer SnapshotEnqueuer,
	cfg AgentConfig,
	logger *slog.Logger,
) *CollectUseCase {
	return &CollectUseCase{
		collector:       collector,
		buffer:          buffer,
		health:          NewCollectHealth(logger),
		logger:          logger,
	}
}

// Execute runs a single collect cycle: poll Redis, then enqueue.
// The scheduler calls this on every tick. ctx carries the cancellation
// signal from the graceful shutdown hook.
//
// Error handling:
//   - Transient errors (1-2 consecutive): logged at WARN, cycle skipped.
//   - Sustained errors (3+):              logged at ERROR with actionable hints.
//   - Recovery:                           logged at INFO.
func (uc *CollectUseCase) Execute(ctx context.Context) error {
	snapshot, err := uc.collector.Collect(ctx)
	if err != nil {
		// FIX 3: route the error through CollectHealth for escalation tracking.
		uc.health.RecordError(fmt.Errorf("collect from redis: %w", err))
		return fmt.Errorf("collect from redis: %w", err)
	}

	// Successful collect: reset the failure streak.
	uc.health.RecordSuccess()

	uc.buffer.Enqueue(snapshot)

	uc.logger.Debug("snapshot collected and enqueued",
		slog.String("agent_id", snapshot.AgentID),
		slog.String("server_label", snapshot.ServerLabel),
		slog.Int("slowlog_entries", len(snapshot.Slowlog)),
	)

	return nil
}

// Health returns the CollectHealth tracker (used in tests and diagnostics).
func (uc *CollectUseCase) Health() *CollectHealth {
	return uc.health
}
