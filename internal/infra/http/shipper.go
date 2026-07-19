package domain

import "context"

// Shipper is the outbound port for delivering a Snapshot to the Keywatch backend.
// Concrete implementations live in internal/infra/http.
//
// Ship must be idempotent — the caller (ShipUseCase) may retry the same
// Snapshot multiple times on transient failures. Implementations should
// attach authentication credentials but must NOT implement retry logic;
// that responsibility belongs to the application layer.
type Shipper interface {
	Ship(ctx context.Context, snapshot *Snapshot) error
}

// ShipError carries enough context for the application layer to decide
// whether a failure is retryable.
type ShipError struct {
	// StatusCode is the HTTP status returned by the API, or 0 for network errors.
	StatusCode int
	// Message is a human-readable description of the failure.
	Message string
	// Retryable indicates whether the caller should attempt again.
	Retryable bool
}

func (e *ShipError) Error() string {
	return e.Message
}
