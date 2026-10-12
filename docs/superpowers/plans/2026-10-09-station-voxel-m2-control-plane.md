# Station ↔ Voxel M2 Control Plane — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Extend Hub↔station agent API so station posts telemetry and polls/executes per-screen TV commands (playback, power, capture, APK) using Hub `tv_monitor` params.

**Architecture:** Grow `internal/hub` with telemetry/commands/results types and HTTP methods. Add `internal/commands` executor and expand `internal/tv` (adb wake/sleep/screencap/record/install). Wire into `agent` poll loop. Depends on M1 air path (`2026-10-09-station-voxel-m1-air.md`).

**Tech Stack:** Same as M1; Hub Rails implements matching routes in a separate workstream — station must speak the contract in the English integration spec.

**Spec:** `docs/superpowers/specs/2026-10-09-station-voxel-integration-design.md` (Hub contract + TV/health M2).

---

## File map

| Path | Responsibility |
| --- | --- |
| `internal/hub/types.go` or `client.go` | `TVMonitor`, telemetry payload, command/result types. |
| `internal/hub/client.go` | `PostTelemetry`, `FetchCommands`, `PostCommandResults`, upload PUT. |
| `internal/tv/adb_ops.go` | Wake, sleep, screenshot, record, install APK via adb runner. |
| `internal/commands/executor.go` | Map command type → tv/air actions; per-screen mutex. |
| `internal/health/watcher.go` | Read intervals from Hub `tv_monitor`. |
| `internal/agent/agent.go` | Poll commands, post telemetry, pause on 401. |
| `README.md` | M2 operator / Hub contract pointer. |

---

### Task 1: Hub types and client methods

**Files:**
- Modify: `internal/hub/client.go`
- Create or modify: `internal/hub/client_test.go`

- [ ] **Step 1: Failing tests with httptest**

```go
func TestPostTelemetry(t *testing.T) {
	var got Telemetry
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/agent/v1/telemetry" {
			t.Fatalf("%s", r.URL.Path)
		}
		_ = json.NewDecoder(r.Body).Decode(&got)
		w.WriteHeader(http.StatusAccepted)
	}))
	defer srv.Close()
	c := New(srv.URL, "tok")
	c.HTTP = srv.Client()
	err := c.PostTelemetry(context.Background(), Telemetry{
		ReportedAt: time.Date(2026, 10, 9, 11, 0, 0, 0, time.UTC),
		Station:    StationTelemetry{UptimeSeconds: 10, BroadcastOK: true},
		Screens:    []ScreenTelemetry{{ScreenID: 7, Playing: true}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if got.Screens[0].ScreenID != 7 {
		t.Fatalf("%+v", got)
	}
}

func TestFetchCommands(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(CommandsResponse{Commands: []Command{{
			ID: "cmd_1", ScreenID: 7, Type: "restart_stream", IssuedAt: "2026-10-09T11:01:00Z",
		}}})
	}))
	defer srv.Close()
	c := New(srv.URL, "tok")
	c.HTTP = srv.Client()
	list, err := c.FetchCommands(context.Background(), "")
	if err != nil || len(list.Commands) != 1 || list.Commands[0].Type != "restart_stream" {
		t.Fatalf("%v %+v", err, list)
	}
}

func TestPostCommandResults(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/agent/v1/command_results" {
			t.Fatal(r.URL.Path)
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()
	c := New(srv.URL, "tok")
	c.HTTP = srv.Client()
	if err := c.PostCommandResults(context.Background(), []CommandResult{{
		CommandID: "cmd_1", OK: true, FinishedAt: time.Now().UTC(),
	}}); err != nil {
		t.Fatal(err)
	}
}
```

- [ ] **Step 2: FAIL then implement types**

```go
type TVMonitor struct {
	HealthIntervalSeconds    int  `json:"health_interval_seconds"`
	AutoRestartWhenNotPlaying bool `json:"auto_restart_when_not_playing"`
	TelemetryIntervalSeconds int  `json:"telemetry_interval_seconds"`
}

// extend Config:
// TVMonitor *TVMonitor `json:"tv_monitor"`

type Telemetry struct {
	ReportedAt time.Time          `json:"reported_at"`
	Station    StationTelemetry   `json:"station"`
	Screens    []ScreenTelemetry  `json:"screens"`
}

type StationTelemetry struct {
	UptimeSeconds int    `json:"uptime_seconds"`
	PackageETag   string `json:"package_etag"`
	BroadcastOK   bool   `json:"broadcast_ok"`
}

type ScreenTelemetry struct {
	ScreenID         int     `json:"screen_id"`
	Reachable        bool    `json:"reachable"`
	AgentOK          bool    `json:"agent_ok"`
	Playing          bool    `json:"playing"`
	URL              string  `json:"url"`
	Wakefulness      string  `json:"wakefulness"`
	StreamPort       int     `json:"stream_port"`
	BroadcastClients int     `json:"broadcast_clients"`
	LastError        *string `json:"last_error"`
}

type Command struct {
	ID       string         `json:"id"`
	ScreenID int            `json:"screen_id"`
	Type     string         `json:"type"`
	Params   map[string]any `json:"params"`
	IssuedAt string         `json:"issued_at"`
}

type CommandsResponse struct {
	Commands []Command `json:"commands"`
}

type CommandResult struct {
	CommandID  string    `json:"command_id"`
	OK         bool      `json:"ok"`
	Error      *string   `json:"error"`
	FinishedAt time.Time `json:"finished_at"`
}
```

