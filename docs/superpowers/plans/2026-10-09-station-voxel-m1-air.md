# Station ↔ Voxel M1 Air — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** On the Voxel path, play Hub-timed `.ts` clips by feeding a FIFO into the existing `broadcast` binary and starting Voxel APK via HTTP `:8989`, then report `play_events`.

**Architecture:** Extend `mediateca-station` with `internal/feeder`, `internal/voxel`, and `internal/tv`. Keep `schedule` / `cache` / `report` unchanged. When Voxel config is present, `agent` starts `broadcast`, feeds files at `starts_at`, and talks to the TV agent instead of VLC/`httpmedia`. Lab VLC path remains when `broadcast_bin` is omitted.

**Tech Stack:** Go 1.22+, existing module `mediateca-station`, host `broadcast` binary, Voxel APK agent `:8989`, `adb` (connect only in M1 start path).

**Spec:** `docs/superpowers/specs/2026-10-09-station-voxel-integration-design.md` (M1 sections). English spec is normative.

**Follow-up:** M2 control plane is a separate plan: `docs/superpowers/plans/2026-10-09-station-voxel-m2-control-plane.md`.

Every path is relative to the `mediateca-station` repo root. Run `gofmt -w` on every Go file before tests.

---

## File map

| Path | Responsibility |
| --- | --- |
| `internal/config/config.go` | Add Voxel fields; dual validation (lab vs Voxel). |
| `internal/feeder/feeder.go` | Pace `.ts` bytes into a FIFO; null packets on gap. |
| `internal/voxel/broadcast.go` | `mkfifo`, start/stop/restart `broadcast` process. |
| `internal/tv/agent.go` | HTTP client for `:8989` (status, start, restart, stop). |
| `internal/tv/adb.go` | Shared adb runner + connect (reuse pattern from `player`). |
| `internal/agent/agent.go` | Wire Voxel air path; optional skip `httpmedia`. |
| `README.md` | Operator notes for `media-voxel` M1. |

Disk under `data_dir` (additions):

| Path | Contents |
| --- | --- |
| `voxel/broadcast_<port>.pid` | PID of `broadcast` for that port. |

---

### Task 1: Config — Voxel screen and stream fields

**Files:**
- Modify: `internal/config/config.go`
- Modify: `internal/config/config_test.go`

- [ ] **Step 1: Write the failing test**

Append to `internal/config/config_test.go`:

```go
func TestLoadVoxelPath(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	body := []byte(`
hub_base_url: "https://hub.example"
agent_token: "token"
data_dir: "/var/lib/mediateca-station"
broadcast_bind: "192.168.33.1"
broadcast_bin: "/home/stream/bin/broadcast"
fifo_dir: "/tmp"
voxel_apk: "/home/stream/apk/voxel-debug.apk"
screens:
  - screen_id: 7
    adb_serial: "192.168.33.182:5555"
    stream_port: 8091
    mac: "78:53:33:4b:2c:29"
`)
	if err := os.WriteFile(path, body, 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if !cfg.VoxelPath() {
		t.Fatal("expected Voxel path")
	}
	if cfg.Screens[0].StreamPort != 8091 {
		t.Fatalf("port=%d", cfg.Screens[0].StreamPort)
	}
	if cfg.ChunkSize != 18800 {
		t.Fatalf("chunk=%d", cfg.ChunkSize)
	}
}

func TestLoadLabPathStillRequiresHTTP(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	body := []byte(`
hub_base_url: "https://hub.example"
agent_token: "token"
http_listen: "127.0.0.1:8080"
http_secret: "0123456789abcdef0123456789abcdef"
data_dir: "/tmp/data"
screens:
  - screen_id: 7
    adb_serial: "192.168.1.21:5555"
