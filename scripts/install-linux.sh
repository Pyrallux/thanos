#!/usr/bin/env bash
# install-linux.sh
# Installs Thanos as a systemd service.
# Run with sudo: sudo ./scripts/install-linux.sh

set -euo pipefail

INSTALL_DIR="/usr/local/bin"
SERVICE_FILE="/etc/systemd/system/thanos.service"
SERVICE_NAME="thanos"

echo "=== Thanos systemd Service Installer ==="

# 1. Build if binary doesn't exist
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PROJECT_DIR="$(dirname "$SCRIPT_DIR")"
BINARY="$PROJECT_DIR/thanos"

if [ ! -f "$BINARY" ]; then
    echo "thanos binary not found. Building from source..."
    cd "$PROJECT_DIR"
    VERSION=$(git describe --tags --always --dirty 2>/dev/null || echo "dev")
    CGO_ENABLED=1 go build -ldflags "-X thanos/internal/version.version=$VERSION" -o thanos ./cmd/thanos
fi

# 2. Copy binary
cp "$BINARY" "$INSTALL_DIR/thanos"
echo "Copied thanos to $INSTALL_DIR/"

# 3. Check for libpcap
if ! ldconfig -p | grep -q libpcap; then
    echo "WARNING: libpcap not found. Packet sniffing requires libpcap-dev." >&2
    echo "  Install with: sudo apt install libpcap-dev (Debian/Ubuntu)" >&2
    echo "  Thanos will run in manual-start-only mode without libpcap." >&2
fi

# 4. Create service file
cat > "$SERVICE_FILE" << EOF
[Unit]
Description=Thanos - Scale-to-Zero for Docker Game Servers
After=network.target docker.service
Wants=docker.service

[Service]
Type=simple
ExecStart=$INSTALL_DIR/thanos
WorkingDirectory=/var/lib/thanos
StateDirectory=thanos
Restart=on-failure
RestartSec=10
User=root

[Install]
WantedBy=multi-user.target
EOF

echo "Created systemd service file at $SERVICE_FILE"

# 5. Enable and start
mkdir -p /var/lib/thanos
systemctl daemon-reload
systemctl enable "$SERVICE_NAME"
systemctl start "$SERVICE_NAME"

echo ""
echo "Service installed and started successfully!"
echo ""
echo "Next steps:"
echo "  1. Check status:  systemctl status $SERVICE_NAME"
echo "  2. View logs:     journalctl -u $SERVICE_NAME -f"
echo "  3. Open http://localhost:4040/setup to complete first-run setup"
echo ""
echo "To uninstall:"
echo "  systemctl stop $SERVICE_NAME; systemctl disable $SERVICE_NAME"
echo "  rm $SERVICE_FILE && systemctl daemon-reload"