# Station ↔ Voxel Media Streaming — design

## What this is

Integration of `mediateca-station` with the existing Voxel Media Streaming
stack on the location mini-PC (`media-voxel`). Station remains the Hub client
and wall-clock scheduler. Video bytes reach Android TVs through Voxel's
`broadcast` binary (HTTP MPEG-TS). TVs run Voxel Media Stream
(`com.voxel.mediastream`) with the HTTP agent on `:8989`.

This document extends
[`2026-10-03-station-agent-design.md`](./2026-10-03-station-agent-design.md).
Where they conflict, this document wins for the Voxel production path.
The VLC + `httpmedia` path stays available for lab/compose only.

## Why this shape

- Hub already owns timed playlists (`starts_at`) and proof-of-play.
- Voxel already owns a proven live TS fan-out (`broadcast`) and a TV player
  that tolerates PTS jumps from spliced `.ts` files (libVLC).
- Stock VLC on Android TV is not the production player on `media-voxel`.
- Continuous `playlist_player` loops do not match Hub wall-clock semantics.
- The mini-PC is not reachable from the Hub; control must be station-initiated
  poll/push over the existing agent Bearer token.

Chosen approach: **hybrid air + orchestrated Voxel runtime + station-owned TV**,
delivered as **one process, two milestones** (M1 air, M2 Hub control plane).

Rejected:

- Pure continuous Voxel playlists (loses Hub `starts_at` / play_events fidelity).
- Porting all Voxel Go utilities into station in v1 (slow; ops already on disk).
- Rewriting `playlist.conf` and restarting the channel per clip (reconnect flash).
- Hub pushing TCP into the site LAN (no inbound path).
- Two daemons (`station` + `tv`) for v1 (extra ops surface).

## Decisions

| Topic | Decision |
| --- | --- |
| Air model | Wall-clock + `play_events` in station; bytes via Voxel `broadcast` |
| Voxel role | Runtime: `broadcast` binary + APK / `:8989`. Not `playlist_player` |
| Timeline → stream | Station rate-limited feeder writes `.ts` into a per-screen FIFO |
| TV ownership | Station fully owns health, wake/sleep, capture, APK |
| Unused for air | `playlist_player`, `tv_verifier`, `controller`, Voxel `broadcast_schedule` |
| Hub reachability | Station polls Hub; Hub never dials the mini-PC |
| NICs | Always at least two: `eth0` TV LAN, `eth1` administration LAN to the Hub |
| Delivery | M1 air → M2 telemetry/commands; one binary |
| Source of truth on PC | Station YAML config, not `broadcast.conf` / `stream_config.json` |

## Architecture

```
Broadcast Hub (Rails)
  │  GET  /api/agent/v1/config
  │  GET  /api/agent/v2/package
  │  GET  /api/agent/v1/commands          (M2)
  │  POST /api/agent/v1/play_events
  │  POST /api/agent/v1/telemetry         (M2)
  │  POST /api/agent/v1/command_results   (M2)
  ▼
┌──────────────────────────────────────────────────────────┐
│  mediateca-station (systemd, one process)                │
│  hub · cache · schedule · report                         │
│  feeder   — paced .ts → FIFO                             │
│  voxel    — lifecycle of broadcast binary                │
│  tv       — :8989 + adb + WOL + screencap/record + APK   │
│  health   — watchdog from Hub tv_monitor params          │
│  commands — poll / execute / ack                         │
└──────────────┬────────────────────────────┬──────────────┘
               │ FIFO                       │ HTTP :8989 / adb
               ▼                            ▼
        broadcast :809x              Voxel APK on Android TV
        bind 192.168.33.1            URL http://192.168.33.1:809x/
```

The Hub path is `eth1`. Television traffic and `broadcast` stay on `eth0`.

### Package responsibilities

