package app_test

import (
	"bytes"
	"errors"
	"log/slog"
	"strings"
	"testing"

	"github.com/keywatch/agent/internal/app"
)

// captureLogger returns a logger that writes to a buffer and the buffer.
func captureLogger() (*slog.Logger, *bytes.Buffer) {
	buf := &bytes.Buffer{}
	logger := slog.New(slog.NewTextHandler(buf, &slog.HandlerOptions{Level: slog.LevelDebug}))
	return logger, buf
}

func TestCollectHealth_FirstFailureIsWarn(t *testing.T) {
	logger, buf := captureLogger()
	h := app.NewCollectHealth(logger)

	h.RecordError(errors.New("connection refused"))

	output := buf.String()
	if !strings.Contains(output, "WARN") {
		t.Errorf("want WARN on first failure, got: %s", output)
	}
	if strings.Contains(output, "ERROR") {
		t.Errorf("want no ERROR on first failure, got: %s", output)
	}
}

func TestCollectHealth_EscalatesAfterThreshold(t *testing.T) {
	logger, buf := captureLogger()
	cfg := app.CollectHealthConfig{WarnThreshold: 1, ErrorThreshold: 3}
	h := app.NewCollectHealthWithConfig(cfg, logger)

	err := errors.New("connection refused")
	h.RecordError(err)
	h.RecordError(err)
	h.RecordError(err) // third failure → ERROR

	output := buf.String()
	if !strings.Contains(output, "ERROR") {
		t.Errorf("want ERROR after %d failures, got: %s", cfg.ErrorThreshold, output)
	}
	if !strings.Contains(output, "ACTION NEEDED") {
		t.Errorf("want ACTION NEEDED hint, got: %s", output)
	}
}

func TestCollectHealth_ActionHintOnlyOnce(t *testing.T) {
	logger, buf := captureLogger()
	cfg := app.CollectHealthConfig{WarnThreshold: 1, ErrorThreshold: 2}
	h := app.NewCollectHealthWithConfig(cfg, logger)

	err := errors.New("connection refused")
	for i := 0; i < 5; i++ {
		h.RecordError(err)
	}

	count := strings.Count(buf.String(), "ACTION NEEDED")
	if count != 1 {
		t.Errorf("want ACTION NEEDED exactly once, got %d occurrences", count)
	}
}

func TestCollectHealth_RecoveryLogsInfo(t *testing.T) {
	logger, buf := captureLogger()
	cfg := app.CollectHealthConfig{WarnThreshold: 1, ErrorThreshold: 2}
	h := app.NewCollectHealthWithConfig(cfg, logger)

	err := errors.New("WRONGPASS")
	h.RecordError(err)
	h.RecordError(err) // escalated

	buf.Reset() // clear; we only care about what happens on recovery
	h.RecordSuccess()

	output := buf.String()
	if !strings.Contains(output, "INFO") {
		t.Errorf("want INFO on recovery, got: %s", output)
	}
	if !strings.Contains(output, "recovered") {
		t.Errorf("want 'recovered' in message, got: %s", output)
	}
}

func TestCollectHealth_RecoveryResetsStreak(t *testing.T) {
	logger, _ := captureLogger()
	cfg := app.CollectHealthConfig{WarnThreshold: 1, ErrorThreshold: 3}
	h := app.NewCollectHealthWithConfig(cfg, logger)

	err := errors.New("timeout")
	h.RecordError(err)
	h.RecordError(err)

	if h.ConsecutiveFailures() != 2 {
		t.Fatalf("want 2 consecutive failures, got %d", h.ConsecutiveFailures())
	}

	h.RecordSuccess()

	if h.ConsecutiveFailures() != 0 {
		t.Errorf("want 0 after recovery, got %d", h.ConsecutiveFailures())
	}
}

func TestCollectHealth_NoRecoveryLogWhenNotEscalated(t *testing.T) {
	logger, buf := captureLogger()
	h := app.NewCollectHealth(logger)

	// One failure (WARN only) then immediate recovery.
	h.RecordError(errors.New("timeout"))
	buf.Reset()
	h.RecordSuccess() // should be silent — was never escalated to ERROR

	if buf.Len() > 0 {
		t.Errorf("want no log on non-escalated recovery, got: %s", buf.String())
	}
}

func TestCollectHealth_AuthErrorHint(t *testing.T) {
	logger, buf := captureLogger()
	cfg := app.CollectHealthConfig{WarnThreshold: 1, ErrorThreshold: 2}
	h := app.NewCollectHealthWithConfig(cfg, logger)

	authErr := errors.New("WRONGPASS invalid username-password pair or user is disabled")
	h.RecordError(authErr)
	h.RecordError(authErr) // triggers escalation + hint

	output := buf.String()
	if !strings.Contains(output, "authentication") {
		t.Errorf("want auth-specific hint, got: %s", output)
	}
}

func TestCollectHealth_TimeoutHint(t *testing.T) {
	logger, buf := captureLogger()
	cfg := app.CollectHealthConfig{WarnThreshold: 1, ErrorThreshold: 2}
	h := app.NewCollectHealthWithConfig(cfg, logger)

	timeoutErr := errors.New("context deadline exceeded")
	h.RecordError(timeoutErr)
	h.RecordError(timeoutErr)

	output := buf.String()
	if !strings.Contains(output, "timed out") {
		t.Errorf("want timeout-specific hint, got: %s", output)
	}
}
