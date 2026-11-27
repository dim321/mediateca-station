# Стороннее и предустановленное ПО на мини-ПК (mediateca-station)

Документ описывает, какое внешнее ПО и протоколы использует агент станции `mediateca-station` для трансляции роликов и управления Android TV. Источник — код репозитория `github.com/dim321/mediateca-station` (Go-демон, не Rails).

Станция **не** создаёт плейлисты и **не** перекодирует видео: Hub отдаёт уже готовые MPEG-TS (`.ts`), станция кэширует файлы и в нужный wall-clock момент запускает показ на ТВ.

---

## 1. Два режима работы

Конфиг YAML выбирает путь по наличию `broadcast_bin`:

| Режим | Условие | Плеер на ТВ | Как ТВ получает байты | Где в коде |
| --- | --- | --- | --- | --- |
| **Production (Voxel)** | `broadcast_bin` задан → `config.File.VoxelPath()` | APK **Voxel Media Stream** (`com.voxel.mediastream`) | HTTP MPEG-TS с мини-ПК через бинарник `broadcast` | `internal/voxel`, `internal/air`, `internal/tv`, `internal/feeder` |
| **Lab (VLC)** | `broadcast_bin` пуст | **VLC** (`org.videolan.vlc`) | Встроенный HTTP-сервер станции (`httpmedia`) | `internal/player`, `internal/httpmedia` |

Переключатель: `internal/config/config.go` (`VoxelPath()`), ветвление в `internal/agent/agent.go` (`newAgent`, `Run`, `apply`).

Production-цепочка (из README и спеки Voxel):

```text
Hub schedule → station cache → feeder → FIFO → broadcast → HTTP :stream_port → Voxel APK (:8989)
```

Lab-цепочка:

```text
Hub schedule → station cache → httpmedia (:http_listen) → ADB am start → VLC открывает URL
```

---

## 2. Обязательное на мини-ПК (production / Voxel)

Это ПО и артефакты, без которых production-эфир на площадке (`media-voxel`) не работает так, как задумано кодом и `deploy/install-stations.sh`.

### 2.1. `mediateca-station` (свой бинарник)

- **Назначение:** единственный демон эфира: poll Hub, кэш медиа, таймлайн, feeder, lifecycle `broadcast`, команды/telemetry, play_events.
- **Где:** `cmd/station/main.go` → `agent.Run`; установка в `/usr/local/bin/mediateca-station` (`README.md`, `deploy/install-stations.sh`).
- **Окружение:** Linux amd64; пользователь `mediateca`; конфиг `/etc/mediateca-station/config.yaml` (mode `0600`); данные `/var/lib/mediateca-station`.

### 2.2. systemd

- **Назначение:** автозапуск и рестарт демона (`Restart=always`).
- **Где в коде/скриптах:** unit в `README.md` (секция Mini-PC), шаблон `render_unit` в `deploy/install-stations.sh`.
- **Ключевые директивы:** `After=`/`Wants=network-online.target` (в README также chrony); `User=mediateca`; `SupplementaryGroups=dnsmasq-leases`; `NoNewPrivileges=true`.
- **Команды окружения:** `systemctl enable --now mediateca-station`, логи `journalctl -u mediateca-station`.

Сам процесс **не** вызывает `systemctl` — только ожидается, что unit его поднимает.

### 2.3. Android Debug Bridge (`adb`)

- **Назначение:** сетевое управление Android TV с мини-ПК: connect, wake/sleep, screencap, screenrecord, install/launch APK; в lab — старт/стоп VLC.
- **Вызовы:**
  - Production control plane: `internal/tv/adb_ops.go` (`tv.Device`, `tv.ExecRunner` → `os/exec`).
  - Lab playback: `internal/player/adb.go` (`player.Player`).
- **Типовые команды:**

  | Действие | Команда |
  | --- | --- |
  | Connect | `adb connect <host:5555>` |
  | Wake | `adb -s <serial> shell input keyevent KEYCODE_WAKEUP` |
  | Sleep | `adb -s <serial> shell input keyevent KEYCODE_SLEEP` |
  | Power state | `adb -s <serial> shell dumpsys power` (поиск `mWakefulness=Asleep`) |
  | Screenshot | `adb -s <serial> exec-out screencap -p` |
  | Record | `adb -s <serial> shell screenrecord --time-limit N /sdcard/.mediateca_screenrecord.mp4` + `adb pull` + `rm` |
  | Install APK | `adb -s <serial> install -r <apk>` |
  | Launch Voxel | `adb -s <serial> shell am start -n com.voxel.mediastream/.MainActivity` |
  | Lab: open VLC | `adb … shell am start -a android.intent.action.VIEW -d <url> -t <mime> -p org.videolan.vlc` |
  | Lab: stop VLC | `adb … shell am force-stop org.videolan.vlc` |