| Package | Job |
| --- | --- |
| `internal/config` | YAML load/validate; new Voxel/TV fields |
| `internal/hub` | Existing client + telemetry, commands, results |
| `internal/cache` | Unchanged: download prepared `.ts` by `media.id` |
| `internal/schedule` | Unchanged pure timeline rules |
| `internal/report` | Unchanged play_events queue |
| `internal/feeder` | Open cached file at `starts_at`; write paced chunks to FIFO |
| `internal/voxel` | Create FIFO; start/stop/restart `broadcast`; pid files |
| `internal/tv` | Replace VLC player: agent + adb + WOL + capture + APK |
| `internal/health` | Periodic checks; auto `restart_stream` per Hub params |
| `internal/commands` | Drain Hub command queue; one command per screen at a time |
| `internal/agent` | Wire loops: poll, feed, play, report, health, commands |
| `internal/httpmedia` | Lab/compose with VLC only; not on Voxel air path |
| `internal/player` | Legacy VLC adb helper; superseded by `internal/tv` on Voxel path |

`api_server` (:8081) on the mini-PC may remain for local ops. It is not on
station's critical path and must not start/stop air that station owns.

## Network

The location mini-PC always has at least two Ethernet interfaces. They are
separate planes: Hub traffic does not leave `eth0`, and television traffic
does not leave `eth1`.

| Interface | Role | Address | MAC |
| --- | --- | --- | --- |
| `eth0` | Television LAN | `192.168.33.1/24` | `e8:ff:1e:dc:23:56` |
| `eth1` | Administration LAN, path to Broadcast Hub | `192.168.1.204/24` | `d0:37:45:78:7e:61` |

Both are up (`UP,BROADCAST,RUNNING,MULTICAST`), MTU 1500, txqueuelen 1000.

### eth0 — televisions

The television subnet is plugged into `eth0`:

```text
eth0: flags=4163<UP,BROADCAST,RUNNING,MULTICAST>  mtu 1500
        inet 192.168.33.1  netmask 255.255.255.0  broadcast 192.168.33.255
        ether e8:ff:1e:dc:23:56  txqueuelen 1000  (Ethernet)
```

- Subnet `192.168.33.0/24`. The mini-PC address on this plane is `192.168.33.1`.
- Every television is on this subnet and receives a fixed address by DHCP
  from `192.168.33.0/24`.
- The DHCP service runs on the mini-PC (`dnsmasq`). It is a host service,
  not a process inside station.
- Addresses of connected televisions are in `/var/lib/misc/dnsmasq.leases`.
- `broadcast` binds `broadcast_bind` (`192.168.33.1`). Stream URLs, the Voxel
  agent (`:8989`), and ADB use addresses from this subnet.

### eth1 — administration

`eth1` is the administration network through which the mini-PC talks to
Broadcast Hub:

```text
eth1: flags=4163<UP,BROADCAST,RUNNING,MULTICAST>  mtu 1500
        inet 192.168.1.204  netmask 255.255.255.0  broadcast 192.168.1.255
        ether d0:37:45:78:7e:61  txqueuelen 1000  (Ethernet)
```

- Subnet `192.168.1.0/24`. The mini-PC address on this plane is `192.168.1.204`.
- Station reaches the Hub through this interface: config, package, media
  download, play events, and (M2) telemetry, commands, and command results.
- The Hub never dials the mini-PC.

## Config

Path: `/etc/mediateca-station/config.yaml` (mode `0600`, refuse group/world bits).

```yaml
hub_base_url: "https://hub.example"
agent_token: "<token>"
late_threshold: "2s"
poll_interval: "1m"
data_dir: "/var/lib/mediateca-station"

# Voxel stream plane (production)
broadcast_bind: "192.168.33.1"
broadcast_bin: "/home/stream/bin/broadcast"
fifo_dir: "/tmp"
voxel_apk: "/home/stream/apk/voxel-debug.apk"
chunk_size: 18800          # bytes per write; align with Voxel TS chunks
feeder_pace: "realtime"    # realtime | max (tests)

# Lab-only (optional). Omit on media-voxel.
# http_listen: "127.0.0.1:8080"
# http_secret: "<at least 32 chars>"

screens:
  - screen_id: 7
    adb_serial: "192.168.33.182:5555"
    stream_port: 8091
    mac: "78:53:33:4b:2c:29"   # WOL; required for wake command / health wake
```

