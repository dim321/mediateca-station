# Station agent — design

## What this is

A new repository, `mediateca-station`, implements the station. The Broadcast Hub
in this repository stays unchanged. The station is a Go process on a Linux
mini-PC at the location. It pulls the station's daily playlist, caches the
media the hub already prepared, and shows it on that station's screens.

Each screen is an Android TV running stock VLC (`org.videolan.vlc`). The
mini-PC does not decode video. It serves cached files over the location LAN
and, over `adb`, tells VLC when to open or leave a URL. There is no app to
install on the TV beyond VLC, and no frame-accurate sync between TVs.

Playback follows wall-clock instants from the hub. A clip starts at its
`starts_at` if the mini-PC is at most 2 seconds late and the file is already
on disk. A later clip is skipped. The play report carries the time just before
the start command, and only after `adb` succeeds.

Video on air is the transcoded MPEG-TS file (`video/mp2t`). The station never
downloads or plays the original upload. The hub already omits a video that has
no `.ts`. A non-video item, when the hub includes one, is the original file
and is shown for `duration_seconds` with the same start and stop rules.

## Why this shape

The hub's agent contract is already the timed playlist
(`GET /api/agent/v2/package`), the screen list
(`GET /api/agent/v1/config`), and proof-of-play
(`POST /api/agent/v1/play_events`). v1 package JSON stays for older agents and
this station does not call it.

Chosen runtime: one Go daemon under systemd, stock VLC, LAN HTTP for bytes,
ADB only for start and stop. Rejected: a VLC remote-control session (unreliable
on Android TV) and a custom player on each TV (a second artifact, and VLC was
the chosen player). Rejected: copying every file onto the TV with `adb push`
(too slow for a clock-aligned start). Rejected: the mini-PC driving HDMI
itself (the screens are Android TVs).

## Decisions already made

- **Process.** One long-running Go binary, module `mediateca-station`, Go 1.22
  or newer. systemd `Restart=always`. A panic exits the process; systemd
  brings it back. The binary stays up when every screen is idle or mis-mapped.
  It exits non-zero only when the config file is missing, unreadable, or
  invalid, so a bad config does not crash-loop quietly.
- **Hub base.** Config supplies `hub_base_url` (no trailing path). All API
  paths below are appended to it.
- **Auth.** `Authorization: Bearer <agent_token>` on `/api/agent/*` only.
  The token is not sent when downloading media.
- **Clocks.** The mini-PC clock is the only clock that matters. The image runs
  chrony (or another NTP client) synced to UTC. TV clocks are ignored.
  Comparisons use the absolute `starts_at` instants. The station does not
  reinterpret them in the location time zone.
- **Late threshold.** Default 2 seconds, key `late_threshold` in config.
- **Poll.** Default every 1 minute, key `poll_interval`. Config and package
  are both fetched each poll.
- **Screen binding.** Local config maps `screen_id` to an ADB serial
  `host:5555`. The hub does not know serials. A hub screen with no serial is
  not played. A serial whose `screen_id` is absent from the latest hub config
  is not played. Both cases are logged at error level once per change, not on
  every tick.
- **Orientation.** Hub config includes `landscape` or `portrait`. v1 logs it
  and does not rotate the television. Mounting is physical.
- **HTTP for TVs.** Listens on `http_listen` (`host:port` from config). Every
  media path is `/m/<http_secret>/<media_id>`. Any other path, and a wrong
  secret, is 404 with an empty body. No directory listing. `http_secret` is a
  string of at least 32 characters. A missing file is 404. A `Range` the file
  cannot satisfy is 416.
- **Secrets on disk.** The process refuses to start when the config file mode
  allows group or world access (any bit in `0077`). Token and HTTP secret are
  never written to the log.
- **Out of v1.** Heartbeat and alerts (the hub has no such route), TV power
  and CEC, HLS, frame sync, a companion APK, edits to the hub, and playback of
  original video uploads.

## Components

Each package has one job. `cmd/station` only parses flags and calls `Run`.

| Package | Responsibility |
| --- | --- |
| `internal/config` | Load and validate YAML. |
| `internal/hub` | HTTP client for config, package, and one play event. |
| `internal/cache` | Download `media.url`, store by `media.id`, drop files the current package does not reference. |
| `internal/httpmedia` | Serve stored files to the TVs, including byte ranges. |
| `internal/schedule` | Pure timeline: given a package, a screen id, and a clock, what should happen. |
| `internal/player` | `adb connect`, VLC start, VLC force-stop. No scheduling policy. |
| `internal/report` | Durable queue of play events and the played-set. |
| `internal/agent` | Wire the loops: poll, download, schedule, play, report. |

`internal/schedule` and `internal/report` do not import the player or the hub.
Tests of those two packages need no network and no `adb`.

