package domain

import "context"

// Collector is the inbound port for pulling observations from a Redis server.
// Concrete implementations live in internal/infra/redis.
//
// Implementations must:
//   - Never call MONITOR (causes severe Redis performance degradation).
//   - Stay within the agent CPU/memory budget (<1% CPU, <30MB RSS).
//   - Deduplicate slowlog entries using the entry ID so successive calls
//     do not re-send entries that were already shipped.
type Collector interface {
	Collect(ctx context.Context) (*Snapshot, error)
}