`)
	if err := os.WriteFile(path, body, 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.VoxelPath() {
		t.Fatal("lab must not be Voxel path")
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

```bash
go test ./internal/config/ -run 'TestLoadVoxelPath|TestLoadLabPathStillRequiresHTTP' -v
```

Expected: FAIL (missing types/methods).

- [ ] **Step 3: Implement config fields**

Extend `Screen` and `File` in `internal/config/config.go`:

```go
type Screen struct {
	ScreenID   int
	ADBSerial  string
	StreamPort int
	MAC        string
}

type File struct {
	HubBaseURL     string
	AgentToken     string
	HTTPListen     string
	HTTPPublic     string
	HTTPSecret     string
	LateThreshold  time.Duration
	PollInterval   time.Duration
	DataDir        string
	BroadcastBind  string
	BroadcastBin   string
	FifoDir        string
	VoxelAPK       string
	ChunkSize      int
	FeederPace     string // "realtime" | "max"
	Screens        []Screen
}

func (f File) VoxelPath() bool {
	return f.BroadcastBin != ""
}

func (f File) ScreenByID(id int) (Screen, bool) {
	for _, s := range f.Screens {
		if s.ScreenID == id {
			return s, true
		}
	}
	return Screen{}, false
}

func (f File) StreamURL(screenID int) (string, bool) {
	s, ok := f.ScreenByID(screenID)
	if !ok || s.StreamPort == 0 {
		return "", false
	}
	return fmt.Sprintf("http://%s:%d/", f.BroadcastBind, s.StreamPort), true
}
```

YAML tags: `broadcast_bind`, `broadcast_bin`, `fifo_dir`, `voxel_apk`, `chunk_size`, `feeder_pace`, per-screen `stream_port`, `mac`.

Validation rules:

- If `BroadcastBin != ""` (Voxel): require `BroadcastBind`, `FifoDir`, `VoxelAPK`; each screen requires unique `StreamPort > 0`; do **not** require `HTTPListen`/`HTTPSecret`.
- Else (lab): keep existing require `HTTPListen` + `HTTPSecret` ≥ 32; `StreamPort` optional/ignored.
- Default `ChunkSize = 18800`, `FeederPace = "realtime"`.
- Duplicate `stream_port` → error.
- `mac` missing on Voxel path → allow load, but tests may check later health warn; for M1 only warn in agent logs (config may leave empty).

Update `Serial` helper to keep working. Update existing lab tests that write minimal YAML so they still pass.

- [ ] **Step 4: Run tests**

```bash
go test ./internal/config/ -v
```

Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/config/
git commit -m "$(cat <<'EOF'
feat(config): support Voxel broadcast path settings

Allow station YAML to map screens to stream ports and broadcast binary
paths without requiring the lab httpmedia secret.

EOF
)"
```

---

### Task 2: Feeder — paced write and null packets

**Files:**
- Create: `internal/feeder/feeder.go`
- Create: `internal/feeder/feeder_test.go`

- [ ] **Step 1: Write the failing test**

```go
package feeder

import (
	"bytes"
	"context"
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestFeedFileRealtimeWritesAllBytes(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "clip.ts")
	payload := bytes.Repeat([]byte{0x47}, 18800*2)
	if err := os.WriteFile(src, payload, 0o600); err != nil {
		t.Fatal(err)
	}
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	done := make(chan []byte, 1)
	go func() {
		b, _ := io.ReadAll(r)
		done <- b
	}()
	f := &Feeder{ChunkSize: 18800, Pace: PaceMax}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := f.FeedFile(ctx, src, w); err != nil {
		t.Fatal(err)
	}
	_ = w.Close()
	got := <-done
	if !bytes.Equal(got, payload) {
		t.Fatalf("len got=%d want=%d", len(got), len(payload))
	}
}

func TestWriteNullPackets(t *testing.T) {
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	buf := make([]byte, 188*100)
	done := make(chan int, 1)
	go func() {
		n, _ := io.ReadFull(r, buf)
		done <- n
	}()
	f := &Feeder{ChunkSize: 18800}
	if err := f.WriteNullPackets(w, 100); err != nil {
		t.Fatal(err)
	}
	_ = w.Close()
	n := <-done
	if n != 188*100 {
		t.Fatalf("n=%d", n)
	}
	if buf[0] != 0x47 || buf[1] != 0x1f || buf[2] != 0xff {
		t.Fatalf("not a null packet header: %x %x %x", buf[0], buf[1], buf[2])
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

```bash
go test ./internal/feeder/ -v
```

Expected: FAIL (package missing).

- [ ] **Step 3: Implement feeder**

```go
package feeder

