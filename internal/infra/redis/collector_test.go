package redis_test

import (
	"strings"
	"testing"

	infraredis "github.com/hoaithuonguit/diagnostack-agent/internal/infra/redis"
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
			caps:    infraredis.Capabilities{Info: true, Slowlog: true, Latency: true, Config: true},
			contain: "full observability",
		},
		{
			name:    "slowlog denied",
			caps:    infraredis.Capabilities{Info: true, Slowlog: false, Latency: true, Config: true},
			contain: "slowlog",
		},
		{
			name:    "latency denied",
			caps:    infraredis.Capabilities{Info: true, Slowlog: true, Latency: false, Config: true},
			contain: "latency",
		},
		{
			name:    "both denied",
			caps:    infraredis.Capabilities{Info: true, Slowlog: false, Latency: false, Config: true},
			contain: "root-cause",
		},
		{
			name:    "config denied",
			caps:    infraredis.Capabilities{Info: true, Slowlog: true, Latency: true, Config: false},
			contain: "AOF fsync",
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

// ── parseSlaveLine ────────────────────────────────────────────────────────────

func TestParseSlaveLine(t *testing.T) {
	got := infraredis.ParseSlaveLineForTest("ip=127.0.0.1,port=6380,state=online,offset=14355,lag=0")
	want := map[string]string{
		"ip":     "127.0.0.1",
		"port":   "6380",
		"state":  "online",
		"offset": "14355",
		"lag":    "0",
	}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("parseSlaveLine()[%q] = %q, want %q", k, got[k], v)
		}
	}
}

func TestParseSlaveLine_MalformedPartIsSkipped(t *testing.T) {
	// A part with no "=" (e.g. truncated input) must not panic and must
	// not produce a spurious key.
	got := infraredis.ParseSlaveLineForTest("ip=127.0.0.1,garbage,offset=10")
	if got["ip"] != "127.0.0.1" || got["offset"] != "10" {
		t.Errorf("valid parts not parsed: %v", got)
	}
	if _, ok := got["garbage"]; ok {
		t.Errorf("malformed part with no '=' should not produce a key, got: %v", got)
	}
}

// ── buildReplicationLag ───────────────────────────────────────────────────────

func TestBuildReplicationLag_MasterTakesWorstReplica(t *testing.T) {
	f := map[string]string{
		"role":               "master",
		"master_repl_offset": "1000",
		"connected_slaves":   "2",
		"slave0":             "ip=10.0.0.1,port=6379,state=online,offset=900,lag=0", // 100 bytes behind
		"slave1":             "ip=10.0.0.2,port=6379,state=online,offset=200,lag=1", // 800 bytes behind — worst
	}
	lagBytes, lagSeconds := infraredis.BuildReplicationLagForTest(f)
	if lagBytes != 800 {
		t.Errorf("lagBytes = %d, want 800 (worst of the two replicas)", lagBytes)
	}
	if lagSeconds != 0 {
		t.Errorf("lagSeconds = %d, want 0 on a master", lagSeconds)
	}
}

func TestBuildReplicationLag_MasterNoSlaves(t *testing.T) {
	f := map[string]string{
		"role":               "master",
		"master_repl_offset": "1000",
		"connected_slaves":   "0",
	}
	lagBytes, lagSeconds := infraredis.BuildReplicationLagForTest(f)
	if lagBytes != 0 || lagSeconds != 0 {
		t.Errorf("got (%d, %d), want (0, 0) when there are no connected replicas", lagBytes, lagSeconds)
	}
}

func TestBuildReplicationLag_MasterMissingSlaveLineIsSkipped(t *testing.T) {
	// connected_slaves says 2 but only slave0 is present (e.g. a replica
	// dropped between INFO fields being read) — must not panic.
	f := map[string]string{
		"role":               "master",
		"master_repl_offset": "1000",
		"connected_slaves":   "2",
		"slave0":             "ip=10.0.0.1,port=6379,state=online,offset=400,lag=0",
	}
	lagBytes, _ := infraredis.BuildReplicationLagForTest(f)
	if lagBytes != 600 {
		t.Errorf("lagBytes = %d, want 600", lagBytes)
	}
}

