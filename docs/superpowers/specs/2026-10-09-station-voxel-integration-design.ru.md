# Station ↔ Voxel Media Streaming — дизайн

> Русский перевод. Нормативный оригинал:
> [`2026-10-09-station-voxel-integration-design.md`](./2026-10-09-station-voxel-integration-design.md).
> При расхождении формулировок приоритет у английской версии.

## Что это

Интеграция `mediateca-station` с уже развёрнутым стеком Voxel Media Streaming
на mini-PC площадки (`media-voxel`). Station остаётся клиентом Hub и
планировщиком по wall-clock. Байты видео доходят до Android TV через бинарник
Voxel `broadcast` (HTTP MPEG-TS). На ТВ — Voxel Media Stream
(`com.voxel.mediastream`) с HTTP-агентом на `:8989`.

Документ расширяет
[`2026-10-03-station-agent-design.md`](./2026-10-03-station-agent-design.md).
При конфликте для production-пути Voxel побеждает этот документ.
Путь VLC + `httpmedia` остаётся только для lab/compose.

## Почему так

- Hub уже владеет timed-плейлистами (`starts_at`) и proof-of-play.
- Voxel уже владеет проверенным live TS fan-out (`broadcast`) и плеером ТВ,
  который терпит скачки PTS при склейке `.ts` (libVLC).
- Stock VLC на Android TV — не production-плеер на `media-voxel`.
- Непрерывные loop’ы `playlist_player` не совпадают с wall-clock семантикой Hub.
- Mini-PC недоступен с Hub снаружи; управление только от station
  (poll/push) по существующему agent Bearer token.

Выбранный подход: **гибридный эфир + оркестрация Voxel runtime + ТВ во владении
station**, поставка **одним процессом, двумя milestone** (M1 эфир, M2 control
plane Hub).

Отклонено:

- Чистые непрерывные плейлисты Voxel (теряется точность Hub `starts_at` /
  play_events).
- Перенос всех Go-утилит Voxel в station в v1 (долго; ops уже на диске).
- Перепись `playlist.conf` и restart канала на каждый клип (вспышка reconnect).
- Hub, стучащийся TCP в LAN площадки (нет входящего пути).
- Два демона (`station` + `tv`) в v1 (лишняя ops-поверхность).

## Решения

| Тема | Решение |
| --- | --- |
| Модель эфира | Wall-clock + `play_events` в station; байты через Voxel `broadcast` |
| Роль Voxel | Runtime: бинарник `broadcast` + APK / `:8989`. Не `playlist_player` |
| Таймлайн → поток | Feeder station с rate limit пишет `.ts` в FIFO на каждый экран |
| Владение ТВ | Station полностью: health, wake/sleep, capture, APK |
| Не используется в эфире | `playlist_player`, `tv_verifier`, `controller`, Voxel `broadcast_schedule` |
| Доступность Hub | Station опрашивает Hub; Hub никогда не звонит на mini-PC |
| Поставка | M1 эфир → M2 telemetry/commands; один бинарник |
| Источник правды на ПК | YAML-конфиг station, не `broadcast.conf` / `stream_config.json` |

## Архитектура

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
│  mediateca-station (systemd, один процесс)               │
│  hub · cache · schedule · report                         │
│  feeder   — paced .ts → FIFO                             │
│  voxel    — lifecycle бинарника broadcast                │
│  tv       — :8989 + adb + WOL + screencap/record + APK   │
│  health   — watchdog по params Hub tv_monitor            │
│  commands — poll / execute / ack                         │
└──────────────┬────────────────────────────┬──────────────┘
               │ FIFO                       │ HTTP :8989 / adb
               ▼                            ▼
        broadcast :809x              Voxel APK на Android TV
        bind 192.168.33.1            URL http://192.168.33.1:809x/
