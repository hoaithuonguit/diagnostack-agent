package redis

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
