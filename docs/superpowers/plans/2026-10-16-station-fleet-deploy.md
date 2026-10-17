# Развёртывание station на несколько mini-PC — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Оператор одним скриптом ставит station на несколько mini-PC из файла, в котором только IP и agent token. Процесс стартует без локальных экранов и берёт `adb_serial`, `mac`, `stream_port` из `GET /api/agent/v1/config`. Экраны с одним `stream_port` делят один feeder.

**Architecture:** Инвентарь и рендер конфига — чистые функции shell, их проверяет `sh` без SSH. Скрипт один раз собирает linux/amd64-бинарник и по очереди ставит его на каждый IP: пользователь `mediateca`, unit systemd, строка `/etc/hosts`, YAML без `screens`. Чтобы такой YAML принимался и эфир шёл, станция на Voxel-пути разрешает пустой список экранов и подменяет его привязкой Hub. Локальный YAML с экранами остаётся прежним пересечением с config Hub. Канал эфира ключуется портом: второй экран того же порта только открывает URL, не пишет второй поток в FIFO.

**Tech Stack:** Go 1.26 (`go.mod`), `gofmt`, `go test ./...`. Скрипт — POSIX `sh`, проверка как у `docker/devnet_test.sh`. SSH и `sudo` на mini-PC. Hub не входит в этот план.

**Spec:** `docs/superpowers/specs/2026-10-16-hub-screen-wiring-design.md` — контракт config, который станция начинает читать. Реализацию Hub делает другой агент в `mediateca-broadcast`. Этот план Hub не меняет.

## Global Constraints

- Файл инвентаря: строки `IP<TAB>token`. Пустые строки и строки с `#` в первой колонке пропускать. Повтор IP — ошибка. Token — непустая строка `^[0-9a-fA-F]+$` без пробелов. Файл инвентаря должен быть mode `0600`, иначе скрипт выходит до SSH.
- В инвентаре нет экранов, портов и MAC.
- `hub_base_url` на каждой станции: `https://broadcast.mediateca.store`.
- На mini-PC в `/etc/hosts` ровно одна строка `192.168.100.250 broadcast.mediateca.store`. Повторный запуск заменяет старую строку с этим именем, остальные строки файла не трогает.
- Общий Voxel-шаблон, одинаковый для всех площадок: `broadcast_bind: "192.168.33.1"`, `broadcast_bin: "/home/stream/bin/broadcast"`, `fifo_dir: "/tmp"`, `voxel_apk: "/home/stream/apk/voxel-debug.apk"`, `chunk_size: 18800`, `feeder_pace: "realtime"`, `late_threshold: "2s"`, `poll_interval: "1m"`, `data_dir: "/var/lib/mediateca-station"`. Ключа `screens` в записанном YAML нет. Ключей `http_listen` и `http_secret` нет.
- Конфиг на mini-PC: `/etc/mediateca-station/config.yaml`, mode `0600`, владелец `mediateca`. Токен не попадает в stdout, stderr и `journalctl` по вине скрипта.
- Lab-путь (нет `broadcast_bin`) по-прежнему требует хотя бы один экран, `http_listen` и `http_secret` длиной ≥ 32. Дубли `screen_id` и, на Voxel-пути с перечисленными экранами, дубли `stream_port` в YAML по-прежнему ошибка загрузки.
- Voxel-путь с пустым или отсутствующим `screens` загружается. До первого config эфира нет.
- Поля экрана в config Hub, которые читает станция: `id`, `adb_serial`, `mac`, `stream_port`. Нет поля — нулевое значение. Экран без `adb_serial` вида `host:port` или без `stream_port > 0` не играет и не поднимает `broadcast`. Это не ошибка poll.
- Если локальный YAML уже содержит экраны, источником serial и порта остаётся YAML. Пересечение с `id` из Hub не меняется. Привязка Hub используется только когда локальный список пуст.
- Один `stream_port` — один `OpenWriter` и один feeder. Второй экран того же порта и того же `media_id` вызывает только `StartStream` своего телевизора. `Stop` последнего зрителя порта закрывает writer. `play_events` остаются по `screen_id`.
- Разный `media_id` на уже играющем порту не открывает второй writer. Первый экран, пришедший с новым `media_id`, сменяет ролик на канале. Остальные экраны этого порта присоединяются к уже сменённому ролику.
- Скрипт ходит по хостам последовательно. Сбой одного IP не останавливает остальные. Ненулевой статус, если хотя бы один IP не поставлен. SSH только `BatchMode=yes`.
- Репозиторий `mediateca-broadcast` не менять.