```

### Ответственность пакетов

| Пакет | Задача |
| --- | --- |
| `internal/config` | Загрузка/валидация YAML; новые поля Voxel/TV |
| `internal/hub` | Существующий клиент + telemetry, commands, results |
| `internal/cache` | Без изменений: скачивание подготовленных `.ts` по `media.id` |
| `internal/schedule` | Без изменений: чистые правила таймлайна |
| `internal/report` | Без изменений: очередь play_events |
| `internal/feeder` | Открыть кэш в `starts_at`; писать paced-чанки в FIFO |
| `internal/voxel` | Создать FIFO; start/stop/restart `broadcast`; pid-файлы |
| `internal/tv` | Замена VLC-плеера: agent + adb + WOL + capture + APK |
| `internal/health` | Периодические проверки; авто `restart_stream` по params Hub |
| `internal/commands` | Очередь команд Hub; одна команда на экран за раз |
| `internal/agent` | Связка циклов: poll, feed, play, report, health, commands |
| `internal/httpmedia` | Только lab/compose с VLC; не на Voxel-пути эфира |
| `internal/player` | Legacy VLC adb helper; на Voxel-пути заменён `internal/tv` |

`api_server` (:8081) на mini-PC может остаться для локального ops. Он не в
критическом пути station и не должен start/stop эфир, которым владеет station.

## Конфиг

Путь: `/etc/mediateca-station/config.yaml` (режим `0600`, отказ при group/world битах).

```yaml
hub_base_url: "https://hub.example"
agent_token: "<token>"
late_threshold: "2s"
poll_interval: "1m"
data_dir: "/var/lib/mediateca-station"

# Плоскость стрима Voxel (production)
broadcast_bind: "192.168.33.1"
broadcast_bin: "/home/stream/bin/broadcast"
fifo_dir: "/tmp"
voxel_apk: "/home/stream/apk/voxel-debug.apk"
chunk_size: 18800          # байт на запись; как чанки TS у Voxel
feeder_pace: "realtime"    # realtime | max (тесты)

# Только lab (опционально). На media-voxel не указывать.
# http_listen: "127.0.0.1:8080"
# http_secret: "<не меньше 32 символов>"

screens:
  - screen_id: 7
    adb_serial: "192.168.33.182:5555"
    stream_port: 8091
    mac: "78:53:33:4b:2c:29"   # WOL; нужен для wake / health wake