### Config file

Path from `-config` (README uses `/etc/mediateca-station/config.yaml`).

```yaml
hub_base_url: "https://hub.example"
agent_token: "<token issued once by the hub>"
http_listen: "192.168.1.10:8080"
http_secret: "<at least 32 bytes, hex or base64>"
late_threshold: "2s"
poll_interval: "1m"
data_dir: "/var/lib/mediateca-station"
screens:
  - screen_id: 7
    adb_serial: "192.168.1.21:5555"
```

`late_threshold` and `poll_interval` are Go durations. Missing optional
durations mean 2s and 1m. `data_dir`, `hub_base_url`, `agent_token`,
`http_listen`, `http_secret`, and at least one screen are required.
`screen_id` values are unique. `adb_serial` must contain a colon (network
ADB). Empty strings are invalid.

### On-disk state

Under `data_dir`, created `0700` if missing:

| Path | Contents |
| --- | --- |
| `config.json` | Last HTTP 200 config body, written by temp file plus rename. |
| `package.json` | Last HTTP 200 package body, written by temp file plus rename. |
| `media/<id>` | Complete file for that `media.id`. |
| `media/<id>.url` | The `media.url` string that produced the file. |
| `media/<id>.mime` | The `mime_type` string. |
| `media/<id>.partial` | In-progress download. Never served and never played. |
| `events.jsonl` | Queued play events, one JSON object per line. |
| `played.json` | Keys `screen_id:media_id:starts_at` already handed to the queue. Rewritten by temp file plus rename. |

`starts_at` in the played key is the RFC3339 UTC string from the package, not
the command timestamp.

## Hub contract the station depends on

The hub authenticates the bearer token to one station. Missing, malformed, and
unknown tokens are HTTP 401:

```json
{ "error": "unauthorized" }
```

### GET `/api/agent/v1/config`

HTTP 200:

```json
{
  "station_id": 3,
  "offline_cache_hours": 24,
  "screens": [
    { "id": 7, "name": "Entrance", "orientation": "landscape" }
  ]
}
```

`offline_cache_hours` is the hub's horizon. The station does not compute a
second horizon from it. It plays the entries of the package it has stored.
The value is logged.

### GET `/api/agent/v2/package`

HTTP 200 with an `ETag` header and `Cache-Control: private, must-revalidate`.
The station stores the raw `ETag` header value and sends it back as
`If-None-Match`. It does not rebuild that header from the JSON `etag` field:
Rails may quote it. A matching `If-None-Match` yields HTTP 304 and an empty
body. The station keeps the previously stored package.

HTTP 200 body (timed entries, never the v1 `items` shape):

```json
{
  "version": "sha256",
  "etag": "sha256",
  "generated_at": "2026-09-02T12:00:00Z",
  "valid_until": "2026-09-03T12:00:00Z",
  "entries": [
    {
      "for_date": "2026-09-02",
      "broadcast_day_starts_at": "2026-09-02T02:00:00Z",
      "position": 1,
      "offset_seconds": 0,
      "starts_at": "2026-09-02T02:00:00Z",
      "duration_seconds": 10,
      "source_kind": "service",
      "media_plan_id": null,
      "screen_ids": [7, 8],
      "media": {
        "id": 19,
        "url": "/rails/active_storage/blobs/redirect/...",
        "mime_type": "video/mp2t"
      }
    }
  ],
  "screen_map": { "7": [0, 1, 2] }
}
```

The station schedules from `entries[].screen_ids`. It does not read
`screen_map`. It does not branch on `source_kind` or `media_plan_id`.

`media.url` is a path, joined to `hub_base_url`. It is an Active Storage
signed redirect and authorizes itself. The downloader follows up to five
redirects and does not attach the bearer token to those requests. Video URLs
point at the `.ts` broadcast file. A video without that file is absent from
`entries`. Non-video uses the original file and the same `media` object.

An entry is ignored, and an error is logged, when `starts_at` is missing,
`duration_seconds` is less than or equal to zero, or `media.id` / `media.url`
/ `media.mime_type` is missing.

`valid_until` is not an extra stop. Playback ends when the stored entries for
that screen have been started or skipped. An empty `entries` array (a closed
day, or a package that has not been generated yet) means every screen is
stopped.

### POST `/api/agent/v1/play_events`

One event per request, so a rejection cannot discard a neighbor:

```json
{
  "events": [
    {
      "screen_id": 7,
      "media_asset_id": 19,
      "started_at": "2026-09-02T02:00:01Z"
    }
  ]
}
```

`started_at` is UTC RFC3339. Success is HTTP 201:

```json
{ "play_log_ids": [101] }
```