---

## File map

| Path | Responsibility |
| --- | --- |
| `internal/config/config.go` | Voxel-путь принимает конфиг без экранов. Lab-путь и дубли портов в YAML не ослаблять. |
| `internal/config/config_test.go` | Пустой Voxel-список принимается. Lab по-прежнему отвергает пустой список. |
| `internal/hub/client.go` | `Screen` читает `adb_serial`, `mac`, `stream_port`. |
| `internal/hub/client_test.go` | JSON с привязкой и JSON без новых полей. |
| `internal/agent/agent.go` | `screens()` выбирает YAML или привязку Hub. `ensureBroadcasts` и telemetry идут по уникальным портам этого списка. |
| `internal/agent/agent_test.go` | Пустой YAML не играет, пока Hub не прислал привязку. Два экрана одного порта оба попадают в playable. |
| `internal/air/controller.go` | Канал на порт: один writer, несколько зрителей. |
| `internal/air/controller_test.go` | Два serial одного порта. `Stop` первого не закрывает writer. |
| `deploy/install-stations.sh` | Парсер инвентаря, рендер YAML / hosts / unit, установка по SSH. |
| `deploy/stations.tsv.example` | Пример инвентаря без живого токена. |
| `deploy/install-stations_test.sh` | Проверка чистых функций, без сети. |
| `README.md` | Как запускать скрипт и что экраны приходят из Hub. |

---

### Task 1: Voxel-конфиг без экранов

**Files:**
- Modify: `internal/config/config.go` (`validate`)
- Test: `internal/config/config_test.go`

**Interfaces:**
- Consumes: текущий `Load`
- Produces: `Load` Voxel YAML без `screens` возвращает `File` с `len(Screens) == 0` и `VoxelPath() == true`. Lab YAML без экранов по-прежнему даёт ошибку `at least one screen`.

- [ ] **Step 1: Write the failing test**

В `internal/config/config_test.go` добавить:

```go
func TestLoadVoxelAllowsEmptyScreens(t *testing.T) {
	body := `
hub_base_url: "https://hub.example"
agent_token: "token"
data_dir: "/var/lib/mediateca-station"
broadcast_bind: "192.168.33.1"
broadcast_bin: "/home/stream/bin/broadcast"
fifo_dir: "/tmp"
voxel_apk: "/home/stream/apk/voxel-debug.apk"
`
	cfg, err := Load(writeConfig(t, body, 0o600))
	if err != nil {
		t.Fatal(err)
	}
	if !cfg.VoxelPath() || len(cfg.Screens) != 0 {
		t.Fatalf("%+v", cfg)
	}
}
```

Существующий кейс `no screens` в `TestLoadRejectsInvalidFields` не удалять: `validYAML` — lab-путь.

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/config/ -run TestLoadVoxelAllowsEmptyScreens -count=1`

Expected: FAIL, ошибка содержит `at least one screen`.

- [ ] **Step 3: Write minimal implementation**

В `validate` заменить безусловную проверку пустого списка. Пустой список запрещён только на lab-пути. На Voxel-пути пустой список законен, и цикл по экранам просто не выполняется.

```go
voxel := f.BroadcastBin != ""
if !voxel && len(doc.Screens) == 0 {
	return fmt.Errorf("at least one screen is required")
}
```

Блок `if voxel { ... } else { http_listen / http_secret }` оставить ниже, до цикла по экранам. Проверку `duplicate stream_port` внутри цикла не удалять: она действует только на экраны, которые оператор вписал в YAML.

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/config/ -count=1`