Implement `PostTelemetry`, `FetchCommands(ctx, ifNoneMatch)`, `PostCommandResults`, and `UploadArtifact(ctx, uploadURL string, r io.Reader, contentType string)` as HTTP PUT.

401 → return `ErrUnauthorized` consistently.

- [ ] **Step 3: PASS + commit**

```bash
go test ./internal/hub/ -v
git add internal/hub/
git commit -m "$(cat <<'EOF'
feat(hub): agent telemetry and command queue client

Speak the M2 Hub control-plane contract for poll/push TV ops.

EOF
)"
```

---

### Task 2: TV adb power, capture, APK

**Files:**
- Create: `internal/tv/adb_ops.go`
- Create: `internal/tv/adb_ops_test.go`
- Reuse `player.Runner` pattern or define `tv.Runner` identically.

- [ ] **Step 1: Fake runner records argv**

```go
func TestWakeUsesKeyevent(t *testing.T) {
	r := &recRunner{}
	d := &Device{Serial: "192.168.33.182:5555", Runner: r, MAC: "78:53:33:4b:2c:29"}
	if err := d.Wake(context.Background()); err != nil {
		t.Fatal(err)
	}
	if !r.hasSubslice("keyevent", "KEYCODE_WAKEUP") {
		t.Fatalf("%v", r.calls)
	}
}

func TestSleepUsesKeyevent(t *testing.T) { /* KEYCODE_SLEEP */ }

func TestScreenshotScreencap(t *testing.T) { /* exec-out screencap -p */ }

func TestInstallAPK(t *testing.T) {
	r := &recRunner{}
	d := &Device{Serial: "1.2.3.4:5555", Runner: r}
	if err := d.InstallAPK(context.Background(), "/home/stream/apk/voxel-debug.apk"); err != nil {
		t.Fatal(err)
	}
	if !r.hasSubslice("install", "-r", "/home/stream/apk/voxel-debug.apk") {
		t.Fatalf("%v", r.calls)
	}
}
```

- [ ] **Step 2: Implement Device**

```go
type Device struct {
	Serial string
	MAC    string
	Runner Runner // same interface as player.Runner
	WOL    func(mac string) error // injectable; default exec wakeonlan/etherwake
}

func (d *Device) Connect(ctx context.Context) error {
	return d.Runner.Run(ctx, "adb", "connect", d.Serial)
}

func (d *Device) Wake(ctx context.Context) error {
	if err := d.Connect(ctx); err != nil {
		return err
	}
	if err := d.Runner.Run(ctx, "adb", "-s", d.Serial, "shell", "input", "keyevent", "KEYCODE_WAKEUP"); err != nil {
		return err
	}
	// optional: check dumpsys; if still asleep and MAC set → d.WOL(d.MAC)
	return nil
}

func (d *Device) Sleep(ctx context.Context) error {
	if err := d.Connect(ctx); err != nil {
		return err
	}
	return d.Runner.Run(ctx, "adb", "-s", d.Serial, "shell", "input", "keyevent", "KEYCODE_SLEEP")
}

func (d *Device) Screenshot(ctx context.Context) ([]byte, error) {
	// run adb exec-out screencap -p and capture stdout — extend Runner to RunOut or use exec in Device for this method with test seam
}

func (d *Device) Record(ctx context.Context, seconds int, dest string) error { /* screenrecord + pull */ }

func (d *Device) InstallAPK(ctx context.Context, apk string) error {
	if err := d.Connect(ctx); err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	return d.Runner.Run(ctx, "adb", "-s", d.Serial, "install", "-r", apk)
}

func (d *Device) LaunchMain(ctx context.Context) error {
	return d.Runner.Run(ctx, "adb", "-s", d.Serial, "shell", "am", "start",
		"-n", "com.voxel.mediastream/.MainActivity")
}

func (d *Device) EnsureApp(ctx context.Context, agent *Agent, apk string) error {
	if _, err := agent.Status(ctx); err == nil {
		return nil
	}
	if err := d.InstallAPK(ctx, apk); err != nil {
		return err
	}
	return d.LaunchMain(ctx)
}
```

Extend `Runner` with `RunOut(ctx, argv...) ([]byte, error)` for screenshot, or a dedicated `OutputRunner` interface in tests.

- [ ] **Step 3: PASS + commit**

```bash
git commit -m "$(cat <<'EOF'
feat(tv): adb wake, sleep, capture, and APK install helpers

Support Hub-driven power and diagnostics commands on Android TV.

EOF
)"
```

---

### Task 3: Command executor

**Files:**
- Create: `internal/commands/executor.go`
- Create: `internal/commands/executor_test.go`

