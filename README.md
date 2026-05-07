# LP-100A Server

WebSocket bridge for the Telepost LP-100A RF Power Meter. One server owns the
serial port; many clients (browsers, logging PCs, phones) get live telemetry
and can issue control actions, with every client kept in sync.

See [PROPOSAL.md](PROPOSAL.md) for the design and [CLAUDE.md](CLAUDE.md) for
the LP-100A serial protocol reference.

## Quick start (development, on a Mac)

```sh
go mod tidy            # first run only — fetches deps and writes go.sum
go test ./...
go run . -config deploy/config.example.toml
# open http://localhost:8088/
```

Without a meter attached the server will retry the serial port forever (with
backoff) and clients will see only heartbeat frames — that's expected.

The reference web client is embedded in the binary — no separate frontend
build step.

## Building for Raspberry Pi

From a dev machine with Go installed (Mac, Linux, or WSL):

```sh
# Pi 3/4/5 with 64-bit Raspberry Pi OS (recommended):
./deploy/build-pi.sh
# -> dist/lp100a-server-linux-arm64

# Pi Zero/1, or 32-bit OS:
ARCH=arm ./deploy/build-pi.sh
# -> dist/lp100a-server-linux-armv7
```

Cross-compilation needs no C toolchain — the serial library is pure Go.

## Deploying on the Raspberry Pi

1. Copy the project to the Pi (or just the binary plus `deploy/`):
   ```sh
   scp -r dist/lp100a-server-linux-arm64 deploy pi@raspberrypi.local:~/lp100a/
   ```
2. SSH in and install:
   ```sh
   ssh pi@raspberrypi.local
   cd ~/lp100a
   sudo ./deploy/install.sh ./lp100a-server-linux-arm64
   ```
3. Confirm:
   ```sh
   systemctl status lp100a-server
   journalctl -u lp100a-server -f
   curl http://raspberrypi.local:8088/healthz
   ```

The installer:
- creates a system user `lp100a` and adds it to `dialout` for serial access,
- installs the binary to `/usr/local/bin/lp100a-server`,
- writes `/etc/lp100a-server/config.toml` (preserving any existing config),
- installs the systemd unit and starts it,
- installs a udev rule that creates `/dev/ttyUSB_LPM` for the meter.

### Verifying the udev rule

The default rule assumes an FTDI FT232 USB-serial chip (VID:PID `0403:6001`).
If your meter uses a different adapter, find its IDs and edit the rule:

```sh
lsusb                                         # find the meter's bus/device
udevadm info -a -n /dev/ttyUSB0 | head -40    # read its idVendor/idProduct
sudo nano /etc/udev/rules.d/99-lp100a.rules
sudo udevadm control --reload-rules && sudo udevadm trigger
ls -l /dev/ttyUSB_LPM
```

If `/dev/ttyUSB_LPM` is missing, set `port = "/dev/ttyUSB0"` (or whatever the
device is) in `/etc/lp100a-server/config.toml` and restart the service.

## Redeploying after a code change

After the first install, push subsequent changes from your dev machine with
[`deploy/redeploy.sh`](deploy/redeploy.sh):

```sh
./deploy/redeploy.sh pi@raspberrypi.local           # rebuild, scp, restart
./deploy/redeploy.sh pi@host --service              # also push systemd unit
./deploy/redeploy.sh pi@host --keep-config          # binary only (skip toml/udev)
./deploy/redeploy.sh pi@host --restart-only         # skip build; just bounce the service
ARCH=arm ./deploy/redeploy.sh pi@host               # 32-bit ARMv7 target
```

By default redeploy refreshes both the application binary **and** the
`config.toml` / udev rule from the templates in `deploy/`. The user's
`serial.port` value is detected and re-applied to the new config so a
custom device path (e.g. `/dev/serial/by-id/usb-FTDI_…`) survives the
update. The previous `config.toml` and udev rule are saved alongside
themselves with `.bak.YYYYMMDD-HHMMSS` suffixes before they're replaced,
so you can always diff or roll back.

Pass `--keep-config` if you've heavily customized `config.toml` (e.g.
edited `enabled_views` or `listen`) and want to keep it untouched —
only the binary will be updated.

## Configuration

`/etc/lp100a-server/config.toml`. Defaults are sensible; the fields you might
change:

| Field                       | Default              | Notes                                            |
|-----------------------------|----------------------|--------------------------------------------------|
| `serial.port`               | `auto`               | `auto`, `/dev/ttyUSB_LPM`, `COM3`, etc.          |
| `serial.poll_interval_ms`   | `100`                | 50–250 ms range; matches LP-100-VCP              |
| `server.listen`             | `0.0.0.0:8088`       | Bind to a LAN IP for safety                      |
| `server.allow_control`      | `true`               | Set `false` for a read-only port                 |
| `server.max_clients`        | `32`                 |                                                  |
| `ui.enabled_views`          | `["normal","vector"]`| Web Mode-button cycle. Match to the meter's "Opt. Mode Display" SETUP screen so they stay in sync. Valid: `normal`, `vector`, `dbm`, `field_strength`, `peak_avg`. |

After editing: `sudo systemctl restart lp100a-server`.

## Security

**There is no authentication.** Anyone with network access to the listen
address can read telemetry and issue control commands. This is intentional for
LAN-only deployment. For remote access use a VPN (Tailscale, WireGuard) and
keep the listen address bound to the VPN interface or `127.0.0.1`.

## Wire protocol

JSON over WebSocket at `/ws`. See [PROPOSAL.md §4](PROPOSAL.md) for the full
schema. Smoke test from the command line:

```sh
# requires websocat: brew install websocat / cargo install websocat
websocat ws://raspberrypi.local:8088/ws
# {"type":"telemetry","seq":1,"ts":"...","data":{"power_w":0,"swr":1.00,...}}
echo '{"type":"command","id":"1","action":"mode_step"}' | websocat ws://raspberrypi.local:8088/ws
```

## Layout

```
.
├── main.go                       # entry point: wires config -> serial -> hub -> http
├── internal/
│   ├── config/                   # TOML loader with defaults + validation
│   ├── meter/                    # serial port owner, parser, snapshot model
│   ├── hub/                      # WebSocket hub: fan-out, fan-in, heartbeats
│   └── web/static/index.html     # reference web client (embedded via go:embed)
├── deploy/
│   ├── config.example.toml
│   ├── lp100a-server.service     # systemd unit
│   ├── 99-lp100a.rules           # udev rule for /dev/ttyUSB_LPM
│   ├── build-pi.sh               # cross-compile from dev box
│   ├── install.sh                # first-time install on the Pi
│   └── redeploy.sh               # subsequent updates (build + scp + restart)
└── .support/                     # vendor + 3rd-party references (read-only)
```

## Operations cheatsheet

```sh
# Tail logs
journalctl -u lp100a-server -f

# Restart after config change
sudo systemctl restart lp100a-server

# Stop / disable
sudo systemctl disable --now lp100a-server

# Health check
curl -sf http://localhost:8088/healthz && echo OK

# Run in the foreground for debugging (as the lp100a user)
sudo -u lp100a /usr/local/bin/lp100a-server -config /etc/lp100a-server/config.toml -v
```