HTTP 404 is `{ "error": "not_found" }` when the screen is not on this station
or the hub will not attribute the media (no matching playlist item). HTTP 422
is `{ "error": "invalid_record", "details": ["..."] }` when the record is
invalid. The hub applies the whole `events` array or none of it; a one-element
array keeps that from mattering.

## Data flow

1. On startup, load `config.json` and `package.json` when present, then start
   the HTTP server, the reporter, and the scheduler. Do not wait for a live
   hub before playing a stored package. A screen plays only when its id is in
   the local serial map and in the latest stored hub config. When no config
   has ever been stored, the local serial map alone is the allow-list, so the
   first boot can play after the first package arrives and an offline reboot
   after a successful config poll uses `config.json`.
2. Each poll: GET config, then GET package with the stored `If-None-Match`.
3. HTTP 304 leaves the stored package and its files alone.
4. HTTP 200 from config replaces `config.json` only after the body parses.
   HTTP 200 from the package replaces `package.json` only after the body
   parses. The scheduler swaps in the new timeline before the cache deletes
   anything. The cache then downloads any `media.id` whose stored `.url`
   differs from the new `media.url`, and deletes stored ids that the new
   package does not reference. Deletion happens only after a successful
   package 200, never while the hub is down. A file whose `.url` does not
   match the current package URL is not ready, even if `media/<id>` still
   exists from the previous URL. The old bytes stay until the new download
   is renamed into place.
5. Downloads run at most two at a time. Bytes go to `media/<id>.partial` and
   are renamed to `media/<id>` only when the response is complete (HTTP 200
   and the body fully read). After that rename, the cache writes `.url` and
   `.mime`. A crash between the rename and the sidecars leaves the id not
   ready, so the next poll downloads it again. A partial file is never
   published to `httpmedia` and never selected by the scheduler.
6. The URL VLC opens is
   `http://<http_listen host>:<port>/m/<http_secret>/<media_id>`.
   The server sends the stored MIME type and honors a single `Range` of the
   form `bytes=start-end` or `bytes=start-`. VLC asks for ranges; a server
   that ignores them fails playback.
7. The reporter drains `events.jsonl` independently of the scheduler.

## Schedule

For one screen, the timeline is the entries that were not ignored and whose
`screen_ids` contain that screen, ordered by `starts_at` ascending, then by
`position` ascending. Two entries with the same `screen` and the same
`starts_at` are a conflict: the smaller `position` is the one that may play,
and the others are logged and skipped.

The scheduler sleeps until the next start or stop instant for any screen. A
package swap wakes it immediately.

Let `now` be the mini-PC clock and `late_threshold` the configured duration.

**Start.** When `now` reaches `starts_at`, start the clip if all of the
following hold:

- the played-set does not already contain `screen_id:media_id:starts_at`;
- `now <= starts_at + late_threshold`;
- `media/<id>` is complete and its `.url` sidecar equals this entry's `media.url`;
- this entry is the conflict winner for that instant.

The player connects, then starts VLC. `started_at` in the event is the UTC
time sampled immediately before the `adb` start command. The event is appended
and the played key is recorded only when that command exits 0. A non-zero
exit, a timeout, or a connect failure records nothing and does not add the
played key, so a later tick inside the late window may try again.

**Skip.** If the scheduler first observes the entry when
`now > starts_at + late_threshold`, it does not start it and does not report
it. A process restart in the middle of a clip is this case: the rest of that
clip stays dark, and the screen waits for the next `starts_at`.

**Stop.** The clip's end is `starts_at + duration_seconds`. If the next clip
on that screen has `starts_at <= end + 1s`, the scheduler does not force-stop
first; it issues the next start at the next `starts_at` (subject to the start
rules). The new intent replaces playback, which avoids a launcher flash on a
tight join. If that next start command fails, the player force-stops VLC and
still does not report the failed start. If the gap to the next `starts_at` is
longer than 1 second, or there is no next clip, the player force-stops VLC at
`end`.

The agent remembers, per screen, the entry key it last started successfully:
`screen_id`, `media.id`, and the package `starts_at`. That memory is the clip
on screen. It is process memory only; a restart does not resume a clip.

**Interrupt.** After a package swap, look at the entry that covers `now`
(`starts_at <= now < starts_at + duration_seconds`). If its key differs from
the remembered key, or nothing covers `now`, force-stop VLC and clear the
remembered key. A covering entry starts only when it still satisfies the
start rules, including the late threshold measured from its own `starts_at`.
Otherwise the screen stays stopped until a future `starts_at`. A swap that
keeps the same covering key does not restart and does not stop.

**Idle.** No current entry means VLC is force-stopped. An empty package stops
every screen on the next wake.

## Player commands

