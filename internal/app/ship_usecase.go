package app

import (
	"context"
	"errors"
	"log/slog"
	"math"
	"math/rand"
	"time"

	"github.com/hoaithuonguit/diagnostack-agent/internal/domain"
)

// ShipUseCase drains the ring buffer and delivers each Snapshot to the
// Diagnostack backend via the Shipper port. It owns the retry policy —
// the Shipper itself is a single-attempt HTTP call with no retry.
type ShipUseCase struct {
	shipper domain.Shipper
	buffer  SnapshotDequeuer
	cfg     RetryConfig
	logger  *slog.Logger

	// droppedTotal counts payloads discarded after max retries.
	// Exposed for observability — the bootstrap layer may surface this.
	droppedTotal int64
}

// SnapshotDequeuer is the narrow interface the ShipUseCase needs from the buffer.
type SnapshotDequeuer interface {
	Dequeue() (*domain.Snapshot, bool)
}

// NewShipUseCase constructs a ShipUseCase.
func NewShipUseCase(
	shipper domain.Shipper,
	buffer SnapshotDequeuer,
	cfg RetryConfig,
	logger *slog.Logger,
) *ShipUseCase {
	return &ShipUseCase{
		shipper: shipper,
		buffer:  buffer,
		cfg:     cfg,
		logger:  logger,
	}
}

// DrainOnce dequeues and ships all Snapshots currently in the buffer.
// The scheduler calls this after every collect cycle.
// ctx carries the cancellation signal — in-flight retries respect it.
func (uc *ShipUseCase) DrainOnce(ctx context.Context) {
	for {
		snapshot, ok := uc.buffer.Dequeue()
		if !ok {
			return // buffer is empty
		}
		uc.shipWithRetry(ctx, snapshot)

		// Respect shutdown signal between items.
		if ctx.Err() != nil {
			return
		}
	}
}

// shipWithRetry attempts to ship a single Snapshot, retrying on transient
// failures according to the exponential backoff policy in RetryConfig.
//
// Backoff formula:  min(base * 2^(attempt-1) + jitter, max)
// Jitter:           ±20% of the computed delay — prevents thundering herd
//
//	when multiple agents reconnect after an outage.
func (uc *ShipUseCase) shipWithRetry(ctx context.Context, snapshot *domain.Snapshot) {
	for attempt := 1; attempt <= uc.cfg.MaxAttempts; attempt++ {
		err := uc.shipper.Ship(ctx, snapshot)
		if err == nil {
			uc.logger.Debug("snapshot shipped",
				slog.String("agent_id", snapshot.AgentID),
				slog.Int("attempt", attempt),
			)
			return
		}

		var shipErr *domain.ShipError
		if errors.As(err, &shipErr) && !shipErr.Retryable {
			uc.logger.Error("non-retryable ship failure — dropping payload",
				slog.String("agent_id", snapshot.AgentID),
				slog.String("error", shipErr.Message),
				slog.Int("status_code", shipErr.StatusCode),
			)
			uc.droppedTotal++
			return
		}

		if attempt == uc.cfg.MaxAttempts {
			break
		}

		delay := uc.backoffDelay(attempt)
		uc.logger.Warn("ship failed, will retry",
			slog.String("agent_id", snapshot.AgentID),
			slog.Int("attempt", attempt),
			slog.Int("max_attempts", uc.cfg.MaxAttempts),
			slog.Duration("retry_in", delay),
			slog.String("error", err.Error()),
		)

		select {
		case <-ctx.Done():
			uc.logger.Info("shutdown signalled during retry backoff — dropping payload")
			uc.droppedTotal++
			return
		case <-time.After(delay):
		}
	}

	uc.logger.Error("max retries exhausted — dropping payload",
		slog.String("agent_id", snapshot.AgentID),
		slog.Int64("total_dropped", uc.droppedTotal),
	)
	uc.droppedTotal++
}

// backoffDelay computes the delay for a given attempt number (1-indexed).
// Returns: min(base * 2^(attempt-1), max) ± 20% jitter.
func (uc *ShipUseCase) backoffDelay(attempt int) time.Duration {
	exp := math.Pow(2, float64(attempt-1))
	base := float64(uc.cfg.BaseDelay) * exp
	if base > float64(uc.cfg.MaxDelay) {
		base = float64(uc.cfg.MaxDelay)
	}
	// ±20% jitter
	jitter := base * 0.2 * (rand.Float64()*2 - 1) //nolint:gosec // non-cryptographic jitter
	delay := time.Duration(base + jitter)
	if delay < 0 {
		delay = 0
	}
	return delay
}

// DroppedTotal returns the cumulative count of dropped payloads.
func (uc *ShipUseCase) DroppedTotal() int64 {
	return uc.droppedTotal
}