import (
	"context"
	"fmt"
	"io"
	"os"
	"time"
)

type PaceMode string

const (
	PaceRealtime PaceMode = "realtime"
	PaceMax      PaceMode = "max"
)

type Feeder struct {
	ChunkSize int
	Pace      PaceMode
}

func (f *Feeder) chunk() int {
	if f.ChunkSize <= 0 {
		return 18800
	}
	return f.ChunkSize
}

// FeedFile copies path to dst. PaceMax writes as fast as possible (tests).
// PaceRealtime sleeps to approximate wall-clock duration = fileSize / bitrate
// using MPEG-TS 188-byte packets at a fixed assumed bitrate derived from
// durationHint if > 0; otherwise writes at ~chunk per 40ms (25 chunks/s)
// which is good enough for M1 smoke — prefer durationHint from schedule.
func (f *Feeder) FeedFile(ctx context.Context, path string, dst io.Writer) error {
	return f.FeedFileDuration(ctx, path, dst, 0)
}

func (f *Feeder) FeedFileDuration(ctx context.Context, path string, dst io.Writer, duration time.Duration) error {
	file, err := os.Open(path)
	if err != nil {
		return err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return err
	}
	size := info.Size()
	buf := make([]byte, f.chunk())
	var written int64
	start := time.Now()
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		n, readErr := file.Read(buf)
		if n > 0 {
			if _, err := dst.Write(buf[:n]); err != nil {
				return err
			}
			written += int64(n)
			if f.Pace != PaceMax && duration > 0 && size > 0 {
				target := time.Duration(float64(duration) * float64(written) / float64(size))
				if sleep := target - time.Since(start); sleep > 0 {
					timer := time.NewTimer(sleep)
					select {
					case <-ctx.Done():
						timer.Stop()
						return ctx.Err()
					case <-timer.C:
					}
				}
			}
		}
		if readErr == io.EOF {
			return nil
		}
		if readErr != nil {
			return readErr
		}
	}
}

// WriteNullPackets writes count MPEG-TS null packets (PID 0x1FFF).
func (f *Feeder) WriteNullPackets(dst io.Writer, count int) error {
	pkt := make([]byte, 188)
	pkt[0] = 0x47
	pkt[1] = 0x1f
	pkt[2] = 0xff
	pkt[3] = 0x10
	for i := 0; i < count; i++ {
		if _, err := dst.Write(pkt); err != nil {
			return fmt.Errorf("null packet: %w", err)
		}
	}
	return nil
}
```

- [ ] **Step 4: Run tests**

```bash
go test ./internal/feeder/ -v
```

Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/feeder/
git commit -m "$(cat <<'EOF'
feat(feeder): pace MPEG-TS files into a FIFO writer

Support realtime pacing from clip duration and null packets for gaps
between scheduled entries.

EOF
)"
```

---

### Task 3: Voxel broadcast process adapter

**Files:**
- Create: `internal/voxel/broadcast.go`
- Create: `internal/voxel/broadcast_test.go`

- [ ] **Step 1: Write the failing test**

