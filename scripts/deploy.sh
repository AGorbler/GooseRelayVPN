#!/usr/bin/env bash
# Deploy the relay-tunnel exit server to a remote host.
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
  echo "Copy server_config.example.json → server_config.json and fill in aes_key_hex." >&2
  exit 1
fi

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
BINARY="$ROOT/relay-server-linux"
SERVICE_TEMPLATE="$ROOT/scripts/relay-tunnel.service"

echo "==> Building Linux amd64 binary..."
cd "$ROOT"
GOOS=linux GOARCH=amd64 go build -o "$BINARY" ./cmd/server
echo "    Built: $BINARY ($(du -sh "$BINARY" | cut -f1))"

# Write a self-contained install script that runs on the droplet.
# It stops the service first (releasing the binary lock), swaps in the
# new binary, then restarts.
INSTALL_SCRIPT="$(mktemp /tmp/relay-install-XXXX.sh)"
cat > "$INSTALL_SCRIPT" <<'SCRIPT'
#!/usr/bin/env bash
set -euo pipefail
DEPLOY_DIR="$HOME"

# Stop service first to release the lock on the binary.
sudo systemctl stop relay-tunnel 2>/dev/null || true

# Swap in the new binary.
mv "$DEPLOY_DIR/relay-server-linux.new" "$DEPLOY_DIR/relay-server-linux"
chmod +x "$DEPLOY_DIR/relay-server-linux"

# Install/update the service file.
sed -i "s|/root|$DEPLOY_DIR|g" ~/relay-tunnel.service
sudo mv ~/relay-tunnel.service /etc/systemd/system/relay-tunnel.service
sudo systemctl daemon-reload
sudo systemctl enable relay-tunnel
sudo systemctl restart relay-tunnel
sleep 1
sudo systemctl status relay-tunnel --no-pager
SCRIPT

echo "==> Copying files to $REMOTE:~/ ..."
# Upload binary as .new so the running process doesn't hold a lock on it.
scp "$BINARY" "$REMOTE:~/relay-server-linux.new"
scp "$CONFIG" "$SERVICE_TEMPLATE" "$INSTALL_SCRIPT" "$REMOTE:~/"
rm "$INSTALL_SCRIPT"

REMOTE_SCRIPT="~/$(basename "$INSTALL_SCRIPT")"

echo "==> Installing on $REMOTE (you may be prompted for your sudo password)..."
ssh -t "$REMOTE" "bash $REMOTE_SCRIPT; rm $REMOTE_SCRIPT"

echo ""
echo "==> Testing /healthz..."
IP=$(echo "$REMOTE" | sed 's/.*@//')
curl -sf --max-time 5 "http://$IP:8443/healthz" && echo "  OK — server is live at $IP:8443"
