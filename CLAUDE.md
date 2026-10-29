# LP-100A Server

A WebSocket server that bridges the **Telepost LP-100A** RF Power Meter to multiple
networked client applications. One server process owns the serial connection to the
meter; many clients subscribe to telemetry and may issue control actions. When any
client invokes an action, the resulting state change is broadcast to all clients.

## Repository layout

```
LP-100A-Server/
├── CLAUDE.md         # this file
├── README.md         # build / install / operate
├── PROPOSAL.md       # design proposal for the server + clients
├── main.go           # entry point: config -> serial -> hub -> http
├── go.mod / go.sum
├── internal/
│   ├── config/       # TOML loader with defaults + validation
│   ├── meter/        # serial port owner, parser, snapshot model + tests
│   ├── hub/          # WebSocket hub: fan-out, fan-in, heartbeats
│   └── web/static/   # reference web client (embedded via go:embed)
├── deploy/           # build-pi.sh, install.sh, redeploy.sh, systemd unit, udev rule
└── .support/         # reference material (do not modify)
    ├── LP-100-VCP/   # manufacturer's Windows VCP app (LP-100.exe + LP100.hlp)
    ├── LP-100A flow.json    # Node-RED reference flow (talks COM port directly)
    └── LP100A-UDP-Bridge.exe # 3rd-party UDP↔serial bridge (Windows)
```

The two reference implementations differ in scope:

- **LP-100-VCP** is the vendor's single-user Windows GUI. It connects to the meter
  over a COM port (default `Com1`, 50 ms poll rate per `config.ini`) and exposes
  *no* network interface. It is the ground-truth UI to mirror feature-for-feature.
- **LP100A-UDP-Bridge.exe** is a third-party Windows daemon that forwards COM-port
  bytes over UDP. Single-client, no fan-out, no state model.
- **`LP-100A flow.json`** is a Node-RED dashboard that opens the meter's serial
  port directly (`/dev/ttyUSB_LPM`, 115200 8N1, polls every 100 ms with `P`).
  The flow's comment nodes are the most complete protocol documentation we have —
  read those before changing parsing logic.

## LP-100A serial protocol (extracted from `.support/LP-100A flow.json`)

- **Link:** 115200 baud, 8N1, no flow control.
- **Poll command:** ASCII `P` (the Node-RED flow sends `" P"` with a leading
  space; the leading space is harmless padding). Send every ~50–100 ms.
- **Response:** ASCII line beginning with `;`, total length **43 bytes**, CSV
  body after the leading `;`. Example:
  ```
  ;1457.00,49.3,005.0,2,N8LP  ,0,2,61.6,1.02
  ```
  Field positions (0-based, including the leading `;`):

  | Field         | Offset | Width | Notes                                              |
  |---------------|-------:|------:|----------------------------------------------------|
  | Power (W)     |      2 |     8 | Float                                              |
  | Z (Ω)         |     10 |     5 | Float; show `--` when power ≈ 0                    |
  | Phase (°)     |     16 |     5 | Float; show `--` when power ≈ 0                    |
  | SWR Alarm pt  |     22 |     1 | `0`=off `1`=1.5 `2`=2.0 `3`=2.5 `4`=3.0 `5`=user   |
  | Callsign      |     24 |     6 | Space-padded                                       |
  | Power range   |     31 |     1 | `0`=High(750W) `1`=Mid(125W) `2`=Low(25W)          |
  | Peak/Hold     |     33 |     1 | `0`=Average `1`=Peak Hold `2`=Tune                 |
  | dBm           |     35 |     4 | Float; subtract 30 for dBW                         |
  | SWR           |     40 |     4 | Float                                              |

- **Control commands** (single ASCII char, no terminator required):
  - `A` — increment SWR Alarm set point (off→1.5→2.0→2.5→3.0→user→off)
  - `M` — increment Peak-mode (Average→Peak Hold→Tune)
  - `F` — toggle Peak/Avg/Tune (alias used by the flow's UI button)

  The meter's *display-mode* cycle (Normal / Vector / dBm-RL / FS / P-to-A) is
  driven from the meter's front panel, not the serial link. The web client
  cycles its own equivalent views client-side; the only two we render are
  **Normal** and **Vector**, because those are the always-on display modes the
  serial protocol can fully drive (see "Removed views" below).

- **Validation:** discard any frame whose first byte is not `;` or whose length
  is not 43. The Node-RED flow's `Valid CK` function enforces this.

- **Post-command settle:** after writing any control byte, wait ~200 ms before
  the next poll. The LP-100A's mode cycle (especially the Peak-to-Avg screen)
  needs that headroom to advance reliably. See `internal/meter/serial.go`'s
  `drainCommands`.

The LP-100A has no asynchronous push — all telemetry is polled. The server must
own the polling loop; it must never expose raw serial access to clients.

### Removed views

The vendor app exposes five display modes; we mirror only **Normal** (power +
SWR bar graphs) and **Vector** (|Z|, phase, R, X, polar). dBm/RL, Direct Input
(Field Strength), and Peak-to-Avg were removed because the meter's serial
Power and dBm fields can read 0 / noise floor (~0.8 dBm) even while the LCD
shows real measurements during TX — Z, phase, and SWR remain correct, so the
two views we keep stay accurate. This is a meter-side serial-protocol quirk,
not a parser bug; do not "fix" it by re-deriving Power from dBm.

## Stack decisions (locked in)

- **Language:** Go. Single static binary per platform, no runtime to install.
  Deps in use: `go.bug.st/serial` (cross-platform serial, pure Go — no CGO),
  `gorilla/websocket` (WS), `BurntSushi/toml` (config), `log/slog` (stdlib
  structured logging).
- **Deployment:** cross-platform — Windows (shack PC), Linux (Raspberry Pi at
  the radio), macOS (dev). Same binary surface, different serial device names
  (`COMx` vs `/dev/ttyUSB*` vs `/dev/cu.*`). The Pi is the primary target;
  see [README.md](README.md) for the install / redeploy flow.
- **Auth:** none. Bind to LAN; network-level trust only. Document this loudly.
  `/ws` keeps gorilla's same-origin check (`CheckOrigin` left nil) — never
  reintroduce `return true`. For a login + TLS, bind `127.0.0.1` behind a
  reverse proxy (README "Security").

## HTTP surface

The same binary serves both the WebSocket and the embedded web client:

| Path             | Method     | Purpose                                                                |
|------------------|------------|------------------------------------------------------------------------|
| `/`              | GET        | Embedded reference web client (`internal/web/static/index.html`)       |
| `/ws`            | GET (WS)   | WebSocket endpoint — telemetry + control verbs (see `PROPOSAL.md` §4)  |
| `/healthz`       | GET        | Liveness probe (returns `ok\n`)                                        |
| `/api/config`    | GET        | UI bootstrap: `{views, allow_control}`                                 |
| `/api/log-level` | GET / POST | Read or change the running server's slog level (`error`/`warn`/`info`/`debug`). In-memory only — restart resets to the value of `-v`. |

The default log level is **error** so the journal stays quiet on a Pi; pass
`-v` to start at debug, or flip it at runtime via `/api/log-level` (the web
client's SETUP overlay has a picker for this).

See `PROPOSAL.md` for the full design.
