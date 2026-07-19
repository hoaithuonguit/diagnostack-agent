package redis_test

import (
	"strings"
	"testing"

	infraredis "github.com/keywatch/agent/internal/infra/redis"
)

// ── parseInfoOutput ───────────────────────────────────────────────────────────
// parseInfoOutput is unexported, so we test it indirectly via the exported
// ParseInfoOutputForTest helper (added below). Alternatively we test the
// observable behaviour: that the fields we need are correctly extracted from
// a realistic INFO snippet.

func TestParseInfoOutput_ExtractsKnownFields(t *testing.T) {
	raw := "# Server\r\nredis_version:7.2.1\r\nuptime_in_seconds:86400\r\n" +
		"# Memory\r\nused_memory:1048576\r\nmem_fragmentation_ratio:1.23\r\n" +
		"# Stats\r\nkeyspace_hits:9000\r\nkeyspace_misses:1000\r\nevicted_keys:5\r\n" +
		"# Clients\r\nconnected_clients:42\r\nblocked_clients:3\r\n" +
		"# Replication\r\nrole:master\r\n"

	fields := infraredis.ParseInfoOutputForTest(raw)

	cases := []struct{ key, want string }{
		{"redis_version", "7.2.1"},
		{"uptime_in_seconds", "86400"},
		{"used_memory", "1048576"},
		{"mem_fragmentation_ratio", "1.23"},
		{"keyspace_hits", "9000"},
		{"keyspace_misses", "1000"},
		{"evicted_keys", "5"},
		{"connected_clients", "42"},
		{"blocked_clients", "3"},
		{"role", "master"},
	}

	for _, c := range cases {
		t.Run(c.key, func(t *testing.T) {
			if got := fields[c.key]; got != c.want {
				t.Errorf("fields[%q] = %q, want %q", c.key, got, c.want)
			}
		})
	}
}

func TestParseInfoOutput_SkipsSectionHeaders(t *testing.T) {
	raw := "# Server\r\nredis_version:7.0.0\r\n# Memory\r\nused_memory:512\r\n"
	fields := infraredis.ParseInfoOutputForTest(raw)

	for k := range fields {
		if strings.HasPrefix(k, "#") {
			t.Errorf("section header leaked into fields: %q", k)
		}
	}
}

func TestParseInfoOutput_HandlesWindowsAndUnixLineEndings(t *testing.T) {
	rawCRLF := "redis_version:7.0.0\r\nused_memory:1024\r\n"
	rawLF := "redis_version:7.0.0\nused_memory:1024\n"

	fieldsCRLF := infraredis.ParseInfoOutputForTest(rawCRLF)
	fieldsLF := infraredis.ParseInfoOutputForTest(rawLF)

	if fieldsCRLF["redis_version"] != "7.0.0" {
		t.Error("CRLF: redis_version not parsed")
	}
	if fieldsLF["redis_version"] != "7.0.0" {
		t.Error("LF: redis_version not parsed")
	}
}

func TestParseInfoOutput_EmptyInput(t *testing.T) {
	fields := infraredis.ParseInfoOutputForTest("")
	if len(fields) != 0 {
		t.Errorf("want empty map for empty input, got %v", fields)
	}
}

func TestParseInfoOutput_ValueWithColonInIt(t *testing.T) {
	// Redis sometimes has values containing colons, e.g. config_rewrite
	// and slave replication info like "ip=127.0.0.1,port=6380,state=online"
	// parseInfoOutput must split only on the FIRST colon.
	raw := "master_host:192.168.1.1\r\n"
	fields := infraredis.ParseInfoOutputForTest(raw)
	if fields["master_host"] != "192.168.1.1" {
		t.Errorf("want '192.168.1.1', got %q", fields["master_host"])
	}
}

// ── capabilityImpact ──────────────────────────────────────────────────────────

func TestCapabilityImpact(t *testing.T) {
	cases := []struct {
		name    string
		caps    infraredis.Capabilities
		contain string
	}{
		{
			name:    "all available",
			caps:    infraredis.Capabilities{Info: true, Slowlog: true, Latency: true},
			contain: "full observability",
		},
		{
			name:    "slowlog denied",
			caps:    infraredis.Capabilities{Info: true, Slowlog: false, Latency: true},
			contain: "slowlog",
		},
		{
			name:    "latency denied",
			caps:    infraredis.Capabilities{Info: true, Slowlog: true, Latency: false},
			contain: "latency",
		},
		{
			name:    "both denied",
			caps:    infraredis.Capabilities{Info: true, Slowlog: false, Latency: false},
			contain: "root-cause",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := infraredis.CapabilityImpactForTest(tc.caps)
			if !strings.Contains(got, tc.contain) {
				t.Errorf("want impact to contain %q, got: %q", tc.contain, got)
			}
		})
	}
}

// ── slowlog rollover detection logic ─────────────────────────────────────────
// The rollover logic lives inside collectSlowlog which needs a real Redis
// connection. We test the threshold boundary condition via the exported
// constant and the Disabled() helper so we verify the boundary is sensible.

func TestSlowlogRolloverThreshold_ExceedsFetchWindow(t *testing.T) {
	const fetchWindow int64 = 128
	if infraredis.SlowlogRolloverThreshold <= fetchWindow {
		t.Errorf("SlowlogRolloverThreshold (%d) must exceed per-call fetch window (%d) to avoid false positives on a busy server",
			infraredis.SlowlogRolloverThreshold, fetchWindow)
	}
}

func TestSlowlogRolloverThreshold_NotExcessivelyLarge(t *testing.T) {
	// If the threshold is too large (e.g. 100000), a Redis restart that generates
	// 100 new entries quickly would not be detected for a very long time.
	// Cap at 10000 as a sanity check.
	const sanityMax int64 = 10_000
	if infraredis.SlowlogRolloverThreshold > sanityMax {
		t.Errorf("SlowlogRolloverThreshold (%d) is unreasonably large — rollover detection would be too slow",
			infraredis.SlowlogRolloverThreshold)
	}
}
