# Стороннее и предустановленное ПО на мини-ПК (mediateca-station)

Документ описывает, какое внешнее ПО и протоколы использует агент станции `mediateca-station` для трансляции роликов и управления Android TV. Источник — код репозитория `github.com/dim321/mediateca-station` (Go-демон, не Rails).

Станция **не** создаёт плейлисты и **не** перекодирует видео: Hub отдаёт уже готовые MPEG-TS (`.ts`), станция кэширует файлы и в нужный wall-clock момент запускает показ на ТВ.

---

## 1. Режимы работы

Конфиг YAML выбирает путь через `stream_backend` и/или legacy `broadcast_bin`:

| Режим | Условие | Плеер на ТВ | Как ТВ получает байты | Где в коде |
| --- | --- | --- | --- | --- |
| **Production (builtin)** | `stream_backend: builtin` → `VoxelPath()` + `UseBuiltinStreamer()` | APK **Voxel Media Stream** | HTTP MPEG-TS in-process (`internal/stream`) | `internal/stream`, `internal/voxel`, `internal/air`, `internal/tv`, `internal/feeder` |
| **Production (legacy bin)** | `broadcast_bin` задан или `stream_backend: voxel_bin` | тот же APK | HTTP MPEG-TS через внешний бинарник `broadcast` | `internal/voxel` (exec) |
| **Lab (VLC)** | нет production-ключа | **VLC** (`org.videolan.vlc`) | Встроенный HTTP-сервер станции (`httpmedia`) | `internal/player`, `internal/httpmedia` |

Переключатель: `internal/config/config.go` (`VoxelPath()`, `UseBuiltinStreamer()`), ветвление в `internal/agent/agent.go`.

Production (builtin, дефолт чистой установки):

```text
Hub schedule → station cache → feeder → FIFO → internal/stream → HTTP :stream_port → Voxel APK (:8989)
```

Production (legacy):

```text
Hub schedule → station cache → feeder → FIFO → broadcast → HTTP :stream_port → Voxel APK (:8989)
```

Lab:

```text
Hub schedule → station cache → httpmedia (:http_listen) → ADB am start → VLC открывает URL
```

---

## 2. Обязательное на мини-ПК (production)

### 2.1. `mediateca-station` (свой бинарник)

- **Назначение:** единственный демон эфира: poll Hub, кэш медиа, таймлайн, feeder, lifecycle streamer/`broadcast`, команды/telemetry, play_events.
- **Где:** `cmd/station/main.go` → `agent.Run`; установка в `/usr/local/bin/mediateca-station` (`README.md`, `deploy/install-stations.sh`).
- **Окружение:** Linux amd64; пользователь `mediateca`; конфиг `/etc/mediateca-station/config.yaml` (mode `0600`); данные `/var/lib/mediateca-station`.

### 2.2. systemd

- **Назначение:** автозапуск и рестарт демона (`Restart=always`).
- **Где:** unit в `README.md`, шаблон `render_unit` в `deploy/install-stations.sh`.
- Install-скрипт проверяет наличие `systemctl`.

### 2.3. Android Debug Bridge (`adb`)

- **Назначение:** сетевое управление Android TV: connect, wake/sleep, screencap, screenrecord, install/launch APK; в lab — старт/стоп VLC.
- **Вызовы:** `internal/tv/adb_ops.go`, lab `internal/player/adb.go`.
- Install ставит `adb` / `android-tools-adb`, если команды нет в PATH.

### 2.4. HTTP MPEG-TS streamer

**Builtin (рекомендуемый путь):** `internal/stream` в том же бинарнике. При `stream_backend: builtin` `voxel.Manager` не делает `exec` внешнего `broadcast`: Listen на `broadcast_bind:port`, чтение FIFO, non-blocking fan-out (медленных клиентов drop-oldest / disconnect).

**Legacy Voxel `broadcast`:** только при `stream_backend: voxel_bin` или старом YAML с `broadcast_bin` (`exec broadcast_bin <fifo> :<port>`). **На чистой установке с builtin бинарник `/home/stream/bin/broadcast` не требуется и не ставится.**

FIFO (этап A): `{fifo_dir}/stream_{port}.ts`; запись из `air.Controller` / `feeder.Feeder`.

### 2.5. Voxel player APK на телевизоре

- **Package / activity:** `com.voxel.mediastream` / `.MainActivity`.
- **Источник в репо:** `deploy/voxel-debug.apk` (обязателен для `install-stations.sh`).
- **Путь APK на ПК:** `voxel_apk` (деплой копирует в `/home/stream/apk/voxel-debug.apk`).
- **HTTP API** на `:8989`: status / start_stream / restart_stream / stop (`internal/tv/agent.go`).
- **Install мини-ПК:** копирует APK на диск; **не** делает `adb install` на ТВ (ТВ могут быть offline).
- **`fetch_screens`:** после чтения leases — `adb connect` + `pm path`; если пакета нет — `adb install -r` с локального `voxel_apk`. Ошибки ADB не роняют discovery.