Validation:

- `broadcast_bind`, `broadcast_bin`, `fifo_dir`, `voxel_apk` required on Voxel path.
- Each screen: unique `screen_id`, unique `stream_port`, `adb_serial` with `:`,
  `mac` required when power commands or health-wake are enabled (M2; in M1
  warn once if missing).
- Lab path: if `http_listen` + `http_secret` set and `broadcast_bin` omitted,
  retain VLC/`httpmedia` behaviour from the 2026-10-03 design.

TV stream URL (no path secret; eth0 is TV-only as in Voxel):

```text
http://<broadcast_bind>:<stream_port>/
```

## Hub contract

Auth: `Authorization: Bearer <agent_token>` on all `/api/agent/*`. Media
downloads still use signed Active Storage URLs without the bearer token.

### Existing (unchanged semantics)

- `GET /api/agent/v1/config`
- `GET /api/agent/v2/package` (ETag / If-None-Match)
- `POST /api/agent/v1/play_events`

### Config extension

Optional object (defaults apply in M1 if absent):

```json
{
  "station_id": 3,
  "offline_cache_hours": 24,
  "screens": [
    { "id": 7, "name": "Entrance", "orientation": "landscape" }
  ],
  "tv_monitor": {
    "health_interval_seconds": 30,
    "auto_restart_when_not_playing": true,
    "telemetry_interval_seconds": 60
  }
}
```

Defaults when `tv_monitor` missing: health 30s, auto_restart true, telemetry 60s.

### `POST /api/agent/v1/telemetry` (M2)

One request per telemetry interval:

```json
{
  "reported_at": "2026-10-09T11:00:00Z",
  "station": {
    "uptime_seconds": 3600,
    "package_etag": "sha256…",
    "broadcast_ok": true
  },
  "screens": [
    {
      "screen_id": 7,
      "reachable": true,
      "agent_ok": true,
      "playing": true,
      "url": "http://192.168.33.1:8091/",
      "wakefulness": "Awake",
      "stream_port": 8091,
      "broadcast_clients": 1,
      "last_error": null
    }
  ]
}
```

Success: HTTP 202 or 204. Network/5xx: keep next interval. HTTP 401: pause
telemetry, commands, and play_events drain until a later config/package poll
returns 200 or 304 (same rule as the existing reporter).

### `GET /api/agent/v1/commands` (M2)

Polled each `poll_interval` (and may be polled faster when a non-empty queue
was just seen — implementation detail, max once per 5s).

HTTP 200:

```json
{
  "commands": [
    {
      "id": "cmd_01J…",
      "screen_id": 7,
      "type": "restart_stream",
      "params": {},
      "issued_at": "2026-10-09T11:01:00Z"
    }
  ]
}
```

Empty queue: `{"commands":[]}`. Optional `ETag` / `If-None-Match` like package.

v1 commands are **per-screen only** (`screen_id` required).

### Command types

| type | params | station action |
| --- | --- | --- |
| `start_stream` | optional `url`, `buffer_ms` | `POST :8989/api/start_stream` (default URL = channel URL) |
| `restart_stream` | — | `POST :8989/api/restart_stream` |
| `stop` | — | `POST :8989/api/stop` |
| `wake` | — | adb `KEYCODE_WAKEUP`; if still asleep → WOL by `mac` |
| `sleep` | — | adb `KEYCODE_SLEEP` |
| `screenshot` | — | adb screencap+pull; upload via command_results |
| `record` | `seconds` (1–120) | adb screenrecord; upload via command_results |
| `ensure_app` | — | health agent; install/launch APK if needed |
| `install_apk` | `url` (https) or omit to use local `voxel_apk` | download if url; `adb install -r`; launch |

