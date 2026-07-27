# diagnostack-agent

The open-source observability agent for [diagnostack](https://diagnostack.io) — a Redis-first SaaS that goes beyond dashboards to tell you **what broke, why, and when**.

The agent runs on your Redis server, collects metrics and slowlog entries every 15 seconds, and ships them to the diagnostack Ingestion API. It is intentionally minimal: a single static binary, under 1% CPU, under 30 MB RSS, no runtime dependencies.

---

## How it works

```
Redis server                    diagnostack cloud
─────────────────────────────   ──────────────────────────────────────
INFO / LATENCY LATEST           ring buffer → HTTPS POST
SLOWLOG GET          ──────►    /v1/ingest   ──────►   dashboard + debugger
(every 15 seconds)              (with retry)
```

On every tick the agent issues three read-only Redis commands:

| Command | What it collects |
|---|---|
| `INFO all` | Memory, clients, throughput, evictions, replication lag, persistence |
| `LATENCY LATEST` | Tail latency per event type (command, fast-command, aof, etc.) |
| `SLOWLOG GET` | Commands that exceeded the slowlog threshold, with client IP and duration |

The agent **never calls `MONITOR`** — the command that logs every Redis operation and can degrade performance by 50%+ on busy servers.

---

## Architecture

The agent follows Clean Architecture. Dependencies only flow inward — the domain layer has zero external imports.

```
cmd/agent/main.go              ← bootstrap: wire + start scheduler
internal/
  domain/                      ← pure models + port interfaces (no external deps)
    metric.go                  ← Snapshot, Metrics, SlowlogEntry, LatencySample
    collector.go               ← Collector interface
    shipper.go                 ← Shipper interface + ShipError
  app/                         ← orchestration only; depends on domain interfaces
    collect_usecase.go         ← poll Redis → enqueue to ring buffer
    ship_usecase.go            ← dequeue → call Shipper → retry on error
    collect_health.go          ← consecutive failure tracker with escalating logs
    config.go                  ← AgentConfig, RetryConfig
  infra/
    redis/
      collector.go             ← RedisCollector: INFO + LATENCY + SLOWLOG
      capabilities.go          ← ACL capability probe (runs once at startup)
    http/
      shipper.go               ← HTTPShipper: POST with X-Agent-Key auth header
    buffer/
      ring.go                  ← fixed-capacity ring buffer, drop-oldest policy
config/
  loader.go                    ← /etc/diagnostack/config.yaml + env overrides
  yaml.go                      ← stdlib-only YAML parser (no external deps)
  uuid.go                      ← crypto/rand UUID generation
scripts/
  install.sh                   ← systemd service + diagnostack system user
config.yaml                    ← default configuration file
```

### Key design decisions

**Single external dependency.** Only `github.com/redis/go-redis/v9` is used. The YAML parser, UUID generation, and all business logic are implemented with the Go standard library. A small dependency surface matters when customers are allowing an agent to run on their production Redis servers.

**ACL capability probe at startup.** On launch the agent probes `INFO`, `SLOWLOG`, and `LATENCY` against the configured Redis ACL. Denied commands are silently skipped for the lifetime of the process — no error noise every 15 seconds — and reported in each Snapshot's `disabled_capabilities` field so the backend can show an informative UI message.

**Ring buffer absorbs outages.** Up to 200 Snapshots (configurable) are held in memory during API outages. At 15-second intervals that covers ~50 minutes of outage with no data loss. When full, oldest entries are dropped to protect the memory budget.

**Per-collect deadline.** Every `Collect()` call runs under its own `context.WithTimeout` (default 10s). A slow or overloaded Redis server cannot block the scheduler — the next tick always fires on time.

**Slowlog rollover detection.** Redis resets its slowlog entry IDs to 0 on every restart. The agent detects this by comparing the new batch's max ID to the stored watermark. On a suspected rollover, the watermark resets and all current entries are re-shipped so no slow commands are silently missed.

**Escalating error logs.** The `CollectHealth` tracker counts consecutive collect failures. After 1–2 failures it logs `WARN`. After 3+ it escalates to `ERROR` and emits a single actionable hint (with the exact command to fix the problem) — then stays silent until recovery. On recovery it logs `INFO` with the total outage duration.

---

## Requirements

- Linux (amd64 or arm64)
- Redis 4.0 or later
- systemd (for the managed service installation)
- Go 1.22 or later (to build from source)

---

## Building

```bash
git clone https://github.com/hoaithuonguit/diagnostack-agent.git
cd diagnostack-agent

make build
# → bin/diagnostack-agent
```

Cross-compile for a remote server:

```bash
GOOS=linux GOARCH=amd64 go build -trimpath -ldflags="-s -w" -o bin/diagnostack-agent ./cmd/agent
```

---

## Installation

Copy the binary to your Redis server, then run the install script as root:

```bash
scp bin/diagnostack-agent user@redis-server:~
scp config.yaml user@redis-server:~
ssh user@redis-server

sudo bash scripts/install.sh
```

The script:
1. Creates the `diagnostack` system user (no login shell)
2. Creates `/etc/diagnostack/` with `640` permissions (root:diagnostack)
3. Installs the binary to `/usr/local/bin/diagnostack-agent`
4. Installs and enables a systemd service with memory and CPU quotas

After installation, edit the config and start the service:

```bash
sudo nano /etc/diagnostack/config.yaml   # set api_key and server_label
sudo systemctl start diagnostack-agent
sudo journalctl -u diagnostack-agent -f  # verify it started cleanly
```

---

## Configuration

The config file lives at `/etc/diagnostack/config.yaml`. All values can be overridden with environment variables — useful for containers and secrets managers.

```yaml
# Human-readable name shown in the diagnostack UI.
# Defaults to the system hostname.
server_label: "prod-redis-01"

redis:
  addr: "127.0.0.1:6379"
  password: ""
  db: 0

api:
  endpoint: "https://ingest.diagnostack.io/v1/ingest"
  # Prefer DIAGNOSTACK_API_KEY env var over storing the key in this file.
  api_key: ""
  timeout: 10s

collect_interval: 15s   # how often to poll Redis
collect_timeout:  10s   # max time for one collect cycle; must be < collect_interval

buffer_capacity: 200    # snapshots held in memory during API outage

retry:
  max_attempts: 5
  base_delay:   1s
  max_delay:    60s

log:
  level:  "info"   # debug | info | warn | error
  format: "text"   # text | json
```

### Environment variable overrides

| Variable | Overrides |
|---|---|
| `DIAGNOSTACK_API_KEY` | `api.api_key` |
| `DIAGNOSTACK_API_ENDPOINT` | `api.endpoint` |
| `DIAGNOSTACK_SERVER_LABEL` | `server_label` |
| `DIAGNOSTACK_REDIS_ADDR` | `redis.addr` |
| `DIAGNOSTACK_REDIS_PASSWORD` | `redis.password` |
| `DIAGNOSTACK_COLLECT_INTERVAL` | `collect_interval` |
| `DIAGNOSTACK_LOG_LEVEL` | `log.level` |
| `DIAGNOSTACK_LOG_FORMAT` | `log.format` |
| `DIAGNOSTACK_RETRY_MAX_ATTEMPTS` | `retry.max_attempts` |

---

## Redis ACL support

If your Redis server uses ACLs (Redis 6+), create a dedicated user with the minimum required permissions:

```
ACL SETUSER diagnostack +ping +info +slowlog +latency ~* on ><password>
```

Then set `redis.password` (or `DIAGNOSTACK_REDIS_PASSWORD`) to that password.

### What happens when commands are denied

The agent probes all three commands at startup:

| Command denied | Behaviour |
|---|---|
| `INFO` | **Fatal** — agent exits with a clear error message. No metrics are possible without `INFO`. |
| `SLOWLOG` | Non-fatal — agent starts, metrics ship, slowlog analysis is unavailable. Noted in logs and in the diagnostack UI. |
| `LATENCY` | Non-fatal — agent starts, metrics ship, latency samples are omitted. Noted in logs and in the diagnostack UI. |

The probe runs once at startup and the result is cached. Denied commands are silently skipped on every subsequent collect cycle — no error logged per tick.

If `INFO` is denied, the agent logs:

```
level=ERROR msg="redis ACL check failed"
  error="Redis ACL denied required command INFO: grant the INFO command: ACL SETUSER diagnostack +info ~* on ><password>"
```

If `SLOWLOG` or `LATENCY` is denied, the agent logs once at `WARN` and then starts normally:

```
level=WARN msg="SLOWLOG denied by Redis ACL — slowlog analysis will be unavailable"
  acl_hint="grant: ACL SETUSER diagnostack +slowlog ~* on ><password>"

level=WARN msg="redis capability probe: some commands are ACL-restricted"
  addr="127.0.0.1:6379"
  disabled=["SLOWLOG"]
  impact="slowlog analysis unavailable — cannot identify slow commands"
  docs="https://docs.diagnostack.io/agent/acl-setup"
```

---

## Observability of the agent itself

All logs go to stdout and are captured by journald. Use `journalctl -u diagnostack-agent -f` to tail them.

### Log levels

| Level | Meaning |
|---|---|
| `DEBUG` | Every collect cycle, snapshot contents, retry attempts |
| `INFO` | Startup, successful connection, ACL summary, recovery from failures |
| `WARN` | Single collect failure, ACL-restricted commands, ring buffer overflow, slowlog rollover detected |
| `ERROR` | Sustained failures (3+ consecutive), non-retryable ship errors, max retries exhausted |

### Actionable ERROR messages

When failures persist the agent emits a structured `ACTION NEEDED` message with concrete remediation steps:

```
# Wrong password / ACL denied
level=ERROR msg="ACTION NEEDED — Redis authentication failure"
  check="redis.password in /etc/diagnostack/config.yaml or DIAGNOSTACK_REDIS_PASSWORD env var"
  verify="redis-cli -h <addr> AUTH <password>"

# ACL missing permission
level=ERROR msg="ACTION NEEDED — Redis ACL denied a required command"
  check="the diagnostack Redis user needs: +info +slowlog +latency"
  example="ACL SETUSER diagnostack +info +slowlog +latency ~* on ><password>"

# Cannot connect
level=ERROR msg="ACTION NEEDED — cannot connect to Redis"
  check_1="redis.addr in /etc/diagnostack/config.yaml"
  check_2="Redis process is running: systemctl status redis"
  check_3="firewall allows agent → Redis on the configured port"

# Repeated timeouts
level=ERROR msg="ACTION NEEDED — Redis collect timed out repeatedly"
  check_1="Redis may be overloaded — check INFO commandstats"
  check_2="consider increasing collect_timeout in config"
  check_3="check network latency between agent and Redis"
```

---

## Development

```bash
make test           # run all tests with race detector
make test-verbose   # same, with -v
make lint           # go vet (+ golangci-lint if installed)
make tidy           # go mod tidy + verify
make clean          # remove build artifacts
```

### Running tests

No Redis server is required. All tests use mocks or test in-memory logic only.

```bash
go test ./... -race
```

### Project layout conventions

- **Domain layer** (`internal/domain/`): zero external imports. If you find yourself importing anything here, stop and reconsider.
- **Application layer** (`internal/app/`): imports domain only. No Redis, no HTTP, no YAML.
- **Infrastructure layer** (`internal/infra/`): imports domain and external libraries. All I/O lives here.
- **Bootstrap** (`cmd/agent/`, `config/`): wires everything together. The only place that imports all three layers.

---

## Security

- The agent binary is open-source so customers can audit what runs on their servers.
- API keys are sent over HTTPS via the `X-Agent-Key` header.
- The systemd service runs as the unprivileged `diagnostack` system user.
- `NoNewPrivileges`, `ProtectSystem`, `ProtectHome`, and `PrivateTmp` are set in the unit file.
- Memory is capped at 64 MB and CPU at 5% by systemd resource controls.
- The agent never stores Redis data to disk — only the `agent_id` UUID is persisted to `/etc/diagnostack/agent_id`.

---

## License

MIT — see [LICENSE](LICENSE).