```go
package voxel

import (
	"context"
	"os"
	"path/filepath"
	"sync"
	"testing"
)

type fakeRunner struct {
	mu   sync.Mutex
	argv [][]string
}

func (f *fakeRunner) Start(ctx context.Context, argv ...string) (Process, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	cp := append([]string(nil), argv...)
	f.argv = append(f.argv, cp)
	return &fakeProc{alive: true}, nil
}

type fakeProc struct{ alive bool }

func (p *fakeProc) Pid() int            { return 42 }
func (p *fakeProc) Wait() error         { return nil }
func (p *fakeProc) Kill() error         { p.alive = false; return nil }
func (p *fakeProc) Running() bool       { return p.alive }

func TestManagerStartsBroadcast(t *testing.T) {
	dir := t.TempDir()
	fifo := filepath.Join(dir, "stream_8091.ts")
	runner := &fakeRunner{}
	m := &Manager{
		Bin:     "/home/stream/bin/broadcast",
		FifoDir: dir,
		DataDir: dir,
		Runner:  runner,
	}
	ctx := context.Background()
	if err := m.Ensure(ctx, 8091); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(fifo); err != nil {
		t.Fatal(err)
	}
	if len(runner.argv) != 1 {
		t.Fatalf("starts=%d", len(runner.argv))
	}
	if runner.argv[0][0] != "/home/stream/bin/broadcast" {
		t.Fatalf("%v", runner.argv[0])
	}
	if runner.argv[0][1] != fifo || runner.argv[0][2] != ":8091" {
		t.Fatalf("%v", runner.argv[0])
	}
}
```

- [ ] **Step 2: Run to verify fail**

```bash
go test ./internal/voxel/ -v
```

Expected: FAIL.

- [ ] **Step 3: Implement manager**

```go
package voxel

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"sync"
	"syscall"
)

type Process interface {
	Pid() int
	Wait() error
	Kill() error
	Running() bool
}

type Runner interface {
	Start(ctx context.Context, argv ...string) (Process, error)
}

type ExecRunner struct{}

func (ExecRunner) Start(ctx context.Context, argv ...string) (Process, error) {
	cmd := exec.CommandContext(ctx, argv[0], argv[1:]...)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	return &osProcess{cmd: cmd}, nil
}

type osProcess struct{ cmd *exec.Cmd }

func (p *osProcess) Pid() int { return p.cmd.Process.Pid }
func (p *osProcess) Wait() error { return p.cmd.Wait() }
func (p *osProcess) Kill() error {
	if p.cmd.Process == nil {
		return nil
	}
	return p.cmd.Process.Kill()
}
func (p *osProcess) Running() bool {
	if p.cmd.ProcessState != nil && p.cmd.ProcessState.Exited() {
		return false
	}
	return p.cmd.Process != nil
}

type Manager struct {
	Bin     string
	FifoDir string
	DataDir string
	Runner  Runner

	mu    sync.Mutex
	procs map[int]Process // port → process
}

func (m *Manager) FifoPath(port int) string {
	return filepath.Join(m.FifoDir, fmt.Sprintf("stream_%d.ts", port))
}

func (m *Manager) Ensure(ctx context.Context, port int) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.procs == nil {
		m.procs = map[int]Process{}
	}
	if p, ok := m.procs[port]; ok && p.Running() {
		return nil
	}
	fifo := m.FifoPath(port)
	if err := ensureFifo(fifo); err != nil {
		return err
	}
	if m.Runner == nil {
		m.Runner = ExecRunner{}
	}
	proc, err := m.Runner.Start(ctx, m.Bin, fifo, fmt.Sprintf(":%d", port))
	if err != nil {
		return err
	}
	m.procs[port] = proc
	_ = os.MkdirAll(filepath.Join(m.DataDir, "voxel"), 0o700)
	pidPath := filepath.Join(m.DataDir, "voxel", fmt.Sprintf("broadcast_%d.pid", port))
	return os.WriteFile(pidPath, []byte(strconv.Itoa(proc.Pid())+"\n"), 0o600)
}

func (m *Manager) Stop(port int) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	p, ok := m.procs[port]
	if !ok {
		return nil
	}
	_ = p.Kill()
	delete(m.procs, port)
	return nil
}

func (m *Manager) OpenFifo(port int) (*os.File, error) {
	return os.OpenFile(m.FifoPath(port), os.O_WRONLY, 0)
}

func ensureFifo(path string) error {
	if st, err := os.Stat(path); err == nil {
		if st.Mode()&os.ModeNamedPipe != 0 {
			return nil
		}
		if err := os.Remove(path); err != nil {
			return err
		}
	}
	return syscall.Mkfifo(path, 0o600)
}
```

