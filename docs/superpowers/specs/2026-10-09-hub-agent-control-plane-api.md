# Hub Agent Control Plane API — контракт для доработки Broadcast Hub

> **Назначение:** нормативное описание API, которое уже реализует
> `mediateca-station` (ветка `002-voxel-integration`). По этому документу
> агент/разработчик дорабатывает **Broadcast Hub (Rails)**, чтобы устранить
> расхождение контрактов.
>
> **Источник истины:** поведение station в коде
> (`internal/hub/client.go`, `internal/commands/executor.go`,
> `internal/agent/agent.go`), а не устаревшие черновики. Где design-spec
> station расходится с кодом — побеждает этот документ.
>
> **Репозиторий Hub:** `mediateca-broadcast` (не менять station в рамках
> этой задачи, кроме согласования багов контракта).
>
> **Связанные доки station:**
> - `docs/superpowers/specs/2026-10-09-station-voxel-integration-design.md`
> - `docs/superpowers/plans/2026-10-09-station-voxel-m2-control-plane.md`

---

## 1. Модель взаимодействия

- Station **всегда инициирует** HTTPS к Hub. Hub **не** открывает TCP на mini-PC.
- Auth на всех `/api/agent/*`: заголовок  
  `Authorization: Bearer <agent_token>`  
  Токен привязан к одной station. Невалидный/неизвестный → **HTTP 401** + тело  
  `{ "error": "unauthorized" }` (как уже для config/package).
- Скачивание медиа (Active Storage) — **без** Bearer; не часть control plane.
- При 401 на telemetry / commands / command_results station ставит на паузу
  drain `play_events`, telemetry и commands, пока следующий poll
  `GET config` или `GET package` не вернёт **200 или 304**.

Цикл station (упрощённо):

```
poll_interval:  GET /api/agent/v1/config
                GET /api/agent/v2/package  (+ If-None-Match)
                GET /api/agent/v1/commands
                (async) execute → POST /api/agent/v1/command_results

telemetry_interval (из tv_monitor или default 60s):
                POST /api/agent/v1/telemetry   (async, не блокирует эфир)

независимо:    POST /api/agent/v1/play_events  (как раньше)
```

---

## 2. Уже существующие эндпоинты (не ломать)

| Метод | Путь | Заметки |
| --- | --- | --- |
| GET | `/api/agent/v1/config` | расширить телом (см. §3) |
| GET | `/api/agent/v2/package` | ETag / If-None-Match / 304 |
| POST | `/api/agent/v1/play_events` | без изменений семантики |

---

## 3. Расширение `GET /api/agent/v1/config`

Добавить опциональный объект `tv_monitor`. Если поля нет — station использует
дефолты: health **30s**, auto_restart **true**, telemetry **60s**.

### 200 body (пример)

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

| Поле | Тип | Смысл |
| --- | --- | --- |
| `health_interval_seconds` | int | Интервал локального watchdog на station (station clamp ≥ 5) |
| `auto_restart_when_not_playing` | bool | Авто `restart_stream` при эфире и `playing=false` |
| `telemetry_interval_seconds` | int | Как часто station шлёт telemetry |

**Важно для Hub:** если отдаёте `tv_monitor: {}` или объект без
`auto_restart_when_not_playing`, JSON bool станет `false` на station и
авторестарт **выключится**. Чтобы сохранить дефолт true — либо не слать
`tv_monitor`, либо явно `"auto_restart_when_not_playing": true`.

Рекомендация Hub: хранить настройки на station/admin UI; при отсутствии
записи в БД **не включать** ключ `tv_monitor` в JSON (а не слать пустой объект).

---

## 4. Новый: `POST /api/agent/v1/telemetry`

Station пушит снимок состояния площадки.

### Request

Headers: `Authorization`, `Content-Type: application/json`, `Accept: application/json`

```json
{
  "reported_at": "2026-10-09T11:00:00Z",
  "station": {
    "uptime_seconds": 3600,
    "package_etag": "W/\"…\"",
    "broadcast_ok": true
  },
  "screens": [
    {
      "screen_id": 7,
      "reachable": true,
      "agent_ok": true,
      "playing": true,
      "url": "http://192.168.33.1:8091/",
      "wakefulness": "",
      "stream_port": 8091,
      "broadcast_clients": 0,
      "last_error": null
    }
  ]
}
```

| Поле | Тип | Примечание (факт station) |
| --- | --- | --- |
| `reported_at` | RFC3339 UTC | время сборки снимка |
| `station.uptime_seconds` | int | uptime процесса station |
| `station.package_etag` | string | сырой ETag последнего package (может быть пустым) |
| `station.broadcast_ok` | bool | удалось ли Ensure broadcast по портам |
| `screens[].screen_id` | int | id экрана Hub |
| `screens[].reachable` | bool | агент `:8989` или сеть достижима |
| `screens[].agent_ok` | bool | HTTP agent ответил |
| `screens[].playing` | bool | из `/api/status` плеера |
| `screens[].url` | string | текущий URL стрима |
| `screens[].wakefulness` | string | **сейчас часто пустая строка** (не заполняется) |
| `screens[].stream_port` | int | порт Voxel broadcast |
| `screens[].broadcast_clients` | int | **сейчас обычно 0** (не заполняется) |
| `screens[].last_error` | string\|null | опционально |