Expected: PASS, включая `TestLoadRejectsInvalidFields/no_screens` и `TestLoadVoxelPath`.

- [ ] **Step 5: Commit**

```bash
git add internal/config/config.go internal/config/config_test.go
git commit -m "$(cat <<'EOF'
feat(config): allow a Voxel station config with no screens

Hub will send screen wiring after install, so the mini-PC YAML no longer
needs a local screen list.

EOF
)"
```

---

### Task 2: Чтение привязки из config Hub

**Files:**
- Modify: `internal/hub/client.go` (`Screen`)
- Test: `internal/hub/client_test.go`

**Interfaces:**
- Consumes: JSON из спеки Hub (`adb_serial`, `mac`, `stream_port` рядом с `id`)
- Produces:
  - `hub.Screen` поля `ADBSerial string \`json:"adb_serial"\``, `MAC string \`json:"mac"\``, `StreamPort int \`json:"stream_port"\``
  - Отсутствующие поля остаются нулевыми. Старый JSON без них по-прежнему парсится.

- [ ] **Step 1: Write the failing test**

Добавить в `internal/hub/client_test.go`:

```go
func TestFetchConfigReadsScreenWiring(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, `{"station_id":3,"offline_cache_hours":24,"screens":[
			{"id":7,"name":"Entrance","orientation":"landscape","adb_serial":"192.168.33.182:5555","mac":"78:53:33:4b:2c:29","stream_port":8091},
			{"id":8,"name":"Window","orientation":"portrait"}
		]}`)
	}))
	defer srv.Close()
	cfg, err := New(srv.URL, "tok").FetchConfig(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Screens[0].ADBSerial != "192.168.33.182:5555" || cfg.Screens[0].MAC != "78:53:33:4b:2c:29" || cfg.Screens[0].StreamPort != 8091 {
		t.Fatalf("wired %+v", cfg.Screens[0])
	}
	if cfg.Screens[1].ADBSerial != "" || cfg.Screens[1].StreamPort != 0 {
		t.Fatalf("unwired %+v", cfg.Screens[1])
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/hub/ -run TestFetchConfigReadsScreenWiring -count=1`

Expected: FAIL, `ADBSerial` пустой (поля ещё нет в структуре; тест компилируется только после Step 3, поэтому сначала добавить поля с пустыми тегами нельзя — сначала тест не соберётся). Написать тест и поля в одном шаге, если компилятор не видит полей: Step 2 тогда ожидает `cfg.Screens[0].ADBSerial undefined`.

- [ ] **Step 3: Write minimal implementation**

```go
type Screen struct {
	ID          int    `json:"id"`
	Name        string `json:"name"`
	Orientation string `json:"orientation"`
	ADBSerial   string `json:"adb_serial"`
	MAC         string `json:"mac"`
	StreamPort  int    `json:"stream_port"`
}
```

`FetchConfig` не фильтрует экраны. Фильтр «можно играть» делает агент.

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/hub/ -count=1`

Expected: PASS. `TestFetchConfig` без новых полей тоже PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/hub/client.go internal/hub/client_test.go
git commit -m "$(cat <<'EOF'
feat(hub): read screen wiring from agent config

Station can take adb_serial, mac, and stream_port from the Hub payload.

EOF
)"
```

---

### Task 3: Пустой YAML играет экраны Hub

**Files:**
- Modify: `internal/agent/agent.go` (`playable`, `logMapping`, `ensureBroadcasts`, `broadcastOK`, `buildTelemetry` — везде, где сейчас цикл по `a.cfg.Screens` для эфира)
- Test: `internal/agent/agent_test.go`