### 2.6. NTP-клиент (chrony или systemd-timesyncd)

- Точные старты клипов по wall-clock. Install ставит chrony, если NTP-клиент не найден.

### 2.7. Сетевой стек Linux (ядро / iproute)

- Две плоскости NIC (ТВ `eth0` / admin `eth1`), TCP к Hub, ADB, stream HTTP, агент ТВ.

---

## 3. Условно обязательное / опциональное на мини-ПК

### 3.1. dnsmasq (DHCP на ТВ-LAN)

- Аренды для `fetch_screens` (`internal/leases`). Install ставит пакет при отсутствии; **не перезаписывает** чужой `dnsmasq.conf`.
- Группа `dnsmasq-leases`, lease file mode `640`.

### 3.2. Wake-on-LAN: `wakeonlan` или `etherwake`

- Опциональный WOL (`tv.DefaultWOL`). Install старается обеспечить хотя бы одну утилиту.

### 3.3. VLC на Android TV (`org.videolan.vlc`)

- Только lab.

### 3.4. Встроенный HTTP media server станции

- Lab: `internal/httpmedia` при `!VoxelPath` и заданном `http_listen`.

### 3.5. SSH / scp (только деплой)

- `deploy/install-stations.sh`.

### 3.6. Docker / Compose / Android emulator (только разработка)

- См. `Dockerfile`, `compose.yaml`, `docker/tv/*`.

---

## 4. Чего в коде нет

| Технология | Статус |
| --- | --- |
| **ffmpeg** | Нет вызовов. |
| **CEC / HDMI-out с мини-ПК** | Отвергнуто в дизайне lab-агента. |
| **HLS / frame-accurate sync** | Вне scope MVP. |
| **Обязательный Voxel `broadcast` на builtin-пути** | Снят (этап A). |

---

## 5. Чистый Go / без внешних бинарников (эфир)

| Пакет | Роль |
| --- | --- |
| `internal/stream` | Live MPEG-TS HTTP + FIFO fan-out (builtin) |
| `internal/hub` | HTTP API к Broadcast Hub |
| `internal/cache` | Кэш медиафайлов |
| `internal/schedule` | Wall-clock команды |
| `internal/feeder` | Запись `.ts` в FIFO/writer с pacing |
| `internal/air` | Оркестрация эфира: feeder + TV agent |
| `internal/report` | Durable очередь play events |
| `internal/httpmedia` | Lab HTTP раздача клипов |
| `internal/health` | HTTP status/restart у APK-агента |
| `internal/leases` | Парсинг dnsmasq leases |
| `internal/fsutil`, `internal/config` | Утилиты / YAML |

Единственная Go-зависимость модуля: `gopkg.in/yaml.v3`.

---

## 6. Сводка production (builtin)

1. **systemd + chrony** — жизнь процесса и точное время.  
2. **mediateca-station** — расписание, кэш, feeder, команды Hub, **in-process streamer**.  
3. **ADB** — питание, диагностика, установка APK.  
4. **Voxel APK (:8989)** — декодирование/показ.  
5. **WOL-утилиты** — опциональный подъём ТВ по MAC.  
6. **dnsmasq** — DHCP + discovery.  
7. **Legacy `broadcast`** — только при явном `voxel_bin` / `broadcast_bin`.

---

## 7. Карта файлов с `os/exec` / внешними протоколами

| Файл | Внешнее взаимодействие |
| --- | --- |
| `internal/player/adb.go` | `adb` → VLC intents |
| `internal/tv/adb_ops.go` | `adb`; `wakeonlan` / `etherwake` |
| `internal/tv/agent.go` | HTTP к APK `:8989` |
| `internal/stream/streamer.go` | Listen HTTP MPEG-TS (builtin, без exec) |
| `internal/voxel/broadcast.go` | exec `broadcast_bin` (legacy) **или** делегирование в `stream` |
| `internal/commands/executor.go` | оркестрация agent/device |
| `internal/commands/fetch_screens.go` | чтение lease-файла dnsmasq |
| `internal/agent/agent.go` | связка путей Lab/Voxel, health, telemetry |
| `internal/air/controller.go` | feeder + TV StartStream/Stop |
| `deploy/install-stations.sh` | ssh/scp, systemd, hosts, deps (adb/WOL/dnsmasq/NTP) |

---

## 8. Зависимости окружения мини-ПК (чеклист)

**Всегда (оба пути):** Linux, `adb`, NTP, systemd unit, сеть до Hub и до ТВ, пользователь `mediateca`.

**Production builtin:** `stream_backend: builtin`, writable `fifo_dir`, `deploy/voxel-debug.apk` в репо → `/home/stream/apk/voxel-debug.apk` на мини-ПК, `broadcast_bind` достижим с ТВ. **`/home/stream/bin/broadcast` не нужен.**

**Production legacy:** исполняемый `broadcast_bin` + те же FIFO/APK/bind.

**Lab:** VLC на ТВ, `http_listen`/`http_secret`, отсутствие production-ключа.

**Discovery:** dnsmasq lease file + группа `dnsmasq-leases`.
