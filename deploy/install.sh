#!/usr/bin/env bash
# Install lp100a-server on a Raspberry Pi (or any systemd Linux box).
# Run from a directory that contains:
#   - the binary (default: ./lp100a-server)
#   - the deploy/ folder (lp100a-server.service, 99-lp100a.rules, config.example.toml)
#
# Usage: sudo ./deploy/install.sh [path-to-binary]
set -euo pipefail

if [ "${EUID:-$(id -u)}" -ne 0 ]; then
  echo "Run as root: sudo $0" >&2
  exit 1
fi

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
PROJECT_DIR="$(cd "$SCRIPT_DIR/.." && pwd)"
BIN="${1:-$PROJECT_DIR/lp100a-server}"

if [ ! -x "$BIN" ]; then
  # Fallback: grab the most recent build under dist/ if no explicit path given.
  for cand in \
      "$PROJECT_DIR/dist/lp100a-server-linux-arm64" \
      "$PROJECT_DIR/dist/lp100a-server-linux-armv7" \
      "$PROJECT_DIR/dist/lp100a-server"; do
    if [ -x "$cand" ]; then
      BIN="$cand"
      break
    fi
  done
fi

if [ ! -x "$BIN" ]; then
  echo "Binary not found or not executable: $BIN" >&2
  echo "Run deploy/build-pi.sh first, or pass the binary path explicitly." >&2
  exit 1
fi

echo "Using binary: $BIN"

if ! id -u lp100a >/dev/null 2>&1; then
  useradd --system --no-create-home --shell /usr/sbin/nologin lp100a
fi
usermod -aG dialout lp100a

install -m 0755 "$BIN" /usr/local/bin/lp100a-server

install -d -m 0755 /etc/lp100a-server
if [ ! -f /etc/lp100a-server/config.toml ]; then
  install -m 0644 "$SCRIPT_DIR/config.example.toml" /etc/lp100a-server/config.toml
  echo "Wrote default config: /etc/lp100a-server/config.toml"
else
  echo "Existing config preserved: /etc/lp100a-server/config.toml"
fi

install -m 0644 "$SCRIPT_DIR/lp100a-server.service" /etc/systemd/system/lp100a-server.service
install -m 0644 "$SCRIPT_DIR/99-lp100a.rules"      /etc/udev/rules.d/99-lp100a.rules

udevadm control --reload-rules
udevadm trigger
systemctl daemon-reload
systemctl enable --now lp100a-server.service

echo
echo "Installed. Inspect with:"
echo "  systemctl status lp100a-server"
echo "  journalctl -u lp100a-server -f"
echo
echo "Web UI: http://$(hostname -I | awk '{print $1}'):8088/"