**Interfaces:**
- Consumes: `config.File.Screens`, `hub.Screen.ADBSerial`, `hub.Screen.MAC`, `hub.Screen.StreamPort`
- Produces: `func (a *Agent) screens() []config.Screen`
  - локальный список непуст → вернуть его как сейчас;
  - локальный список пуст и `a.hubCfg == nil` → пустой срез;
  - локальный список пуст → по каждому `a.hubCfg.Screens` собрать `config.Screen`, если `adb_serial` содержит `:` не на краях и `stream_port > 0`. Повтор `id` пропустить. Повтор `stream_port` оставить.
  - `playable` при пустом локальном списке возвращает id из `screens()`. При непустом локальном списке логика пересечения с Hub не меняется.

- [ ] **Step 1: Write the failing test**

В `internal/agent/agent_test.go` добавить:

```go
func TestHubWiringScreensWhenYAMLEmpty(t *testing.T) {
	cfg := voxelTestCfg(t.TempDir())
	cfg.Screens = nil
	a := &Agent{
		cfg: cfg,
		hubCfg: &hub.Config{Screens: []hub.Screen{
			{ID: 7, ADBSerial: "192.168.33.182:5555", MAC: "78:53:33:4b:2c:29", StreamPort: 8091},
			{ID: 8, ADBSerial: "192.168.33.183:5555", MAC: "78:53:33:4b:2c:30", StreamPort: 8091},
			{ID: 9, Name: "Window"},
			{ID: 7, ADBSerial: "192.168.33.182:5555", StreamPort: 8091},
		}},
	}
	got := a.screens()
	if len(got) != 2 || got[0].ScreenID != 7 || got[1].ScreenID != 8 {
		t.Fatalf("screens %+v", got)
	}
	if got[0].ADBSerial != "192.168.33.182:5555" || got[0].MAC != "78:53:33:4b:2c:29" || got[0].StreamPort != 8091 || got[1].StreamPort != 8091 {
		t.Fatalf("wiring %+v", got)
	}
	ids := a.playable()
	if len(ids) != 2 || ids[0] != 7 || ids[1] != 8 {
		t.Fatalf("playable %v", ids)
	}
}

func TestLocalYAMLIgnoresHubWiring(t *testing.T) {
	cfg := voxelTestCfg(t.TempDir())
	a := &Agent{
		cfg: cfg,
		hubCfg: &hub.Config{Screens: []hub.Screen{
			{ID: 8, ADBSerial: "192.168.33.183:5555", StreamPort: 8092},
		}},
	}
	ids := a.playable()
	if len(ids) != 0 {
		t.Fatalf("yaml screen 7 is not in hub config, playable %v", ids)
	}
	if len(a.screens()) != 1 || a.screens()[0].ScreenID != 7 {
		t.Fatalf("screens %+v", a.screens())
	}
}
```

`voxelTestCfg` уже задаёт локальный экран 7. Во втором тесте Hub знает только экран 8, поэтому пересечение пустое. Это текущее правило, его нельзя заменить привязкой Hub, пока YAML не пуст.

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/agent/ -run 'TestHubWiring|TestLocalYAML' -count=1`

Expected: FAIL, `screens` не определён либо пустой playable.

- [ ] **Step 3: Write minimal implementation**

Добавить `screens()` по контракту из Interfaces. Заменить циклы эфира:

- `ensureBroadcasts` и `broadcastOK` идут по `a.screens()`, но `voxel.Ensure` вызывается один раз на каждый `StreamPort` (`seen map[int]struct{}`).
- `buildTelemetry` и `tick` берут экраны из `a.screens()`.
- `playable`: если `len(a.cfg.Screens) == 0`, вернуть id из `a.screens()`. Иначе оставить текущее пересечение.
- `logMapping`: если локальный список пуст, для hub-экрана без привязки один раз записать `screen mapping` с `reason=waiting_for_wiring`. Не писать `no_serial` на каждом poll. Экраны, которые `screens()` принял, не логировать как `no_serial`.

`Serial` в `tick` вызывать от `config.File`, собранного из `a.screens()`, либо маленьким циклом по `a.screens()` — не читать serial только из YAML, когда список YAML пуст.

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/agent/ -count=1`

