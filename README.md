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

## CI / releases

Two GitHub Actions workflows ship with the repo:

- [`.github/workflows/ci.yml`](.github/workflows/ci.yml) runs on every push
  and pull request to `main`: `go vet`, `go test -race`, `go build`. Required
  before merging a branch.
- [`.github/workflows/release.yml`](.github/workflows/release.yml) is
  triggered manually from the **Actions** tab. Pick *Release* → *Run
  workflow*, type a tag like `v1.0.0`, and it will:
  1. Tag the current `main` commit,
  2. Cross-compile binaries for `linux/{amd64,arm64,armv7}`,
     `darwin/{amd64,arm64}`, and `windows/amd64`,
  3. Publish a GitHub Release with the binaries + a `SHA256SUMS` file
     attached and changelog auto-generated from commits since the previous
     release.

  Locally you don't need to run anything — Watchers of the repo get a
  GitHub notification when the release is published.

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
| `ui.enabled_views`          | `["normal","vector"]`| Web Mode-button cycle: `normal` (power + SWR) and `vector` (|Z|, phase, R, X). These are the LP-100A's two always-on display modes; the dBm/RL, Direct Input (FS), and Peak-to-Avg views were removed because the meter's serial protocol doesn't always carry usable values for them. |

After editing: `sudo systemctl restart lp100a-server`.

### Logging

The server defaults to **error**-level logging so the systemd journal stays
quiet. Two ways to turn it up:

- **At start time:** pass `-v` (sets debug). The deployed unit edits this via
  `systemctl edit lp100a-server` → `ExecStart=/usr/local/bin/lp100a-server -v
  -config /etc/lp100a-server/config.toml`.
- **At runtime, no restart:** open the web client, click **SETUP**, and pick a
  level (ERROR / WARN / INFO / DEBUG). Or hit the API directly:
  ```sh
  curl -s http://host:8088/api/log-level
  curl -s -XPOST http://host:8088/api/log-level -d '{"level":"debug"}'
  ```
  The change is in-memory; a restart resets to whatever `-v` produces.

## Security

**There is no authentication.** Anyone with network access to the listen
address can read telemetry and issue control commands. This is intentional for
LAN-only deployment. For remote access use a VPN (Tailscale, WireGuard) and
keep the listen address bound to the VPN interface or `127.0.0.1`.

**Browser origin check.** `/ws` refuses a WebSocket upgrade whose `Origin`
header names a different host:port than the request, so a web page on
another site can't use a browser on your LAN to drive the meter. The
embedded web client (same origin), native apps and Node-RED (no `Origin`)
are unaffected.

**Adding a login and TLS.** Set `listen = "127.0.0.1:8088"` and put a
reverse proxy (e.g. Caddy with `basicauth` + `tls`) on the LAN address. The
proxy must pass the original `Host` header through, which Caddy does by
default, or the origin check will refuse the browser client.

## HTTP / WebSocket surface

| Path             | Purpose                                                                   |
|------------------|---------------------------------------------------------------------------|
| `/`              | Embedded reference web client                                             |
| `/ws`            | WebSocket — telemetry stream + control verbs (see [PROPOSAL.md §4](PROPOSAL.md)) |
| `/healthz`       | `ok\n` — for systemd / monitoring probes                                  |
| `/api/config`    | `{views, allow_control}` — UI bootstrap                                   |
| `/api/log-level` | GET / POST the running slog level (`error`/`warn`/`info`/`debug`)         |

Smoke test from the command line:

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

# Bump log verbosity on a running instance without a restart
curl -s -XPOST http://localhost:8088/api/log-level -d '{"level":"debug"}'
```

## Web client

The embedded page mirrors the LP-100A LCD for the **Normal** and **Vector**
display modes. Power is shown with the meter's own unit suffix — `w`
(Average), `W` (Peak Hold), `T` (Tune) — so the web view tracks whichever
Peak-mode the device is in. The mode cycle on the page is driven client-side;
if it ever drifts from the LCD (e.g. after a meter power-cycle), open
**SETUP** and click the Peak-mode the meter is actually showing to re-align.

Opening multiple tabs / devices is the point of this server: any control
button pressed on one client is reflected on every other client within one
poll cycle.