- [ ] **Step 1: Tests per command type**

```go
func TestExecutorRestartStream(t *testing.T) {
	tv := &fakeTV{}
	ex := &Executor{
		TV:     func(screenID int) AgentClient { return tv },
		Device: func(screenID int) DeviceClient { return &fakeDev{} },
		Screen: func(id int) (config.Screen, bool) {
			return config.Screen{ScreenID: id, ADBSerial: "1.2.3.4:5555", StreamPort: 8091}, true
		},
		StreamURL: func(id int) string { return "http://192.168.33.1:8091/" },
		APKPath:   func() string { return "/apk.apk" },
	}
	res := ex.Run(context.Background(), hub.Command{ID: "c1", ScreenID: 7, Type: "restart_stream"})
	if !res.OK || tv.restarts != 1 {
		t.Fatalf("%+v %d", res, tv.restarts)
	}
}

func TestExecutorUnknownType(t *testing.T) {
	ex := &Executor{ /* minimal stubs */ }
	res := ex.Run(context.Background(), hub.Command{ID: "c1", ScreenID: 7, Type: "nope"})
	if res.OK || res.Error == nil || *res.Error != "unknown_type" {
		t.Fatalf("%+v", res)
	}
}

func TestExecutorPerScreenMutex(t *testing.T) {
	// start two blocking commands on same screen sequentially — second waits
}
```

Map:

| type | call |
| --- | --- |
| `start_stream` | TV.StartStream (params url or StreamURL) |
| `restart_stream` | TV.RestartStream |
| `stop` | TV.Stop |
| `wake` | Device.Wake |
| `sleep` | Device.Sleep |
| `screenshot` | Device.Screenshot → Upload if params.upload_url |
| `record` | Device.Record(seconds) → Upload |
| `ensure_app` | Device.EnsureApp |
| `install_apk` | download params.url if set else APKPath; InstallAPK; LaunchMain |

Return `hub.CommandResult`. Never panic on unknown type.

- [ ] **Step 2: Implement + PASS + commit**

```bash
git commit -m "$(cat <<'EOF'
feat(commands): execute Hub per-screen TV command queue

Map playback, power, capture, and APK command types to tv helpers.

EOF
)"
```

---

### Task 4: Health uses Hub tv_monitor

**Files:**
- Modify: `internal/health/watcher.go`
- Modify: `internal/agent/agent.go` (apply config after FetchConfig)

- [ ] **Step 1: Test defaults vs Hub override**

When `Config.TVMonitor` is nil → 30s / auto true / telemetry 60s.  
When set → use those values (clamp health interval to ≥5s).

- [ ] **Step 2: Apply on each successful config poll; restart ticker.**

- [ ] **Step 3: Commit**

```bash
git commit -m "$(cat <<'EOF'
feat(health): honor Hub tv_monitor intervals and flags

EOF
)"
```

---

### Task 5: Wire agent — telemetry + commands + 401 pause

**Files:**
- Modify: `internal/agent/agent.go`
- Modify: `internal/agent/agent_test.go`

- [ ] **Step 1: Agent behaviour tests with fake Hub**

```go
type fakeHubM2 struct {
	hub.Client // or interface stub
	commands []hub.Command
	teleports int
	results  int
}

// After poll: FetchCommands → Executor.Run each → PostCommandResults
// Every telemetry interval: build Telemetry from tv status + PostTelemetry
// On ErrUnauthorized: set reportPaused (existing) AND skip commands/telemetry
```

Build `ScreenTelemetry` from `tv.Agent.Status` + ping reachability (optional TCP dial `:5555` or `:8989`).

- [ ] **Step 2: Implement in `loop` / `poll`**

Order per poll tick: config → package → (if !paused) commands drain → drain play_events.  
Separate ticker for telemetry interval from health.

- [ ] **Step 3: `go test ./...` PASS + commit**

```bash
git commit -m "$(cat <<'EOF'
feat(agent): poll Hub commands and post TV telemetry

Complete the M2 control plane on the station side of the agent API.

EOF
)"
```

---

### Task 6: README M2

**Files:**
- Modify: `README.md`

Document:

- Hub must expose telemetry/commands/command_results.
- Command types table (pointer to spec).
- `mac` required for wake/WOL.
- Artifact upload via `params.upload_url`.

Commit:

```bash
git commit -m "$(cat <<'EOF'
docs: document Hub control-plane ops for station M2

EOF
)"
```

---

## M2 done when

- Station posts telemetry and executes all command types in the spec against fakes.
- 401 pauses control plane like play_events.
- Health intervals follow Hub `tv_monitor`.
- Hub Rails can be implemented against the same JSON examples.

## Spec coverage (M2)

| Spec item | Task |
| --- | --- |
| `tv_monitor` in config | 1, 4 |
| `POST telemetry` | 1, 5 |
| `GET commands` / `POST command_results` | 1, 3, 5 |
| playback / power / capture / apk commands | 2, 3 |
| upload_url for artifacts | 3 |
| 401 pause | 5 |
| Health params from Hub | 4 |