Expected: PASS. Старые тесты с локальным экраном 7 не меняют поведение.

- [ ] **Step 5: Commit**

```bash
git add internal/agent/agent.go internal/agent/agent_test.go
git commit -m "$(cat <<'EOF'
feat(agent): play Hub screen wiring when local YAML has none

A deployed station can start before any television is known and pick up
adb targets from the next config poll.

EOF
)"
```

---

### Task 4: Один feeder на stream_port

**Files:**
- Modify: `internal/air/controller.go`
- Test: `internal/air/controller_test.go`

**Interfaces:**
- Consumes: `StartReq{ScreenID, Serial, Port, MediaID, Duration}`, `TV.StartStream`, `TV.Stop`
- Produces: то же публичное API `Start`, `Stop`, `GapNull`. Поведение:
  - два `Start` с одним `Port` и одним `MediaID` и разными `ScreenID` → `OpenWriter` один раз, `StartStream` два раза;
  - `Stop` первого зрителя не закрывает writer и не останавливает feeder;
  - `Stop` последнего зрителя закрывает writer;
  - повторный `Start` того же `ScreenID` и того же `MediaID` не вызывает `StartStream` второй раз (`TestControllerJoinReusesWriterAndSkipsSecondStartStream` остаётся зелёным);
  - `Start` с другим `MediaID` на том же порту отменяет прежний feeder, writer переоткрывает только если прежний закрыт, и зовёт `StartStream` только у экрана, который ещё не смотрит этот ролик.

- [ ] **Step 1: Write the failing test**

Добавить `TestControllerTwoScreensShareOnePort`:

```go
func TestControllerTwoScreensShareOnePort(t *testing.T) {
	dir := t.TempDir()
	media := filepath.Join(dir, "19")
	if err := os.WriteFile(media, []byte{0x47, 0x00}, 0o600); err != nil {
		t.Fatal(err)
	}
	var openCalls atomic.Int32
	var closeCalls atomic.Int32
	r, pipeW, _ := os.Pipe()
	go io.Copy(io.Discard, r)
	tracked := &countCloser{WriteCloser: pipeW, onClose: func() { closeCalls.Add(1) }}
	tvs := map[string]*memTV{}
	c := &Controller{
		OpenWriter: func(port int) (io.WriteCloser, error) {
			openCalls.Add(1)
			return tracked, nil
		},
		NewTV: func(serial string) TV {
			tv := &memTV{}
			tvs[serial] = tv
			return tv
		},
		MediaPath: func(id int) string { return media },
		StreamURL: func(screenID int) string { return "http://192.168.33.1:8091/" },
		Pace:      "max",
		ChunkSize: 18800,
	}
	ctx := context.Background()
	first := StartReq{ScreenID: 7, Serial: "192.168.33.182:5555", Port: 8091, MediaID: 19, Duration: time.Second}
	second := StartReq{ScreenID: 8, Serial: "192.168.33.183:5555", Port: 8091, MediaID: 19, Duration: time.Second}
	if err := c.Start(ctx, first); err != nil {
		t.Fatal(err)
	}
	if err := c.Start(ctx, second); err != nil {
		t.Fatal(err)
	}
	if openCalls.Load() != 1 {
		t.Fatalf("OpenWriter=%d", openCalls.Load())
	}
	if len(tvs[first.Serial].urls) != 1 || len(tvs[second.Serial].urls) != 1 {
		t.Fatalf("starts %d %d", len(tvs[first.Serial].urls), len(tvs[second.Serial].urls))
	}
	if err := c.Stop(ctx, first.Serial, first.ScreenID); err != nil {
		t.Fatal(err)
	}
	if closeCalls.Load() != 0 || tvs[first.Serial].stops != 1 {
		t.Fatalf("close=%d stops=%d", closeCalls.Load(), tvs[first.Serial].stops)
	}
	if err := c.Stop(ctx, second.Serial, second.ScreenID); err != nil {
		t.Fatal(err)
	}
	if closeCalls.Load() != 1 {
		t.Fatalf("close=%d", closeCalls.Load())
	}
}
```

