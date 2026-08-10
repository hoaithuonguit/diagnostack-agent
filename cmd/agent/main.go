// Command agent is the Diagnostack observability agent.
// It polls a local Redis server and ships metrics to the Diagnostack Ingestion API.
//
// Usage:
//
//	diagnostack-agent [--config /path/to/config.yaml]
//
// Default config path: /etc/diagnostack/config.yaml
package main

import (
	"context"
	"flag"
	"log/slog"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"

	"github.com/hoaithuonguit/diagnostack-agent/config"
	"github.com/hoaithuonguit/diagnostack-agent/internal/app"
	"github.com/hoaithuonguit/diagnostack-agent/internal/infra/buffer"
	infrahttp "github.com/hoaithuonguit/diagnostack-agent/internal/infra/http"
	infraredis "github.com/hoaithuonguit/diagnostack-agent/internal/infra/redis"
)

// drainSafetyNetInterval bounds how long buffered-but-unshipped data can sit
// idle if a wake signal is ever missed (e.g. the wake channel was full).
// It is not the primary drain trigger — see wake below.
const drainSafetyNetInterval = 5 * time.Second

func main() {
	configPath := flag.String("config", "", "path to config file (default: /etc/diagnostack/config.yaml)")
	flag.Parse()

	// ── Load config ──────────────────────────────────────────────────────────
	cfg, err := config.Load(*configPath)
	if err != nil {
		// Use plain stderr before the logger is ready.
		slog.Error("failed to load config", slog.String("error", err.Error()))
		os.Exit(1)
	}

	// ── Set up structured logger ─────────────────────────────────────────────
	logger := buildLogger(cfg.Log.Level, cfg.Log.Format)
	logger.Info("diagnostack agent starting",
		slog.String("agent_id", cfg.App.AgentID),
		slog.String("server_label", cfg.App.ServerLabel),
		slog.Duration("collect_interval", cfg.App.CollectInterval),
	)

	// ── Build infrastructure adapters ────────────────────────────────────────
	collector, err := infraredis.New(infraredis.CollectorConfig{
		Addr:           cfg.Redis.Addr,
		Password:       cfg.Redis.Password,
		DB:             cfg.Redis.DB,
		AgentID:        cfg.Redis.AgentID,
		ServerLabel:    cfg.Redis.ServerLabel,
		CollectTimeout: cfg.Redis.CollectTimeout,
	}, logger)
	if err != nil {
		logger.Error("cannot connect to Redis", slog.String("error", err.Error()))
		os.Exit(1)
	}
	defer func() { _ = collector.Close() }()

	shipper := infrahttp.New(infrahttp.ShipperConfig{
		Endpoint: cfg.API.Endpoint,
		APIKey:   cfg.API.APIKey,
		Timeout:  cfg.API.Timeout,
	}, logger)

	ring := buffer.New(cfg.App.BufferCapacity, logger)

	// ── Build application use cases ──────────────────────────────────────────
	collectUC := app.NewCollectUseCase(collector, ring, cfg.App, logger)
	shipUC := app.NewShipUseCase(shipper, ring, cfg.App.Retry, logger)

	// ── Set up graceful shutdown ─────────────────────────────────────────────
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	// ── Run the scheduler ────────────────────────────────────────────────────
	// Collect and drain run as independent loops so that ship retries
	// (which can take tens of seconds under exponential backoff during an
	// Ingestion API outage) never delay the next Redis collect cycle. The
	// two loops only share the ring buffer, which is already safe for
	// concurrent access.
	//
	// wake lets a freshly collected snapshot ship promptly instead of
	// waiting for the drain loop's own safety-net ticker. It is buffered
	// with capacity 1 and sent to non-blockingly: if a wake is already
	// pending, coalescing into one is fine because DrainOnce always drains
	// the whole buffer, not just one item.
	wake := make(chan struct{}, 1)

	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		runDrainLoop(ctx, shipUC, wake, logger)
	}()

	runCollectLoop(ctx, cfg.App, collectUC, wake, logger)

	// Collect loop has stopped (ctx cancelled). Wait for the drain loop to
	// finish its own final flush before reporting totals.
	wg.Wait()

	logger.Info("diagnostack agent stopped",
		slog.Int64("total_dropped_payloads", shipUC.DroppedTotal()),
	)
}

// runCollectLoop polls Redis on cfg.CollectInterval and pushes each
// Snapshot into the ring buffer. It never blocks on shipping — that is the
// drain loop's job — so a slow or unavailable Ingestion API cannot degrade
// collection cadence. It blocks until ctx is cancelled (SIGINT / SIGTERM).
func runCollectLoop(
	ctx context.Context,
	cfg app.AgentConfig,
	collectUC *app.CollectUseCase,
	wake chan<- struct{},
	logger *slog.Logger,
) {
	ticker := time.NewTicker(cfg.CollectInterval)
	defer ticker.Stop()

	// Run once immediately so the first data point is not delayed.
	collectTick(ctx, collectUC, wake, logger)

	for {
		select {
		case <-ctx.Done():
			logger.Info("collect loop stopped")
			return

		case <-ticker.C:
			collectTick(ctx, collectUC, wake, logger)
		}
	}
}

// collectTick runs a single collect cycle and nudges the drain loop.
func collectTick(
	ctx context.Context,
	collectUC *app.CollectUseCase,
	wake chan<- struct{},
	logger *slog.Logger,
) {
	if err := collectUC.Execute(ctx); err != nil {
		logger.Error("collect cycle failed", slog.String("error", err.Error()))
		return
	}

	select {
	case wake <- struct{}{}:
	default:
		// A wake is already pending — DrainOnce will pick up this
		// snapshot too once it runs, so dropping the duplicate is fine.
	}
}

// runDrainLoop drains the ring buffer to the Ingestion API whenever the
// collect loop signals new data (wake), or at least every
// drainSafetyNetInterval as a fallback in case a wake was ever missed. It
// owns retry/backoff timing entirely on its own goroutine, so a slow
// outage-recovery drain never blocks runCollectLoop. On shutdown it makes
// one final bounded-time drain attempt before returning.
func runDrainLoop(
	ctx context.Context,
	shipUC *app.ShipUseCase,
	wake <-chan struct{},
	logger *slog.Logger,
) {
	ticker := time.NewTicker(drainSafetyNetInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			logger.Info("shutdown signal received — flushing remaining buffer")
			flushCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			shipUC.DrainOnce(flushCtx)
			cancel()
			return

		case <-wake:
			shipUC.DrainOnce(ctx)

		case <-ticker.C:
			shipUC.DrainOnce(ctx)
		}
	}
}

// buildLogger constructs a slog.Logger from the configured level and format.
func buildLogger(level, format string) *slog.Logger {
	var lvl slog.Level
	switch level {
	case "debug":
		lvl = slog.LevelDebug
	case "warn":
		lvl = slog.LevelWarn
	case "error":
		lvl = slog.LevelError
	default:
		lvl = slog.LevelInfo
	}

	opts := &slog.HandlerOptions{Level: lvl}
	var handler slog.Handler
	if format == "json" {
		handler = slog.NewJSONHandler(os.Stdout, opts)
	} else {
		handler = slog.NewTextHandler(os.Stdout, opts)
	}
	return slog.New(handler)
}