- **Окружение мини-ПК:** бинарник `adb` в `PATH` у пользователя `mediateca`; доступ к `~/.android` (ключи ADB); на ТВ включён **network ADB** (`adb_serial` вида `192.168.33.182:5555`).
- **Кто вызывает:** `commands.Executor` (wake/sleep/screenshot/record/ensure_app/install_apk) и lab-путь через `player` при `schedule.KindStart`/`KindStop`.

### 2.4. Бинарник Voxel `broadcast`

- **Назначение:** читает MPEG-TS из named pipe (FIFO) и отдаёт live HTTP-поток на порт экрана; ТВ (APK) подключается к `http://<broadcast_bind>:<stream_port>/`.
- **Где:** `internal/voxel/broadcast.go` — `Manager.Ensure` делает `exec`  
  `broadcast_bin <fifo_path> :<stream_port>`  
  например `/home/stream/bin/broadcast /tmp/stream_8091.ts :8091`.
- **FIFO:** `syscall.Mkfifo` → `{fifo_dir}/stream_{port}.ts`; запись из `air.Controller` / `feeder.Feeder`.
- **Pid-файлы:** `data_dir/voxel/broadcast_{port}.pid`.
- **Конфиг:** `broadcast_bin`, `broadcast_bind`, `fifo_dir` (обязательны на Voxel-пути). Деплой-шаблон: `/home/stream/bin/broadcast`, bind `192.168.33.1` (`deploy/install-stations.sh` → `render_config`).
- **Окружение:** файл бинарника должен существовать и быть исполняемым для `mediateca`; `fifo_dir` writable; ТВ должны маршрутизировать до `broadcast_bind` на каждом `stream_port`. Бинарник **не** входит в этот репозиторий — предустановка образа/стека Voxel (`media-voxel`).

Station **не** вызывает старые Voxel-обёртки `start_broadcast` / `stop_broadcast` и **не** читает `/home/.config/broadcast.conf` (см. спеку `docs/superpowers/specs/2026-10-09-station-voxel-integration-design.ru.md`).

### 2.5. Voxel player APK на телевизоре

- **Назначение:** плеер эфира + локальный HTTP control agent на порту **8989**.
- **Package / activity:** `com.voxel.mediastream` / `.MainActivity` (`internal/tv/adb_ops.go`).
- **Путь APK на ПК:** конфиг `voxel_apk` (деплой: `/home/stream/apk/voxel-debug.apk`).
- **HTTP API агента** (`internal/tv/agent.go` — `tv.Agent`, base `http://<tv-host>:8989`):

  | Метод | Путь | Зачем |
  | --- | --- | --- |
  | GET | `/api/status` | playing / url / buffer_ms; health + telemetry |
  | POST | `/api/start_stream` | начать поток `{url, buffer_ms}` |
  | POST | `/api/restart_stream` | перезапуск при health |
  | POST | `/api/stop` | остановить показ |

- **Кто вызывает:** `air.Controller` (эфир), `commands.Executor` (Hub-команды), `health.Watcher` (auto-restart), telemetry в `agent.screenTelemetry`.
- **Окружение:** APK установлен на каждом ТВ; агент отвечает на `:8989`; тот же билд, что лежит в `voxel_apk` на мини-ПК.

### 2.6. NTP-клиент (chrony или systemd-timesyncd)

- **Назначение:** точные старты клипов по wall-clock станции; часы ТВ игнорируются.
- **Где:** README (Mini-PC / systemd unit `After=`/`Wants=chronyd.service`); в коде **нет** вызова chrony — только операционное требование.
- **Окружение:** синхронизация UTC до старта сервиса; имя unit зависит от образа (`chronyd` / `chrony` / `systemd-timesyncd`).

### 2.7. Сетевой стек Linux (ядро / iproute)

