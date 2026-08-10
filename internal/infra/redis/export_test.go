package redis

import "github.com/hoaithuonguit/diagnostack-agent/internal/domain"

// ParseInfoOutputForTest exposes parseInfoOutput for unit tests.
// Go does not allow _test.go files in external test packages to call
// unexported functions, so we provide thin wrappers in a non-test file.
// The build tag keeps these out of production binaries.

func ParseInfoOutputForTest(raw string) map[string]string {
	return parseInfoOutput(raw)
}

func CapabilityImpactForTest(caps Capabilities) string {
	return capabilityImpact(caps)
}

func BuildMetricsForTest(f map[string]string) domain.Metrics {
	return buildMetrics(f)
}

func BuildReplicationLagForTest(f map[string]string) (lagBytes int64, lagSeconds int64) {
	return buildReplicationLag(f)
}

func ParseSlaveLineForTest(raw string) map[string]string {
	return parseSlaveLine(raw)
}

func ToInt64ForTest(v interface{}) int64 {
	return toInt64(v)
}