Hub должен **принимать** поля даже если они пустые/нулевые; не 422 из‑за
пустого `wakefulness`.

### Response

- **202 Accepted** или **204 No Content** — успех (station принимает оба).
- **401** — unauthorized (пауза control plane).
- **4xx/5xx** кроме 401 — station логирует и повторит на следующем интервале.

### Поведение Hub

- Идемпотентная запись «последнего снимка» per station (и история — по желанию).
- Привязка `screen_id` только к экранам этой station; чужой id — игнорировать
  или копить в `last_error` админки, но **не** валить весь батч 422
  (station шлёт все локальные экраны одним запросом).

---

## 5. Новый: `GET /api/agent/v1/commands`

Очередь команд **на эту station**, только **per-screen** (`screen_id` обязателен).

### Request

Headers: `Authorization`, `Accept: application/json`  
Опционально: `If-None-Match: <etag>` (station умеет; сейчас часто шлёт пустой).

### Response 200

```json
{
  "commands": [
    {
      "id": "cmd_01HXYZ…",
      "screen_id": 7,
      "type": "restart_stream",
      "params": {},
      "issued_at": "2026-10-09T11:01:00Z"
    }
  ]
}
```

Пустая очередь:

```json
{ "commands": [] }
```

Опционально: заголовок `ETag` на ответ; при совпадении `If-None-Match` → **304**
и пустое тело (station трактует как пустую очередь).

### Семантика очереди (обязательно согласовать на Hub)

1. Команда появляется в GET, пока station не подтвердит её через
   `POST command_results` с тем же `command_id`.
2. После успешного ack Hub **убирает** команду из выдачи (или помечает
   completed и больше не отдаёт в GET).
3. Повторный GET до ack может вернуть ту же команду — station дедупит
   локально (~100 последних id) и отвечает `ok: true`, `error: "already_done"`.
   Hub обязан принять такой result как успешный ack (HTTP 200).
4. `id` — стабильный уникальный string (ULID/UUID). Не переиспользовать id.
5. `issued_at` — RFC3339 UTC string.
6. `params` — JSON object (может быть `{}` или отсутствовать → station даст empty map).

### Типы команд и params (как station исполняет)

| `type` | `params` | Требования Hub |
| --- | --- | --- |
| `start_stream` | опц. `url` (string), `buffer_ms` (number, default 300) | если `url` нет — station подставит свой channel URL |
| `restart_stream` | `{}` | — |
| `stop` | `{}` | — |
| `wake` | `{}` | на station нужен `mac` в YAML для WOL-fallback |
| `sleep` | `{}` | — |
| `screenshot` | **`upload_url` (string, обязателен)** | signed HTTPS PUT URL; Content-Type при загрузке `image/png` |
| `record` | **`upload_url` (обязателен)**, опц. `seconds` (1–120, default 10) | PUT `video/mp4` |
| `ensure_app` | `{}` | — |
| `install_apk` | опц. `url` (**только https**) | без `url` — локальный APK на station |

**Факт реализации (важно):** без `upload_url` для `screenshot`/`record`
station вернёт `ok: false`, `error: "missing_upload_url"` и **не** шлёт
multipart в `command_results`. Multipart-fallback из раннего design-spec
**не реализован** — Hub обязан всегда выдавать `upload_url`.

### Загрузка артефактов

1. Hub при создании команды `screenshot`/`record` генерирует
   short-lived signed URL (Active Storage / S3 presign) на **HTTP PUT**.
2. Кладёт его в `params.upload_url`.
3. Station делает `PUT` байтов **без** Bearer station-токена (URL сам
   авторизует).
4. Ожидаемый успех PUT: любой **2xx**.
5. Затем station шлёт `command_results` без тела файла.

Рекомендуемый TTL upload URL: ≥ 5 минут (record до 120s + запас).

---

## 6. Новый: `POST /api/agent/v1/command_results`

### Request

```json
{
  "results": [
    {
      "command_id": "cmd_01HXYZ…",
      "ok": true,
      "error": null,
      "finished_at": "2026-10-09T11:01:02Z"
    },
    {
      "command_id": "cmd_01HABC…",
      "ok": false,
      "error": "missing_upload_url",
      "finished_at": "2026-10-09T11:01:03Z"
    }
  ]
}
```

| Поле | Тип |
| --- | --- |
| `command_id` | string |
| `ok` | bool |
| `error` | string \| null |
| `finished_at` | RFC3339 UTC |

### Response

- **200 OK** — успех; повтор с тем же `command_id` тоже **200** (идемпотентность).
- **401** — unauthorized.
- Не отдавать 404 на unknown `command_id` так, чтобы station ретраил вечно:
  лучше **200** + ignore, либо 200 already_applied.