Note: `OpenFifo` blocks until `broadcast` opens the read end — call `Ensure` before `OpenFifo`, and open FIFO from a goroutine after Ensure, or open RDWR on Linux to avoid block in tests. For tests of agent later, prefer injecting an `io.WriteCloser` instead of real FIFO.

Add method for tests:

```go
func (m *Manager) SetWriter(port int, w io.WriteCloser) // optional test hook
```

Prefer agent to own the write end via interface `FifoOpener` in Task 5 to keep unit tests free of blocking FIFOs.

- [ ] **Step 4: Run tests**

```bash
go test ./internal/voxel/ -v
```

Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/voxel/
git commit -m "$(cat <<'EOF'
feat(voxel): manage broadcast binary and per-port FIFOs

Start and track the stock Voxel broadcast process without playlist_player.

EOF
)"
```

---

### Task 4: TV HTTP agent client

**Files:**
- Create: `internal/tv/agent.go`
- Create: `internal/tv/agent_test.go`

- [ ] **Step 1: Failing test**

```go
package tv

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestAgentStartStream(t *testing.T) {
	var gotURL string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/start_stream" || r.Method != http.MethodPost {
			t.Fatalf("%s %s", r.Method, r.URL.Path)
		}
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		gotURL, _ = body["url"].(string)
		_ = json.NewEncoder(w).Encode(map[string]any{"ok": true, "url": gotURL})
	}))
	defer srv.Close()
	a := &Agent{BaseURL: srv.URL, HTTP: srv.Client()}
	if err := a.StartStream(context.Background(), "http://192.168.33.1:8091/", 300); err != nil {
		t.Fatal(err)
	}
	if gotURL != "http://192.168.33.1:8091/" {
		t.Fatalf("%q", gotURL)
	}
}

func TestAgentStatus(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"playing": true, "url": "http://x/", "buffer_ms": 300})
	}))
	defer srv.Close()
	a := &Agent{BaseURL: srv.URL, HTTP: srv.Client()}
	st, err := a.Status(context.Background())
	if err != nil || !st.Playing {
		t.Fatalf("%v %#v", err, st)
	}
}
```

- [ ] **Step 2: Run — expect FAIL**

```bash
go test ./internal/tv/ -run 'TestAgent' -v
```

- [ ] **Step 3: Implement**

```go
package tv

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"
)

type Status struct {
	Playing  bool   `json:"playing"`
	URL      string `json:"url"`
	BufferMs int    `json:"buffer_ms"`
}

type Agent struct {
	BaseURL string // e.g. http://192.168.33.182:8989
	HTTP    *http.Client
}

func NewAgent(tvHost string) *Agent {
	host := tvHost
	if i := strings.IndexByte(tvHost, ':'); i >= 0 {
		// adb_serial is host:5555 — agent is host:8989
		host = tvHost[:i]
	}
	return &Agent{
		BaseURL: fmt.Sprintf("http://%s:8989", host),
		HTTP:    &http.Client{Timeout: 5 * time.Second},
	}
}

func (a *Agent) Status(ctx context.Context) (Status, error) {
	var st Status
	err := a.json(ctx, http.MethodGet, "/api/status", nil, &st)
	return st, err
}

func (a *Agent) StartStream(ctx context.Context, url string, bufferMs int) error {
	body := map[string]any{"url": url, "buffer_ms": bufferMs}
	return a.json(ctx, http.MethodPost, "/api/start_stream", body, nil)
}

func (a *Agent) RestartStream(ctx context.Context) error {
	return a.json(ctx, http.MethodPost, "/api/restart_stream", nil, nil)
}

func (a *Agent) Stop(ctx context.Context) error {
	return a.json(ctx, http.MethodPost, "/api/stop", nil, nil)
}

