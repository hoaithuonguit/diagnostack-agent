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
	"syscall"
	"time"

	"github.com/hoaithuonguit/diagnostack-agent/config"
	"github.com/hoaithuonguit/diagnostack-agent/internal/app"
	"github.com/hoaithuonguit/diagnostack-agent/internal/infra/buffer"
	infrahttp "github.com/hoaithuonguit/diagnostack-agent/internal/infra/http"
	infraredis "github.com/hoaithuonguit/diagnostack-agent/internal/infra/redis"
)

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
	run(ctx, cfg.App, collectUC, shipUC, logger)

	logger.Info("diagnostack agent stopped",
		slog.Int64("total_dropped_payloads", shipUC.DroppedTotal()),
	)
}

// run is the main scheduler loop.
// On each tick it collects from Redis then drains the buffer to the API.
// It blocks until ctx is cancelled (SIGINT / SIGTERM).
func run(
	ctx context.Context,
	cfg app.AgentConfig,
	collectUC *app.CollectUseCase,
	shipUC *app.ShipUseCase,
	logger *slog.Logger,
) {
	ticker := time.NewTicker(cfg.CollectInterval)
	defer ticker.Stop()

	// Run once immediately so the first data point is not delayed.
	tick(ctx, collectUC, shipUC, logger)

	for {
		select {
		case <-ctx.Done():
			logger.Info("shutdown signal received — flushing remaining buffer")
			// One final drain attempt on shutdown.
			flushCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			shipUC.DrainOnce(flushCtx)
			cancel()
			return

		case <-ticker.C:
			tick(ctx, collectUC, shipUC, logger)
		}
	}
}

// tick runs a single collect + ship cycle.
func tick(
	ctx context.Context,
	collectUC *app.CollectUseCase,
	shipUC *app.ShipUseCase,
	logger *slog.Logger,
) {
	if err := collectUC.Execute(ctx); err != nil {
		logger.Error("collect cycle failed", slog.String("error", err.Error()))
	}
	shipUC.DrainOnce(ctx)
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
