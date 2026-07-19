package app_test

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"testing"
	"time"

	"github.com/keywatch/agent/internal/app"
	"github.com/keywatch/agent/internal/domain"
)

var testLogger = slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))

// ── Mock shipper ─────────────────────────────────────────────────────────────

type mockShipper struct {
	calls     int
	responses []error
}

func (m *mockShipper) Ship(_ context.Context, _ *domain.Snapshot) error {
	idx := m.calls
	m.calls++
	if idx >= len(m.responses) {
		return nil
	}
	return m.responses[idx]
}

// ── Mock buffer ──────────────────────────────────────────────────────────────

type mockBuffer struct {
	items []*domain.Snapshot
	pos   int
}

func (b *mockBuffer) Dequeue() (*domain.Snapshot, bool) {
	if b.pos >= len(b.items) {
		return nil, false
	}
	s := b.items[b.pos]
	b.pos++
	return s, true
}

func fastRetryConfig() app.RetryConfig {
	return app.RetryConfig{
		MaxAttempts: 3,
		BaseDelay:   1 * time.Millisecond,
		MaxDelay:    5 * time.Millisecond,
	}
}

// ── Tests ────────────────────────────────────────────────────────────────────

func TestShipUseCase_SuccessOnFirstAttempt(t *testing.T) {
	shipper := &mockShipper{responses: []error{nil}}
	buf := &mockBuffer{items: []*domain.Snapshot{{AgentID: "a"}}}

	uc := app.NewShipUseCase(shipper, buf, fastRetryConfig(), testLogger)
	uc.DrainOnce(context.Background())

	if shipper.calls != 1 {
		t.Fatalf("want 1 call, got %d", shipper.calls)
	}
	if uc.DroppedTotal() != 0 {
		t.Fatalf("want 0 dropped, got %d", uc.DroppedTotal())
	}
}

func TestShipUseCase_RetryOnTransientError(t *testing.T) {
	transient := &domain.ShipError{StatusCode: 503, Message: "server error", Retryable: true}
	shipper := &mockShipper{responses: []error{transient, transient, nil}}
	buf := &mockBuffer{items: []*domain.Snapshot{{AgentID: "a"}}}

	uc := app.NewShipUseCase(shipper, buf, fastRetryConfig(), testLogger)
	uc.DrainOnce(context.Background())

	if shipper.calls != 3 {
		t.Fatalf("want 3 calls (2 failures + 1 success), got %d", shipper.calls)
	}
	if uc.DroppedTotal() != 0 {
		t.Fatalf("want 0 dropped, got %d", uc.DroppedTotal())
	}
}

func TestShipUseCase_DropAfterMaxRetries(t *testing.T) {
	transient := &domain.ShipError{StatusCode: 500, Message: "server error", Retryable: true}
	// Always fail — exceeds MaxAttempts=3
	shipper := &mockShipper{responses: []error{transient, transient, transient, transient}}
	buf := &mockBuffer{items: []*domain.Snapshot{{AgentID: "a"}}}

	cfg := app.RetryConfig{MaxAttempts: 3, BaseDelay: 1 * time.Millisecond, MaxDelay: 5 * time.Millisecond}
	uc := app.NewShipUseCase(shipper, buf, cfg, testLogger)
	uc.DrainOnce(context.Background())

	if shipper.calls != 3 {
		t.Fatalf("want exactly 3 attempts, got %d", shipper.calls)
	}
	if uc.DroppedTotal() != 1 {
		t.Fatalf("want 1 dropped, got %d", uc.DroppedTotal())
	}
}

func TestShipUseCase_NonRetryableDropsImmediately(t *testing.T) {
	authErr := &domain.ShipError{StatusCode: 401, Message: "unauthorized", Retryable: false}
	shipper := &mockShipper{responses: []error{authErr}}
	buf := &mockBuffer{items: []*domain.Snapshot{{AgentID: "a"}}}

	uc := app.NewShipUseCase(shipper, buf, fastRetryConfig(), testLogger)
	uc.DrainOnce(context.Background())

	if shipper.calls != 1 {
		t.Fatalf("non-retryable error should not retry, got %d calls", shipper.calls)
	}
	if uc.DroppedTotal() != 1 {
		t.Fatalf("want 1 dropped, got %d", uc.DroppedTotal())
	}
}

func TestShipUseCase_ContextCancelDuringRetry(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())

	callCount := 0
	shipper := &mockShipper{}
	shipper.responses = []error{
		&domain.ShipError{StatusCode: 503, Retryable: true},
	}
	_ = errors.New("") // satisfy import
	_ = callCount

	// We replace the shipper response inline to cancel on second call.
	cancelShipper := &cancelOnCallShipper{cancel: cancel, cancelAfter: 1}
	buf := &mockBuffer{items: []*domain.Snapshot{{AgentID: "a"}}}

	cfg := app.RetryConfig{MaxAttempts: 5, BaseDelay: 50 * time.Millisecond, MaxDelay: 500 * time.Millisecond}
	uc := app.NewShipUseCase(cancelShipper, buf, cfg, testLogger)
	uc.DrainOnce(ctx)

	if cancelShipper.calls > 2 {
		t.Fatalf("context cancel should stop retry quickly, got %d calls", cancelShipper.calls)
	}
}

type cancelOnCallShipper struct {
	cancel      context.CancelFunc
	cancelAfter int
	calls       int
}

func (s *cancelOnCallShipper) Ship(_ context.Context, _ *domain.Snapshot) error {
	s.calls++
	if s.calls >= s.cancelAfter {
		s.cancel()
	}
	return &domain.ShipError{StatusCode: 503, Message: "error", Retryable: true}
}