- **Назначение:** две плоскости NIC (ТВ `eth0` / admin `eth1`), TCP к Hub, ADB, broadcast HTTP, агент ТВ.
- **В коде демона:** чистый Go `net`/`http` (`internal/hub`, `internal/tv/agent.go`, `screenReachable`).
- **В Docker-обвязке (не production binary):** `ip route`, `ss` в `docker/entrypoint.sh`, `docker/devnet.sh`.
- **Окружение production:** типично `192.168.33.1/24` на ТВ-плоскости; Hub через admin LAN / VPN (`192.168.100.250` → `broadcast.mediateca.store` в `/etc/hosts` от install-скрипта).

---

## 3. Условно обязательное / опциональное на мини-ПК

### 3.1. dnsmasq (DHCP на ТВ-LAN)

- **Назначение:** раздача IP телевизорам; файл аренд для команды Hub `fetch_screens` (discovery экранов по MAC/IP).
- **Чтение станцией:** `internal/leases/file.go` — путь по умолчанию `/var/lib/misc/dnsmasq.leases`; парсер `internal/leases/parse.go`; команда `internal/commands/fetch_screens.go`.
- **Запуск dnsmasq:** **не** из Go-демона. В lab Docker — `docker/entrypoint.sh` + `docker/dnsmasq.conf`. На площадке — отдельный DHCP-сервис мини-ПК.
- **Окружение:** группа `dnsmasq-leases`, `chmod 640` / `chgrp` на lease-файл; в unit станции `SupplementaryGroups=dnsmasq-leases`.
- **Обязательность:** для эфира не нужен, если экраны уже привязаны в Hub (`adb_serial` / `mac` / `stream_port`). Нужен для discovery и типичной топологии площадки.

### 3.2. Wake-on-LAN: `wakeonlan` или `etherwake`

- **Назначение:** разбудить ТВ по MAC, если ADB connect не удался или после `KEYCODE_WAKEUP` устройство всё ещё `Asleep`.
- **Где:** `tv.DefaultWOL` в `internal/tv/adb_ops.go`:

  ```text
  wakeonlan <mac>   # сначала
  etherwake <mac>   # fallback
  ```

- **Когда:** команда Hub `wake` и сценарии `Device.Wake`; нужен `mac` у экрана.
- **Обязательность:** опционально. Без MAC / без утилит wake остаётся только ADB keyevent.

### 3.3. VLC на Android TV (`org.videolan.vlc`)

- **Назначение:** lab/compose-плеер: открыть HTTP URL с клипом через intent.
- **Где:** только `internal/player/adb.go` (и Docker TV-образ: `docker/tv/Dockerfile` качает VLC APK, `docker/tv/entrypoint.sh` ставит через `adb install`).
- **На Voxel production:** README явно: «VLC is not used on this path».
- **Обязательность:** только lab.

### 3.4. Встроенный HTTP media server станции

- **Назначение:** lab — отдача кэшированных `.ts` ТВ по URL вида `/m/<http_secret>/<media_id>` с Range.
- **Где:** `internal/httpmedia/server.go`; поднимается в `agent.Run`, если **не** VoxelPath и задан `http_listen`.
- **Внешний бинарник:** нет (чистый `net/http` + `http.ServeContent`).
- **Обязательность:** только lab (требуются `http_listen`, `http_secret` ≥ 32 символов).

### 3.5. SSH / scp (только деплой)

- **Назначение:** `deploy/install-stations.sh` ставит бинарник, конфиг, hosts, systemd unit на парк мини-ПК.
- **Не участвует** в runtime эфира.

### 3.6. Docker / Compose / Android emulator (только разработка)

| Компонент | Зачем |
| --- | --- |
| `Dockerfile` | образ станции: Go build + `adb`, `dnsmasq`, `iproute2` |
| `compose.yaml` | две сети (television / Hub); profile `tv` — эмулятор |
| `docker/tv/*` | Android TV 9 emulator, DHCP client, установка VLC |
| KVM (`/dev/kvm`) | для эмулятора |
| Host tip `scrcpy` | отладка с хоста (README), станция **не** вызывает |

---

## 4. Чего в коде нет (явно отвергнуто или отсутствует)

| Технология | Статус |
| --- | --- |
| **ffmpeg** | Нет вызовов. Перекод на Hub; станция играет готовый MPEG-TS. |
| **CEC / HDMI-out с мини-ПК** | Отвергнуто в дизайне lab-агента (`docs/superpowers/specs/2026-10-03-station-agent-design.md`): мини-ПК не гоняет HDMI сам. |
| **HLS / frame-accurate sync** | Вне scope MVP. |
| **VLC remote-control session** | Отвергнуто как ненадёжное на Android TV; lab использует только `am start` / `force-stop`. |
| **Ruby gems / Rails на станции** | Репозиторий — Go (`go.mod`: только `gopkg.in/yaml.v3`). |
| **Sidekiq / Solid Queue и т.п.** | Нет; всё в одном процессе + goroutines. |

