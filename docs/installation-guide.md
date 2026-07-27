# Diagnostack Agent — Installation & Operation Guide

This guide is for developers and DevOps engineers who want to install the Diagnostack agent on a Redis server and connect it to the Diagnostack dashboard.

**Time to complete:** 5–10 minutes.

---

## Before you start

You need:

- A Diagnostack account and an API key (get one at [diagnostack.io](https://diagnostack.io))
- SSH access to the Linux server running Redis
- `sudo` privileges on that server
- Redis 4.0 or later

---

## Step 1 — Download the agent binary

On your Redis server, download the latest release:

```bash
curl -Lo diagnostack-agent https://github.com/hoaithuonguit/diagnostack-agent/releases/latest/download/diagnostack-agent-linux-amd64
chmod +x diagnostack-agent
```

Verify the download:

```bash
./diagnostack-agent --version
```

---

## Step 2 — Run the install script

The install script sets up a system user, creates the config directory, and registers a systemd service.

```bash
sudo bash scripts/install.sh
```

What it does:

- Creates the `diagnostack` system user (no login shell, no home directory)
- Creates `/etc/diagnostack/` with restricted permissions (only root and the diagnostack user can read it)
- Copies the binary to `/usr/local/bin/diagnostack-agent`
- Installs a systemd service with memory and CPU limits

You will see output like:

```
→ Creating system user: diagnostack
→ Creating config directory: /etc/diagnostack
→ Installing default config: /etc/diagnostack/config.yaml
→ Installing binary: /usr/local/bin/diagnostack-agent
→ Installing systemd service: /etc/systemd/system/diagnostack-agent.service
✓ Diagnostack agent installed successfully.

Next steps:
  1. Edit /etc/diagnostack/config.yaml — add your API key and server label
  2. Start the agent:   sudo systemctl start diagnostack-agent
  3. Check the logs:    sudo journalctl -u diagnostack-agent -f
```

---

## Step 3 — Configure the agent

Open the config file:

```bash
sudo nano /etc/diagnostack/config.yaml
```

There are two required fields — everything else has a safe default.

### Required: API key

```yaml
api:
  api_key: "kw_live_xxxxxxxxxxxxxxxxxxxx"   # ← paste your Diagnostack API key here
```

You can also set this as an environment variable instead of storing it in the file:

```bash
# Add to /etc/systemd/system/diagnostack-agent.service under [Service]:
Environment="DIAGNOSTACK_API_KEY=kw_live_xxxxxxxxxxxxxxxxxxxx"
```

### Required: server label

```yaml
server_label: "prod-redis-01"   # ← how this server appears in the Diagnostack UI
```

Choose something descriptive. If you leave this empty, the server's hostname is used.

### Redis connection

If Redis is on the same machine (the common case), the defaults work:

```yaml
redis:
  addr: "127.0.0.1:6379"
  password: ""
  db: 0
```

If Redis requires a password:

```yaml
redis:
  addr: "127.0.0.1:6379"
  password: "your-redis-password"
```

> **Security tip:** Use `DIAGNOSTACK_REDIS_PASSWORD` as an environment variable instead of putting the password in the config file, especially if the config is checked into version control.

### Full config reference

```yaml
server_label: "prod-redis-01"

redis:
  addr: "127.0.0.1:6379"   # Redis address
  password: ""              # Redis password (leave empty if no auth)
  db: 0                     # Redis database number (almost always 0)

api:
  endpoint: "https://ingest.diagnostack.io/v1/ingest"   # do not change
  api_key: ""               # your Diagnostack API key
  timeout: 10s              # HTTP request timeout

collect_interval: 15s       # how often to poll Redis (minimum 1s)
collect_timeout:  10s       # max time for one poll; must be less than collect_interval

buffer_capacity: 200        # snapshots buffered during API outages (~50 min at 15s)

retry:
  max_attempts: 5           # retry attempts before dropping a snapshot
  base_delay:   1s          # initial retry delay
  max_delay:    60s         # maximum retry delay (exponential backoff cap)

log:
  level:  "info"            # debug | info | warn | error
  format: "text"            # text | json (use json for Datadog/Loki/etc.)
```

---

## Step 4 — Start the agent

```bash
sudo systemctl start diagnostack-agent
```

Check that it started cleanly:

```bash
sudo journalctl -u diagnostack-agent -f
```

You should see:

```
INFO diagnostack agent starting  agent_id=<uuid>  server_label=prod-redis-01  collect_interval=15s
INFO redis collector connected  addr=127.0.0.1:6379
INFO redis capability probe passed — all commands available  addr=127.0.0.1:6379
```

Within 30 seconds your server will appear in the Diagnostack dashboard.

Enable the service to start on boot:

```bash
sudo systemctl enable diagnostack-agent
```

---

## Redis ACL setup (Redis 6+ with ACLs enabled)

If your Redis server uses ACLs, you need to create a dedicated user for the Diagnostack agent with the minimum required permissions.

### Minimum required permissions

```
ACL SETUSER diagnostack +ping +info +slowlog +latency ~* on ><password>
```

Run this in `redis-cli` (connected as your admin user):

```bash
redis-cli -h 127.0.0.1 -p 6379 -a <admin-password>

# In the redis-cli session:
ACL SETUSER diagnostack +ping +info +slowlog +latency ~* on >diagnostack-secret-password
ACL SAVE   # persist to aclfile if you use one
```

Then update the agent config:

```yaml
redis:
  addr: "127.0.0.1:6379"
  password: "diagnostack-secret-password"
```

### What each permission does

| Permission | Required for | Impact if missing |
|---|---|---|
| `+ping` | Startup connectivity check | Fatal — agent cannot start |
| `+info` | Metrics (memory, clients, throughput, etc.) | Fatal — no metrics possible |
| `+slowlog` | Slow command analysis (root-cause feature) | Non-fatal — slowlog tab is unavailable in the UI |
| `+latency` | Latency samples for charts | Non-fatal — latency chart shows no data |
| `~*` | No key access needed | Not used — agent never reads your data keys |

### What you'll see if a permission is missing

**`+info` missing (fatal):**

```
ERROR redis ACL check failed
  error="Redis ACL denied required command INFO: grant the INFO command:
         ACL SETUSER diagnostack +info ~* on ><password>"
```

The agent will exit. Fix the ACL and restart.

**`+slowlog` or `+latency` missing (non-fatal):**

```
WARN SLOWLOG denied by Redis ACL — slowlog analysis will be unavailable
  acl_hint="grant: ACL SETUSER diagnostack +slowlog ~* on ><password>"

WARN redis capability probe: some commands are ACL-restricted
  disabled=["SLOWLOG"]
  impact="slowlog analysis unavailable — cannot identify slow commands"
```

The agent starts and ships metrics. The Diagnostack UI shows a banner on the affected tab explaining what is missing and how to fix it.

### Verify your ACL user

```bash
redis-cli -h 127.0.0.1 -a diagnostack-secret-password INFO server
# Should return server info, not an error.

redis-cli -h 127.0.0.1 -a diagnostack-secret-password SLOWLOG GET 1
# Should return an empty list or a slowlog entry, not an error.
```

---

## Upgrading the agent

1. Download the new binary
2. Stop the service: `sudo systemctl stop diagnostack-agent`
3. Replace the binary: `sudo cp diagnostack-agent /usr/local/bin/diagnostack-agent`
4. Start the service: `sudo systemctl start diagnostack-agent`

The config file is not touched during upgrade.

---

## Uninstalling

```bash
sudo systemctl stop diagnostack-agent
sudo systemctl disable diagnostack-agent
sudo rm /etc/systemd/system/diagnostack-agent.service
sudo systemctl daemon-reload
sudo rm /usr/local/bin/diagnostack-agent
sudo rm -rf /etc/diagnostack
sudo userdel diagnostack
```

---

## Troubleshooting

### Agent won't start — "redis ping failed"

Redis is unreachable. Check:

```bash
# Is Redis running?
systemctl status redis

# Can you reach it from this server?
redis-cli -h 127.0.0.1 -p 6379 PING
# Expected: PONG

# Is the port open in the firewall?
ss -tlnp | grep 6379
```

Common causes:
- Wrong `redis.addr` in the config (check port number)
- Redis bound only to `127.0.0.1` but the agent is connecting to a different address
- Firewall blocking the port

### Agent won't start — "redis ACL check failed"

The API key set in `redis.password` is correct (PING succeeded) but the `INFO` command is denied. See the [Redis ACL setup](#redis-acl-setup-redis-6-with-acls-enabled) section above.

### Agent won't start — "api.api_key is required"

The API key is missing from the config. Either:

```yaml
api:
  api_key: "kw_live_xxxxxxxxxxxxxxxxxxxx"
```

or:

```bash
export DIAGNOSTACK_API_KEY="kw_live_xxxxxxxxxxxxxxxxxxxx"
```

### Agent starts but the server doesn't appear in the dashboard

1. Check the logs for ship errors: `sudo journalctl -u diagnostack-agent | grep ERROR`
2. Verify network access to the API: `curl -v https://ingest.diagnostack.io/v1/health`
3. Check your API key is correct and active in the Diagnostack dashboard

### Slowlog tab is empty in the dashboard

Two possible causes:

**ACL:** Run `redis-cli SLOWLOG GET 1` as the diagnostack user. If you get a `NOPERM` error, add `+slowlog` to the ACL. See [Redis ACL setup](#redis-acl-setup-redis-6-with-acls-enabled).

**Slowlog threshold too high:** Redis only logs commands that exceed `slowlog-log-slower-than` (in microseconds). Check the current setting:

```bash
redis-cli CONFIG GET slowlog-log-slower-than
# Default: 10000 (10ms)
```

A value of `-1` disables the slowlog entirely. Set it to a reasonable threshold:

```bash
redis-cli CONFIG SET slowlog-log-slower-than 10000   # log commands slower than 10ms
```

### High CPU or memory usage

The agent is designed to use under 1% CPU and 30 MB RSS. If you see more:

- Increase `collect_interval` (e.g. to `30s`) to halve the polling frequency
- Check if the Redis server itself is very slow — the agent's `collect_timeout` will cap each cycle, but a slow Redis means more time spent waiting

### Viewing detailed logs

Switch to debug level to see every collect cycle:

```bash
sudo nano /etc/diagnostack/config.yaml
# Set: log.level: "debug"
sudo systemctl restart diagnostack-agent
sudo journalctl -u diagnostack-agent -f
```

Switch back to `info` when done — debug is verbose.

### Reading logs in JSON format (for log aggregators)

```yaml
log:
  format: "json"
```

Each log line becomes a JSON object:

```json
{"time":"2026-06-22T10:15:00Z","level":"INFO","msg":"snapshot collected and enqueued","agent_id":"...","slowlog_entries":3}
```

---

## Resource usage

| Resource | Limit | Notes |
|---|---|---|
| CPU | < 1% per core | Enforced by systemd `CPUQuota=5%` |
| Memory | < 30 MB RSS | systemd `MemoryMax=64M` (headroom for spikes) |
| Disk | None during operation | Only `/etc/diagnostack/agent_id` (~36 bytes) is written |
| Network | ~5 KB/request outbound | One HTTPS POST per collect interval |
| Redis load | Negligible | Three read-only commands per interval; no `MONITOR` |

---

## Data the agent collects

The agent collects and ships only operational metadata — it never reads your application data.

**From `INFO all`:** memory usage, connected clients, blocked clients, operations per second, keyspace hit/miss ratios, evicted keys, rejected connections, replication lag, RDB save timestamps, server uptime, Redis version, role (primary/replica).

**From `LATENCY LATEST`:** latency event names and most recent latency measurement (milliseconds). No command arguments or data values.

**From `SLOWLOG GET`:** command name, execution duration, timestamp, client IP:port, and optional client name. Argument values are truncated by Redis to 128 bytes by default. The agent ships what Redis provides.

**Agent metadata:** agent UUID, server label, collect timestamp.

The agent does not read key names, values, TTLs, or any application data stored in Redis.

---

## Support

- Documentation: [docs.diagnostack.io](https://docs.diagnostack.io)
- ACL setup guide: [docs.diagnostack.io/agent/acl-setup](https://docs.diagnostack.io/agent/acl-setup)
- Issues: [github.com/hoaithuonguit/diagnostack-agent/issues](https://github.com/hoaithuonguit/diagnostack-agent/issues)
- Email: support@diagnostack.io