func (a *Agent) json(ctx context.Context, method, path string, body any, out any) error {
	var rdr *bytes.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return err
		}
		rdr = bytes.NewReader(b)
	} else {
		rdr = bytes.NewReader(nil)
	}
	req, err := http.NewRequestWithContext(ctx, method, strings.TrimRight(a.BaseURL, "/")+path, rdr)
	if err != nil {
		return err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	client := a.HTTP
	if client == nil {
		client = http.DefaultClient
	}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		return fmt.Errorf("tv agent %s: %s", path, resp.Status)
	}
	if out != nil {
		return json.NewDecoder(resp.Body).Decode(out)
	}
	return nil
}
```

- [ ] **Step 4: PASS**

```bash
go test ./internal/tv/ -v
```

- [ ] **Step 5: Commit**

```bash
git add internal/tv/
git commit -m "$(cat <<'EOF'
feat(tv): HTTP client for Voxel player agent on :8989

Status, start_stream, restart_stream, and stop for Mediateca air control.

EOF
)"
```

---

### Task 5: Air controller — combine feeder + voxel + tv

**Files:**
- Create: `internal/air/controller.go`
- Create: `internal/air/controller_test.go`

This package is what `agent` calls instead of `player.Player` on the Voxel path.

- [ ] **Step 1: Failing test**

```go
package air

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

type memTV struct {
	mu    sync.Mutex
	urls  []string
	stops int
}

func (m *memTV) StartStream(ctx context.Context, url string, bufferMs int) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.urls = append(m.urls, url)
	return nil
}
func (m *memTV) Stop(ctx context.Context) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.stops++
	return nil
}

func TestControllerStartFeedsAndStartsTV(t *testing.T) {
	dir := t.TempDir()
	media := filepath.Join(dir, "19")
	if err := os.WriteFile(media, []byte{0x47, 0x00}, 0o600); err != nil {
		t.Fatal(err)
	}
	r, w, _ := os.Pipe()
	go io.Copy(io.Discard, r)
	tv := &memTV{}
	c := &Controller{
		OpenWriter: func(port int) (io.WriteCloser, error) { return w, nil },
		NewTV:      func(serial string) TV { return tv },
		MediaPath:  func(id int) string { return media },
		StreamURL:  func(screenID int) string { return "http://192.168.33.1:8091/" },
		Pace:       "max",
		ChunkSize:  18800,
	}
	ctx := context.Background()
	if err := c.Start(ctx, StartReq{
		ScreenID: 7, Serial: "192.168.33.182:5555", Port: 8091,
		MediaID: 19, Duration: time.Second,
	}); err != nil {
		t.Fatal(err)
	}
	// allow feed goroutine
	time.Sleep(50 * time.Millisecond)
	tv.mu.Lock()
	defer tv.mu.Unlock()
	if len(tv.urls) != 1 {
		t.Fatalf("urls=%v", tv.urls)
	}
}
```

- [ ] **Step 2: FAIL then implement**

```go
package air

import (
	"context"
	"io"
	"sync"
	"time"

	"mediateca-station/internal/feeder"
)

type TV interface {
	StartStream(ctx context.Context, url string, bufferMs int) error
	Stop(ctx context.Context) error
}

type StartReq struct {
	ScreenID int
	Serial   string
	Port     int
	MediaID  int
	Duration time.Duration
}

type Controller struct {
	OpenWriter func(port int) (io.WriteCloser, error)
	NewTV      func(serial string) TV
	MediaPath  func(id int) string
	StreamURL  func(screenID int) string
	Pace       string
	ChunkSize  int

	mu      sync.Mutex
	feeds   map[int]context.CancelFunc // screenID
	writers map[int]io.WriteCloser
}