`NewTV` в тесте должен возвращать тот же `*memTV` на повторный вызов serial. Кэш `tvs` заполнять до возврата и при повторном serial возвращать уже созданный объект.

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/air/ -run TestControllerTwoScreensShareOnePort -count=1`

Expected: FAIL. Текущий `Start` открывает writer на каждый `ScreenID` либо второй `Stop` не единственный `Close`. Зафиксировать фактический провал в сообщении теста, не подгоняя ожидание под старое поведение.

- [ ] **Step 3: Write minimal implementation**

Заменить карты `feeds` / `writers` / `ports` по `screenID` на одну карту канала по порту:

```go
type channel struct {
	mediaID int
	writer  io.WriteCloser
	cancel  context.CancelFunc
	done    chan struct{}
	viewers map[int]string // screenID -> serial
}
```

`Start`:

1. Если файл медиа не открывается — вернуть ошибку, канал не трогать.
2. Под `c.mu`: если канал порта есть и `mediaID` совпал — добавить зрителя. Если этот `screenID` уже был, вернуть nil без `StartStream`. Если зритель новый, после отпускания мьютекса вызвать `StartStream` и вернуть его ошибку.
3. Если канала нет или `mediaID` другой — забрать прежний канал из карты, записать новый с одним зрителем и новым `mediaID`, отпустить мьютекс, дождаться `done` прежнего feeder, закрыть прежний writer только если он не будет переиспользован. Для смены ролика открыть writer заново одним `OpenWriter` (прежний закрыть до открывания, чтобы FIFO не имел двух писателей). Затем `StartStream` только для экранов из нового списка зрителей, у которых этот URL ещё не запускался в этом вызове (в новом канале это один экран — инициатор смены).
4. Запустить один `feeder.FeedFileDuration` на writer канала.

`Stop`:

1. Удалить `screenID` из `viewers` этого порта. Вызвать `TV.Stop` этого serial.
2. Если зрители ещё есть — writer и feeder не трогать.
3. Если зрителей не осталось — `cancel`, дождаться `done`, `Close` writer, удалить канал.

`GapNull(screenID, count)` пишет в writer того канала, где `screenID` есть среди зрителей. Нет канала — `nil`, как сейчас при отсутствии writer.

Удерживать мьютекс только на картах. Не вызывать `StartStream`, `Stop` и `FeedFileDuration` под мьютексом.

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/air/ -count=1`

Expected: PASS, включая `TestControllerStartFeedsAndStartsTV` и `TestControllerJoinReusesWriterAndSkipsSecondStartStream`.

- [ ] **Step 5: Commit**

```bash
git add internal/air/controller.go internal/air/controller_test.go
git commit -m "$(cat <<'EOF'
feat(air): share one feeder across screens on the same port

Televisions with one Hub playlist attach as viewers of a single MPEG-TS
channel instead of each writing the FIFO.

EOF
)"
```

---

### Task 5: Скрипт установки

**Files:**
- Create: `deploy/install-stations.sh`
- Create: `deploy/stations.tsv.example`
- Create: `deploy/install-stations_test.sh`
- Modify: `README.md` (секция после Voxel production path)

**Interfaces:**
- Consumes: бинарник `./cmd/station`, шаблон YAML из Global Constraints
- Produces функции в `deploy/install-stations.sh`, которые тест вызывает после `. ./deploy/install-stations.sh` при `DEPLOY_STATIONS_LIB=1` (при этом значении скрипт не вызывает `main`):
  - `parse_inventory path` — печатает в stdout строки `ip<TAB>token`, в stderr ничего с токеном при успехе; при ошибке ненулевой статус и сообщение без значения токена
  - `render_config token` — YAML на stdout, mode не проверяет (mode ставит вызывающий код)
  - `render_hosts_file current` — файл hosts на stdout с одной строкой имени `broadcast.mediateca.store`
  - `render_unit` — текст systemd unit на stdout
  - `main` при обычном запуске: проверить mode инвентаря, собрать бинарник, для каждого IP выполнить установку

