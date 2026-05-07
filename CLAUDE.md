# LP-100A Server

A WebSocket server that bridges the **Telepost LP-100A** RF Power Meter to multiple
networked client applications. One server process owns the serial connection to the
meter; many clients subscribe to telemetry and may issue control actions. When any
client invokes an action, the resulting state change is broadcast to all clients.

## Repository layout

```
LP-100A-Server/
├── CLAUDE.md         # this file
├── PROPOSAL.md       # design proposal for the server + clients
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
  | Peak/Hold     |     33 |     1 | `0`=Average `1`=Peak Hold `2`=Fast (Tune)          |
  | dBm           |     35 |     4 | Float; subtract 30 for dBW                         |
  | SWR           |     40 |     4 | Float                                              |

- **Control commands** (single ASCII char, no terminator required):
  - `A` — increment SWR Alarm set point (off→1.5→2.0→2.5→3.0→user→off)
  - `M` — increment Mode (Average→Peak Hold→Fast)
  - `F` — toggle Peak/Avg/Tune (alias used by the flow's UI button)

- **Validation:** discard any frame whose first byte is not `;` or whose length
  is not 43. The Node-RED flow's `Valid CK` function enforces this.

The LP-100A has no asynchronous push — all telemetry is polled. The server must
own the polling loop; it must never expose raw serial access to clients.

## Stack decisions (locked in)

- **Language:** Go. Single static binary per platform, no runtime to install.
  Likely deps: `go.bug.st/serial` (cross-platform serial), `nhooyr.io/websocket`
  or `gorilla/websocket` (WS).
- **Deployment:** cross-platform — Windows (shack PC), Linux (Raspberry Pi at
  the radio), macOS (dev). Same binary surface, different serial device names
  (`COMx` vs `/dev/ttyUSB*` vs `/dev/cu.*`).
- **Auth:** none. Bind to LAN; network-level trust only. Document this loudly.

See `PROPOSAL.md` for the full design.