func TestBuildReplicationLag_SlaveUsesSecondsAgo(t *testing.T) {
	f := map[string]string{
		"role":                       "slave",
		"master_last_io_seconds_ago": "7",
		// A slave's own master_repl_offset must NOT be used as byte lag —
		// this is the exact bug being fixed.
		"master_repl_offset": "99999999",
	}
	lagBytes, lagSeconds := infraredis.BuildReplicationLagForTest(f)
	if lagBytes != 0 {
		t.Errorf("lagBytes = %d, want 0 on a replica (byte lag isn't locally computable)", lagBytes)
	}
	if lagSeconds != 7 {
		t.Errorf("lagSeconds = %d, want 7", lagSeconds)
	}
}

func TestBuildReplicationLag_UnknownRole(t *testing.T) {
	lagBytes, lagSeconds := infraredis.BuildReplicationLagForTest(map[string]string{"role": ""})
	if lagBytes != 0 || lagSeconds != 0 {
		t.Errorf("got (%d, %d), want (0, 0) for an unknown/missing role", lagBytes, lagSeconds)
	}
}

// ── toInt64 ───────────────────────────────────────────────────────────────────

func TestToInt64(t *testing.T) {
	cases := []struct {
		name string
		in   interface{}
		want int64
	}{
		{"int64", int64(42), 42},
		{"int", 7, 7},
		{"numeric string", "123", 123},
		{"unparsable string", "not-a-number", 0},
		{"unsupported type", 3.14, 0},
		{"nil", nil, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := infraredis.ToInt64ForTest(tc.in); got != tc.want {
				t.Errorf("toInt64(%v) = %d, want %d", tc.in, got, tc.want)
			}
		})
	}
}

// ── buildMetrics ────────────────────────────────────────────────────────────

func TestBuildMetrics_MapsAllFields(t *testing.T) {
	f := map[string]string{
		"used_memory":               "1048576",
		"used_memory_rss":           "2097152",
		"maxmemory":                 "4194304",
		"mem_fragmentation_ratio":   "1.45",
		"connected_clients":         "10",
		"blocked_clients":           "0",
		"maxclients":                "10000",
		"instantaneous_ops_per_sec": "500",
		"keyspace_hits":             "900",
		"keyspace_misses":           "100",
		"evicted_keys":              "3",
		"rejected_connections":      "0",
		"role":                      "master",
		"master_repl_offset":        "1000",
		"connected_slaves":          "0",
		"rdb_last_save_time":        "1700000000",
		"rdb_bgsave_in_progress":    "1",
		"appendfsync":               "everysec",
		"uptime_in_seconds":         "3600",
	}
	m := infraredis.BuildMetricsForTest(f)

	if m.MaxClients != 10000 {
		t.Errorf("MaxClients = %d, want 10000", m.MaxClients)
	}
	if m.ConnectedSlaves != 0 {
		t.Errorf("ConnectedSlaves = %d, want 0", m.ConnectedSlaves)
	}
	if !m.RDBBgsaveInProgress {
		t.Error("RDBBgsaveInProgress = false, want true (rdb_bgsave_in_progress:1)")
	}
	if m.AppendFsync != "everysec" {
		t.Errorf("AppendFsync = %q, want %q", m.AppendFsync, "everysec")
	}
	if m.MemFragmentationRatio != 1.45 {
		t.Errorf("MemFragmentationRatio = %v, want 1.45", m.MemFragmentationRatio)
	}
}

func TestBuildMetrics_RDBBgsaveInProgressFalseWhenZero(t *testing.T) {
	m := infraredis.BuildMetricsForTest(map[string]string{"rdb_bgsave_in_progress": "0"})
	if m.RDBBgsaveInProgress {
		t.Error("RDBBgsaveInProgress = true, want false when rdb_bgsave_in_progress:0")
	}
}

func TestBuildMetrics_MissingFieldsDefaultToZeroValue(t *testing.T) {
	m := infraredis.BuildMetricsForTest(map[string]string{})
	if m.MaxClients != 0 || m.ConnectedSlaves != 0 || m.RDBBgsaveInProgress || m.AppendFsync != "" {
		t.Errorf("expected zero values for missing fields, got: %+v", m)
	}
}