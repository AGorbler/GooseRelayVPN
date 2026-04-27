#!/usr/bin/env bash
# Deploy the GooseRelayVPN exit server to a remote host.
#
# Usage: bash scripts/deploy.sh kianmhz@146.190.246.7
#        bash scripts/deploy.sh user@host [server_config.json]
#
# Requirements: go, ssh, scp in PATH; user needs sudo on the remote host.
set -euo pipefail

REMOTE="${1:-}"
CONFIG="${2:-server_config.json}"

if [[ -z "$REMOTE" ]]; then
  echo "Usage: $0 user@host [server_config.json]" >&2
  exit 1
fi

if [[ ! -f "$CONFIG" ]]; then
  echo "Error: config file '$CONFIG' not found." >&2
  echo "Copy server_config.example.json → server_config.json and fill in tunnel_key." >&2
  exit 1
fi

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
BINARY="$ROOT/goose-server-linux"
SERVICE_TEMPLATE="$ROOT/scripts/goose-relay.service"

echo "==> Building Linux amd64 binary..."
cd "$ROOT"
GOOS=linux GOARCH=amd64 go build -o "$BINARY" ./cmd/server
echo "    Built: $BINARY ($(du -sh "$BINARY" | cut -f1))"

# Write a self-contained install script that runs on the droplet.
# It stops the service first (releasing the binary lock), swaps in the
# new binary, then restarts.
INSTALL_SCRIPT="$(mktemp /tmp/goose-install-XXXX.sh)"
cat > "$INSTALL_SCRIPT" <<'SCRIPT'
#!/usr/bin/env bash
set -euo pipefail
DEPLOY_DIR="$HOME"

# Stop service first to release the lock on the binary.
sudo systemctl stop goose-relay 2>/dev/null || true
# Stop legacy relay-tunnel service if it exists from a pre-rename install.
sudo systemctl stop relay-tunnel 2>/dev/null || true
sudo systemctl disable relay-tunnel 2>/dev/null || true
sudo rm -f /etc/systemd/system/relay-tunnel.service

# Swap in the new binary.
mv "$DEPLOY_DIR/goose-server-linux.new" "$DEPLOY_DIR/goose-server-linux"
chmod +x "$DEPLOY_DIR/goose-server-linux"

# Install/update the service file.
sed -i "s|/root|$DEPLOY_DIR|g" ~/goose-relay.service
sudo mv ~/goose-relay.service /etc/systemd/system/goose-relay.service
sudo systemctl daemon-reload
sudo systemctl enable goose-relay
sudo systemctl restart goose-relay
sleep 1
sudo systemctl status goose-relay --no-pager
SCRIPT

echo "==> Copying files to $REMOTE:~/ ..."
# Upload binary as .new so the running process doesn't hold a lock on it.
scp "$BINARY" "$REMOTE:~/goose-server-linux.new"
scp "$CONFIG" "$SERVICE_TEMPLATE" "$INSTALL_SCRIPT" "$REMOTE:~/"
rm "$INSTALL_SCRIPT"

REMOTE_SCRIPT="~/$(basename "$INSTALL_SCRIPT")"

echo "==> Installing on $REMOTE (you may be prompted for your sudo password)..."
ssh -t "$REMOTE" "bash $REMOTE_SCRIPT; rm $REMOTE_SCRIPT"

echo ""
echo "==> Testing /healthz..."
IP=$(echo "$REMOTE" | sed 's/.*@//')
curl -sf --max-time 5 "http://$IP:8443/healthz" && echo "  OK — server is live at $IP:8443"