func (c *Controller) Start(ctx context.Context, req StartReq) error {
	c.mu.Lock()
	if c.feeds == nil {
		c.feeds = map[int]context.CancelFunc{}
		c.writers = map[int]io.WriteCloser{}
	}
	if cancel, ok := c.feeds[req.ScreenID]; ok {
		cancel()
		delete(c.feeds, req.ScreenID)
	}
	c.mu.Unlock()

	tv := c.NewTV(req.Serial)
	url := c.StreamURL(req.ScreenID)
	if err := tv.StartStream(ctx, url, 300); err != nil {
		return err
	}
	w, err := c.OpenWriter(req.Port)
	if err != nil {
		return err
	}
	feedCtx, cancel := context.WithCancel(context.Background())
	c.mu.Lock()
	c.feeds[req.ScreenID] = cancel
	c.writers[req.ScreenID] = w
	c.mu.Unlock()

	path := c.MediaPath(req.MediaID)
	f := &feeder.Feeder{ChunkSize: c.ChunkSize, Pace: feeder.PaceMode(c.Pace)}
	go func() {
		_ = f.FeedFileDuration(feedCtx, path, w, req.Duration)
	}()
	return nil
}

func (c *Controller) Stop(ctx context.Context, serial string, screenID int) error {
	c.mu.Lock()
	if cancel, ok := c.feeds[screenID]; ok {
		cancel()
		delete(c.feeds, screenID)
	}
	if w, ok := c.writers[screenID]; ok {
		_ = w.Close()
		delete(c.writers, screenID)
	}
	c.mu.Unlock()
	return c.NewTV(serial).Stop(ctx)
}

func (c *Controller) GapNull(screenID int, count int) error {
	c.mu.Lock()
	w := c.writers[screenID]
	c.mu.Unlock()
	if w == nil {
		return nil
	}
	f := &feeder.Feeder{}
	return f.WriteNullPackets(w, count)
}
```

For **join ≤1s**: `Start` cancels previous feed and starts next file without `Stop` on TV (already the case — `Start` does not call TV Stop).

For **idle stop**: agent calls `Stop` which stops TV agent.

- [ ] **Step 3: PASS + commit**

```bash
go test ./internal/air/ -v
git add internal/air/
git commit -m "$(cat <<'EOF'
feat(air): coordinate FIFO feed and Voxel TV start/stop

Replace VLC URL starts on the Voxel path with feeder plus :8989 start_stream.

EOF
)"
```

---

### Task 6: Wire agent to Voxel air path

**Files:**
- Modify: `internal/agent/agent.go`
- Modify: `internal/agent/agent_test.go`
- Modify: `internal/cache/cache.go` if needed for `Path(id) string` helper

- [ ] **Step 1: Add cache path helper if missing**

```go
// in cache.Cache
func (c *Cache) Path(id int) string {
	return filepath.Join(c.dir, strconv.Itoa(id))
}
```

- [ ] **Step 2: Extend agent construction**

In `Run` / `newAgent`:

- If `cfg.VoxelPath()`:
  - Do **not** listen `httpmedia` (or listen only if `HTTPListen` set).
  - Create `voxel.Manager` with `Bin`, `FifoDir`, `DataDir`.
  - For each screen, `Ensure(ctx, stream_port)` at startup and after config load.
  - Set `air.Controller` with `OpenWriter` → open FIFO write end (use `os.OpenFile(fifo, os.O_WRONLY|os.O_NONBLOCK)` carefully; on Linux prefer starting broadcast first, then open in a short-retry loop).
  - `NewTV: func(serial) air.TV { return tv.NewAgent(serial) }`.
- Else: keep existing VLC `player` + `httpmedia`.

Change `apply` KindStart on Voxel path:

```go
screen, _ := a.cfg.ScreenByID(cmd.ScreenID)
started := a.now().UTC()
err := a.air.Start(ctx, air.StartReq{
	ScreenID: cmd.ScreenID,
	Serial:   serial,
	Port:     screen.StreamPort,
	MediaID:  cmd.Entry.Media.ID,
	Duration: cmd.Entry.Duration,
})
```

`schedule.Entry.Duration` is already a `time.Duration`.

KindStop → `a.air.Stop`.

Play event rules unchanged: enqueue only after successful Start.

- [ ] **Step 3: Update agent tests**

Existing tests use fake player — keep lab path config with `http_listen`/`http_secret` so they stay green.

Add one Voxel-path agent test with fake air controller (inject interface on Agent) proving Start is called with port/media and play event enqueued.

- [ ] **Step 4: Full test suite**

```bash
go test ./...
```

Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/agent/ internal/cache/
git commit -m "$(cat <<'EOF'
feat(agent): play Hub timeline through Voxel broadcast path

On Voxel config, feed FIFOs and drive the APK agent instead of VLC httpmedia.

EOF
)"
```

