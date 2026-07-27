package http_test

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	infrahttp "github.com/hoaithuonguit/diagnostack-agent/internal/infra/http"
	"github.com/hoaithuonguit/diagnostack-agent/internal/domain"
)

var noopLog = slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError + 1}))

func testSnapshot() *domain.Snapshot {
	return &domain.Snapshot{
		AgentID:      "agent-123",
		ServerLabel:  "test-redis",
		CollectedAt:  time.Now().UTC(),
		RedisVersion: "7.2.1",
	}
}

// startServer returns a test HTTP server that responds with the given status code.
func startServer(t *testing.T, status int, assertFn func(r *http.Request)) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if assertFn != nil {
			assertFn(r)
		}
		w.WriteHeader(status)
	}))
}

// ── success ───────────────────────────────────────────────────────────────────

func TestHTTPShipper_Ship_Success(t *testing.T) {
	srv := startServer(t, http.StatusAccepted, nil)
	defer srv.Close()

	shipper := infrahttp.New(infrahttp.ShipperConfig{
		Endpoint: srv.URL,
		APIKey:   "test-key",
	}, noopLog)

	err := shipper.Ship(context.Background(), testSnapshot())
	if err != nil {
		t.Fatalf("want nil error on 202, got: %v", err)
	}
}

func TestHTTPShipper_Ship_200OK(t *testing.T) {
	srv := startServer(t, http.StatusOK, nil)
	defer srv.Close()

	shipper := infrahttp.New(infrahttp.ShipperConfig{
		Endpoint: srv.URL,
		APIKey:   "key",
	}, noopLog)

	if err := shipper.Ship(context.Background(), testSnapshot()); err != nil {
		t.Fatalf("want nil error on 200, got: %v", err)
	}
}

// ── auth header ───────────────────────────────────────────────────────────────

func TestHTTPShipper_Ship_SetsAPIKeyHeader(t *testing.T) {
	var capturedKey string
	srv := startServer(t, http.StatusOK, func(r *http.Request) {
		capturedKey = r.Header.Get("X-Agent-Key")
	})
	defer srv.Close()

	shipper := infrahttp.New(infrahttp.ShipperConfig{
		Endpoint: srv.URL,
		APIKey:   "kw_live_secret",
	}, noopLog)

	_ = shipper.Ship(context.Background(), testSnapshot())

	if capturedKey != "kw_live_secret" {
		t.Errorf("want X-Agent-Key=kw_live_secret, got %q", capturedKey)
	}
}

func TestHTTPShipper_Ship_SetsContentTypeJSON(t *testing.T) {
	var capturedCT string
	srv := startServer(t, http.StatusOK, func(r *http.Request) {
		capturedCT = r.Header.Get("Content-Type")
	})
	defer srv.Close()

	shipper := infrahttp.New(infrahttp.ShipperConfig{Endpoint: srv.URL, APIKey: "k"}, noopLog)
	_ = shipper.Ship(context.Background(), testSnapshot())

	if capturedCT != "application/json" {
		t.Errorf("want Content-Type=application/json, got %q", capturedCT)
	}
}

// ── request body ─────────────────────────────────────────────────────────────

