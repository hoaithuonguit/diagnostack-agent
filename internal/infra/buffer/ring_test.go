package buffer_test

import (
	"log/slog"
	"os"
	"testing"

	"github.com/hoaithuonguit/diagnostack-agent/internal/domain"
	"github.com/hoaithuonguit/diagnostack-agent/internal/infra/buffer"
)

var noopLogger = slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))

func snapshot(id string) *domain.Snapshot {
	return &domain.Snapshot{AgentID: id}
}

func TestRing_EnqueueDequeue(t *testing.T) {
	r := buffer.New(3, noopLogger)

	r.Enqueue(snapshot("a"))
	r.Enqueue(snapshot("b"))
	r.Enqueue(snapshot("c"))

	if got := r.Len(); got != 3 {
		t.Fatalf("want Len=3, got %d", got)
	}

	s, ok := r.Dequeue()
	if !ok || s.AgentID != "a" {
		t.Fatalf("want first=a, got %v (ok=%v)", s, ok)
	}
	s, ok = r.Dequeue()
	if !ok || s.AgentID != "b" {
		t.Fatalf("want second=b, got %v", s)
	}
	s, ok = r.Dequeue()
	if !ok || s.AgentID != "c" {
		t.Fatalf("want third=c, got %v", s)
	}

	_, ok = r.Dequeue()
	if ok {
		t.Fatal("want empty dequeue to return false")
	}
}

func TestRing_DropOldestOnOverflow(t *testing.T) {
	r := buffer.New(2, noopLogger)

	r.Enqueue(snapshot("a"))
	r.Enqueue(snapshot("b"))
	r.Enqueue(snapshot("c")) // should evict "a"

	if got := r.Len(); got != 2 {
		t.Fatalf("want Len=2 after overflow, got %d", got)
	}
	if got := r.DroppedTotal(); got != 1 {
		t.Fatalf("want DroppedTotal=1, got %d", got)
	}

	first, _ := r.Dequeue()
	if first.AgentID != "b" {
		t.Fatalf("want oldest surviving = b, got %s", first.AgentID)
	}
}

func TestRing_EmptyDequeue(t *testing.T) {
	r := buffer.New(10, noopLogger)
	_, ok := r.Dequeue()
	if ok {
		t.Fatal("empty ring should return (nil, false)")
	}
}