Устаревший стек Voxel, который station **должен вытеснить** (README «Retire conflicting Voxel services»): `tv-verifier.service`, host `controller` для эфира, `playlist_player`, заполненный Voxel `broadcast_schedule`.

---

## 5. Чистый Go / без внешних бинарников

Модули, которые реализуют логику сами (файлы, HTTP-клиент Hub, планирование), без `exec` внешних утилит:

| Пакет | Роль |
| --- | --- |
| `internal/hub` | HTTP API к Broadcast Hub (config, package, play_events, telemetry, commands) |
| `internal/cache` | Кэш медиафайлов на диске |
| `internal/schedule` | Wall-clock команды start/stop/skip |
| `internal/feeder` | Запись `.ts` в FIFO/writer с pacing |
| `internal/air` | Оркестрация эфира Voxel: feeder + TV agent |
| `internal/report` | Durable очередь play events |
| `internal/httpmedia` | Lab HTTP раздача клипов |
| `internal/health` | Периодический HTTP status/restart у APK-агента |
| `internal/leases` | Парсинг текста dnsmasq leases (сам dnsmasq не запускает) |
| `internal/fsutil`, `internal/config` | Утилиты / YAML |

Единственная Go-зависимость модуля: `gopkg.in/yaml.v3` (парсинг конфига, не обёртка над CLI).

---

## 6. Сводка: «кто за что отвечает» в эфире и управлении ТВ

### Production (мини-ПК площадки)

1. **systemd + chrony** — жизнь процесса и точное время.  
2. **mediateca-station** — расписание, кэш, feeder, команды Hub.  
3. **`broadcast`** — HTTP fan-out MPEG-TS с FIFO.  
4. **ADB** — питание, диагностика, установка APK.  
5. **Voxel APK (:8989)** — декодирование/показ и приём start/stop/restart.  
6. **WOL-утилиты** — опциональный подъём ТВ по MAC.  
7. **dnsmasq** — DHCP + discovery (`fetch_screens`), не путь байтов эфира.

### Lab

1. **mediateca-station + httpmedia** — раздача файла по LAN.  
2. **ADB + VLC** — открыть/закрыть URL на ТВ.  
3. Docker: **dnsmasq + Android emulator + VLC APK** для локальной проверки.

---

## 7. Карта файлов с `os/exec` / внешними протоколами

| Файл | Внешнее взаимодействие |
| --- | --- |
| `internal/player/adb.go` | `adb` → VLC intents |
| `internal/tv/adb_ops.go` | `adb`; `wakeonlan` / `etherwake` |
| `internal/tv/agent.go` | HTTP к APK `:8989` |
| `internal/voxel/broadcast.go` | exec `broadcast_bin`; `mkfifo` |
| `internal/commands/executor.go` | оркестрация agent/device (+ HTTPS download APK) |
| `internal/commands/fetch_screens.go` | чтение lease-файла dnsmasq |
| `internal/agent/agent.go` | связка путей Lab/Voxel, health, telemetry |
| `internal/air/controller.go` | feeder + TV StartStream/Stop |
| `deploy/install-stations.sh` | ssh/scp, systemd, hosts |
| `docker/entrypoint.sh` | dnsmasq, iproute |
| `Dockerfile` | пакеты `adb`, `dnsmasq` |
| `docker/tv/Dockerfile` | Android SDK emulator, VLC APK |

---

## 8. Зависимости окружения мини-ПК (чеклист)

**Всегда (оба пути):** Linux, `adb`, NTP, systemd unit, сеть до Hub и до ТВ, пользователь `mediateca`.

**Только Voxel production:** `/home/stream/bin/broadcast` (или путь из конфига), writable `fifo_dir`, файл `voxel_apk`, APK на каждом ТВ с агентом `:8989`, `broadcast_bind` достижим с ТВ, опционально `wakeonlan`/`etherwake` и `mac` в привязке экранов.

**Только Lab:** VLC на ТВ, `http_listen`/`http_secret`, отсутствие `broadcast_bin`.

**Discovery:** dnsmasq lease file + группа `dnsmasq-leases`.

---

*Документ подготовлен по состоянию кода репозитория mediateca-station в рабочей копии агента; код станции не изменялся.*