Unknown `type`: finish with `ok: false`, `error: "unknown_type"`; do not crash.

### `POST /api/agent/v1/command_results` (M2)

```json
{
  "results": [
    {
      "command_id": "cmd_01J…",
      "ok": true,
      "error": null,
      "finished_at": "2026-10-09T11:01:02Z"
    }
  ]
}
```

For `screenshot` / `record`: Hub puts a signed `params.upload_url` on the
command; station HTTP PUTs the bytes to that URL, then posts
`command_results` with `ok` and no file body. If `upload_url` is absent,
station posts multipart `file` on `command_results` (fallback for small
screenshots only).

Hub treats duplicate `command_id` as already applied (HTTP 200).

Auto health restarts **do not** invent command ids; they only appear in logs
and telemetry (`last_error` / state transitions).

## Data flow (Voxel path)

1. Startup: load stored config/package; ensure FIFO + `broadcast` per mapped
   screen; start health; do not wait for live Hub.
2. Poll: GET config, GET package (If-None-Match), (M2) GET commands, POST
   telemetry on its interval.
3. Cache: same as 2026-10-03 (partial → rename, URL sidecar, prune on 200).
4. Schedule wake → for each screen action:
   - **Start clip:** if late rules pass and file ready → feeder begins writing
     that file to the screen FIFO; `POST :8989/api/start_stream` with channel
     URL; sample `started_at` immediately before successful start-of-feed (and
     after agent accepted start); enqueue play_event only when both feed start
     and agent start succeeded.
   - **Join ≤1s:** switch feeder to next file without stopping the TV player.
   - **Gap >1s:** stop file feed; keep `broadcast` up; write MPEG-TS null
     packets into the FIFO (same idea as Voxel EOF resync) so the HTTP client
     does not stall. Do not force-stop the player during a short gap.
   - **Idle (no covering entry / end of day):** stop file feed; `POST :8989/api/stop`
     so the player does not spin on reconnect UI. Leave `broadcast` running.
   - **Package swap:** same covering-key rules as 2026-10-03.
5. Reporter drains `events.jsonl` independently.

## Feeder

- One goroutine (or worker) per screen; does not block other screens.
- Read from `data_dir/media/<id>`; write to `{fifo_dir}/stream_{port}.ts`.
- Pace: default realtime aligned to wall clock for the clip duration; chunk
  size `chunk_size` (default 18800).
- Gap >1s: write null packets only (no silence file required in v1).
- Late / skip / conflict / played-set: unchanged from schedule package.
- Mid-clip process restart: do not resume (skip remainder); wait for next
  `starts_at`.
- Missing file at start instant: skip + log `missing_file`; no play_event.

## Voxel adapter

- Start: `mkfifo` if needed; exec
  `broadcast_bin <fifo> :<stream_port>` with working assumptions matching
  Voxel (`broadcast` binds `stream_server_ip` / we pass bind via env or rely on
  binary default `192.168.33.1` — station sets `broadcast_bind` in config and
  documents required Voxel binary behaviour: listen on that IP).
- If the stock `broadcast` hardcodes `192.168.33.1`, `broadcast_bind` must equal
  that address on the host; changing bind requires a Voxel binary that reads
  bind address (out of station scope unless we patch Voxel separately).
- Pid + logs under `data_dir/voxel/` (or `log_dir` if configured).
- On crash: restart broadcast; feeder reopens FIFO; do not double play_events.
- Do **not** invoke Voxel `start_broadcast` / `stop_broadcast` (they start
  `playlist_player`).
- Do **not** treat `/home/.config/broadcast.conf` as source of truth.

## TV control

Per-screen mutex. Timeouts: 5s for agent HTTP; 15s for adb install / record.