```

Валидация:

- На Voxel-пути обязательны `broadcast_bind`, `broadcast_bin`, `fifo_dir`,
  `voxel_apk`.
- Каждый экран: уникальный `screen_id`, уникальный `stream_port`,
  `adb_serial` с `:`; `mac` обязателен, когда включены power-команды или
  health-wake (M2; в M1 — однократный warn, если нет).
- Lab-путь: если заданы `http_listen` + `http_secret` и нет `broadcast_bin`,
  сохраняется поведение VLC/`httpmedia` из дизайна 2026-10-03.

URL потока для ТВ (без path-secret; eth0 только для ТВ, как у Voxel):

```text
http://<broadcast_bind>:<stream_port>/
```

## Контракт Hub

Auth: `Authorization: Bearer <agent_token>` на всех `/api/agent/*`. Скачивание
медиа по-прежнему через подписанные URL Active Storage без bearer token.

### Существующие (семантика без изменений)

- `GET /api/agent/v1/config`
- `GET /api/agent/v2/package` (ETag / If-None-Match)
- `POST /api/agent/v1/play_events`

### Расширение config

Опциональный объект (в M1 при отсутствии — дефолты):

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

Дефолты без `tv_monitor`: health 30с, auto_restart true, telemetry 60с.

### `POST /api/agent/v1/telemetry` (M2)

Один запрос за интервал telemetry:

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

Успех: HTTP 202 или 204. Сеть/5xx: следующий интервал. HTTP 401: пауза
telemetry, commands и drain play_events, пока следующий poll config/package
не вернёт 200 или 304 (то же правило, что у существующего reporter).

### `GET /api/agent/v1/commands` (M2)

Опрос каждый `poll_interval` (допустимо чаще сразу после непустой очереди —
деталь реализации, не чаще раза в 5с).

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

Пустая очередь: `{"commands":[]}`. Опционально `ETag` / `If-None-Match`, как у package.

Команды v1 — **только per-screen** (`screen_id` обязателен).

### Типы команд

| type | params | действие station |
| --- | --- | --- |
| `start_stream` | опционально `url`, `buffer_ms` | `POST :8989/api/start_stream` (по умолчанию URL канала) |
| `restart_stream` | — | `POST :8989/api/restart_stream` |
| `stop` | — | `POST :8989/api/stop` |
| `wake` | — | adb `KEYCODE_WAKEUP`; если всё ещё спит → WOL по `mac` |
| `sleep` | — | adb `KEYCODE_SLEEP` |
| `screenshot` | — | adb screencap+pull; upload через command_results |
| `record` | `seconds` (1–120) | adb screenrecord; upload через command_results |
| `ensure_app` | — | проверка агента; install/launch APK при необходимости |
| `install_apk` | `url` (https) или omit → локальный `voxel_apk` | скачать при url; `adb install -r`; launch |

Неизвестный `type`: завершить с `ok: false`, `error: "unknown_type"`; процесс
не падает.

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

Для `screenshot` / `record`: Hub кладёт подписанный `params.upload_url` в
команду; station делает HTTP PUT байтов по этому URL, затем шлёт
`command_results` с `ok` без тела файла. Если `upload_url` нет — station
шлёт multipart `file` в `command_results` (fallback только для маленьких
скриншотов).

Повторный `command_id` Hub считает уже применённым (HTTP 200).

Авто-рестарты health **не** выдумывают command id; они только в логах и
telemetry (`last_error` / переходы состояния).

## Поток данных (путь Voxel)

1. Старт: загрузить сохранённые config/package; обеспечить FIFO + `broadcast`
   на каждый замапленный экран; запустить health; не ждать живой Hub.
2. Poll: GET config, GET package (If-None-Match), (M2) GET commands, POST
   telemetry по своему интервалу.
3. Cache: как в 2026-10-03 (partial → rename, URL sidecar, prune на 200).
4. Пробуждение schedule → для каждого действия экрана:
   - **Старт клипа:** если правила late пройдены и файл готов → feeder начинает
     писать файл в FIFO экрана; `POST :8989/api/start_stream` с URL канала;
     зафиксировать `started_at` непосредственно перед успешным стартом feed
     (и после того, как агент принял start); поставить play_event в очередь
     только если и feed, и agent start успешны.
   - **Стык ≤1с:** переключить feeder на следующий файл без остановки плеера ТВ.
   - **Gap >1с:** остановить file feed; оставить `broadcast`; писать в FIFO
     null-пакеты MPEG-TS (как resync EOF у Voxel), чтобы HTTP-клиент не
     зависал. Плеер на коротком gap не force-stop.
   - **Idle (нет covering entry / конец дня):** остановить file feed;
     `POST :8989/api/stop`, чтобы плеер не крутил UI reconnect.
     `broadcast` оставить работать.
   - **Смена package:** те же правила covering-key, что в 2026-10-03.
5. Reporter независимо дренирует `events.jsonl`.

## Feeder

- Одна goroutine (или worker) на экран; не блокирует другие экраны.
- Читает `data_dir/media/<id>`; пишет в `{fifo_dir}/stream_{port}.ts`.
- Темп: по умолчанию realtime, привязанный к wall-clock длительности клипа;
  размер чанка `chunk_size` (дефолт 18800).
- Gap >1с: только null-пакеты (silence-файл в v1 не нужен).
- Late / skip / conflict / played-set: без изменений относительно schedule.
- Рестарт процесса mid-clip: не продолжать (хвост skip); ждать следующий
  `starts_at`.
- Нет файла в момент старта: skip + лог `missing_file`; без play_event.

## Адаптер Voxel

- Старт: `mkfifo` при необходимости; exec
  `broadcast_bin <fifo> :<stream_port>` с допущениями как у Voxel
  (`broadcast` биндится на `stream_server_ip` / bind через env или дефолт
  бинарника `192.168.33.1` — station задаёт `broadcast_bind` в конфиге и
  документирует требование: слушать на этом IP).
- Если stock `broadcast` жёстко прошит на `192.168.33.1`, `broadcast_bind`
  должен совпадать с этим адресом на хосте; смена bind — только бинарником
  Voxel, который читает адрес (вне scope station, пока Voxel не патчим отдельно).
- Pid + логи в `data_dir/voxel/` (или `log_dir`, если задан).
- При crash: перезапустить broadcast; feeder переоткрывает FIFO; play_events
  не дублировать.
- **Не** вызывать Voxel `start_broadcast` / `stop_broadcast` (они поднимают
  `playlist_player`).
- **Не** считать `/home/.config/broadcast.conf` источником правды.

## Управление ТВ

Mutex на экран. Таймауты: 5с для HTTP агента; 15с для adb install / record.

| Действие | Механизм |
| --- | --- |
| Status | `GET http://<tv-ip>:8989/api/status` |
| Start | `POST /api/start_stream` `{url, buffer_ms}` |
| Restart | `POST /api/restart_stream` |
| Stop | `POST /api/stop` |
| Wake | `adb shell input keyevent KEYCODE_WAKEUP`; fallback WOL |
| Sleep | `adb shell input keyevent KEYCODE_SLEEP` |
| Screenshot | `adb exec-out screencap -p` |
| Record | `adb shell screenrecord --time-limit N …` + pull |
| ensure_app | если агент down: `adb install -r voxel_apk` + start MainActivity |

Хост в `adb_serial` — IP ТВ для URL агента.

## Health

- Интервал из `tv_monitor.health_interval_seconds`.
- Когда есть covering entry расписания (в эфире): ping → статус агента → если
  `playing=false` и `auto_restart_when_not_playing` → `restart_stream`;
  проверить `mWakefulness`; разбудить, если спит.
- В idle: не авто-рестартить playback loop; не авто-sleep (sleep в v1 только
  командой Hub).
- При переходе в error: опциональный скриншот в `data_dir/diagnostics/` и
  ссылка в telemetry `last_error`.

## Ошибки

| Сбой | Поведение |
| --- | --- |
| Hub 401 | Пауза reporter, telemetry, commands; эфир из кэша |
| Hub 5xx / сеть | Retry на следующем интервале; эфир продолжается |
| Download fail / диск полный | Оставить прежний полный файл; новый URL не ready |
| crash broadcast | Перезапуск бинарника; переоткрыть FIFO |
| Backpressure feeder | Log warn; другие экраны работают |
| Провал команды | `command_results` с `ok:false`; очередь дальше |
| Неизвестный тип команды | `ok:false`, `unknown_type` |

## Milestone

### M1 — Эфир на media-voxel

- Feeder + voxel adapter + tv start_stream + play_events.
- Локальный health с дефолтами конфига (команды/telemetry Hub не обязательны).
- Отключить или замаскировать владение эфиром у Voxel `tv_verifier` /
  `controller` на хосте, чтобы не дрались со station (`broadcast_schedule`
  пустой / units остановлены).
- Operator smoke: один экран, один закешированный клип, в Hub появляется
  play_event.

### M2 — Control plane

- Hub реализует `tv_monitor`, telemetry, commands, command_results.
- Station опрашивает/исполняет все типы команд из этой спеки.
- Hub UI/ops: wake, sleep, restart, screenshot, ensure APK, настройка интервалов.

Работа Hub Rails — отдельный workstream; этот репозиторий поставляет сторону
station и контракт выше. Примеры в стиле OpenAPI в документе нормативны для
обеих сторон.

## Тесты

`go test ./...` без реальных ТВ и бинарников Voxel:

- Регрессии существующих schedule / report / cache / hub client.
- Feeder: fake clock + pipe; старт на пороге; нет старта после late; null-пакеты
  на gap; join без restart на стыке.
- Voxel adapter: fake exec пишет argv (`broadcast`, fifo, port).
- TV: `httptest` агент + fake recorder argv adb.
- Commands: poll → run → result; неизвестный type; порядок mutex на экран.
- Health: not playing + auto_restart → один restart_stream; idle → без restart.

Ручной прогон на `media-voxel` после M1/M2.

## Логирование

`log/slog` в stdout/journald. Info: poll, старт/стоп feed, завершение команды.
Error: сбой adb/агента, restart broadcast, mismatch маппинга. Никогда не логировать
`agent_token` и заголовки Authorization.

## Готово, когда

- Реализатор может собрать M1 и M2 по этому документу и дизайну 2026-10-03,
  не читая исходники Voxel дальше указанных бинарников/API.
- `go test ./...` проходит офлайн.
- На `media-voxel` M1 крутит Hub-timed `.ts` через `broadcast` в Voxel APK и
  шлёт play_events; M2 принимает команды Hub и постит telemetry.
- Для эфира Mediateca не требуются Voxel `playlist_player` / `tv_verifier`.

## Связь с документацией Voxel

Ссылки upstream (в этот репозиторий не копируются):

- `stream_project/DOC/STREAM-server/CONSOLIDATED_DOCS.md` — broadcast, API,
  поведение tv_verifier, которое мы заменяем или выборочно переиспользуем.
- `stream_project/DOC/voxel-media-player/TZ_ANDROID_PLAYER.md` — агент `:8989`.
- `stream_project/DOC/STREAM-server/CHANGELOG.md` — FanOut, WOL, заметки по
  расписанию.
