#!/usr/bin/env bash
# Cross-compile the lp100a-server binary for a Raspberry Pi.
#
# Pi 3 / 4 / 5 with 64-bit Raspberry Pi OS  : ARCH=arm64 (default, recommended)
# Pi Zero / 1 / older 32-bit OS             : ARCH=arm
#
# Usage: ./deploy/build-pi.sh           # arm64
#        ARCH=arm ./deploy/build-pi.sh  # 32-bit ARMv7
set -euo pipefail
cd "$(dirname "$0")/.."
mkdir -p dist

# Resolve module dependencies on first build (idempotent thereafter).
go mod tidy

ARCH="${ARCH:-arm64}"
LDFLAGS="-s -w"

case "$ARCH" in
  arm64)
    OUT="dist/lp100a-server-linux-arm64"
    GOOS=linux GOARCH=arm64 \
      go build -trimpath -ldflags="$LDFLAGS" -o "$OUT" .
    ;;
  arm)
    OUT="dist/lp100a-server-linux-armv7"
    GOOS=linux GOARCH=arm GOARM=7 \
      go build -trimpath -ldflags="$LDFLAGS" -o "$OUT" .
    ;;
  *)
    echo "Unknown ARCH: $ARCH (expected arm64 or arm)" >&2
    exit 1
    ;;
esac

echo "Built: $OUT"
ls -lh "$OUT"
file "$OUT" 2>/dev/null || true