func TestHTTPShipper_Ship_BodyIsValidJSON(t *testing.T) {
	var body []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var err error
		body, err = io.ReadAll(r.Body)
		if err != nil {
			t.Errorf("read body: %v", err)
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	snapshot := testSnapshot()
	shipper := infrahttp.New(infrahttp.ShipperConfig{Endpoint: srv.URL, APIKey: "k"}, noopLog)
	_ = shipper.Ship(context.Background(), snapshot)

	var decoded domain.Snapshot
	if err := json.Unmarshal(body, &decoded); err != nil {
		t.Fatalf("body is not valid JSON: %v\nbody: %s", err, body)
	}
	if decoded.AgentID != snapshot.AgentID {
		t.Errorf("decoded agent_id=%q, want %q", decoded.AgentID, snapshot.AgentID)
	}
}

// ── non-retryable errors ──────────────────────────────────────────────────────

func TestHTTPShipper_Ship_401_NonRetryable(t *testing.T) {
	srv := startServer(t, http.StatusUnauthorized, nil)
	defer srv.Close()

	shipper := infrahttp.New(infrahttp.ShipperConfig{Endpoint: srv.URL, APIKey: "wrong"}, noopLog)
	err := shipper.Ship(context.Background(), testSnapshot())

	shipErr := asShipError(t, err)
	if shipErr.Retryable {
		t.Error("401 must be non-retryable")
	}
	if shipErr.StatusCode != http.StatusUnauthorized {
		t.Errorf("want StatusCode=401, got %d", shipErr.StatusCode)
	}
}

func TestHTTPShipper_Ship_400_NonRetryable(t *testing.T) {
	srv := startServer(t, http.StatusBadRequest, nil)
	defer srv.Close()

	shipper := infrahttp.New(infrahttp.ShipperConfig{Endpoint: srv.URL, APIKey: "k"}, noopLog)
	err := shipper.Ship(context.Background(), testSnapshot())

	shipErr := asShipError(t, err)
	if shipErr.Retryable {
		t.Error("400 must be non-retryable")
	}
}

// ── retryable errors ──────────────────────────────────────────────────────────

func TestHTTPShipper_Ship_500_Retryable(t *testing.T) {
	srv := startServer(t, http.StatusInternalServerError, nil)
	defer srv.Close()

	shipper := infrahttp.New(infrahttp.ShipperConfig{Endpoint: srv.URL, APIKey: "k"}, noopLog)
	err := shipper.Ship(context.Background(), testSnapshot())

	shipErr := asShipError(t, err)
	if !shipErr.Retryable {
		t.Error("500 must be retryable")
	}
}

func TestHTTPShipper_Ship_503_Retryable(t *testing.T) {
	srv := startServer(t, http.StatusServiceUnavailable, nil)
	defer srv.Close()

	shipper := infrahttp.New(infrahttp.ShipperConfig{Endpoint: srv.URL, APIKey: "k"}, noopLog)
	err := shipper.Ship(context.Background(), testSnapshot())

	shipErr := asShipError(t, err)
	if !shipErr.Retryable {
		t.Error("503 must be retryable")
	}
}

func TestHTTPShipper_Ship_429_Retryable(t *testing.T) {
	srv := startServer(t, http.StatusTooManyRequests, nil)
	defer srv.Close()

	shipper := infrahttp.New(infrahttp.ShipperConfig{Endpoint: srv.URL, APIKey: "k"}, noopLog)
	err := shipper.Ship(context.Background(), testSnapshot())

	shipErr := asShipError(t, err)
	if !shipErr.Retryable {
		t.Error("429 must be retryable")
	}
}

// ── network errors ────────────────────────────────────────────────────────────

func TestHTTPShipper_Ship_NetworkError_Retryable(t *testing.T) {
	// Use a port that is definitely not listening.
	shipper := infrahttp.New(infrahttp.ShipperConfig{
		Endpoint: "http://127.0.0.1:19999/ingest",
		APIKey:   "k",
		Timeout:  2 * time.Second,
	}, noopLog)

	err := shipper.Ship(context.Background(), testSnapshot())
	if err == nil {
		t.Fatal("want error for unreachable endpoint, got nil")
	}

	shipErr := asShipError(t, err)
	if !shipErr.Retryable {
		t.Error("network error must be retryable")
	}
	if shipErr.StatusCode != 0 {
		t.Errorf("want StatusCode=0 for network error, got %d", shipErr.StatusCode)
	}
}

func TestHTTPShipper_Ship_ContextCancelled(t *testing.T) {
	// Server that sleeps so the context cancel fires first.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(500 * time.Millisecond)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	shipper := infrahttp.New(infrahttp.ShipperConfig{Endpoint: srv.URL, APIKey: "k"}, noopLog)
	err := shipper.Ship(ctx, testSnapshot())

	if err == nil {
		t.Fatal("want error on context cancel")
	}
}

// ── helper ────────────────────────────────────────────────────────────────────

func asShipError(t *testing.T, err error) *domain.ShipError {
	t.Helper()
	if err == nil {
		t.Fatal("want a ShipError, got nil")
	}
	se, ok := err.(*domain.ShipError)
	if !ok {
		t.Fatalf("want *domain.ShipError, got %T: %v", err, err)
	}
	return se
}