---

### Task 7: Local health (M1 defaults)

**Files:**
- Create: `internal/health/watcher.go`
- Create: `internal/health/watcher_test.go`
- Modify: `internal/agent/agent.go` to run watcher in loop

- [ ] **Step 1: Test — not playing triggers one restart**

```go
package health

import (
	"context"
	"sync/atomic"
	"testing"
	"time"
)

type stub struct {
	playing bool
	restarts int32
}

func (s *stub) Status(ctx context.Context) (tvStatus, error) {
	return tvStatus{Playing: s.playing}, nil
}
func (s *stub) RestartStream(ctx context.Context) error {
	atomic.AddInt32(&s.restarts, 1)
	return nil
}

func TestWatcherRestartsWhenNotPlayingOnAir(t *testing.T) {
	s := &stub{playing: false}
	w := &Watcher{
		Interval:    time.Millisecond,
		AutoRestart: true,
		OnAir:       func(screenID int) bool { return true },
		Client:      func(screenID int) ScreenClient { return s },
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	_ = w.Run(ctx, []int{7})
	if atomic.LoadInt32(&s.restarts) < 1 {
		t.Fatal("expected restart")
	}
}
```

Define small interfaces in `health` package (`ScreenClient` with Status/RestartStream). Map to `tv.Agent` in agent wiring.

- [ ] **Step 2: Implement + wire**

Defaults: interval 30s, auto_restart true. Only call restart when `OnAir(screenID)` true (covering entry exists — agent provides callback using schedule covering key / `onScreen` map).

Idle: no restart.

- [ ] **Step 3: `go test ./...` PASS + commit**

```bash
git commit -m "$(cat <<'EOF'
feat(health): local TV watchdog with default intervals

Restart Voxel streams that drop while a covering schedule entry is on air.

EOF
)"
```

---

### Task 8: README operator notes for media-voxel

**Files:**
- Modify: `README.md`

- [ ] **Step 1: Document**

Add section **Voxel production path**:

- Require `broadcast_bin`, `broadcast_bind`, `fifo_dir`, `voxel_apk`, per-screen `stream_port`.
- Stop `tv-verifier.service` / do not run `controller` for air (or empty `broadcast_schedule`).
- Voxel APK installed on TVs; network ADB enabled.
- Example YAML matching the spec.
- Smoke: one clip in cache, journalctl shows `start`, Hub receives play_event.

- [ ] **Step 2: Commit**

```bash
git add README.md
git commit -m "$(cat <<'EOF'
docs: describe Voxel M1 air path on media-voxel

EOF
)"
```

---

## M1 done when

- `go test ./...` passes offline.
- On Voxel config, agent never requires VLC/`http_secret`.
- Feeder + broadcast + `:8989` start + play_events work in unit tests with fakes.
- README tells operator how to smoke on `media-voxel`.

## Spec coverage (M1)

| Spec item | Task |
| --- | --- |
| Config Voxel fields | 1 |
| Feeder + null packets | 2 |
| broadcast lifecycle | 3 |
| TV :8989 | 4 |
| Start = feed + start_stream | 5–6 |
| Schedule rules unchanged | 6 (reuse schedule) |
| Local health defaults | 7 |
| Ops notes / disable tv_verifier | 8 |
| play_events | 6 |

M2 (telemetry, commands, power, capture, apk, Hub `tv_monitor`) → next plan.
