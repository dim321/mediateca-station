# mediateca-station

Station process for one location. It plays playlists that the Broadcast Hub
already built. It does not create playlists, orders, or `.ts` files.

The mini-PC downloads the hub package (`GET /api/agent/v2/package`) and screen
list (`GET /api/agent/v1/config`). Video in that package is already MPEG-TS.
Proof of play is `POST /api/agent/v1/play_events`.

**Lab (VLC):** serve clips over HTTP and open them in VLC via ADB — see
[Television](#television) and [Mini-PC](#mini-pc).

**Production (`media-voxel`):** feed per-screen FIFOs through the Voxel
`broadcast` binary and drive the Voxel APK agent on each TV (`:8989`). No VLC
and no clip HTTP server — see [Voxel production path](#voxel-production-path).

## Television

Lab / VLC path only.

On each Android TV:

1. Install VLC from the store. The package name must be `org.videolan.vlc`.
2. Enable network ADB. Note the serial as `host:5555`.
3. Mount the set. This process does not rotate portrait versus landscape.

## Mini-PC

Lab / VLC path. On `media-voxel`, use the same install steps below but the
[Voxel config](#voxel-production-path) instead of `http_listen` / `http_secret`.

Install the `adb` binary and an NTP client (chrony or systemd-timesyncd).
The station clock is the clock that starts clips. Do not start the process
until the clock has stepped.

```bash
go build -o mediateca-station ./cmd/station
sudo install -m 0755 mediateca-station /usr/local/bin/mediateca-station
sudo useradd --system --create-home --home-dir /var/lib/mediateca-station mediateca
sudo install -d -m 0700 -o mediateca -g mediateca /var/lib/mediateca-station
sudo install -d -m 0750 /etc/mediateca-station
```

Write `/etc/mediateca-station/config.yaml` and `chmod 0600` it. The process
refuses a config that is group or world readable.

```yaml
hub_base_url: "https://hub.example"
agent_token: "<token the hub showed once>"
http_listen: "192.168.1.10:8080"
http_secret: "<at least 32 characters>"
late_threshold: "2s"
poll_interval: "1m"
data_dir: "/var/lib/mediateca-station"
screens:
  - screen_id: 7
    adb_serial: "192.168.1.21:5555"
```

`http_listen` is the address the TVs can reach. `screen_id` is the hub screen
id. `adb_serial` is that TV.

Allow the `mediateca` user to run `adb` (the binary and `~/.android`).

`/etc/systemd/system/mediateca-station.service`:

```ini
[Unit]
Description=Mediateca station
After=network-online.target chronyd.service
Wants=network-online.target chronyd.service

[Service]
ExecStart=/usr/local/bin/mediateca-station -config /etc/mediateca-station/config.yaml
Restart=always
RestartSec=2
User=mediateca
NoNewPrivileges=true

[Install]
WantedBy=multi-user.target
```

If this image uses `chrony.service` or `systemd-timesyncd.service` instead of
`chronyd.service`, put that unit name in both `After=` and `Wants=`.

```bash
sudo systemctl daemon-reload
sudo systemctl enable --now mediateca-station
```

## Voxel production path

Use this on the location mini-PC (`media-voxel`) when `broadcast_bin` is set in
config. Station owns the air timeline: Hub schedule → feeder → FIFO →
`broadcast` → TV APK. Do not run the legacy Voxel air stack in parallel.

### Retire conflicting Voxel services

Stop and disable anything that still starts or verifies air outside station:

```bash
sudo systemctl stop tv-verifier.service
sudo systemctl disable tv-verifier.service
```

Do not run the host `controller` for air. If the old stack reads Hub
`broadcast_schedule`, leave that schedule empty for this location so only
station drives playback.

### Television (Voxel)

On each Android TV:

1. Install the Voxel player APK (same build as `voxel_apk` on the mini-PC).
2. Enable network ADB. Note the serial as `host:5555`.
3. Confirm the APK agent answers on port `8989` on the TV LAN address.

VLC is not used on this path.

### Config

Required YAML keys: `broadcast_bind`, `broadcast_bin`, `fifo_dir`, `voxel_apk`.
Each screen needs `screen_id`, `adb_serial` (`host:port`), and a unique
`stream_port`. Optional: `chunk_size`, `feeder_pace`, per-screen `mac` (M2
power/wake).

`http_listen` and `http_secret` are **not** required when `broadcast_bin` is
set; omit them on `media-voxel`. The stream URL for a screen is
`http://<broadcast_bind>:<stream_port>/`.

Example (matches the Voxel integration spec):

```yaml
hub_base_url: "https://hub.example"
agent_token: "<token>"
late_threshold: "2s"
poll_interval: "1m"
data_dir: "/var/lib/mediateca-station"

broadcast_bind: "192.168.33.1"
broadcast_bin: "/home/stream/bin/broadcast"
fifo_dir: "/tmp"
voxel_apk: "/home/stream/apk/voxel-debug.apk"
chunk_size: 18800
feeder_pace: "realtime"

screens:
  - screen_id: 7
    adb_serial: "192.168.33.182:5555"
    stream_port: 8091
    mac: "78:53:33:4b:2c:29"
```

Install the binary, systemd unit, and `adb` access as in [Mini-PC](#mini-pc).
Ensure `broadcast_bin` exists, `fifo_dir` is writable by `mediateca`, and TVs
can reach `broadcast_bind` on each `stream_port`.

### Smoke test on media-voxel

1. Hub has a near-future schedule entry for the screen; wait for poll (or
   restart station) so one clip is cached under `data_dir/media/`.
2. When the entry goes on air, watch logs:

   ```bash
   journalctl -u mediateca-station -f
   ```

   Expect a `start` line with `screen_id` and `media_id` (not `air start
   failed` or repeated `skip` for that entry).
3. In the Hub, confirm a play event for that screen and media asset.

## Hub control plane (M2)

On the Voxel path, station still polls config and package and drains
`play_events`. Hub must also expose the M2 agent routes (Bearer token, same as
today):

| Method | Path |
| --- | --- |
| `POST` | `/api/agent/v1/telemetry` |
| `GET` | `/api/agent/v1/commands` |
| `POST` | `/api/agent/v1/command_results` |

Hub config may include `tv_monitor` (`health_interval_seconds`,
`auto_restart_when_not_playing`, `telemetry_interval_seconds`). Normative JSON
examples and status codes are in the English integration spec:
[`docs/superpowers/specs/2026-10-09-station-voxel-integration-design.md`](docs/superpowers/specs/2026-10-09-station-voxel-integration-design.md).

### Command types

Per-screen commands (`screen_id` required). Full params and Hub behaviour are
in the spec; station maps types as follows:

| type | params | station action |
| --- | --- | --- |
| `start_stream` | optional `url`, `buffer_ms` | Voxel agent start (default URL = channel URL) |
| `restart_stream` | — | agent restart |
| `stop` | — | agent stop |
| `wake` | — | adb wakeup; WOL if still asleep |
| `sleep` | — | adb sleep |
| `screenshot` | optional `upload_url` | screencap; PUT bytes if URL set |
| `record` | `seconds`, optional `upload_url` | screenrecord; PUT file if URL set |
| `ensure_app` | — | install/launch `voxel_apk` if needed |
| `install_apk` | optional `url` | download or use local APK; install + launch |

Set **`mac`** on each screen when Hub may send **`wake`** or when health should
WOL a TV that stays asleep after the adb keyevent. Without `mac`, wake is
adb-only.

For **`screenshot`** and **`record`**, Hub should put a signed
**`params.upload_url`** on the command. Station HTTP PUTs the artifact to that
URL, then posts `command_results` with `ok` / `error` only (no file in the JSON
body).

HTTP **401** on telemetry or commands pauses telemetry, command polling, and
`play_events` drain until a later config or package poll succeeds (same rule as
the existing play reporter). Air from cache continues.

## Docker

Development Compose reproduces the mini-PC's two Ethernet planes.

| Plane | Interface | Network | Address |
| --- | --- | --- | --- |
| Televisions | `eth0` | `television` `192.168.33.0/24` | station `192.168.33.1`, emulator `192.168.33.182` |
| Administration, path to Broadcast Hub | `eth1` | external `mediateca_broadcast_default` | assigned by the Hub project |

Hub traffic stays on `eth1`. Clip HTTP and ADB stay on `eth0`. The process binds media on `192.168.33.1:8080` only. From the host that URL is `http://192.168.33.1:8080` through the television bridge.

Start the Hub first. Copy `.env.example` to `.env`, set `AGENT_TOKEN` and `SCREEN_ID`, then `docker compose up --build`. The entrypoint writes a mode-0600 config and calls the hub container by IP, because development rejects the hostname `web`.

`docker compose --profile tv up --build` also starts an Android TV 9 emulator with VLC at `192.168.33.182`. It needs `/dev/kvm`. The station connects to `192.168.33.182:5555`, and VLC opens `http://192.168.33.1:8080/...`. Watch the screen with `scrcpy -s emulator-5554`. Host adb names this published port `emulator-5554`, not `127.0.0.1:5555`.

## Logs

```bash
journalctl -u mediateca-station -f
```

`skip` lines use `reason=late`, `reason=missing_file`, `reason=conflict`, or
`reason=ignored_entry`. `hub token rejected` means the bearer token was
refused; playback of the stored package continues, and new play reports wait
until a later poll succeeds. The token and the HTTP secret are not written to
the log.
