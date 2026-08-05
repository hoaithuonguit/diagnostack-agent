// Package http provides the concrete outbound adapter that ships
// domain.Snapshots to the Diagnostack Ingestion API over HTTPS.
//
// It implements the domain.Shipper port. All retry/backoff logic lives
// one layer up in the application ShipUseCase — this adapter only knows
// how to make a single HTTP attempt and classify the outcome.
package http

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"github.com/hoaithuonguit/diagnostack-agent/internal/domain"
)

// defaultTimeout is used when ShipperConfig.Timeout is zero.
const defaultTimeout = 10 * time.Second

// ShipperConfig configures the HTTP shipper adapter.
type ShipperConfig struct {
	// Endpoint is the full URL of the Ingestion API push endpoint.
	Endpoint string
	// APIKey authenticates this agent installation. Sent as X-Agent-Key.
	APIKey string
	// Timeout bounds a single HTTP attempt. Defaults to 10s if zero.
	Timeout time.Duration
}

// Shipper is the concrete HTTP implementation of domain.Shipper.
type Shipper struct {
	cfg    ShipperConfig
	client *http.Client
	logger *slog.Logger
}

// New constructs a Shipper. It performs no network I/O — the HTTP client
// is created eagerly but connections are only opened on Ship().
func New(cfg ShipperConfig, logger *slog.Logger) *Shipper {
	if cfg.Timeout <= 0 {
		cfg.Timeout = defaultTimeout
	}
	return &Shipper{
		cfg:    cfg,
		client: &http.Client{Timeout: cfg.Timeout},
		logger: logger,
	}
}

// Ship sends a single Snapshot to the Ingestion API.
//
// Outcome classification:
//   - 2xx                     → nil error
//   - 400, 401, 403, 404, 422 → non-retryable *domain.ShipError (bad request/auth — retrying won't help)
//   - 429, 5xx                → retryable *domain.ShipError (transient — caller should retry)
//   - network / timeout error → retryable *domain.ShipError with StatusCode 0
func (s *Shipper) Ship(ctx context.Context, snapshot *domain.Snapshot) error {
	body, err := json.Marshal(snapshot)
	if err != nil {
		return &domain.ShipError{
			StatusCode: 0,
			Message:    fmt.Sprintf("marshal snapshot: %v", err),
			Retryable:  false,
		}
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, s.cfg.Endpoint, bytes.NewReader(body))
	if err != nil {
		return &domain.ShipError{
			StatusCode: 0,
			Message:    fmt.Sprintf("build request: %v", err),
			Retryable:  false,
		}
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Agent-Key", s.cfg.APIKey)

	resp, err := s.client.Do(req)
	if err != nil {
		// Network errors, DNS failures, timeouts, and context cancellation
		// all land here. Treat as retryable — the caller (ShipUseCase)
		// owns backoff and eventual give-up.
		s.logger.Warn("ship request failed", slog.String("error", err.Error()))
		return &domain.ShipError{
			StatusCode: 0,
			Message:    fmt.Sprintf("request failed: %v", err),
			Retryable:  true,
		}
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		return nil
	}

	retryable := resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode >= 500

	return &domain.ShipError{
		StatusCode: resp.StatusCode,
		Message:    fmt.Sprintf("ingestion API returned status %d", resp.StatusCode),
		Retryable:  retryable,
	}
}
