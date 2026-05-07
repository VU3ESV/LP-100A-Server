# Proposal: LP-100A WebSocket Server

**Status:** Draft · **Date:** 2026-05-07 · **Owner:** vinodes4@gmail.com

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
    "range": "high",          // high | mid | low
    "mode": "fast",           // average | peak_hold | fast
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

Single config file (TOML), overridable by env vars and CLI flags:

```toml
[serial]
port = "auto"          # "auto" picks first matching device; or "COM3", "/dev/ttyUSB0"
baud = 115200
poll_interval_ms = 100 # 50–250 ms, matches LP-100-VCP's pollrate range

[server]
listen = "0.0.0.0:8088"  # bind to LAN interface; document the no-auth posture
heartbeat_ms = 2000
max_clients = 32

[meter]
power_ranges_w = { high = 750, mid = 125, low = 25 }
```

`port = "auto"` walks the OS device list looking for a USB-serial adapter whose
VID/PID matches the LP-100A's USB-to-serial chip — useful on Linux/macOS where
the device name shifts between reboots. Manual override always wins.

## 6. Reference web client (v1)

A static HTML/JS page served from the same Go binary at `/`. Mirrors the
LP-100-VCP control surface:

- Big readouts: Power (W + dBW), SWR, Z, Phase
- Bar/level meters with the same low/mid/high range LEDs the Node-RED flow shows
- Three buttons: **Alarm Setpoint**, **Mode**, **Peak/Avg/Tune**
- Connection-state pill (green/yellow/red) driven by heartbeat + `status` frames

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

1. **M1 — serial parity** *(week 1)*: Go binary that opens the port, polls,
   parses, and prints snapshots. Validates against the meter on a real cable.
   No network yet.
2. **M2 — WS server** *(week 2)*: `/ws` endpoint, snapshot-on-connect, telemetry
   diff stream, command verbs, heartbeats. Two-tab manual fan-out test passes.
3. **M3 — reference web client** *(week 3)*: static page bundled into the
   binary, deployable as a single executable. Smoke test on Windows + Linux
   (Raspberry Pi).
4. **M4 — hardening** *(week 4)*: auto-reconnect on the serial side, backoff,
   metrics endpoint (`/healthz`, `/metrics`), config file + flag handling,
   release builds for win/linux/darwin amd64+arm64.

## 9. Open questions

- **Does the LP-100A tolerate `P` arriving while it's mid-response?** The
  Node-RED flow sandwiches outgoing commands through a 30 ms `Interleave` delay
  node — we should reproduce that conservatism initially and measure.
- **Callsign field as identity?** The protocol returns a 6-char callsign. If a
  user runs more than one LP-100A, do we want to multiplex them in one server
  process, or run one server per meter? v1 assumes one meter; design leaves
  room for later.
- **Do we need a read-only mode?** With no auth, anyone on the LAN can step
  the alarm setpoint. Even without full auth, a config-file flag
  `allow_control = false` on a per-listener basis would let an operator expose
  a read-only port for casual viewers. Decide before M2.

## 10. Out of scope, explicitly

- Cloud relay / NAT traversal. If users want this off-LAN they front the server
  with a VPN (Tailscale, WireGuard) — we will not bake one in.
- Logging/charting. A separate consumer can subscribe to `/ws` and write to
  InfluxDB or similar; that's a different program.
- Replacing LP-100-VCP on Windows. The vendor app stays usable; our server just
  needs the COM port when it's running.