### Известные значения `error` от station

| error | Смысл |
| --- | --- |
| `unknown_type` | тип команды не поддержан |
| `missing_upload_url` | нет `params.upload_url` у screenshot/record |
| `missing_stream_url` | не удалось собрать URL для start_stream |
| `already_done` | дедуп на station; считать ack успешным (`ok: true`) |
| `not_configured` | нет TV/Device wiring (редко) |
| `install_apk url must be https` | `params.url` не https |
| произвольная строка | текст ошибки adb/HTTP/install |

---

## 7. Что нужно сделать на Hub (чеклист реализации)

### Rails / API

- [ ] Расширить serializer `GET /api/agent/v1/config` полем `tv_monitor` (опционально).
- [ ] `POST /api/agent/v1/telemetry` — принять JSON §4, сохранить last snapshot (+ опц. история).
- [ ] Модель очереди команд station (pending → running/acked).
- [ ] `GET /api/agent/v1/commands` — только pending для token→station; формат §5.
- [ ] Генерация `upload_url` (PUT) для screenshot/record.
- [ ] `POST /api/agent/v1/command_results` — ack, идемпотентность по `command_id`.
- [ ] Все новые маршруты под тот же agent auth middleware, что config/package.
- [ ] Тесты request specs: 401 без токена; 202/204 telemetry; пустая очередь; ack убирает команду; дубль result 200.

### Admin / ops (минимум)

- [ ] UI или API для постановки команды на экран station (`type` + `params`).
- [ ] Просмотр последнего telemetry snapshot per station/screen.
- [ ] Настройки `tv_monitor` per station.

### Не делать / не ожидать от station в этой версии

- Multipart file в `command_results`.
- Station-wide команды без `screen_id`.
- Заполненные `wakefulness` / `broadcast_clients` в telemetry (поля есть, значения могут быть пустыми — Hub не должен падать).
- Входящий push Hub → station.

---

## 8. Примеры для тестов Hub

### Поставить screenshot

Создать pending command:

```json
{
  "id": "cmd_test_shot_1",
  "screen_id": 7,
  "type": "screenshot",
  "params": {
    "upload_url": "https://hub.example/rails/active_storage/direct_uploads/..."
  },
  "issued_at": "2026-10-09T12:00:00Z"
}
```

Station: PUT PNG → POST result `{ "ok": true, "error": null, ... }`.

### Поставить record на 15 секунд

```json
{
  "id": "cmd_test_rec_1",
  "screen_id": 7,
  "type": "record",
  "params": {
    "upload_url": "https://…",
    "seconds": 15
  },
  "issued_at": "2026-10-09T12:00:00Z"
}
```

### Ошибочная команда без upload_url

```json
{
  "id": "cmd_bad",
  "screen_id": 7,
  "type": "screenshot",
  "params": {},
  "issued_at": "2026-10-09T12:00:00Z"
}
```

Ожидаемый result: `ok: false`, `error: "missing_upload_url"`, команда всё равно
должна уйти из pending после ack.

---

## 9. Критерии готовности Hub

Hub считается совместимым со station M2, когда:

1. Реальный station (или интеграционный тест с записанными запросами station)
   успешно шлёт telemetry и получает 202/204.
2. Команда `restart_stream` из Hub появляется в GET, исполняется, ack'ается,
   исчезает из очереди.
3. `screenshot` с валидным `upload_url` кладёт PNG в storage Hub.
4. 401 на любом agent-маршруте останавливает reporting на station до
   восстановления токена (поведение уже на station — Hub должен отдавать 401
   консистентно).
5. `tv_monitor` из admin меняет интервалы на живой station после следующего
   config poll.

---

## 10. Открытые согласования (не блокеры первого PR Hub)

| Тема | Рекомендация |
| --- | --- |
| Хранить историю telemetry | да, с TTL (например 7–14 дней) |
| UI конфликтов: Hub `stop` vs auto-restart | документировать: при `auto_restart_when_not_playing` stop может быть «отменён» health |
| `*bool` для auto_restart | опциональный follow-up на station; пока Hub шлёт явное true/false |
| Заполнение wakefulness | follow-up на station; Hub уже принимает string |

---

## 11. Для агента, пишущего код Hub

Работай в репозитории **Broadcast Hub** (`mediateca-broadcast`).

1. Найди существующие agent routes (`/api/agent/v1/config`, `v2/package`,
   `play_events`) и тот же auth.
2. Добавь три маршрута и расширение config строго по JSON из этого файла.
3. Покрой request specs таблицей из §7.
4. Не меняй контракт package/play_events.
5. Если нужен пример клиента — смотри
   `mediateca-station/internal/hub/client.go` (методы `PostTelemetry`,
   `FetchCommands`, `PostCommandResults`, `UploadArtifact`).

После merge Hub — прогнать smoke против station на `media-voxel` или
compose-стенде из README station.
