package app_test

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"testing"
	"time"

	"github.com/hoaithuonguit/diagnostack-agent/internal/app"
	"github.com/hoaithuonguit/diagnostack-agent/internal/domain"
)

// ── mock collector ────────────────────────────────────────────────────────────

type mockCollector struct {
	snapshot *domain.Snapshot
	err      error
	calls    int
}

func (m *mockCollector) Collect(_ context.Context) (*domain.Snapshot, error) {
	m.calls++
	return m.snapshot, m.err
}

// ── mock enqueuer ─────────────────────────────────────────────────────────────

type mockEnqueuer struct {
	items []*domain.Snapshot
}

func (m *mockEnqueuer) Enqueue(s *domain.Snapshot) {
	m.items = append(m.items, s)
}

// ── helpers ───────────────────────────────────────────────────────────────────

func silentLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError + 1}))
}

func defaultConfig() app.AgentConfig {
	return app.AgentConfig{
		AgentID:         "test-agent",
		ServerLabel:     "test-server",
		CollectInterval: 15 * time.Second,
		CollectTimeout:  10 * time.Second,
		BufferCapacity:  200,
		Retry:           app.RetryConfig{MaxAttempts: 3, BaseDelay: time.Millisecond, MaxDelay: 5 * time.Millisecond},
	}
}

func goodSnapshot() *domain.Snapshot {
	return &domain.Snapshot{
		AgentID:      "test-agent",
		ServerLabel:  "test-server",
		CollectedAt:  time.Now(),
		RedisVersion: "7.2.1",
	}
}

// ── tests ─────────────────────────────────────────────────────────────────────

func TestCollectUseCase_SuccessEnqueuesSnapshot(t *testing.T) {
	collector := &mockCollector{snapshot: goodSnapshot()}
	buf := &mockEnqueuer{}

	uc := app.NewCollectUseCase(collector, buf, defaultConfig(), silentLogger())
	err := uc.Execute(context.Background())

	if err != nil {
		t.Fatalf("want nil error on success, got: %v", err)
	}
	if len(buf.items) != 1 {
		t.Fatalf("want 1 snapshot in buffer, got %d", len(buf.items))
	}
	if buf.items[0].AgentID != "test-agent" {
		t.Errorf("want agent_id=test-agent, got %s", buf.items[0].AgentID)
	}
}

func TestCollectUseCase_CollectorErrorReturnsError(t *testing.T) {
	collector := &mockCollector{err: errors.New("connection refused")}
	buf := &mockEnqueuer{}

	uc := app.NewCollectUseCase(collector, buf, defaultConfig(), silentLogger())
	err := uc.Execute(context.Background())

	if err == nil {
		t.Fatal("want error when collector fails, got nil")
	}
	if len(buf.items) != 0 {
		t.Errorf("want 0 items in buffer on error, got %d", len(buf.items))
	}
}

func TestCollectUseCase_ErrorIncrementsHealthCounter(t *testing.T) {
	collector := &mockCollector{err: errors.New("timeout")}
	buf := &mockEnqueuer{}

	uc := app.NewCollectUseCase(collector, buf, defaultConfig(), silentLogger())

	for i := 0; i < 3; i++ {
		_ = uc.Execute(context.Background())
	}

	if got := uc.Health().ConsecutiveFailures(); got != 3 {
		t.Errorf("want 3 consecutive failures, got %d", got)
	}
}

func TestCollectUseCase_SuccessResetsHealthCounter(t *testing.T) {
	collector := &mockCollector{}
	buf := &mockEnqueuer{}
	uc := app.NewCollectUseCase(collector, buf, defaultConfig(), silentLogger())

	// Fail twice
	collector.err = errors.New("timeout")
	_ = uc.Execute(context.Background())
	_ = uc.Execute(context.Background())

	if uc.Health().ConsecutiveFailures() != 2 {
		t.Fatalf("want 2 failures before recovery")
	}

	// Recover
	collector.err = nil
	collector.snapshot = goodSnapshot()
	_ = uc.Execute(context.Background())

	if got := uc.Health().ConsecutiveFailures(); got != 0 {
		t.Errorf("want 0 after recovery, got %d", got)
	}
}

func TestCollectUseCase_ContextCancellationPropagates(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // cancel before Execute

	// collector should receive the cancelled context and return an error
	collector := &mockCollector{err: context.Canceled}
	buf := &mockEnqueuer{}

	uc := app.NewCollectUseCase(collector, buf, defaultConfig(), silentLogger())
	err := uc.Execute(ctx)

	if err == nil {
		t.Fatal("want error when context is cancelled")
	}
}

func TestCollectUseCase_MultipleSuccessiveCalls(t *testing.T) {
	collector := &mockCollector{snapshot: goodSnapshot()}
	buf := &mockEnqueuer{}

	uc := app.NewCollectUseCase(collector, buf, defaultConfig(), silentLogger())

	for i := 0; i < 5; i++ {
		if err := uc.Execute(context.Background()); err != nil {
			t.Fatalf("call %d: unexpected error: %v", i, err)
		}
	}

	if len(buf.items) != 5 {
		t.Errorf("want 5 snapshots in buffer, got %d", len(buf.items))
	}
	if collector.calls != 5 {
		t.Errorf("want 5 collector calls, got %d", collector.calls)
	}
}

func TestCollectUseCase_HealthAccessorNotNil(t *testing.T) {
	collector := &mockCollector{snapshot: goodSnapshot()}
	buf := &mockEnqueuer{}

	uc := app.NewCollectUseCase(collector, buf, defaultConfig(), silentLogger())
	if uc.Health() == nil {
		t.Error("Health() must not be nil")
	}
}