- [ ] **Step 1: Write the failing test**

Создать `deploy/install-stations_test.sh` по образцу `docker/devnet_test.sh` (`set -eu`, `fail`, source библиотеки).

```sh
#!/bin/sh
set -eu
cd "$(dirname "$0")/.."
DEPLOY_STATIONS_LIB=1
# shellcheck disable=SC1091
. ./deploy/install-stations.sh

fail() { echo "FAIL: $*" >&2; exit 1; }

tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT
printf '%s\n' '# comment' '' '192.168.1.204	abcdef' > "$tmp/inv"
got=$(parse_inventory "$tmp/inv")
[ "$got" = "192.168.1.204	abcdef" ] || fail "parse: $got"

printf '%s\n' '192.168.1.204	abcdef' '192.168.1.204	ffffff' > "$tmp/dup"
if parse_inventory "$tmp/dup" >/dev/null 2>"$tmp/err"; then
  fail "duplicate ip must fail"
fi
grep -q abcdef "$tmp/err" && fail "token leaked into the error"

cfg=$(render_config "abcdef")
printf '%s\n' "$cfg" | grep -qx 'hub_base_url: "https://broadcast.mediateca.store"' || fail "hub url"
printf '%s\n' "$cfg" | grep -qx 'agent_token: "abcdef"' || fail "token"
if printf '%s\n' "$cfg" | grep -q '^screens:'; then
  fail "deploy config must not list screens"
fi
printf '%s\n' "$cfg" | grep -qx 'broadcast_bind: "192.168.33.1"' || fail "bind"

hosts=$(printf '%s\n' '127.0.0.1 localhost' '1.2.3.4 broadcast.mediateca.store' | render_hosts_file)
printf '%s\n' "$hosts" | grep -qx '127.0.0.1 localhost' || fail "localhost dropped"
printf '%s\n' "$hosts" | grep -c 'broadcast.mediateca.store' | grep -qx 1 || fail "name count"
printf '%s\n' "$hosts" | grep -qx '192.168.100.250 broadcast.mediateca.store' || fail "hub line"

unit=$(render_unit)
printf '%s\n' "$unit" | grep -qx 'User=mediateca' || fail "user"
printf '%s\n' "$unit" | grep -qx 'ExecStart=/usr/local/bin/mediateca-station -config /etc/mediateca-station/config.yaml' || fail "exec"

echo "ok"
```

- [ ] **Step 2: Run test to verify it fails**

Run: `sh deploy/install-stations_test.sh`

Expected: FAIL, скрипта ещё нет либо функции не определены.

- [ ] **Step 3: Write minimal implementation**

`deploy/stations.tsv.example`:

```text
# ip	token
# 192.168.1.204	paste the token from Station#assign_agent_token!
```

Разделитель в примере — настоящий TAB. В комментарии написать, что живой токен в репозиторий не кладут. Добавить `deploy/stations.tsv` в `.gitignore`, если файла ещё нет в ignore.

`deploy/install-stations.sh`:

- `set -eu`
- функции `parse_inventory`, `render_config`, `render_hosts_file`, `render_unit`
- `render_config` подставляет только проверенный hex-токен в двойные кавычки
- `render_hosts_file` читает stdin, выбрасывает строки, где второе поле `broadcast.mediateca.store`, дописывает `192.168.100.250 broadcast.mediateca.store`
- `render_unit` — unit из README (секция Mini-PC) без привязки к имени chrony: `After=network-online.target` и `Wants=network-online.target`, `User=mediateca`, `SupplementaryGroups=dnsmasq-leases`, `NoNewPrivileges=true`
- в конце файла:

