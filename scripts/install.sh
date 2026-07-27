#!/usr/bin/env bash
# install.sh — Diagnostack agent installation script
# Usage: sudo bash install.sh
#
# What this does:
#   1. Creates the diagnostack system user (no login shell)
#   2. Creates /etc/diagnostack/ with the default config
#   3. Copies the binary to /usr/local/bin/diagnostack-agent
#   4. Installs and enables the systemd service
#
# Run after building: go build -o diagnostack-agent ./cmd/agent

set -euo pipefail

BINARY_NAME="diagnostack-agent"
INSTALL_DIR="/usr/local/bin"
CONFIG_DIR="/etc/diagnostack"
CONFIG_FILE="$CONFIG_DIR/config.yaml"
SERVICE_NAME="diagnostack-agent"
SERVICE_FILE="/etc/systemd/system/${SERVICE_NAME}.service"
SYSTEM_USER="diagnostack"

# ── Checks ────────────────────────────────────────────────────────────────────
if [[ $EUID -ne 0 ]]; then
  echo "Error: this script must be run as root (use sudo)" >&2
  exit 1
fi

if [[ ! -f "./$BINARY_NAME" ]]; then
  echo "Error: ./$BINARY_NAME not found. Build first: go build -o $BINARY_NAME ./cmd/agent" >&2
  exit 1
fi

# ── System user ───────────────────────────────────────────────────────────────
if ! id "$SYSTEM_USER" &>/dev/null; then
  echo "→ Creating system user: $SYSTEM_USER"
  useradd --system --no-create-home --shell /usr/sbin/nologin "$SYSTEM_USER"
fi

# ── Config directory ──────────────────────────────────────────────────────────
echo "→ Creating config directory: $CONFIG_DIR"
mkdir -p "$CONFIG_DIR"
chown root:"$SYSTEM_USER" "$CONFIG_DIR"
chmod 750 "$CONFIG_DIR"

if [[ ! -f "$CONFIG_FILE" ]]; then
  echo "→ Installing default config: $CONFIG_FILE"
  cp config.yaml "$CONFIG_FILE"
  chown root:"$SYSTEM_USER" "$CONFIG_FILE"
  chmod 640 "$CONFIG_FILE"
  echo ""
  echo "  !! Action required: edit $CONFIG_FILE and set api.api_key"
  echo ""
else
  echo "→ Skipping config (already exists): $CONFIG_FILE"
fi

# ── Binary ────────────────────────────────────────────────────────────────────
echo "→ Installing binary: $INSTALL_DIR/$BINARY_NAME"
cp "./$BINARY_NAME" "$INSTALL_DIR/$BINARY_NAME"
chmod 755 "$INSTALL_DIR/$BINARY_NAME"

# ── systemd service ───────────────────────────────────────────────────────────
echo "→ Installing systemd service: $SERVICE_FILE"
cat > "$SERVICE_FILE" << EOF
[Unit]
Description=Diagnostack Redis Observability Agent
Documentation=https://docs.diagnostack.io/agent
After=network.target

[Service]
Type=simple
User=$SYSTEM_USER
Group=$SYSTEM_USER
ExecStart=$INSTALL_DIR/$BINARY_NAME --config $CONFIG_FILE
Restart=on-failure
RestartSec=10s

# Security hardening
NoNewPrivileges=true
ProtectSystem=strict
ProtectHome=true
PrivateTmp=true
ReadWritePaths=$CONFIG_DIR

# Resource limits — agent must stay within 30MB RSS
MemoryMax=64M
CPUQuota=5%

[Install]
WantedBy=multi-user.target
EOF

systemctl daemon-reload
systemctl enable "$SERVICE_NAME"

echo ""
echo "✓ Diagnostack agent installed successfully."
echo ""
echo "Next steps:"
echo "  1. Edit $CONFIG_FILE — add your API key and server label"
echo "  2. Start the agent:   sudo systemctl start $SERVICE_NAME"
echo "  3. Check the logs:    sudo journalctl -u $SERVICE_NAME -f"
