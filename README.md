# mediateca-station

Station process for one location. It plays playlists that the Broadcast Hub
already built. It does not create playlists, orders, or `.ts` files.

The mini-PC downloads the hub package (`GET /api/agent/v2/package`) and screen
list (`GET /api/agent/v1/config`). Video in that package is already MPEG-TS.
The process serves those files on the location LAN and tells each Android TV
to open the URL in VLC. Proof of play is `POST /api/agent/v1/play_events`.

## Television

On each Android TV:

1. Install VLC from the store. The package name must be `org.videolan.vlc`.
2. Enable network ADB. Note the serial as `host:5555`.
3. Mount the set. This process does not rotate portrait versus landscape.

## Mini-PC

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

## Docker

`compose.yaml` runs this process on the Broadcast Hub Compose network
(`mediateca_broadcast_default`) so it can call the hub the way a mini-PC
would. Start the hub first. Copy `.env.example` to `.env`, set `AGENT_TOKEN`
and `SCREEN_ID`, then `docker compose up --build`. The entrypoint writes a
mode-0600 config and calls the hub container by IP, because development
rejects the hostname `web`.

`docker compose --profile tv up --build` also starts an Android TV 9 emulator
with VLC. It needs `/dev/kvm`. The station connects to `tv:5555` and serves
clips on the address VLC can reach. Watch the screen with
`scrcpy -s emulator-5554`. Host adb names this published port
`emulator-5554`, not `127.0.0.1:5555`.

## Logs

```bash
journalctl -u mediateca-station -f
```

`skip` lines use `reason=late`, `reason=missing_file`, `reason=conflict`, or
`reason=ignored_entry`. `hub token rejected` means the bearer token was
refused; playback of the stored package continues, and new play reports wait
until a later poll succeeds. The token and the HTTP secret are not written to
the log.