The player runs `adb` with an argument vector, not a shell string. Each serial
has its own mutex, so two screens do not interleave, and one screen does not
overlap connect, start, and stop. Each command's timeout is 5 seconds.

Connect, before every start:

```text
adb connect 192.168.1.21:5555
```

Start (MIME is the package `mime_type`, URL is the LAN URL):

```text
adb -s 192.168.1.21:5555 shell am start -a android.intent.action.VIEW -d <url> -t <mime> -p org.videolan.vlc
```

Stop:

```text
adb -s 192.168.1.21:5555 shell am force-stop org.videolan.vlc
```

Connect failure or a missing VLC package is a failed start. Other screens
continue.

## Reporter

The queue is `events.jsonl`. The reporter posts the first line, then:

| Result | Action |
| --- | --- |
| HTTP 201 | Remove that line. |
| HTTP 401 | Leave the queue. Pause posts until a later config or package poll returns 200 or 304. Playback continues. Log once that the token is rejected. |
| HTTP 404 or 422 | Drop that line and log the body. Do not retry. A stale package can be refused after the hub regenerates; retrying would spin. |
| Network error, timeout, or HTTP 5xx | Leave the line. Retry on the next poll interval. Do not block playback. |

A poll that receives 401 does not replace the package or the config and does
not delete media. The reporter stays paused until a later poll of config or
package returns 200 or 304. The stored timeline keeps playing until its
entries are exhausted.
The same is true for a network error or HTTP 5xx on the poll. A failed
download leaves the previous complete file in place when the URL has not
changed; when the URL changed, the old file stays until the new one is
complete, and the scheduler keeps treating the old file as not ready for the
new URL (the `.url` sidecar does not match), so the new clip is skipped until
the download finishes.

Disk full is a failed download. Already complete files keep playing.

## Logging

`log/slog` on stdout, which journald captures. Info: poll result, clip start,
clip stop, skip with a reason (`late`, `missing_file`, `conflict`,
`ignored_entry`). Error: adb failure, hub 401, dropped play event, screen
mapping mismatch. Never log `agent_token`, `http_secret`, or the `Authorization`
header.

## Tests

`go test ./...` uses a fake clock, `httptest` as the hub and as the redirect
target, and a fake player that records the argument vector. No Android device
and no `adb` binary are required.

The suite covers:

- start when `now == starts_at`, and when `now == starts_at + late_threshold`;
- no start and no event when `now` is 1 nanosecond past the threshold;
- a restart mid-clip does not resume it;
- a failed `adb` start inside the window may be retried; a successful one may
  not, including across a reload of `played.json`;
- two clips joined within 1 second do not force-stop between them;
- a longer gap force-stops at `end`;
- an entry whose `screen_ids` omit this screen never starts;
- the same `starts_at` plays only the smaller `position`;
- an empty `entries` array force-stops the screen;
- a new package whose current entry differs force-stops the old clip;
- a relative `media.url` is joined to `hub_base_url` and fetched without the
  bearer token, following a redirect;
- a `.partial` file is not served and not played;
- a changed URL for the same `media.id` downloads again;
- an id missing from a new HTTP 200 package is deleted; it is kept when the
  poll fails;
- with no stored hub config, a screen in the local serial map may play; once
  `config.json` exists, a screen absent from it does not;
- reporter: 201 removes the line, 404 and 422 drop it, 401 and a network
  error keep it;
- HTTP media returns the file only with the secret, sends the stored MIME
  type, and answers `Range: bytes=0-3` with 206 and those bytes.

## README the new repository ships

The README is the operator page for the mini-PC, not a hub guide. It states:

- the process is a client of an existing hub; it does not create playlists;
- install VLC from the store on each Android TV and enable network ADB;
- install the binary, write the config (mode `0600`), enable chrony, and
  install the systemd unit;
- the unit runs as a dedicated user that owns `data_dir` and is in the group
  allowed to run `adb`;
- how to check journald for `late`, `missing_file`, and token rejection.

The README includes this unit:

```ini
[Unit]
Description=Mediateca station
After=network-online.target chronyd.service
Wants=network-online.target

[Service]
ExecStart=/usr/local/bin/mediateca-station -config /etc/mediateca-station/config.yaml
Restart=always
RestartSec=2
User=mediateca
NoNewPrivileges=true

[Install]
WantedBy=multi-user.target
```

`chronyd.service` may be `chrony.service` or `systemd-timesyncd.service` on a
given image. The README says to set `After=` and `Wants=` to the NTP unit that
image actually ships, and not to start the station before the clock has
stepped.

## Done when

A new agent can implement `mediateca-station` from this document alone.
`go test ./...` passes without a television. The README is enough for an
operator to point a mini-PC at a hub token and a set of TV serials. The hub
repository gains no runtime code.
