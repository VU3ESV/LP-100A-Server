#!/usr/bin/env bash
# Redeploy lp100a-server to a Raspberry Pi (or any systemd Linux box where
# install.sh has already run once). Run this from your dev machine.
#
# Scope:
#   This script only updates the application binary (and, with --service, the
#   systemd unit file). It NEVER touches:
#     - /etc/lp100a-server/config.toml      (operator-managed config)
#     - /etc/udev/rules.d/99-lp100a.rules   (operator-managed device rules)
#   To change either, edit the file on the Pi directly, or re-run install.sh
#   which is the authoritative path for changes to system config.
#
# What it does:
#   1. Cross-compiles the binary (skips if --restart-only)
#   2. Opens a single multiplexed SSH session to the target (one auth prompt)
#   3. scp's the binary + a remote install script to /tmp/
#   4. Runs the script under `ssh -t` so sudo can prompt for a password
#      (and so passwordless sudo also works without complaint)
#   5. Hits /healthz to confirm the service is live
#
# Auth:
#   Recommended  : SSH key auth + passwordless sudo (one quiet redeploy).
#   Also fine    : SSH password + interactive sudo password (you'll be
#                  prompted at most twice — once for SSH, once for sudo).
#   To set up keys: ssh-copy-id pi@host
#   To set up passwordless sudo on the Pi:
#     echo "$USER ALL=(ALL) NOPASSWD: ALL" | sudo tee /etc/sudoers.d/$USER-nopasswd
#
# Prerequisites on the Pi:
#   - install.sh has already run once (creates user, service, paths, udev rule, config)
#
# Usage:
#   ./deploy/redeploy.sh pi@raspberrypi.local
#   ARCH=arm ./deploy/redeploy.sh pi@raspberrypi.local      # 32-bit ARMv7
#   ./deploy/redeploy.sh pi@host --service                  # also reinstall systemd unit
#   ./deploy/redeploy.sh pi@host --restart-only             # skip build, just restart
#   ./deploy/redeploy.sh pi@host --no-healthcheck           # skip the curl /healthz step
set -euo pipefail
cd "$(dirname "$0")/.."

TARGET="${1:-}"
if [ -z "$TARGET" ]; then
  echo "usage: $0 user@host [--service] [--restart-only] [--no-healthcheck]" >&2
  exit 1
fi
shift

UPDATE_SERVICE=0
RESTART_ONLY=0
HEALTHCHECK=1
for arg in "$@"; do
  case "$arg" in
    --service)         UPDATE_SERVICE=1 ;;
    --restart-only)    RESTART_ONLY=1 ;;
    --no-healthcheck)  HEALTHCHECK=0 ;;
    --udev|--config)
      echo "$arg is not supported by redeploy: udev rules and config.toml are operator-managed." >&2
      echo "Edit them on the Pi directly, or re-run install.sh." >&2
      exit 1
      ;;
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

if ! command -v ssh >/dev/null 2>&1; then echo "ssh not found in PATH" >&2; exit 1; fi
if ! command -v scp >/dev/null 2>&1; then echo "scp not found in PATH" >&2; exit 1; fi
if [ "$RESTART_ONLY" -eq 0 ] && ! command -v go >/dev/null 2>&1; then
  echo "go not found in PATH (needed to cross-compile; install Go or pass --restart-only)" >&2
  exit 1
fi

step() { printf '\n\033[1;36m>>>\033[0m %s\n' "$*"; }
warn() { printf '\033[1;33m!!! %s\033[0m\n' "$*"; }

# --- SSH connection multiplexing -------------------------------------------
# One ControlMaster connection serves all scp + ssh calls below, so password
# auth (if used) only prompts once.
SSH_TMPDIR=$(mktemp -d -t lp100a-ssh.XXXXXX)
SSH_CTRL="$SSH_TMPDIR/cm.sock"
SSH_OPTS=(-o "ControlMaster=auto" -o "ControlPath=$SSH_CTRL" -o "ControlPersist=120s")
LOCAL_SCRIPT=""

cleanup() {
  if [ -S "$SSH_CTRL" ]; then
    ssh "${SSH_OPTS[@]}" -O exit "$TARGET" 2>/dev/null || true
  fi
  [ -n "$LOCAL_SCRIPT" ] && rm -f "$LOCAL_SCRIPT"
  rm -rf "$SSH_TMPDIR"
}
trap cleanup EXIT

# --- 1. Build ---------------------------------------------------------------
if [ "$RESTART_ONLY" -eq 0 ]; then
  step "Cross-compiling for $ARCH"
  ARCH="$ARCH" ./deploy/build-pi.sh
  [ -x "$BIN" ] || { echo "build did not produce $BIN" >&2; exit 1; }
fi

# --- 2. Compose the remote install script (on this machine) ----------------
LOCAL_SCRIPT=$(mktemp -t lp100a-redeploy.XXXXXX)
build_remote() {
  cat <<'HEAD'
#!/usr/bin/env bash
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
  cat <<'TAIL'
sudo systemctl start lp100a-server
sleep 1
if ! sudo systemctl is-active --quiet lp100a-server; then
  echo "service failed to start"
  sudo journalctl -u lp100a-server -n 30 --no-pager
  exit 1
fi
echo "service: $(systemctl is-active lp100a-server) ($(systemctl show lp100a-server --property=ActiveEnterTimestamp --value))"
sudo journalctl -u lp100a-server -n 8 --no-pager
TAIL
}
build_remote > "$LOCAL_SCRIPT"

# --- 3. Open the SSH control connection (one auth prompt) ------------------
step "Opening SSH session to $TARGET"
if ! ssh "${SSH_OPTS[@]}" -o "ConnectTimeout=10" "$TARGET" true; then
  echo "could not establish SSH session to $TARGET" >&2
  exit 1
fi

# --- 4. Copy files to /tmp/ on the Pi --------------------------------------
SCP_FILES=()
[ "$RESTART_ONLY" -eq 0 ]   && SCP_FILES+=("$BIN")
[ "$UPDATE_SERVICE" -eq 1 ] && SCP_FILES+=(deploy/lp100a-server.service)
SCP_FILES+=("$LOCAL_SCRIPT")  # the install script always goes along

step "Copying ${#SCP_FILES[@]} file(s) to $TARGET:/tmp/"
# We need a stable remote name for the install script regardless of mktemp's
# local basename. scp the regular files first, then the script under a fixed
# remote path.
if [ "${#SCP_FILES[@]}" -gt 1 ]; then
  scp -q "${SSH_OPTS[@]}" "${SCP_FILES[@]:0:${#SCP_FILES[@]}-1}" "$TARGET:/tmp/"
fi
REMOTE_SCRIPT="/tmp/lp100a-redeploy.sh"
scp -q "${SSH_OPTS[@]}" "$LOCAL_SCRIPT" "$TARGET:$REMOTE_SCRIPT"

# --- 5. Run install script with a TTY so sudo can prompt --------------------
step "Updating service on $TARGET"
ssh -t "${SSH_OPTS[@]}" "$TARGET" "bash $REMOTE_SCRIPT; rm -f $REMOTE_SCRIPT"

# --- 6. Health check from the dev machine ----------------------------------
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
