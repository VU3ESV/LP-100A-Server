#!/usr/bin/env bash
# Redeploy lp100a-server to a Raspberry Pi (or any systemd Linux box where
# install.sh has already run once). Run this from your dev machine.
#
# What it does:
#   1. Cross-compiles the binary (skips if --restart-only)
#   2. scp's it to /tmp/ on the target
#   3. ssh's in, stops the service, swaps the binary, restarts, tails logs
#   4. Hits /healthz to confirm it's live
#
# Prerequisites on the dev machine:
#   - Go installed (for cross-compile)
#   - SSH key auth to the target (no password prompts)
#
# Prerequisites on the Pi:
#   - install.sh has already run once (creates user, service, paths)
#   - The SSH user has passwordless sudo (default on Raspberry Pi OS)
#
# Usage:
#   ./deploy/redeploy.sh pi@raspberrypi.local
#   ARCH=arm ./deploy/redeploy.sh pi@raspberrypi.local      # 32-bit ARMv7
#   ./deploy/redeploy.sh pi@host --service                  # also push systemd unit
#   ./deploy/redeploy.sh pi@host --udev                     # also push udev rule
#   ./deploy/redeploy.sh pi@host --config                   # also push config.example.toml (won't overwrite existing)
#   ./deploy/redeploy.sh pi@host --restart-only             # skip build, just restart
#   ./deploy/redeploy.sh pi@host --no-healthcheck           # skip the curl /healthz step
set -euo pipefail
cd "$(dirname "$0")/.."

TARGET="${1:-}"
if [ -z "$TARGET" ]; then
  echo "usage: $0 user@host [--service] [--udev] [--config] [--restart-only] [--no-healthcheck]" >&2
  exit 1
fi
shift

UPDATE_SERVICE=0
UPDATE_UDEV=0
UPDATE_CONFIG=0
RESTART_ONLY=0
HEALTHCHECK=1
for arg in "$@"; do
  case "$arg" in
    --service)         UPDATE_SERVICE=1 ;;
    --udev)            UPDATE_UDEV=1 ;;
    --config)          UPDATE_CONFIG=1 ;;
    --restart-only)    RESTART_ONLY=1 ;;
    --no-healthcheck)  HEALTHCHECK=0 ;;
    *) echo "unknown flag: $arg" >&2; exit 1 ;;
  esac
done

ARCH="${ARCH:-arm64}"
case "$ARCH" in
  arm64) BIN_NAME="lp100a-server-linux-arm64" ;;
  arm)   BIN_NAME="lp100a-server-linux-armv7" ;;
  *) echo "ARCH must be arm64 or arm (got: $ARCH)" >&2; exit 1 ;;
esac
BIN="dist/$BIN_NAME"

if ! command -v ssh >/dev/null 2>&1; then
  echo "ssh not found in PATH" >&2; exit 1
fi
if [ "$RESTART_ONLY" -eq 0 ] && ! command -v go >/dev/null 2>&1; then
  echo "go not found in PATH (needed to cross-compile; install Go or pass --restart-only)" >&2
  exit 1
fi

step() { printf '\n\033[1;36m>>>\033[0m %s\n' "$*"; }
warn() { printf '\033[1;33m!!! %s\033[0m\n' "$*"; }

# 1. Build
if [ "$RESTART_ONLY" -eq 0 ]; then
  step "Cross-compiling for $ARCH"
  ARCH="$ARCH" ./deploy/build-pi.sh
  [ -x "$BIN" ] || { echo "build did not produce $BIN" >&2; exit 1; }
fi

# 2. Stage files on the target
SCP_FILES=()
if [ "$RESTART_ONLY" -eq 0 ]; then
  SCP_FILES+=("$BIN")
fi
if [ "$UPDATE_SERVICE" -eq 1 ]; then SCP_FILES+=(deploy/lp100a-server.service); fi
if [ "$UPDATE_UDEV"    -eq 1 ]; then SCP_FILES+=(deploy/99-lp100a.rules); fi
if [ "$UPDATE_CONFIG"  -eq 1 ]; then SCP_FILES+=(deploy/config.example.toml); fi

if [ "${#SCP_FILES[@]}" -gt 0 ]; then
  step "Copying $(echo "${SCP_FILES[@]}" | wc -w | tr -d ' ') file(s) to $TARGET:/tmp/"
  scp -q "${SCP_FILES[@]}" "$TARGET:/tmp/"
fi

# 3. Build the remote install script. Each line is conditional on the flags
# above so we don't run unnecessary sudo commands on the Pi.
build_remote() {
  cat <<'HEAD'
set -euo pipefail
echo "host: $(hostname) — kernel: $(uname -r)"
sudo systemctl stop lp100a-server 2>/dev/null || true
HEAD
  if [ "$RESTART_ONLY" -eq 0 ]; then
    cat <<HEAD
sudo install -m 0755 -o root -g root "/tmp/$(basename "$BIN")" /usr/local/bin/lp100a-server
rm -f "/tmp/$(basename "$BIN")"
HEAD
  fi
  if [ "$UPDATE_SERVICE" -eq 1 ]; then
    cat <<'HEAD'
sudo install -m 0644 -o root -g root /tmp/lp100a-server.service /etc/systemd/system/lp100a-server.service
sudo systemctl daemon-reload
rm -f /tmp/lp100a-server.service
HEAD
  fi
  if [ "$UPDATE_UDEV" -eq 1 ]; then
    cat <<'HEAD'
sudo install -m 0644 -o root -g root /tmp/99-lp100a.rules /etc/udev/rules.d/99-lp100a.rules
sudo udevadm control --reload-rules
sudo udevadm trigger
rm -f /tmp/99-lp100a.rules
HEAD
  fi
  if [ "$UPDATE_CONFIG" -eq 1 ]; then
    cat <<'HEAD'
if [ ! -f /etc/lp100a-server/config.toml ]; then
  sudo install -d -m 0755 /etc/lp100a-server
  sudo install -m 0644 -o root -g root /tmp/config.example.toml /etc/lp100a-server/config.toml
  echo "wrote /etc/lp100a-server/config.toml from example"
else
  echo "/etc/lp100a-server/config.toml already exists — leaving it alone"
fi
rm -f /tmp/config.example.toml
HEAD
  fi
  cat <<'TAIL'
sudo systemctl start lp100a-server
sleep 1
sudo systemctl is-active lp100a-server >/dev/null \
  || { echo "service failed to start"; sudo journalctl -u lp100a-server -n 30 --no-pager; exit 1; }
echo "service: $(systemctl is-active lp100a-server) ($(systemctl show lp100a-server --property=ActiveEnterTimestamp --value))"
sudo journalctl -u lp100a-server -n 8 --no-pager
TAIL
}

step "Updating service on $TARGET"
build_remote | ssh "$TARGET" 'bash -s'

# 4. Health check from the dev machine.
if [ "$HEALTHCHECK" -eq 1 ]; then
  HOST="${TARGET#*@}"
  step "Health check: http://$HOST:8088/healthz"
  if curl -sf --max-time 5 "http://$HOST:8088/healthz" >/dev/null; then
    printf '    \033[1;32mOK\033[0m   http://%s:8088/\n' "$HOST"
  else
    warn "/healthz not responding from this host (firewall? bound to a different interface?)"
  fi
fi

step "Done."
