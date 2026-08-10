package redis_test

import (
	"errors"
	"testing"

	infraredis "github.com/hoaithuonguit/diagnostack-agent/internal/infra/redis"
)

// ── isACLDenied ──────────────────────────────────────────────────────────────

func TestIsACLDenied_NOPERM(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want bool
	}{
		{
			name: "standard NOPERM for command",
			err:  errors.New("NOPERM this user has no permissions to run the 'info' command"),
			want: true,
		},
		{
			name: "NOPERM for key pattern",
			err:  errors.New("NOPERM this user has no permissions to access one of the keys"),
			want: true,
		},
		{
			name: "NOPERM for channel",
			err:  errors.New("NOPERM this user has no permissions to access one of the channels used"),
			want: true,
		},
		{
			name: "WRONGPASS is not NOPERM",
			err:  errors.New("WRONGPASS invalid username-password pair"),
			want: false,
		},
		{
			name: "network error is not NOPERM",
			err:  errors.New("dial tcp: connection refused"),
			want: false,
		},
		{
			name: "timeout is not NOPERM",
			err:  errors.New("context deadline exceeded"),
			want: false,
		},
		{
			name: "nil error",
			err:  nil,
			want: false,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := infraredis.IsACLDenied(tc.err)
			if got != tc.want {
				t.Errorf("IsACLDenied(%v) = %v, want %v", tc.err, got, tc.want)
			}
		})
	}
}

// ── Capabilities.disabled() ──────────────────────────────────────────────────

func TestCapabilities_Disabled(t *testing.T) {
	cases := []struct {
		name string
		caps infraredis.Capabilities
		want []string
	}{
		{
			name: "all available",
			caps: infraredis.Capabilities{Info: true, Slowlog: true, Latency: true, Config: true},
			want: nil,
		},
		{
			name: "slowlog denied",
			caps: infraredis.Capabilities{Info: true, Slowlog: false, Latency: true, Config: true},
			want: []string{"SLOWLOG"},
		},
		{
			name: "latency denied",
			caps: infraredis.Capabilities{Info: true, Slowlog: true, Latency: false, Config: true},
			want: []string{"LATENCY"},
		},
		{
			name: "config denied",
			caps: infraredis.Capabilities{Info: true, Slowlog: true, Latency: true, Config: false},
			want: []string{"CONFIG"},
		},
		{
			name: "slowlog and latency denied",
			caps: infraredis.Capabilities{Info: true, Slowlog: false, Latency: false, Config: true},
			want: []string{"SLOWLOG", "LATENCY"},
		},
		{
			name: "all three denied",
			caps: infraredis.Capabilities{Info: true, Slowlog: false, Latency: false, Config: false},
			want: []string{"SLOWLOG", "LATENCY", "CONFIG"},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := tc.caps.Disabled()
			if len(got) != len(tc.want) {
				t.Fatalf("want disabled=%v, got %v", tc.want, got)
			}
			for i := range tc.want {
				if got[i] != tc.want[i] {
					t.Errorf("disabled[%d]: want %s, got %s", i, tc.want[i], got[i])
				}
			}
		})
	}
}

// ── ACLError ─────────────────────────────────────────────────────────────────

func TestACLError_Message(t *testing.T) {
	err := &infraredis.ACLError{
		Command: "INFO",
		Hint:    "grant: ACL SETUSER diagnostack +info on",
	}
	msg := err.Error()
	if msg == "" {
		t.Fatal("ACLError.Error() must not be empty")
	}
	// Must mention the command name so the operator knows what to fix.
	if !contains(msg, "INFO") {
		t.Errorf("want 'INFO' in error message, got: %s", msg)
	}
}

// ── SlowlogRolloverThreshold sanity ──────────────────────────────────────────

func TestSlowlogRolloverThreshold(t *testing.T) {
	// Threshold must exceed our per-call fetch window (128) so a busy server
	// doesn't trigger a false rollover, but still detect a restart quickly.
	const fetchWindow = 128
	if infraredis.SlowlogRolloverThreshold <= fetchWindow {
		t.Errorf("SlowlogRolloverThreshold (%d) must be > fetch window (%d)",
			infraredis.SlowlogRolloverThreshold, fetchWindow)
	}
}

func contains(s, sub string) bool {
	return len(s) >= len(sub) && (s == sub || len(sub) == 0 ||
		func() bool {
			for i := 0; i <= len(s)-len(sub); i++ {
				if s[i:i+len(sub)] == sub {
					return true
				}
			}
			return false
		}())
}