| Action | Mechanism |
| --- | --- |
| Status | `GET http://<tv-ip>:8989/api/status` |
| Start | `POST /api/start_stream` `{url, buffer_ms}` |
| Restart | `POST /api/restart_stream` |
| Stop | `POST /api/stop` |
| Wake | `adb shell input keyevent KEYCODE_WAKEUP`; fallback WOL |
| Sleep | `adb shell input keyevent KEYCODE_SLEEP` |
| Screenshot | `adb exec-out screencap -p` |
| Record | `adb shell screenrecord --time-limit N …` + pull |
| ensure_app | if agent down: `adb install -r voxel_apk` + start MainActivity |

`adb_serial` host part is the TV's fixed DHCP address on `192.168.33.0/24`
(see Network; live table `/var/lib/misc/dnsmasq.leases`). That host is the
IP used in agent URLs.

## Health

- Interval from `tv_monitor.health_interval_seconds`.
- When a covering schedule entry exists (on air): ping → agent status → if
  `playing=false` and `auto_restart_when_not_playing` → `restart_stream`;
  check `mWakefulness`; wake if asleep.
- When idle: do not auto-restart playback loops; do not auto-sleep (sleep only
  via Hub command in v1).
- On transition into error: optional screenshot retained under
  `data_dir/diagnostics/` and referenced in telemetry `last_error`.

## Errors

| Failure | Behaviour |
| --- | --- |
| Hub 401 | Pause reporter, telemetry, commands; keep air from cache |
| Hub 5xx / network | Retry next interval; keep air |
| Download fail / disk full | Keep previous complete file; new URL not ready |
| broadcast crash | Restart binary; reopen FIFO |
| Feeder backpressure | Log warn; other screens continue |
| Command failure | `command_results` with `ok:false`; continue queue |
| Unknown command type | `ok:false`, `unknown_type` |

## Milestones

### M1 — Air on media-voxel

- Feeder + voxel adapter + tv start_stream + play_events.
- Local health with config defaults (no Hub commands/telemetry required).
- Disable or mask Voxel `tv_verifier` / `controller` air ownership on the host
  so they do not fight station (`broadcast_schedule` empty / units stopped).
- Operator smoke: one screen, one cached clip, Hub play_event appears.

### M2 — Control plane

- Hub implements `tv_monitor`, telemetry, commands, command_results.
- Station polls/executes all command types in this spec.
- Hub UI/ops can wake, sleep, restart, screenshot, ensure APK, tune intervals.

Hub Rails work is a separate workstream; this repo ships the station side and
the contract above. OpenAPI-style examples in this doc are normative for both.

## Tests

`go test ./...` without real TVs or Voxel binaries:

- Existing schedule / report / cache / hub client regressions.
- Feeder: fake clock + pipe; start at threshold; no start past late; null
  packets on gap; join without gap restart.
- Voxel adapter: fake exec records argv (`broadcast`, fifo, port).
- TV: `httptest` agent + fake adb argv recorder.
- Commands: poll → run → result; unknown type; per-screen mutex ordering.
- Health: not playing + auto_restart → one restart_stream; idle → no restart.

Manual on `media-voxel` after M1/M2.

## Logging

`log/slog` to stdout/journald. Info: poll, feed start/stop, command done.
Error: adb/agent failure, broadcast restart, mapping mismatch. Never log
`agent_token` or Authorization headers.

## Done when

- An implementer can build M1 and M2 from this document and the 2026-10-03
  design without reading Voxel sources beyond the cited binaries/APIs.
- `go test ./...` passes offline.
- On `media-voxel`, M1 plays Hub-timed `.ts` through `broadcast` into Voxel APK
  and reports play_events; M2 accepts Hub commands and posts telemetry.
- Voxel `playlist_player` / `tv_verifier` are not required for Mediateca air.

## Relation to Voxel docs

Upstream references (not copied into this repo):

- `stream_project/DOC/STREAM-server/CONSOLIDATED_DOCS.md` — broadcast, API,
  tv_verifier behaviour we replace or reuse selectively.
- `stream_project/DOC/voxel-media-player/TZ_ANDROID_PLAYER.md` — agent `:8989`.
- `stream_project/DOC/STREAM-server/CHANGELOG.md` — FanOut, WOL, schedule notes.
