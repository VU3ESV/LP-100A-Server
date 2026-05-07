# Proposal: LP-100A WebSocket Server

**Status:** Implemented (M1–M4 shipped) · **Date:** 2026-05-07 · **Owner:** [VU3ESV/LP-100A-Server](https://github.com/VU3ESV/LP-100A-Server)

## 1. Problem

The Telepost LP-100A RF Power Meter exposes its data and controls only through a
single serial (RS-232 / VCP) link. The vendor's Windows app
([`.support/LP-100-VCP/LP-100.exe`](.support/LP-100-VCP/LP-100.exe)) is a
single-user GUI bound to one COM port. The two existing networked references
each fall short:

- **`.support/LP100A-UDP-Bridge.exe`** — forwards raw COM bytes over UDP. One
  client, no parsing, no shared state, Windows-only.
- **`.support/LP-100A flow.json`** — a Node-RED dashboard with a real UI but
  also single-tenant; multiple browser tabs each poll independently and can
  fight over the serial port.

Operators want the meter readable from a phone, a logging PC, and a station
dashboard *simultaneously*, with control actions (alarm threshold, display mode,
peak/avg/tune toggle) reflected on every screen the moment any one of them
changes. None of the references do this.

## 2. Goals & non-goals

**Goals**

1. One server process owns the LP-100A serial port and is the *only* thing that
   talks to it.
2. Many clients connect concurrently over WebSocket and receive a live stream of
   telemetry (power, SWR, Z, phase, range, mode, alarm setpoint, callsign).
3. Any client may issue `A` / `M` / `F` control actions; the resulting state
   change is broadcast to every connected client within one poll cycle.
4. Cross-platform: a single Go codebase that builds for Windows, Linux, and
   macOS.
5. Survives the meter being unplugged, the cable being yanked, or a client
   disappearing — auto-reconnect on both edges.

**Non-goals (v1)**

- Authentication / per-user accounts — LAN-only deployment, rely on network
  trust. (Documented as a known limitation; revisit if a remote/WAN use case
  appears.)
- Historical telemetry storage, charting, alerting.
- Re-implementing every LP-100-VCP screen. Mirror its *control surface* (A/M/F
  + the readouts the meter actually reports), not its skin.
- Mobile/native client apps. v1 ships a reference web client only.

## 3. Architecture

```
   ┌─────────────────────┐
   │     LP-100A         │ 115200 8N1, polled every ~100 ms with `P`
   └──────────┬──────────┘
              │ serial (USB-VCP / RS-232)
   ┌──────────▼──────────────────────────────────────────────┐
   │  lp100a-server  (Go, single static binary)              │
   │  ┌──────────────┐    ┌──────────────┐    ┌───────────┐  │
   │  │ Serial owner │───►│  State hub   │───►│  WS hub   │  │
   │  │  (poller +   │◄───│  (last-known │◄───│ (fan-out, │  │
   │  │   parser)    │    │   snapshot)  │    │  fan-in)  │  │
   │  └──────────────┘    └──────────────┘    └─────┬─────┘  │
   └──────────────────────────────────────────────┬─┴────────┘
                                                  │  WebSocket /ws
              ┌────────────────────────┬──────────┼──────────┐
              ▼                        ▼          ▼          ▼
        Web dashboard           Logging PC     Phone     Station PC
```

**Three goroutines, one shared state:**

1. **Serial owner** — opens the configured port, polls with `P` on a ticker,
   parses the 43-byte response per the protocol table in `CLAUDE.md`, and
   writes a `Snapshot` struct to the state hub. Owns a single-writer command
   queue: control bytes (`A`/`M`/`F`) submitted by clients are interleaved
   between polls so we never collide on the wire.
2. **State hub** — keeps the most recent `Snapshot` plus a `seq` counter.
   Diffs each new snapshot against the previous and emits a `telemetry` event
   when any field changes (with a heartbeat every N seconds even when idle, so
   clients can detect a dead server).
3. **WS hub** — accepts connections at `/ws`, sends each new client the current
   snapshot immediately (so a freshly opened tab is correct without waiting for
   the next change), then streams events. Inbound control messages are validated
   and forwarded to the serial owner's command queue.

**Why this shape:** the LP-100A is poll-only with a single transport, so a
single-writer/single-reader design is the natural fit. Putting the state hub in
the middle means clients are decoupled from the poll cadence and from each
other — one slow client cannot stall the serial loop, and a new connection is
warm immediately.

## 4. Wire protocol (WebSocket, JSON)

All frames are JSON text. v1 uses a `type` discriminator and a monotonic `seq`
on server→client frames so clients can detect drops.

**Server → client**

```json
// Sent immediately on connect, then on every changed field.
{
  "type": "telemetry",
  "seq": 12345,
  "ts": "2026-05-07T18:22:01.103Z",
  "data": {
    "power_w": 1457.00,
    "z_ohm": 49.3,
    "phase_deg": 5.0,
    "swr": 1.02,
    "dbm": 61.6,
    "dbw": 31.6,
    "range": "high",          // high | mid | low (LP-100A power range)
    "mode": "tune",           // average | peak_hold | tune (Peak-mode)
    "alarm_setpoint": "2.0",  // off | 1.5 | 2.0 | 2.5 | 3.0 | user
    "alarm_tripped": false,
    "callsign": "N8LP"
  }
}

// Heartbeat when nothing has changed for >2s.
{ "type": "heartbeat", "seq": 12346, "ts": "..." }

// Serial port problems, parse errors, etc.
{ "type": "status", "level": "warn", "msg": "serial reopened after 1.3s gap" }

// Reply to a client command.
{ "type": "ack", "ref": "client-supplied-id", "ok": true }
```

**Client → server**

```json
// The only three control verbs the meter actually accepts.
{ "type": "command", "id": "abc-1", "action": "alarm_step" }   // sends 'A'
{ "type": "command", "id": "abc-2", "action": "mode_step"  }   // sends 'M'
{ "type": "command", "id": "abc-3", "action": "peak_toggle"}   // sends 'F'

// Optional: ask the server to re-emit the current snapshot.
{ "type": "resync" }
```

The server intentionally does not expose raw serial bytes. Clients speak in
named verbs; the server is the only thing that knows about ASCII `A`/`M`/`F`.
This keeps the protocol stable if the meter firmware ever changes.

## 5. Configuration

Single config file (TOML); CLI flags `-config <path>` and `-v` (verbose start).
The shipped shape (see [`deploy/config.example.toml`](deploy/config.example.toml)):

```toml
[serial]
port             = "/dev/ttyUSB_LPM"  # "auto", "/dev/ttyUSB0", "COM3", etc.
baud             = 115200
poll_interval_ms = 100                # 20–1000 ms range; matches LP-100-VCP

[server]
listen        = "0.0.0.0:8088"
heartbeat_ms  = 2000
max_clients   = 32
allow_control = true                  # set false for a read-only port

[ui]
enabled_views = ["normal", "vector"]  # mode-button cycle; see §6
```

`port = "auto"` walks the OS device list, preferring the udev symlink
`/dev/ttyUSB_LPM` (installed by `deploy/99-lp100a.rules`) and otherwise
falling back to the first USB-serial adapter — useful on Linux/macOS where
the device name shifts between reboots. Manual override always wins.

The default log level is **error** so the systemd journal stays quiet on a Pi.
Use `-v` to start at debug, or flip it at runtime via `POST /api/log-level`
(the web client's SETUP overlay has a picker for this).

## 6. Reference web client (v1)

A static HTML/JS page served from the same Go binary at `/`, embedded via
`go:embed` so the deployable artifact stays a single file. Mirrors the
LP-100-VCP control surface, narrowed to the two display modes the serial
protocol can fully drive:

- **Normal** — Power (W) with mode-suffixed unit (`w`/`W`/`T` for
  Average/Peak Hold/Tune, matching the LP-100A LCD), SWR, range LEDs.
- **Vector** — |Z|, phase, R, X, with a polar compass.
- Three buttons: **Alarm Setpoint**, **Mode**, **Peak/Avg/Tune**.
- Connection-state pill (green/yellow/red) driven by heartbeat + `status` frames.
- **SETUP overlay** with two operator knobs:
  - *Re-align web with meter* — picks which of the on-meter Peak-mode states
    (Average / Peak Hold / Tune) the meter is currently showing, so the web
    cycle and the LCD stay in lockstep after the meter is power-cycled.
  - *Server log level* — read/write `/api/log-level` to change verbosity at
    runtime without restarting the service.

**Removed views.** The original proposal mentioned dBm/RL, Direct Input
(Field Strength), and Peak-to-Avg modes. They were dropped because the
LP-100A's serial Power and dBm fields can read 0 / noise floor (~0.8 dBm)
even while the LCD shows real measurements during TX — Z, phase, and SWR
remain correct, so Normal and Vector stay accurate. This is a meter-side
quirk, not a parser bug.

This is the minimum needed to demonstrate fan-out: open the page in two tabs,
click *Mode* in one, watch both update.

## 7. Failure modes

| Failure                          | Behavior                                             |
|----------------------------------|------------------------------------------------------|
| Serial port disappears (USB pull)| Emit `status` warn, reconnect with backoff (1s→30s). Clients see heartbeats only. |
| Bad/short serial frame           | Drop frame (per `Valid CK` rule), increment a parse-error counter, do not disconnect. |
| Client sends malformed JSON      | Send `ack ok:false` if `id` known; otherwise close with code 1003. |
| >`max_clients` connect           | Reject with 503 + reason; existing clients unaffected. |
| Two clients press *Mode* at once | Both commands queued FIFO; both `ack`s sent; one telemetry update reflects the final state. No locking visible to clients. |

## 8. Milestones

1. **M1 — serial parity** ✅: Go binary opens the port, polls with `P`, parses
   the 9-field CSV, prints snapshots. Validated against real hardware.
2. **M2 — WS server** ✅: `/ws` endpoint, snapshot-on-connect, telemetry diff
   stream with per-field deadbands, command verbs (`alarm_step`/`mode_step`/
   `peak_toggle`), heartbeats, `allow_control` gate. Two-tab fan-out works.
3. **M3 — reference web client** ✅: static page embedded via `go:embed`,
   single-binary deploy. Smoke-tested on macOS dev and Linux (Raspberry Pi).
4. **M4 — hardening** ✅: serial auto-reconnect with 1s→30s exponential
   backoff, `/healthz`, TOML config + `-v` flag, runtime log-level via
   `/api/log-level`, ARM64/ARMv7 cross-compile via `deploy/build-pi.sh`,
   systemd unit + udev rule + `install.sh` / `redeploy.sh`.

## 9. Open questions / resolved decisions

- **`P` mid-response collisions** — resolved by a single-writer serial owner
  goroutine that interleaves commands between polls and waits 200 ms after
  any control byte before resuming polling (`drainCommands` in
  `internal/meter/serial.go`). The `P` poll itself is sent as a bare byte,
  matching the Node-RED flow that's known-good with this firmware.
- **Multi-meter support** — still one meter per server process. The callsign
  field is reported in telemetry but not used for routing; if multi-meter
  becomes a need, run multiple service instances on different ports.
- **Read-only mode** — shipped as `server.allow_control = false` in the TOML.
  When set, control verbs are NACKed with `"control disabled"`.
- **Display-mode set** — the originally-proposed five views were trimmed to
  just Normal and Vector (see §6). The serial protocol can't drive the others
  reliably on this firmware.

## 10. Out of scope, explicitly

- Cloud relay / NAT traversal. If users want this off-LAN they front the server
  with a VPN (Tailscale, WireGuard) — we will not bake one in.
- Logging/charting. A separate consumer can subscribe to `/ws` and write to
  InfluxDB or similar; that's a different program.
- Replacing LP-100-VCP on Windows. The vendor app stays usable; our server just
  needs the COM port when it's running.