```sh
if [ "${DEPLOY_STATIONS_LIB:-}" = 1 ]; then
  return 0 2>/dev/null || exit 0
fi
```

`main`:

1. Аргумент — путь к инвентарю. Нет аргумента — usage в stderr и статус 2.
2. Отказать, если mode файла не `0600` (`stat -c %a`). В сообщении нет содержимого файла.
3. `parse_inventory`. Пустой результат — ошибка «no stations».
4. Один раз: `GOOS=linux GOARCH=amd64 go build -o "$tmp/mediateca-station" ./cmd/station` из корня репозитория (каталог скрипта `/deploy`, корень — на уровень выше).
5. На каждый IP, `SSH_USER` по умолчанию `$USER`:
   - `ssh -o BatchMode=yes "${SSH_USER}@${ip}"` с `sudo` создаёт пользователя `mediateca`, группу `dnsmasq-leases`, каталоги как в README Mini-PC;
   - `scp` бинарника в `/usr/local/bin/mediateca-station`, mode `0755`;
   - заливает stdout `render_config` в конфиг через `install -m 0600 -o mediateca -g mediateca`;
   - на удалённой стороне читает `/etc/hosts`, прогоняет через `render_hosts_file`, кладёт результат на место атомарно (`mktemp` + `mv`);
   - пишет unit, `systemctl daemon-reload`, `systemctl enable --now mediateca-station`, затем `systemctl restart mediateca-station`;
   - печатает только `installed <ip>` или `failed <ip> <stage>`.
6. После цикла удалить временный бинарник. Статус 0, если все IP прошли, иначе 1.

Не вызывать `ssh` из тестового режима. Функции рендера не открывают сеть.

- [ ] **Step 4: Run test to verify it passes**

Run: `sh deploy/install-stations_test.sh && gofmt -w internal/config/config.go internal/hub/client.go internal/agent/agent.go internal/air/controller.go && go test ./...`

Expected: `ok` от shell-теста и PASS от `go test ./...`.

- [ ] **Step 5: README**

В `README.md` после секции Voxel production path добавить секцию «Несколько mini-PC». Текст:

- инвентарь `deploy/stations.tsv` (пример — `deploy/stations.tsv.example`), mode `0600`, колонки IP и token;
- команда `sh deploy/install-stations.sh deploy/stations.tsv` с уже поднятым VPN до `192.168.1.0/24`;
- скрипт пишет hosts на `192.168.100.250` и YAML без экранов;
- экраны появляются после того, как Hub отдаёт привязку в config; до этого в логе нет эфира;
- `SSH_USER`, если логин не равен локальному `$USER`;
- повторный запуск обновляет бинарник и перезапускает unit.

Не вставлять живой токен и не описывать шаги Hub.

- [ ] **Step 6: Commit**

```bash
git add deploy/install-stations.sh deploy/install-stations_test.sh deploy/stations.tsv.example README.md .gitignore
git commit -m "$(cat <<'EOF'
feat(deploy): install station on mini-PCs from an ip and token file

The host list stays an access inventory. Screen wiring arrives from the Hub
config after the process starts.

EOF
)"
```

---

## Self-review

Покрытие спеки Hub этим планом:

| Требование спеки | Задача |
| --- | --- |
| Читать `adb_serial`, `mac`, `stream_port` из config | Task 2, Task 3 |
| Несколько экранов одного порта — один канал | Task 4 |
| Экран без полей привязки не играть и не ронять poll | Task 3 |
| Пакет v2 и `screen_ids` не менять | вне правок |
| Пустой YAML на новой площадке | Task 1, Task 5 |
| Запись hosts и домен Hub | Task 5 |
| Реализация реестра портов на Hub | не этот план |

Проверено при составлении: в тексте нет TBD; `screens()` и поля `hub.Screen` называются одинаково в Task 2 и Task 3; `render_config` / `parse_inventory` / `render_hosts_file` / `render_unit` совпадают в тесте и в интерфейсе Task 5.
