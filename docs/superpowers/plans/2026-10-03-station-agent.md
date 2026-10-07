# Station Agent Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Build `mediateca-station`, a Go process that plays a hub playlist on Android TV screens from cached `.ts` files.

**Architecture:** One systemd daemon on a Linux mini-PC polls the existing Broadcast Hub, stores the timed v2 package, serves media on the location LAN, and starts or stops stock VLC over `adb`. Wall-clock `starts_at` is the only schedule. The hub repository is not modified.

**Tech Stack:** Go 1.22+, `gopkg.in/yaml.v3`, standard-library HTTP, `adb` on the host (tests use a fake runner).

**Spec:** `docs/superpowers/specs/2026-10-03-station-agent-design.md` in the hub repo. Implement from that spec and this plan. If a test and the spec disagree, stop and report it; do not change the hub.

**Repository:** Create a new git repo. Do not add Go files to `mediateca-broadcast`.

```bash
mkdir -p /home/dim/Projects/MyPets/mediateca-station
cd /home/dim/Projects/MyPets/mediateca-station
git init
```

Every path below is relative to that directory. Run `gofmt -w` on every Go file you write before running tests.

---

## File map

| Path | Responsibility |
| --- | --- |
| `go.mod` | Module `mediateca-station`. |
| `internal/fsutil/atomic.go` | Temp-file plus rename writes. |
| `internal/config/config.go` | YAML load and validation. |
| `internal/hub/client.go` | Config, v2 package, one play event. |
| `internal/schedule/timeline.go` | Pure timeline decisions. No hub, player, or disk. |
| `internal/report/queue.go` | `events.jsonl`, `played.json`, HTTP outcome table. |
| `internal/player/adb.go` | `adb connect`, VLC start, VLC force-stop. |
| `internal/cache/cache.go` | Download `media.url`, sidecars, delete unreferenced ids. |
| `internal/httpmedia/server.go` | `GET /m/<secret>/<id>` with byte ranges. |
| `internal/agent/agent.go` | Poll, tick, drain, HTTP listen. |
| `cmd/station/main.go` | `-config` flag and process exit. |
| `README.md` | Operator install for the mini-PC. |

Disk layout under `data_dir`:

| Path | Contents |
| --- | --- |
| `config.json` | Last hub config HTTP 200 body. |
| `package.json` | Last hub package HTTP 200 body. |
| `package.etagheader` | Raw `ETag` response header, sent back as `If-None-Match`. |
| `media/<id>` | Complete file. |
| `media/<id>.url` | Package `media.url` that produced the file. |
| `media/<id>.mime` | MIME type. |
| `media/<id>.partial` | Incomplete download. Never played or served. |
| `events.jsonl` | Queued play events. |
| `played.json` | JSON array of `screen:media:starts_at` keys. |

`package.etagheader` is required so a restart can send the Rails header unchanged. Do not rebuild it from the JSON `etag` field.

---

### Task 1: Module and atomic writes

**Files:**
- Create: `go.mod`
- Create: `internal/fsutil/atomic.go`
- Create: `internal/fsutil/atomic_test.go`

- [ ] **Step 1: Create the module**

```bash
go mod init mediateca-station
```

Expected: `go.mod` contains `module mediateca-station` and a `go 1.22` line (or newer, if the installed toolchain writes a higher version). If it wrote `go 1.21` or older, set the line to `go 1.22`.

- [ ] **Step 2: Write the failing test**

`internal/fsutil/atomic_test.go`

```go
package fsutil

import (
	"os"
	"path/filepath"
	"testing"
)

func TestWriteAtomicReplacesAndLeavesNoTemp(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "package.json")
	if err := WriteAtomic(path, []byte("one")); err != nil {
		t.Fatal(err)
	}
	if err := WriteAtomic(path, []byte("two")); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "two" {
		t.Fatalf("got %q", got)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Name() != "package.json" {
		t.Fatalf("dir entries: %v", entries)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm()&0o077 != 0 {
		t.Fatalf("mode %o is group or world readable", info.Mode().Perm())
	}
}
```

- [ ] **Step 3: Run the test and confirm it fails**

```bash
go test ./internal/fsutil -count=1
```

Expected: FAIL, `WriteAtomic` undefined.

- [ ] **Step 4: Write the implementation**

`internal/fsutil/atomic.go`

```go
package fsutil

import (
	"os"
	"path/filepath"
)

func WriteAtomic(path string, data []byte) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	f, err := os.CreateTemp(dir, filepath.Base(path)+".tmp-*")
	if err != nil {
		return err
	}
	tmp := f.Name()
	defer os.Remove(tmp)
	if _, err := f.Write(data); err != nil {
		f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	if err := os.Chmod(tmp, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}
```

- [ ] **Step 5: Run the test and confirm it passes**

```bash
gofmt -w internal/fsutil/atomic.go internal/fsutil/atomic_test.go
go test ./internal/fsutil -count=1
```

Expected: `ok mediateca-station/internal/fsutil`.

- [ ] **Step 6: Commit**

```bash
git add go.mod internal/fsutil
git commit -m "$(cat <<'EOF'
feat: add atomic file writes for station state

EOF
)"
```

---

### Task 2: Config file

**Files:**
- Create: `internal/config/config.go`
- Create: `internal/config/config_test.go`
- Modify: `go.mod`, `go.sum`

- [ ] **Step 1: Add the YAML dependency**

```bash
go get gopkg.in/yaml.v3@v3.0.1
```

- [ ] **Step 2: Write the failing test**

`internal/config/config_test.go`

```go
package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const validYAML = `
hub_base_url: "https://hub.example"
agent_token: "tok"
http_listen: "192.168.1.10:8080"
http_secret: "0123456789abcdef0123456789abcdef"
data_dir: "/var/lib/mediateca-station"
screens:
  - screen_id: 7
    adb_serial: "192.168.1.21:5555"
`

func writeConfig(t *testing.T, body string, mode os.FileMode) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, mode); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestLoadDefaultsAndScreens(t *testing.T) {
	cfg, err := Load(writeConfig(t, validYAML, 0o600))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.HubBaseURL != "https://hub.example" || cfg.AgentToken != "tok" {
		t.Fatalf("%+v", cfg)
	}
	if cfg.LateThreshold != 2*time.Second || cfg.PollInterval != time.Minute {
		t.Fatalf("defaults: late=%s poll=%s", cfg.LateThreshold, cfg.PollInterval)
	}
	serial, ok := cfg.Serial(7)
	if !ok || serial != "192.168.1.21:5555" {
		t.Fatalf("serial %q ok=%v", serial, ok)
	}
	if _, ok := cfg.Serial(8); ok {
		t.Fatal("unexpected serial")
	}
}

func TestLoadRejectsGroupReadableFile(t *testing.T) {
	_, err := Load(writeConfig(t, validYAML, 0o640))
	if err == nil || !strings.Contains(err.Error(), "group or world readable") {
		t.Fatalf("err=%v", err)
	}
}

func TestLoadRejectsInvalidFields(t *testing.T) {
	cases := []struct {
		name string
		yaml string
		want string
	}{
		{"secret", strings.Replace(validYAML, "0123456789abcdef0123456789abcdef", "short", 1), "http_secret"},
		{"serial", strings.Replace(validYAML, "192.168.1.21:5555", "emulator", 1), "adb_serial"},
		{"no screens", strings.Replace(validYAML, "screens:\n  - screen_id: 7\n    adb_serial: \"192.168.1.21:5555\"\n", "screens: []\n", 1), "at least one screen"},
		{"dup", validYAML + "  - screen_id: 7\n    adb_serial: \"192.168.1.22:5555\"\n", "duplicate screen_id"},
		{"duration", validYAML + "late_threshold: \"nope\"\n", "late_threshold"},
		{"token", strings.Replace(validYAML, "agent_token: \"tok\"", "agent_token: \"\"", 1), "agent_token"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := Load(writeConfig(t, tc.yaml, 0o600))
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err=%v want substring %q", err, tc.want)
			}
		})
	}
}
```

- [ ] **Step 3: Run the test and confirm it fails**

```bash
go test ./internal/config -count=1
```

Expected: FAIL, package `config` does not exist or `Load` is undefined.

- [ ] **Step 4: Write the implementation**

`internal/config/config.go`

```go
package config

import (
	"fmt"
	"net"
	"net/url"
	"os"
	"time"

	"gopkg.in/yaml.v3"
)

type Screen struct {
	ScreenID  int
	ADBSerial string
}

type File struct {
	HubBaseURL    string
	AgentToken    string
	HTTPListen    string
	HTTPSecret    string
	LateThreshold time.Duration
	PollInterval  time.Duration
	DataDir       string
	Screens       []Screen
}

func (f File) Serial(screenID int) (string, bool) {
	for _, screen := range f.Screens {
		if screen.ScreenID == screenID {
			return screen.ADBSerial, true
		}
	}
	return "", false
}

type fileYAML struct {
	HubBaseURL    string       `yaml:"hub_base_url"`
	AgentToken    string       `yaml:"agent_token"`
	HTTPListen    string       `yaml:"http_listen"`
	HTTPSecret    string       `yaml:"http_secret"`
	LateThreshold string       `yaml:"late_threshold"`
	PollInterval  string       `yaml:"poll_interval"`
	DataDir       string       `yaml:"data_dir"`
	Screens       []screenYAML `yaml:"screens"`
}

type screenYAML struct {
	ScreenID  int    `yaml:"screen_id"`
	ADBSerial string `yaml:"adb_serial"`
}

func Load(path string) (File, error) {
	info, err := os.Stat(path)
	if err != nil {
		return File{}, err
	}
	if info.Mode().Perm()&0o077 != 0 {
		return File{}, fmt.Errorf("config %s is group or world readable", path)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return File{}, err
	}
	var doc fileYAML
	if err := yaml.Unmarshal(raw, &doc); err != nil {
		return File{}, err
	}
	cfg := File{
		HubBaseURL:    doc.HubBaseURL,
		AgentToken:    doc.AgentToken,
		HTTPListen:    doc.HTTPListen,
		HTTPSecret:    doc.HTTPSecret,
		LateThreshold: 2 * time.Second,
		PollInterval:  time.Minute,
		DataDir:       doc.DataDir,
	}
	if err := cfg.applyDurations(doc); err != nil {
		return File{}, err
	}
	if err := cfg.validate(doc); err != nil {
		return File{}, err
	}
	return cfg, nil
}

func (f *File) applyDurations(doc fileYAML) error {
	if doc.LateThreshold != "" {
		parsed, err := time.ParseDuration(doc.LateThreshold)
		if err != nil {
			return fmt.Errorf("late_threshold: %w", err)
		}
		f.LateThreshold = parsed
	}
	if doc.PollInterval != "" {
		parsed, err := time.ParseDuration(doc.PollInterval)
		if err != nil {
			return fmt.Errorf("poll_interval: %w", err)
		}
		f.PollInterval = parsed
	}
	return nil
}

func (f *File) validate(doc fileYAML) error {
	if f.HubBaseURL == "" {
		return fmt.Errorf("hub_base_url is required")
	}
	u, err := url.Parse(f.HubBaseURL)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return fmt.Errorf("hub_base_url is invalid")
	}
	if u.Path != "" && u.Path != "/" {
		return fmt.Errorf("hub_base_url must not include a path")
	}
	if f.AgentToken == "" {
		return fmt.Errorf("agent_token is required")
	}
	if _, _, err := net.SplitHostPort(f.HTTPListen); err != nil {
		return fmt.Errorf("http_listen is invalid")
	}
	if len(f.HTTPSecret) < 32 {
		return fmt.Errorf("http_secret must be at least 32 characters")
	}
	if f.DataDir == "" {
		return fmt.Errorf("data_dir is required")
	}
	if len(doc.Screens) == 0 {
		return fmt.Errorf("at least one screen is required")
	}
	seen := map[int]struct{}{}
	for _, screen := range doc.Screens {
		if screen.ScreenID == 0 {
			return fmt.Errorf("screen_id is required")
		}
		if _, ok := seen[screen.ScreenID]; ok {
			return fmt.Errorf("duplicate screen_id %d", screen.ScreenID)
		}
		seen[screen.ScreenID] = struct{}{}
		if !stringsContainsColon(screen.ADBSerial) {
			return fmt.Errorf("adb_serial must be host:port")
		}
		f.Screens = append(f.Screens, Screen{ScreenID: screen.ScreenID, ADBSerial: screen.ADBSerial})
	}
	return nil
}

func stringsContainsColon(value string) bool {
	for _, r := range value {
		if r == ':' {
			return value != "" && value[0] != ':' && value[len(value)-1] != ':'
		}
	}
	return false
}
```

- [ ] **Step 5: Run the test and confirm it passes**

```bash
gofmt -w internal/config/config.go internal/config/config_test.go
go test ./internal/config -count=1
```

Expected: `ok mediateca-station/internal/config`.

- [ ] **Step 6: Commit**

```bash
git add go.mod go.sum internal/config
git commit -m "$(cat <<'EOF'
feat: load the station config from a private YAML file

EOF
)"
```

---

### Task 3: Hub client

**Files:**
- Create: `internal/hub/client.go`
- Create: `internal/hub/client_test.go`

The client calls only these paths:

- `GET /api/agent/v1/config`
- `GET /api/agent/v2/package`
- `POST /api/agent/v1/play_events`

It must not call `GET /api/agent/v1/package`. Bearer auth is attached on these three calls only.

- [ ] **Step 1: Write the failing test**

`internal/hub/client_test.go`

```go
package hub

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestFetchConfig(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/agent/v1/config" {
			t.Errorf("path %s", r.URL.Path)
		}
		if r.Header.Get("Authorization") != "Bearer tok" {
			t.Errorf("auth %q", r.Header.Get("Authorization"))
		}
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"station_id":3,"offline_cache_hours":24,"screens":[{"id":7,"name":"Entrance","orientation":"landscape"}]}`)
	}))
	defer srv.Close()
	cfg, err := New(srv.URL, "tok").FetchConfig(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if cfg.StationID != 3 || cfg.OfflineCacheHours != 24 || cfg.Screens[0].Orientation != "landscape" {
		t.Fatalf("%+v", cfg)
	}
}

func TestFetchPackageStoresHeaderETagAnd304(t *testing.T) {
	var sawNoneMatch string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/agent/v2/package" {
			t.Errorf("path %s", r.URL.Path)
		}
		sawNoneMatch = r.Header.Get("If-None-Match")
		if sawNoneMatch == `"abc"` {
			w.WriteHeader(http.StatusNotModified)
			return
		}
		w.Header().Set("ETag", `"abc"`)
		io.WriteString(w, `{"version":"abc","etag":"abc","generated_at":"2026-09-02T12:00:00Z","valid_until":"2026-09-03T12:00:00Z","entries":[{"position":1,"starts_at":"2026-09-02T02:00:00Z","duration_seconds":10,"screen_ids":[7],"media":{"id":19,"url":"/rails/active_storage/blobs/redirect/x","mime_type":"video/mp2t"}}],"screen_map":{"7":[0]}}`)
	}))
	defer srv.Close()
	client := New(srv.URL, "tok")
	pkg, notMod, err := client.FetchPackage(context.Background(), "")
	if err != nil || notMod {
		t.Fatalf("notMod=%v err=%v", notMod, err)
	}
	if pkg.HeaderETag != `"abc"` || pkg.Entries[0].Media.ID != 19 || pkg.Entries[0].Media.MimeType != "video/mp2t" {
		t.Fatalf("%+v", pkg)
	}
	_, notMod, err = client.FetchPackage(context.Background(), pkg.HeaderETag)
	if err != nil || !notMod {
		t.Fatalf("second notMod=%v err=%v", notMod, err)
	}
	if sawNoneMatch != `"abc"` {
		t.Fatalf("If-None-Match %q", sawNoneMatch)
	}
}

func TestUnauthorized(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		io.WriteString(w, `{"error":"unauthorized"}`)
	}))
	defer srv.Close()
	_, err := New(srv.URL, "tok").FetchConfig(context.Background())
	if !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("err=%v", err)
	}
}

func TestPostPlayEvent(t *testing.T) {
	var body string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/agent/v1/play_events" || r.Method != http.MethodPost {
			t.Errorf("request %s %s", r.Method, r.URL.Path)
		}
		raw, _ := io.ReadAll(r.Body)
		body = string(raw)
		w.WriteHeader(http.StatusCreated)
		io.WriteString(w, `{"play_log_ids":[101]}`)
	}))
	defer srv.Close()
	started := time.Date(2026, 9, 2, 2, 0, 1, 0, time.UTC)
	status, resp, err := New(srv.URL, "tok").PostPlayEvent(context.Background(), 7, 19, started)
	if err != nil || status != 201 {
		t.Fatalf("status=%d err=%v", status, err)
	}
	if !strings.Contains(body, `"screen_id":7`) || !strings.Contains(body, `"media_asset_id":19`) || !strings.Contains(body, `"started_at":"2026-09-02T02:00:01Z"`) {
		t.Fatalf("body %s", body)
	}
	if !strings.Contains(resp, "101") {
		t.Fatalf("resp %s", resp)
	}
}

func TestPostPlayEventTransportError(t *testing.T) {
	client := New("http://127.0.0.1:1", "tok")
	status, _, err := client.PostPlayEvent(context.Background(), 7, 19, time.Now().UTC())
	if err == nil || status != 0 {
		t.Fatalf("status=%d err=%v", status, err)
	}
}
```

- [ ] **Step 2: Run the test and confirm it fails**

```bash
go test ./internal/hub -count=1
```

Expected: FAIL, `New` undefined.

- [ ] **Step 3: Write the implementation**

`internal/hub/client.go`

```go
package hub

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

var ErrUnauthorized = errors.New("unauthorized")

type Screen struct {
	ID          int    `json:"id"`
	Name        string `json:"name"`
	Orientation string `json:"orientation"`
}

type Config struct {
	StationID         int      `json:"station_id"`
	OfflineCacheHours int      `json:"offline_cache_hours"`
	Screens           []Screen `json:"screens"`
}

type Media struct {
	ID       int    `json:"id"`
	URL      string `json:"url"`
	MimeType string `json:"mime_type"`
}

type Entry struct {
	ForDate              string  `json:"for_date"`
	BroadcastDayStartsAt string  `json:"broadcast_day_starts_at"`
	Position             int     `json:"position"`
	OffsetSeconds        int     `json:"offset_seconds"`
	StartsAt             *string `json:"starts_at"`
	DurationSeconds      int     `json:"duration_seconds"`
	SourceKind           string  `json:"source_kind"`
	MediaPlanID          *int    `json:"media_plan_id"`
	ScreenIDs            []int   `json:"screen_ids"`
	Media                Media   `json:"media"`
}

type Package struct {
	Version     string         `json:"version"`
	ETag        string         `json:"etag"`
	GeneratedAt string         `json:"generated_at"`
	ValidUntil  string         `json:"valid_until"`
	Entries     []Entry        `json:"entries"`
	ScreenMap   map[string][]int `json:"screen_map"`
	HeaderETag  string         `json:"-"`
}

type Client struct {
	BaseURL string
	Token   string
	HTTP    *http.Client
}

func New(baseURL, token string) *Client {
	return &Client{
		BaseURL: strings.TrimRight(baseURL, "/"),
		Token:   token,
		HTTP:    &http.Client{Timeout: 30 * time.Second},
	}
}

func (c *Client) FetchConfig(ctx context.Context) (Config, error) {
	resp, err := c.do(ctx, http.MethodGet, "/api/agent/v1/config", "", nil)
	if err != nil {
		return Config{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusUnauthorized {
		return Config{}, ErrUnauthorized
	}
	if resp.StatusCode != http.StatusOK {
		return Config{}, fmt.Errorf("hub config: status %d", resp.StatusCode)
	}
	var cfg Config
	if err := json.NewDecoder(resp.Body).Decode(&cfg); err != nil {
		return Config{}, err
	}
	return cfg, nil
}

func (c *Client) FetchPackage(ctx context.Context, ifNoneMatch string) (Package, bool, error) {
	resp, err := c.do(ctx, http.MethodGet, "/api/agent/v2/package", ifNoneMatch, nil)
	if err != nil {
		return Package{}, false, err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusUnauthorized {
		return Package{}, false, ErrUnauthorized
	}
	if resp.StatusCode == http.StatusNotModified {
		return Package{}, true, nil
	}
	if resp.StatusCode != http.StatusOK {
		return Package{}, false, fmt.Errorf("hub package: status %d", resp.StatusCode)
	}
	var pkg Package
	if err := json.NewDecoder(resp.Body).Decode(&pkg); err != nil {
		return Package{}, false, err
	}
	pkg.HeaderETag = resp.Header.Get("ETag")
	return pkg, false, nil
}

func (c *Client) PostPlayEvent(ctx context.Context, screenID, mediaID int, startedAt time.Time) (int, string, error) {
	payload := map[string]any{
		"events": []map[string]any{{
			"screen_id":      screenID,
			"media_asset_id": mediaID,
			"started_at":     startedAt.UTC().Format(time.RFC3339),
		}},
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		return 0, "", err
	}
	resp, err := c.do(ctx, http.MethodPost, "/api/agent/v1/play_events", "", raw)
	if err != nil {
		return 0, "", err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return resp.StatusCode, "", err
	}
	return resp.StatusCode, string(body), nil
}

func (c *Client) do(ctx context.Context, method, path, ifNoneMatch string, body []byte) (*http.Response, error) {
	var reader io.Reader
	if body != nil {
		reader = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.BaseURL+path, reader)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+c.Token)
	req.Header.Set("Accept", "application/json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if ifNoneMatch != "" {
		req.Header.Set("If-None-Match", ifNoneMatch)
	}
	return c.HTTP.Do(req)
}
```

- [ ] **Step 4: Run the test and confirm it passes**

```bash
gofmt -w internal/hub/client.go internal/hub/client_test.go
go test ./internal/hub -count=1
```

Expected: `ok mediateca-station/internal/hub`.

- [ ] **Step 5: Commit**

```bash
git add internal/hub
git commit -m "$(cat <<'EOF'
feat: call the hub config, package, and play-event routes

EOF
)"
```

---

### Task 4: Schedule

**Files:**
- Create: `internal/schedule/timeline.go`
- Create: `internal/schedule/timeline_test.go`

This package decides what one screen should do at one instant. It does not import `hub`, `player`, or `report`.

Played keys are `screenID:mediaID:startsAtRaw`, where `startsAtRaw` is the package string, not a reformatted time.

- [ ] **Step 1: Write the failing test**

`internal/schedule/timeline_test.go`

```go
package schedule

import (
	"testing"
	"time"
)

func entry(start time.Time, duration time.Duration, position, screenID, mediaID int) Entry {
	raw := start.UTC().Format(time.RFC3339)
	return Entry{
		Position:    position,
		StartsAt:    start,
		StartsAtRaw: raw,
		Duration:    duration,
		ScreenIDs:   []int{screenID},
		Media:       Media{ID: mediaID, URL: "/clip", MimeType: "video/mp2t"},
	}
}

func nonePlayed(string) bool           { return false }
func alwaysReady(int, string) bool     { return true }
func neverReady(int, string) bool      { return false }

func kinds(cmds []Command) []Kind {
	out := make([]Kind, len(cmds))
	for i, cmd := range cmds {
		out[i] = cmd.Kind
	}
	return out
}

func TestParseEntryRejectsIncomplete(t *testing.T) {
	raw := "2026-09-02T02:00:00Z"
	if _, err := ParseEntry(&raw, 0, []int{7}, 19, "/clip", "video/mp2t", 1); err == nil {
		t.Fatal("zero duration")
	}
	if _, err := ParseEntry(nil, 10, []int{7}, 19, "/clip", "video/mp2t", 1); err == nil {
		t.Fatal("nil start")
	}
	if _, err := ParseEntry(&raw, 10, []int{7}, 19, "", "video/mp2t", 1); err == nil {
		t.Fatal("empty url")
	}
	got, err := ParseEntry(&raw, 10, []int{7}, 19, "/clip", "video/mp2t", 1)
	if err != nil || got.StartsAtRaw != raw || got.Media.ID != 19 {
		t.Fatalf("got %+v err=%v", got, err)
	}
}

func TestStartAtExactAndAtThreshold(t *testing.T) {
	start := time.Date(2026, 9, 2, 2, 0, 0, 0, time.UTC)
	clip := entry(start, 10*time.Second, 1, 7, 19)
	late := 2 * time.Second
	for _, now := range []time.Time{start, start.Add(late)} {
		cmds := Commands(now, late, 7, []Entry{clip}, nonePlayed, alwaysReady, nil)
		if len(cmds) != 1 || cmds[0].Kind != KindStart || cmds[0].Entry.Media.ID != 19 {
			t.Fatalf("now=%s cmds=%+v", now, cmds)
		}
	}
}

func TestSkipOneNanosecondPastThreshold(t *testing.T) {
	start := time.Date(2026, 9, 2, 2, 0, 0, 0, time.UTC)
	clip := entry(start, 30*time.Second, 1, 7, 19)
	now := start.Add(2*time.Second + time.Nanosecond)
	cmds := Commands(now, 2*time.Second, 7, []Entry{clip}, nonePlayed, alwaysReady, nil)
	if len(cmds) != 1 || cmds[0].Kind != KindSkip || cmds[0].Reason != "late" {
		t.Fatalf("%+v", cmds)
	}
}

func TestMissingFileWaitsInsideWindowAndSkipsAfter(t *testing.T) {
	start := time.Date(2026, 9, 2, 2, 0, 0, 0, time.UTC)
	clip := entry(start, 30*time.Second, 1, 7, 19)
	cmds := Commands(start, 2*time.Second, 7, []Entry{clip}, nonePlayed, neverReady, nil)
	if len(cmds) != 0 {
		t.Fatalf("inside window %+v", cmds)
	}
	now := start.Add(3 * time.Second)
	cmds = Commands(now, 2*time.Second, 7, []Entry{clip}, nonePlayed, neverReady, nil)
	if len(cmds) != 1 || cmds[0].Reason != "missing_file" {
		t.Fatalf("%+v", cmds)
	}
}

func TestOtherScreenNeverStarts(t *testing.T) {
	start := time.Date(2026, 9, 2, 2, 0, 0, 0, time.UTC)
	clip := entry(start, 10*time.Second, 1, 8, 19)
	cmds := Commands(start, 2*time.Second, 7, []Entry{clip}, nonePlayed, alwaysReady, nil)
	if len(cmds) != 0 {
		t.Fatalf("%+v", cmds)
	}
}

func TestConflictPlaysSmallerPosition(t *testing.T) {
	start := time.Date(2026, 9, 2, 2, 0, 0, 0, time.UTC)
	laterPos := entry(start, 10*time.Second, 2, 7, 20)
	earlierPos := entry(start, 10*time.Second, 1, 7, 19)
	cmds := Commands(start, 2*time.Second, 7, []Entry{laterPos, earlierPos}, nonePlayed, alwaysReady, nil)
	var started, skipped int
	for _, cmd := range cmds {
		if cmd.Kind == KindStart && cmd.Entry.Media.ID == 19 {
			started++
		}
		if cmd.Kind == KindSkip && cmd.Reason == "conflict" && cmd.Entry.Media.ID == 20 {
			skipped++
		}
	}
	if started != 1 || skipped != 1 {
		t.Fatalf("%+v", cmds)
	}
}

func TestTightJoinDoesNotStop(t *testing.T) {
	start := time.Date(2026, 9, 2, 2, 0, 0, 0, time.UTC)
	first := entry(start, 10*time.Second, 1, 7, 19)
	second := entry(start.Add(10*time.Second), 10*time.Second, 2, 7, 20)
	on := EntryKey(7, first)
	played := func(key string) bool { return key == on.String() }
	cmds := Commands(start.Add(10*time.Second), 2*time.Second, 7, []Entry{first, second}, played, alwaysReady, &on)
	for _, cmd := range cmds {
		if cmd.Kind == KindStop {
			t.Fatalf("stopped on a tight join: %+v", cmds)
		}
	}
	if len(cmds) != 1 || cmds[0].Kind != KindStart || cmds[0].Entry.Media.ID != 20 {
		t.Fatalf("%+v", cmds)
	}
}

func TestGapForceStops(t *testing.T) {
	start := time.Date(2026, 9, 2, 2, 0, 0, 0, time.UTC)
	first := entry(start, 10*time.Second, 1, 7, 19)
	second := entry(start.Add(12*time.Second), 10*time.Second, 2, 7, 20)
	on := EntryKey(7, first)
	played := func(key string) bool { return key == on.String() }
	cmds := Commands(start.Add(10*time.Second), 2*time.Second, 7, []Entry{first, second}, played, alwaysReady, &on)
	if len(kinds(cmds)) != 1 || cmds[0].Kind != KindStop {
		t.Fatalf("%+v", cmds)
	}
}

func TestEmptyPackageStops(t *testing.T) {
	on := Key{ScreenID: 7, MediaID: 19, StartsAt: "2026-09-02T02:00:00Z"}
	now := time.Date(2026, 9, 2, 2, 0, 5, 0, time.UTC)
	cmds := Commands(now, 2*time.Second, 7, nil, nonePlayed, alwaysReady, &on)
	if len(cmds) != 1 || cmds[0].Kind != KindStop {
		t.Fatalf("%+v", cmds)
	}
}

func TestSameCoveringKeyDoesNotRestart(t *testing.T) {
	start := time.Date(2026, 9, 2, 2, 0, 0, 0, time.UTC)
	clip := entry(start, 30*time.Second, 1, 7, 19)
	on := EntryKey(7, clip)
	played := func(key string) bool { return key == on.String() }
	cmds := Commands(start.Add(5*time.Second), 2*time.Second, 7, []Entry{clip}, played, alwaysReady, &on)
	if len(cmds) != 0 {
		t.Fatalf("%+v", cmds)
	}
}

func TestDifferentCoveringKeyStops(t *testing.T) {
	start := time.Date(2026, 9, 2, 2, 0, 0, 0, time.UTC)
	old := entry(start, 60*time.Second, 1, 7, 19)
	neu := entry(start, 60*time.Second, 1, 7, 30)
	on := EntryKey(7, old)
	now := start.Add(30 * time.Second)
	cmds := Commands(now, 2*time.Second, 7, []Entry{neu}, nonePlayed, alwaysReady, &on)
	if len(cmds) < 1 || cmds[0].Kind != KindStop {
		t.Fatalf("%+v", cmds)
	}
	for _, cmd := range cmds {
		if cmd.Kind == KindStart {
			t.Fatalf("started a late replacement: %+v", cmds)
		}
	}
}

func TestWakeRetriesUnplayedClipInsideWindow(t *testing.T) {
	start := time.Date(2026, 9, 2, 2, 0, 0, 0, time.UTC)
	clip := entry(start, 30*time.Second, 1, 7, 19)
	wake := WakeAt(start, 2*time.Second, 7, []Entry{clip}, nonePlayed, neverReady)
	if !wake.Equal(start.Add(200 * time.Millisecond)) {
		t.Fatalf("wake %s", wake)
	}
}
```

- [ ] **Step 2: Run the test and confirm it fails**

```bash
go test ./internal/schedule -count=1
```

Expected: FAIL, undefined names.

- [ ] **Step 3: Write the implementation**

`internal/schedule/timeline.go`

```go
package schedule

import (
	"fmt"
	"sort"
	"time"
)

type Media struct {
	ID       int
	URL      string
	MimeType string
}

type Entry struct {
	Position    int
	StartsAt    time.Time
	StartsAtRaw string
	Duration    time.Duration
	ScreenIDs   []int
	Media       Media
}

type Key struct {
	ScreenID int
	MediaID  int
	StartsAt string
}

func (k Key) String() string {
	return fmt.Sprintf("%d:%d:%s", k.ScreenID, k.MediaID, k.StartsAt)
}

func EntryKey(screenID int, entry Entry) Key {
	return Key{ScreenID: screenID, MediaID: entry.Media.ID, StartsAt: entry.StartsAtRaw}
}

type Kind int

const (
	KindStart Kind = iota + 1
	KindStop
	KindSkip
)

type Command struct {
	Kind     Kind
	ScreenID int
	Entry    Entry
	Reason   string
}

type PlayedFunc func(key string) bool
type ReadyFunc func(mediaID int, url string) bool

func ParseEntry(startsAt *string, durationSeconds int, screenIDs []int, mediaID int, rawURL, mime string, position int) (Entry, error) {
	if startsAt == nil || *startsAt == "" || durationSeconds <= 0 || mediaID == 0 || rawURL == "" || mime == "" {
		return Entry{}, fmt.Errorf("ignored_entry")
	}
	parsed, err := time.Parse(time.RFC3339, *startsAt)
	if err != nil {
		return Entry{}, fmt.Errorf("ignored_entry")
	}
	return Entry{
		Position:    position,
		StartsAt:    parsed,
		StartsAtRaw: *startsAt,
		Duration:    time.Duration(durationSeconds) * time.Second,
		ScreenIDs:   append([]int(nil), screenIDs...),
		Media:       Media{ID: mediaID, URL: rawURL, MimeType: mime},
	}, nil
}

func Commands(now time.Time, late time.Duration, screenID int, entries []Entry, played PlayedFunc, ready ReadyFunc, onScreen *Key) []Command {
	play, losers := split(entries, screenID)
	var cmds []Command
	for _, loser := range losers {
		if !now.Before(loser.StartsAt) {
			cmds = append(cmds, Command{Kind: KindSkip, ScreenID: screenID, Entry: loser, Reason: "conflict"})
		}
	}
	covering := coveringEntry(play, now)
	if onScreen != nil && !sameCovering(screenID, covering, *onScreen) {
		if !holdForJoin(now, play, *onScreen, covering, screenID) {
			cmds = append(cmds, Command{Kind: KindStop, ScreenID: screenID})
		}
	}
	if covering != nil {
		cmds = append(cmds, startOrSkip(now, late, screenID, *covering, played, ready, onScreen)...)
	}
	for _, entry := range play {
		if covering != nil && EntryKey(screenID, entry) == EntryKey(screenID, *covering) {
			continue
		}
		if !now.Before(entry.StartsAt) && now.After(entry.StartsAt.Add(late)) && !played(EntryKey(screenID, entry).String()) {
			cmds = append(cmds, Command{Kind: KindSkip, ScreenID: screenID, Entry: entry, Reason: "late"})
		}
	}
	return cmds
}

func WakeAt(now time.Time, late time.Duration, screenID int, entries []Entry, played PlayedFunc, ready ReadyFunc) time.Time {
	play, _ := split(entries, screenID)
	wake := now.Add(24 * time.Hour)
	consider := func(at time.Time) {
		if at.After(now) && at.Before(wake) {
			wake = at
		}
	}
	for _, entry := range play {
		consider(entry.StartsAt)
		consider(entry.StartsAt.Add(entry.Duration))
		consider(entry.StartsAt.Add(late))
	}
	if covering := coveringEntry(play, now); covering != nil && !now.After(covering.StartsAt.Add(late)) && !played(EntryKey(screenID, *covering).String()) {
		soon := now.Add(200 * time.Millisecond)
		if soon.Before(wake) {
			wake = soon
		}
		_ = ready
	}
	return wake
}

func startOrSkip(now time.Time, late time.Duration, screenID int, entry Entry, played PlayedFunc, ready ReadyFunc, onScreen *Key) []Command {
	key := EntryKey(screenID, entry)
	if played(key.String()) || (onScreen != nil && *onScreen == key) {
		return nil
	}
	if now.After(entry.StartsAt.Add(late)) {
		reason := "late"
		if !ready(entry.Media.ID, entry.Media.URL) {
			reason = "missing_file"
		}
		return []Command{{Kind: KindSkip, ScreenID: screenID, Entry: entry, Reason: reason}}
	}
	if !ready(entry.Media.ID, entry.Media.URL) {
		return nil
	}
	return []Command{{Kind: KindStart, ScreenID: screenID, Entry: entry}}
}

func sameCovering(screenID int, covering *Entry, onScreen Key) bool {
	return covering != nil && EntryKey(screenID, *covering) == onScreen
}

func holdForJoin(now time.Time, play []Entry, onScreen Key, covering *Entry, screenID int) bool {
	cur, ok := find(play, onScreen)
	if !ok {
		return false
	}
	next, ok := nextAfter(play, cur)
	if !ok || next.StartsAt.After(cur.StartsAt.Add(cur.Duration).Add(time.Second)) {
		return false
	}
	if now.Before(next.StartsAt) {
		return true
	}
	return covering != nil && EntryKey(screenID, *covering) == EntryKey(screenID, next)
}

func split(entries []Entry, screenID int) (play []Entry, losers []Entry) {
	var list []Entry
	for _, entry := range entries {
		if contains(entry.ScreenIDs, screenID) {
			list = append(list, entry)
		}
	}
	sort.Slice(list, func(i, j int) bool {
		if list[i].StartsAt.Equal(list[j].StartsAt) {
			return list[i].Position < list[j].Position
		}
		return list[i].StartsAt.Before(list[j].StartsAt)
	})
	for i, entry := range list {
		if i > 0 && list[i-1].StartsAt.Equal(entry.StartsAt) {
			losers = append(losers, entry)
			continue
		}
		play = append(play, entry)
	}
	return play, losers
}

func coveringEntry(play []Entry, now time.Time) *Entry {
	var found *Entry
	for i := range play {
		end := play[i].StartsAt.Add(play[i].Duration)
		if !now.Before(play[i].StartsAt) && now.Before(end) {
			if found == nil || play[i].StartsAt.After(found.StartsAt) {
				entry := play[i]
				found = &entry
			}
		}
	}
	return found
}

func find(play []Entry, key Key) (Entry, bool) {
	for _, entry := range play {
		if EntryKey(key.ScreenID, entry) == key {
			return entry, true
		}
	}
	return Entry{}, false
}

func nextAfter(play []Entry, cur Entry) (Entry, bool) {
	for i := range play {
		if play[i].StartsAt.Equal(cur.StartsAt) && play[i].Position == cur.Position && play[i].Media.ID == cur.Media.ID {
			if i+1 < len(play) {
				return play[i+1], true
			}
			return Entry{}, false
		}
	}
	return Entry{}, false
}

func contains(ids []int, want int) bool {
	for _, id := range ids {
		if id == want {
			return true
		}
	}
	return false
}
```

- [ ] **Step 4: Run the test and confirm it passes**

```bash
gofmt -w internal/schedule/timeline.go internal/schedule/timeline_test.go
go test ./internal/schedule -count=1
```

Expected: `ok mediateca-station/internal/schedule`.

- [ ] **Step 5: Commit**

```bash
git add internal/schedule
git commit -m "$(cat <<'EOF'
feat: decide wall-clock start, skip, and stop per screen

EOF
)"
```

---

### Task 5: Play-event queue

**Files:**
- Create: `internal/report/queue.go`
- Create: `internal/report/queue_test.go`

`OutcomeFor` is the HTTP table. The queue itself does not dial the hub.

| Status | Outcome |
| --- | --- |
| 201 | remove the line |
| 401 | pause, keep the line |
| 404, 422 | drop the line |
| transport error, 5xx, anything else | keep the line and retry |

- [ ] **Step 1: Write the failing test**

`internal/report/queue_test.go`

```go
package report

import (
	"path/filepath"
	"testing"
	"time"
)

func TestOutcomeFor(t *testing.T) {
	if OutcomeFor(201, false) != OutcomeRemove {
		t.Fatal("201")
	}
	if OutcomeFor(401, false) != OutcomePause {
		t.Fatal("401")
	}
	if OutcomeFor(404, false) != OutcomeDrop || OutcomeFor(422, false) != OutcomeDrop {
		t.Fatal("drop")
	}
	if OutcomeFor(0, true) != OutcomeRetry || OutcomeFor(500, false) != OutcomeRetry {
		t.Fatal("retry")
	}
}

func TestEnqueuePeekAndPlayedSurvivesReopen(t *testing.T) {
	dir := t.TempDir()
	q, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	started := time.Date(2026, 9, 2, 2, 0, 1, 0, time.UTC)
	key := "7:19:2026-09-02T02:00:00Z"
	if err := q.Enqueue(Event{ScreenID: 7, MediaAssetID: 19, StartedAt: started}, key); err != nil {
		t.Fatal(err)
	}
	if !q.Played(key) {
		t.Fatal("not played")
	}
	ev, ok, err := q.Peek()
	if err != nil || !ok || ev.MediaAssetID != 19 || !ev.StartedAt.Equal(started) {
		t.Fatalf("%+v ok=%v err=%v", ev, ok, err)
	}
	reopened, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	if !reopened.Played(key) {
		t.Fatal("played key lost")
	}
	if err := reopened.RemoveFirst(); err != nil {
		t.Fatal(err)
	}
	_, ok, err = reopened.Peek()
	if err != nil || ok {
		t.Fatalf("after remove ok=%v err=%v", ok, err)
	}
	if !reopened.Played(key) {
		t.Fatal("remove cleared played key")
	}
	if _, err := Open(filepath.Join(dir, "missing")); err != nil {
		t.Fatal(err)
	}
}
```

- [ ] **Step 2: Run the test and confirm it fails**

```bash
go test ./internal/report -count=1
```

Expected: FAIL, undefined names.

- [ ] **Step 3: Write the implementation**

`internal/report/queue.go`

```go
package report

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"time"

	"mediateca-station/internal/fsutil"
)

type Outcome int

const (
	OutcomeRetry Outcome = iota
	OutcomeRemove
	OutcomeDrop
	OutcomePause
)

func OutcomeFor(status int, netErr bool) Outcome {
	if netErr {
		return OutcomeRetry
	}
	switch status {
	case 201:
		return OutcomeRemove
	case 401:
		return OutcomePause
	case 404, 422:
		return OutcomeDrop
	default:
		return OutcomeRetry
	}
}

type Event struct {
	ScreenID     int
	MediaAssetID int
	StartedAt    time.Time
}

type stored struct {
	ScreenID     int    `json:"screen_id"`
	MediaAssetID int    `json:"media_asset_id"`
	StartedAt    string `json:"started_at"`
	PlayedKey    string `json:"played_key"`
}

type Queue struct {
	mu      sync.Mutex
	dir     string
	played  map[string]struct{}
	events  string
	playedP string
}

func Open(dir string) (*Queue, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	q := &Queue{
		dir:     dir,
		played:  map[string]struct{}{},
		events:  filepath.Join(dir, "events.jsonl"),
		playedP: filepath.Join(dir, "played.json"),
	}
	if raw, err := os.ReadFile(q.playedP); err == nil && len(raw) > 0 {
		var keys []string
		if err := json.Unmarshal(raw, &keys); err != nil {
			return nil, err
		}
		for _, key := range keys {
			q.played[key] = struct{}{}
		}
	}
	lines, err := q.readLines()
	if err != nil {
		return nil, err
	}
	for _, line := range lines {
		if line.PlayedKey != "" {
			q.played[line.PlayedKey] = struct{}{}
		}
	}
	return q, nil
}

func (q *Queue) Played(key string) bool {
	q.mu.Lock()
	defer q.mu.Unlock()
	_, ok := q.played[key]
	return ok
}

func (q *Queue) Enqueue(ev Event, playedKey string) error {
	q.mu.Lock()
	defer q.mu.Unlock()
	line, err := json.Marshal(stored{
		ScreenID:     ev.ScreenID,
		MediaAssetID: ev.MediaAssetID,
		StartedAt:    ev.StartedAt.UTC().Format(time.RFC3339),
		PlayedKey:    playedKey,
	})
	if err != nil {
		return err
	}
	f, err := os.OpenFile(q.events, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	if _, err := f.Write(append(line, '\n')); err != nil {
		f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	q.played[playedKey] = struct{}{}
	return q.writePlayed()
}

func (q *Queue) Peek() (Event, bool, error) {
	q.mu.Lock()
	defer q.mu.Unlock()
	lines, err := q.readLines()
	if err != nil || len(lines) == 0 {
		return Event{}, false, err
	}
	started, err := time.Parse(time.RFC3339, lines[0].StartedAt)
	if err != nil {
		return Event{}, false, err
	}
	return Event{ScreenID: lines[0].ScreenID, MediaAssetID: lines[0].MediaAssetID, StartedAt: started}, true, nil
}

func (q *Queue) RemoveFirst() error {
	q.mu.Lock()
	defer q.mu.Unlock()
	lines, err := q.readLines()
	if err != nil || len(lines) == 0 {
		return err
	}
	var buf []byte
	for _, line := range lines[1:] {
		raw, err := json.Marshal(line)
		if err != nil {
			return err
		}
		buf = append(buf, raw...)
		buf = append(buf, '\n')
	}
	return fsutil.WriteAtomic(q.events, buf)
}

func (q *Queue) writePlayed() error {
	keys := make([]string, 0, len(q.played))
	for key := range q.played {
		keys = append(keys, key)
	}
	raw, err := json.Marshal(keys)
	if err != nil {
		return err
	}
	return fsutil.WriteAtomic(q.playedP, raw)
}

func (q *Queue) readLines() ([]stored, error) {
	f, err := os.Open(q.events)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	defer f.Close()
	var lines []stored
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		text := scanner.Bytes()
		if len(text) == 0 {
			continue
		}
		var line stored
		if err := json.Unmarshal(text, &line); err != nil {
			return nil, err
		}
		lines = append(lines, line)
	}
	return lines, scanner.Err()
}
```

- [ ] **Step 4: Run the test and confirm it passes**

```bash
gofmt -w internal/report/queue.go internal/report/queue_test.go
go test ./internal/report -count=1
```

Expected: `ok mediateca-station/internal/report`.

- [ ] **Step 5: Commit**

```bash
git add internal/report
git commit -m "$(cat <<'EOF'
feat: persist play events and the played-set

EOF
)"
```

---

### Task 6: ADB player

**Files:**
- Create: `internal/player/adb.go`
- Create: `internal/player/adb_test.go`

Each command times out after 5 seconds. Arguments are an argv vector, not a shell string. One serial cannot overlap connect, start, and stop.

- [ ] **Step 1: Write the failing test**

`internal/player/adb_test.go`

```go
package player

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"
)

type fakeRun struct {
	mu    sync.Mutex
	args  [][]string
	fail  string
	wait  chan struct{}
	onRun func()
}

func (f *fakeRun) Run(ctx context.Context, argv ...string) error {
	if f.onRun != nil {
		f.onRun()
	}
	f.mu.Lock()
	cp := append([]string{}, argv...)
	f.args = append(f.args, cp)
	fail := len(argv) > 1 && argv[1] == f.fail
	f.mu.Unlock()
	if f.wait != nil {
		select {
		case <-f.wait:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	if fail {
		return errors.New("fail")
	}
	return nil
}

func TestStartConnectsThenLaunchesVLC(t *testing.T) {
	run := &fakeRun{}
	p := &Player{Runner: run}
	err := p.Start(context.Background(), "192.168.1.21:5555", "http://192.168.1.10:8080/m/secret/19", "video/mp2t")
	if err != nil {
		t.Fatal(err)
	}
	if len(run.args) != 2 {
		t.Fatalf("%v", run.args)
	}
	if run.args[0][0] != "adb" || run.args[0][1] != "connect" || run.args[0][2] != "192.168.1.21:5555" {
		t.Fatalf("connect %v", run.args[0])
	}
	want := []string{"adb", "-s", "192.168.1.21:5555", "shell", "am", "start", "-a", "android.intent.action.VIEW", "-d", "http://192.168.1.10:8080/m/secret/19", "-t", "video/mp2t", "-p", "org.videolan.vlc"}
	if stringsJoin(run.args[1]) != stringsJoin(want) {
		t.Fatalf("start %v", run.args[1])
	}
}

func TestStartStopsWhenConnectFails(t *testing.T) {
	run := &fakeRun{fail: "connect"}
	p := &Player{Runner: run}
	if err := p.Start(context.Background(), "192.168.1.21:5555", "http://host/m/s/1", "video/mp2t"); err == nil {
		t.Fatal("expected error")
	}
	if len(run.args) != 1 {
		t.Fatalf("launched VLC after connect failure: %v", run.args)
	}
}

func TestStopForceStopsPackage(t *testing.T) {
	run := &fakeRun{}
	p := &Player{Runner: run}
	if err := p.Stop(context.Background(), "192.168.1.21:5555"); err != nil {
		t.Fatal(err)
	}
	want := []string{"adb", "-s", "192.168.1.21:5555", "shell", "am", "force-stop", "org.videolan.vlc"}
	if stringsJoin(run.args[0]) != stringsJoin(want) {
		t.Fatalf("%v", run.args[0])
	}
}

func TestSerialDoesNotOverlap(t *testing.T) {
	entered := make(chan struct{})
	release := make(chan struct{})
	var once sync.Once
	run := &fakeRun{
		wait: release,
		onRun: func() {
			once.Do(func() { close(entered) })
		},
	}
	p := &Player{Runner: run}
	go func() {
		_ = p.Start(context.Background(), "192.168.1.21:5555", "http://host/a", "video/mp2t")
	}()
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("start did not reach adb")
	}
	done := make(chan struct{})
	go func() {
		_ = p.Stop(context.Background(), "192.168.1.21:5555")
		close(done)
	}()
	select {
	case <-done:
		t.Fatal("stop overlapped start")
	case <-time.After(40 * time.Millisecond):
	}
	close(release)
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("stop did not finish")
	}
}

func stringsJoin(parts []string) string {
	out := ""
	for i, part := range parts {
		if i > 0 {
			out += "\x00"
		}
		out += part
	}
	return out
}
```

- [ ] **Step 2: Run the test and confirm it fails**

```bash
go test ./internal/player -count=1
```

Expected: FAIL, undefined `Player`.

- [ ] **Step 3: Write the implementation**

`internal/player/adb.go`

```go
package player

import (
	"context"
	"fmt"
	"os/exec"
	"sync"
	"time"
)

type Runner interface {
	Run(ctx context.Context, argv ...string) error
}

type ExecRunner struct{}

func (ExecRunner) Run(ctx context.Context, argv ...string) error {
	cmd := exec.CommandContext(ctx, argv[0], argv[1:]...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("adb: %w", err)
	}
	_ = out
	return nil
}

func New() *Player {
	return &Player{Runner: ExecRunner{}}
}

type Player struct {
	Runner Runner
	mapMu  sync.Mutex
	mu     map[string]*sync.Mutex
}

func (p *Player) Start(ctx context.Context, serial, mediaURL, mime string) error {
	unlock := p.lock(serial)
	defer unlock()
	if err := p.run(ctx, "adb", "connect", serial); err != nil {
		return err
	}
	return p.run(ctx, "adb", "-s", serial, "shell", "am", "start",
		"-a", "android.intent.action.VIEW",
		"-d", mediaURL,
		"-t", mime,
		"-p", "org.videolan.vlc")
}

func (p *Player) Stop(ctx context.Context, serial string) error {
	unlock := p.lock(serial)
	defer unlock()
	return p.run(ctx, "adb", "-s", serial, "shell", "am", "force-stop", "org.videolan.vlc")
}

func (p *Player) run(ctx context.Context, argv ...string) error {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	return p.Runner.Run(ctx, argv...)
}

func (p *Player) lock(serial string) func() {
	p.mapMu.Lock()
	if p.mu == nil {
		p.mu = map[string]*sync.Mutex{}
	}
	m, ok := p.mu[serial]
	if !ok {
		m = &sync.Mutex{}
		p.mu[serial] = m
	}
	p.mapMu.Unlock()
	m.Lock()
	return m.Unlock
}
```

`ExecRunner` drops combined output so a URL that contains `http_secret` is not returned to the logger.

- [ ] **Step 4: Run the test and confirm it passes**

```bash
gofmt -w internal/player/adb.go internal/player/adb_test.go
go test ./internal/player -count=1
```

Expected: `ok mediateca-station/internal/player`.

- [ ] **Step 5: Commit**

```bash
git add internal/player
git commit -m "$(cat <<'EOF'
feat: start and stop VLC through adb

EOF
)"
```

---

### Task 7: Media cache

**Files:**
- Create: `internal/cache/cache.go`
- Create: `internal/cache/cache_test.go`

Downloads use a client that never sets `Authorization` and follows at most five redirects. At most two downloads run at once. Bytes land in `<id>.partial` and are renamed only after a full HTTP 200. `.url` and `.mime` are written after the rename. `Reconcile` is called only after a package HTTP 200. It deletes ids absent from that package even if some other download failed. An id that is still wanted keeps its previous file when the new download fails.

- [ ] **Step 1: Write the failing test**

`internal/cache/cache_test.go`

```go
package cache

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestResolveJoinsRelativeURL(t *testing.T) {
	got, err := Resolve("https://hub.example", "/rails/active_storage/blobs/redirect/x")
	if err != nil || got != "https://hub.example/rails/active_storage/blobs/redirect/x" {
		t.Fatalf("%s %v", got, err)
	}
}

func TestDownloadFollowsRedirectWithoutBearer(t *testing.T) {
	file := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "" {
			t.Errorf("bearer leaked: %q", r.Header.Get("Authorization"))
		}
		w.Write([]byte("ts-bytes"))
	}))
	defer file.Close()
	hub := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, file.URL, http.StatusFound)
	}))
	defer hub.Close()
	dir := t.TempDir()
	c := New(dir)
	item := Item{ID: 19, URL: "/rails/active_storage/blobs/redirect/x", Mime: "video/mp2t"}
	if err := c.Reconcile(context.Background(), hub.URL, []Item{item}); err != nil {
		t.Fatal(err)
	}
	if !c.Ready(19, item.URL) {
		t.Fatal("not ready")
	}
	body, err := os.ReadFile(filepath.Join(dir, "19"))
	if err != nil || string(body) != "ts-bytes" {
		t.Fatalf("body %q err %v", body, err)
	}
	if _, err := os.Stat(filepath.Join(dir, "19.partial")); !os.IsNotExist(err) {
		t.Fatal("partial remained")
	}
}

func TestPartialIsNotReady(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "19.partial"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if New(dir).Ready(19, "/clip") {
		t.Fatal("partial was ready")
	}
}

func TestChangedURLDownloadsAgain(t *testing.T) {
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		w.Write([]byte(r.URL.Path))
	}))
	defer srv.Close()
	dir := t.TempDir()
	c := New(dir)
	first := Item{ID: 19, URL: "/a.ts", Mime: "video/mp2t"}
	second := Item{ID: 19, URL: "/b.ts", Mime: "video/mp2t"}
	if err := c.Reconcile(context.Background(), srv.URL, []Item{first}); err != nil {
		t.Fatal(err)
	}
	if err := c.Reconcile(context.Background(), srv.URL, []Item{second}); err != nil {
		t.Fatal(err)
	}
	if !c.Ready(19, "/b.ts") || c.Ready(19, "/a.ts") {
		t.Fatal("url sidecar")
	}
	if hits.Load() != 2 {
		t.Fatalf("hits %d", hits.Load())
	}
}

func TestDeleteUnreferencedEvenWhenAnotherDownloadFails(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/bad" {
			w.WriteHeader(http.StatusBadGateway)
			return
		}
		w.Write([]byte("ok"))
	}))
	defer srv.Close()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "19"), []byte("old"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "19.url"), []byte("/old"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "20"), []byte("keep"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "20.url"), []byte("/bad"), 0o600); err != nil {
		t.Fatal(err)
	}
	err := New(dir).Reconcile(context.Background(), srv.URL, []Item{{ID: 20, URL: "/bad", Mime: "video/mp2t"}})
	if err == nil {
		t.Fatal("expected download error")
	}
	if _, statErr := os.Stat(filepath.Join(dir, "19")); !os.IsNotExist(statErr) {
		t.Fatal("unreferenced file kept")
	}
	if _, statErr := os.Stat(filepath.Join(dir, "20")); statErr != nil {
		t.Fatal("wanted file removed")
	}
}

func TestAtMostTwoDownloads(t *testing.T) {
	var current atomic.Int32
	var max atomic.Int32
	release := make(chan struct{})
	var entered sync.WaitGroup
	entered.Add(2)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := current.Add(1)
		for {
			old := max.Load()
			if n <= old || max.CompareAndSwap(old, n) {
				break
			}
		}
		if n <= 2 {
			entered.Done()
		}
		<-release
		current.Add(-1)
		w.Write([]byte("x"))
	}))
	defer srv.Close()
	c := New(t.TempDir())
	done := make(chan struct{})
	go func() {
		_ = c.Reconcile(context.Background(), srv.URL, []Item{
			{ID: 1, URL: "/f", Mime: "video/mp2t"},
			{ID: 2, URL: "/f", Mime: "video/mp2t"},
			{ID: 3, URL: "/f", Mime: "video/mp2t"},
		})
		close(done)
	}()
	select {
	case <-done:
		t.Fatal("reconcile finished before the third download was released")
	case <-waitEntered(&entered):
	case <-time.After(2 * time.Second):
		t.Fatal("two downloads did not start")
	}
	time.Sleep(50 * time.Millisecond)
	if max.Load() != 2 {
		t.Fatalf("max %d", max.Load())
	}
	close(release)
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("reconcile did not finish")
	}
}

func waitEntered(wg *sync.WaitGroup) <-chan struct{} {
	ch := make(chan struct{})
	go func() {
		wg.Wait()
		close(ch)
	}()
	return ch
}
```

`Reconcile` starts one download goroutine per id and the cache semaphore admits two of them. The third stays blocked until `release` is closed.

- [ ] **Step 2: Run the test and confirm it fails**

```bash
go test ./internal/cache -count=1
```

Expected: FAIL, undefined `New`.

- [ ] **Step 3: Write the implementation**

`internal/cache/cache.go`

```go
package cache

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

type Item struct {
	ID   int
	URL  string
	Mime string
}

type Cache struct {
	Dir  string
	HTTP *http.Client
	sem  chan struct{}
}

func New(dir string) *Cache {
	return &Cache{
		Dir: dir,
		HTTP: &http.Client{
			Timeout: 10 * time.Minute,
			CheckRedirect: func(req *http.Request, via []*http.Request) error {
				if len(via) >= 5 {
					return errors.New("too many redirects")
				}
				req.Header.Del("Authorization")
				return nil
			},
		},
		sem: make(chan struct{}, 2),
	}
}

func Resolve(base, raw string) (string, error) {
	b, err := url.Parse(strings.TrimRight(base, "/"))
	if err != nil {
		return "", err
	}
	ref, err := url.Parse(raw)
	if err != nil {
		return "", err
	}
	return b.ResolveReference(ref).String(), nil
}

func (c *Cache) Ready(id int, rawURL string) bool {
	got, err := os.ReadFile(c.sidecar(id, ".url"))
	if err != nil || string(got) != rawURL {
		return false
	}
	info, err := os.Stat(c.final(id))
	return err == nil && info.Mode().IsRegular()
}

func (c *Cache) Open(id int) (*os.File, string, error) {
	f, err := os.Open(c.final(id))
	if err != nil {
		return nil, "", err
	}
	mime, err := os.ReadFile(c.sidecar(id, ".mime"))
	if err != nil {
		f.Close()
		return nil, "", err
	}
	return f, string(mime), nil
}

func (c *Cache) Reconcile(ctx context.Context, base string, items []Item) error {
	if err := os.MkdirAll(c.Dir, 0o700); err != nil {
		return err
	}
	want := map[int]struct{}{}
	var wg sync.WaitGroup
	errCh := make(chan error, len(items))
	for _, item := range items {
		want[item.ID] = struct{}{}
		wg.Add(1)
		go func(item Item) {
			defer wg.Done()
			if err := c.ensure(ctx, base, item); err != nil {
				errCh <- err
			}
		}(item)
	}
	wg.Wait()
	close(errCh)
	var errs []error
	for err := range errCh {
		errs = append(errs, err)
	}
	if err := c.deleteMissing(want); err != nil {
		errs = append(errs, err)
	}
	return errors.Join(errs...)
}

func (c *Cache) ensure(ctx context.Context, base string, item Item) error {
	if c.Ready(item.ID, item.URL) {
		return nil
	}
	resolved, err := Resolve(base, item.URL)
	if err != nil {
		return err
	}
	return c.download(ctx, item, resolved)
}

func (c *Cache) download(ctx context.Context, item Item, resolved string) error {
	select {
	case c.sem <- struct{}{}:
		defer func() { <-c.sem }()
	case <-ctx.Done():
		return ctx.Err()
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, resolved, nil)
	if err != nil {
		return err
	}
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("download %d: status %d", item.ID, resp.StatusCode)
	}
	partial := c.sidecar(item.ID, ".partial")
	f, err := os.OpenFile(partial, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	if _, err := io.Copy(f, resp.Body); err != nil {
		f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	if err := os.Rename(partial, c.final(item.ID)); err != nil {
		return err
	}
	if err := os.WriteFile(c.sidecar(item.ID, ".url"), []byte(item.URL), 0o600); err != nil {
		return err
	}
	return os.WriteFile(c.sidecar(item.ID, ".mime"), []byte(item.Mime), 0o600)
}

func (c *Cache) deleteMissing(want map[int]struct{}) error {
	entries, err := os.ReadDir(c.Dir)
	if err != nil {
		return err
	}
	seen := map[int]struct{}{}
	for _, entry := range entries {
		name := entry.Name()
		idPart := name
		if dot := strings.IndexByte(name, '.'); dot >= 0 {
			idPart = name[:dot]
		}
		id, err := strconv.Atoi(idPart)
		if err != nil {
			continue
		}
		if _, ok := want[id]; ok {
			continue
		}
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		for _, suffix := range []string{"", ".url", ".mime", ".partial"} {
			_ = os.Remove(filepath.Join(c.Dir, strconv.Itoa(id)+suffix))
		}
	}
	return nil
}

func (c *Cache) final(id int) string {
	return filepath.Join(c.Dir, strconv.Itoa(id))
}

func (c *Cache) sidecar(id int, suffix string) string {
	return filepath.Join(c.Dir, strconv.Itoa(id)+suffix)
}
```

- [ ] **Step 4: Run the test and confirm it passes**

```bash
gofmt -w internal/cache/cache.go internal/cache/cache_test.go
go test ./internal/cache -count=1
```

Expected: `ok mediateca-station/internal/cache`.

If `TestAtMostTwoDownloads` is flaky because both of the first requests have not entered before the assertion, the `entered` wait already covers that. Do not raise the semaphore above 2 to make it pass.

- [ ] **Step 5: Commit**

```bash
git add internal/cache
git commit -m "$(cat <<'EOF'
feat: cache hub media without sending the station token

EOF
)"
```

---

### Task 8: LAN HTTP server

**Files:**
- Create: `internal/httpmedia/server.go`
- Create: `internal/httpmedia/server_test.go`

- [ ] **Step 1: Write the failing test**

`internal/httpmedia/server_test.go`

```go
package httpmedia

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

func TestSecretMimeAndRange(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "19"), []byte("abcdef"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "19.mime"), []byte("video/mp2t"), 0o600); err != nil {
		t.Fatal(err)
	}
	secret := "0123456789abcdef0123456789abcdef"
	h := Handler(secret, func(id int) (*os.File, string, error) {
		f, err := os.Open(filepath.Join(dir, "19"))
		if err != nil {
			return nil, "", err
		}
		mime, err := os.ReadFile(filepath.Join(dir, "19.mime"))
		if err != nil {
			f.Close()
			return nil, "", err
		}
		if id != 19 {
			f.Close()
			return nil, "", os.ErrNotExist
		}
		return f, string(mime), nil
	})

	ok := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/m/"+secret+"/19", nil)
	req.Header.Set("Range", "bytes=0-3")
	h.ServeHTTP(ok, req)
	if ok.Code != http.StatusPartialContent || ok.Body.String() != "abcd" {
		t.Fatalf("code %d body %q", ok.Code, ok.Body.String())
	}
	if ok.Header().Get("Content-Type") != "video/mp2t" {
		t.Fatalf("type %q", ok.Header().Get("Content-Type"))
	}

	missing := httptest.NewRecorder()
	h.ServeHTTP(missing, httptest.NewRequest(http.MethodGet, "/m/wrong/19", nil))
	if missing.Code != http.StatusNotFound || missing.Body.Len() != 0 {
		t.Fatalf("secret code %d body %q", missing.Code, missing.Body.String())
	}

	notFound := httptest.NewRecorder()
	h.ServeHTTP(notFound, httptest.NewRequest(http.MethodGet, "/other", nil))
	if notFound.Code != http.StatusNotFound || notFound.Body.Len() != 0 {
		t.Fatalf("path code %d", notFound.Code)
	}

	badRange := httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodGet, "/m/"+secret+"/19", nil)
	req.Header.Set("Range", "bytes=100-200")
	h.ServeHTTP(badRange, req)
	if badRange.Code != http.StatusRequestedRangeNotSatisfiable {
		t.Fatalf("range code %d", badRange.Code)
	}
}

func TestMissingFileIs404(t *testing.T) {
	h := Handler("0123456789abcdef0123456789abcdef", func(int) (*os.File, string, error) {
		return nil, "", os.ErrNotExist
	})
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/m/0123456789abcdef0123456789abcdef/19", nil))
	if rec.Code != http.StatusNotFound || rec.Body.Len() != 0 {
		t.Fatalf("code %d body %q", rec.Code, rec.Body.String())
	}
}
```

- [ ] **Step 2: Run the test and confirm it fails**

```bash
go test ./internal/httpmedia -count=1
```

Expected: FAIL, `Handler` undefined.

- [ ] **Step 3: Write the implementation**

`internal/httpmedia/server.go`

```go
package httpmedia

import (
	"net/http"
	"os"
	"strconv"
	"strings"
)

type Opener func(id int) (*os.File, string, error)

func Handler(secret string, open Opener) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		id, ok := parse(r.URL.Path, secret)
		if !ok {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		f, mime, err := open(id)
		if err != nil {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		defer f.Close()
		info, err := f.Stat()
		if err != nil {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", mime)
		http.ServeContent(w, r, "media", info.ModTime(), f)
	})
}

func parse(path, secret string) (int, bool) {
	parts := strings.Split(path, "/")
	if len(parts) != 4 || parts[1] != "m" || parts[2] != secret {
		return 0, false
	}
	id, err := strconv.Atoi(parts[3])
	if err != nil || id <= 0 {
		return 0, false
	}
	return id, true
}
```

Do not use `http.Error` or `http.NotFound`. Those write a body. A wrong secret or unknown path is an empty 404.

- [ ] **Step 4: Run the test and confirm it passes**

```bash
gofmt -w internal/httpmedia/server.go internal/httpmedia/server_test.go
go test ./internal/httpmedia -count=1
```

Expected: `ok mediateca-station/internal/httpmedia`.

- [ ] **Step 5: Commit**

```bash
git add internal/httpmedia
git commit -m "$(cat <<'EOF'
feat: serve cached media to VLC on the LAN

EOF
)"
```

---

### Task 9: Agent loop

**Files:**
- Create: `internal/agent/agent.go`
- Create: `internal/agent/agent_test.go`

`poll` writes config and package only after those responses parse. A 401 writes nothing and pauses the reporter. A 200 or 304 clears the pause. Package 200 swaps the in-memory timeline before `cache.Reconcile`. `tick` asks `schedule.Commands` and talks to the player. `started_at` is sampled immediately before `Start`. A failed start force-stops and does not enqueue. Logs never contain `agent_token` or `http_secret`.

- [ ] **Step 1: Write the failing test**

`internal/agent/agent_test.go`

```go
package agent

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"testing"
	"time"

	"mediateca-station/internal/config"
	"mediateca-station/internal/hub"
	"mediateca-station/internal/report"
	"mediateca-station/internal/schedule"
)

type fakeHub struct {
	cfg    hub.Config
	pkg    hub.Package
	cfgErr error
	pkgErr error
	notMod bool
	posts  []int
	post   int
}

func (f *fakeHub) FetchConfig(context.Context) (hub.Config, error) {
	return f.cfg, f.cfgErr
}

func (f *fakeHub) FetchPackage(context.Context, string) (hub.Package, bool, error) {
	return f.pkg, f.notMod, f.pkgErr
}

func (f *fakeHub) PostPlayEvent(context.Context, int, int, time.Time) (int, string, error) {
	f.posts = append(f.posts, f.post)
	if f.post == 0 {
		return 0, "", errors.New("dial")
	}
	return f.post, `{"error":"x"}`, nil
}

type fakePlayer struct {
	starts int
	stops  int
	err    error
	urls   []string
}

func (f *fakePlayer) Start(_ context.Context, _, mediaURL, _ string) error {
	f.starts++
	f.urls = append(f.urls, mediaURL)
	return f.err
}

func (f *fakePlayer) Stop(context.Context, string) error {
	f.stops++
	return nil
}

func testCfg(dir string) config.File {
	return config.File{
		HubBaseURL:    "https://hub.example",
		AgentToken:    "super-secret-token",
		HTTPListen:    "192.168.1.10:8080",
		HTTPSecret:    "0123456789abcdef0123456789abcdef",
		LateThreshold: 2 * time.Second,
		PollInterval:  time.Minute,
		DataDir:       dir,
		Screens:       []config.Screen{{ScreenID: 7, ADBSerial: "192.168.1.21:5555"}},
	}
}

func seedReady(t *testing.T, dir string, id int, rawURL string) {
	t.Helper()
	media := filepath.Join(dir, "media")
	if err := os.MkdirAll(media, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(media, "19"), []byte("ts"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(media, "19.url"), []byte(rawURL), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(media, "19.mime"), []byte("video/mp2t"), 0o600); err != nil {
		t.Fatal(err)
	}
	_ = id
}

func pkgAt(start time.Time, mediaID int, screens []int) hub.Package {
	raw := start.UTC().Format(time.RFC3339)
	return hub.Package{
		HeaderETag: `"v1"`,
		Entries: []hub.Entry{{
			Position:        1,
			StartsAt:        &raw,
			DurationSeconds: 30,
			ScreenIDs:       screens,
			Media:           hub.Media{ID: mediaID, URL: "/clip.ts", MimeType: "video/mp2t"},
		}},
	}
}

func newTestAgent(t *testing.T, cfg config.File, h *fakeHub, p *fakePlayer) (*Agent, *bytes.Buffer) {
	t.Helper()
	var buf bytes.Buffer
	log := slog.New(slog.NewTextHandler(&buf, nil))
	a, err := newAgent(cfg, h, p, log)
	if err != nil {
		t.Fatal(err)
	}
	return a, &buf
}

func TestTickStartsAndDoesNotRepeat(t *testing.T) {
	dir := t.TempDir()
	start := time.Date(2026, 9, 2, 2, 0, 0, 0, time.UTC)
	seedReady(t, dir, 19, "/clip.ts")
	h := &fakeHub{post: 201}
	p := &fakePlayer{}
	a, buf := newTestAgent(t, testCfg(dir), h, p)
	a.now = func() time.Time { return start }
	a.pkg = ptrPkg(pkgAt(start, 19, []int{7}))
	a.tick(context.Background())
	if p.starts != 1 || p.stops != 0 {
		t.Fatalf("starts %d stops %d", p.starts, p.stops)
	}
	if p.urls[0] != "http://192.168.1.10:8080/m/0123456789abcdef0123456789abcdef/19" {
		t.Fatalf("url %s", p.urls[0])
	}
	if !a.queue.Played("7:19:2026-09-02T02:00:00Z") {
		t.Fatal("played key missing")
	}
	a.tick(context.Background())
	if p.starts != 1 {
		t.Fatalf("repeated start %d", p.starts)
	}
	if bytes.Contains(buf.Bytes(), []byte("super-secret-token")) || bytes.Contains(buf.Bytes(), []byte(a.cfg.HTTPSecret)) {
		t.Fatal("secret logged")
	}
}

func TestFailedStartRetriesInsideWindow(t *testing.T) {
	dir := t.TempDir()
	start := time.Date(2026, 9, 2, 2, 0, 0, 0, time.UTC)
	seedReady(t, dir, 19, "/clip.ts")
	p := &fakePlayer{err: errors.New("http://192.168.1.10:8080/m/0123456789abcdef0123456789abcdef/19 boom")}
	a, buf := newTestAgent(t, testCfg(dir), &fakeHub{}, p)
	a.now = func() time.Time { return start }
	a.pkg = ptrPkg(pkgAt(start, 19, []int{7}))
	a.tick(context.Background())
	if p.starts != 1 || p.stops != 1 {
		t.Fatalf("starts %d stops %d", p.starts, p.stops)
	}
	if a.queue.Played("7:19:2026-09-02T02:00:00Z") {
		t.Fatal("failed start was marked played")
	}
	p.err = nil
	a.tick(context.Background())
	if p.starts != 2 {
		t.Fatalf("starts %d", p.starts)
	}
	if bytes.Contains(buf.Bytes(), []byte(a.cfg.HTTPSecret)) {
		t.Fatal("secret logged")
	}
}

func TestRestartMidClipDoesNotResume(t *testing.T) {
	dir := t.TempDir()
	start := time.Date(2026, 9, 2, 2, 0, 0, 0, time.UTC)
	seedReady(t, dir, 19, "/clip.ts")
	p := &fakePlayer{}
	a, _ := newTestAgent(t, testCfg(dir), &fakeHub{}, p)
	a.now = func() time.Time { return start.Add(5 * time.Second) }
	a.pkg = ptrPkg(pkgAt(start, 19, []int{7}))
	a.tick(context.Background())
	if p.starts != 0 {
		t.Fatalf("resumed mid clip starts=%d", p.starts)
	}
}

func TestPlayedFileBlocksStart(t *testing.T) {
	dir := t.TempDir()
	start := time.Date(2026, 9, 2, 2, 0, 0, 0, time.UTC)
	seedReady(t, dir, 19, "/clip.ts")
	if err := os.WriteFile(filepath.Join(dir, "played.json"), []byte(`["7:19:2026-09-02T02:00:00Z"]`), 0o600); err != nil {
		t.Fatal(err)
	}
	p := &fakePlayer{}
	a, _ := newTestAgent(t, testCfg(dir), &fakeHub{}, p)
	a.now = func() time.Time { return start }
	a.pkg = ptrPkg(pkgAt(start, 19, []int{7}))
	a.tick(context.Background())
	if p.starts != 0 {
		t.Fatal("started despite played.json")
	}
}

func TestEmptyPackageStops(t *testing.T) {
	dir := t.TempDir()
	p := &fakePlayer{}
	a, _ := newTestAgent(t, testCfg(dir), &fakeHub{}, p)
	a.onScreen = map[int]schedule.Key{7: {ScreenID: 7, MediaID: 19, StartsAt: "2026-09-02T02:00:00Z"}}
	a.pkg = &hub.Package{}
	a.tick(context.Background())
	if p.stops != 1 {
		t.Fatalf("stops %d", p.stops)
	}
}

func TestHubConfigBlocksUnlistedScreen(t *testing.T) {
	dir := t.TempDir()
	start := time.Date(2026, 9, 2, 2, 0, 0, 0, time.UTC)
	seedReady(t, dir, 19, "/clip.ts")
	p := &fakePlayer{}
	a, _ := newTestAgent(t, testCfg(dir), &fakeHub{}, p)
	a.now = func() time.Time { return start }
	a.pkg = ptrPkg(pkgAt(start, 19, []int{7}))
	a.hubCfg = &hub.Config{Screens: []hub.Screen{{ID: 8, Name: "Other", Orientation: "portrait"}}}
	a.tick(context.Background())
	a.tick(context.Background())
	if p.starts != 0 {
		t.Fatal("played a screen absent from hub config")
	}
}

func TestNoStoredConfigUsesLocalMap(t *testing.T) {
	dir := t.TempDir()
	start := time.Date(2026, 9, 2, 2, 0, 0, 0, time.UTC)
	seedReady(t, dir, 19, "/clip.ts")
	p := &fakePlayer{}
	a, _ := newTestAgent(t, testCfg(dir), &fakeHub{}, p)
	a.now = func() time.Time { return start }
	a.pkg = ptrPkg(pkgAt(start, 19, []int{7}))
	a.tick(context.Background())
	if p.starts != 1 {
		t.Fatal("local screen did not play")
	}
}

func TestPoll401DoesNotReplacePackage(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "package.json"), []byte(`{"entries":[]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	seedReady(t, dir, 19, "/clip.ts")
	h := &fakeHub{cfgErr: hub.ErrUnauthorized}
	a, buf := newTestAgent(t, testCfg(dir), h, &fakePlayer{})
	a.poll(context.Background())
	body, err := os.ReadFile(filepath.Join(dir, "package.json"))
	if err != nil || string(body) != `{"entries":[]}` {
		t.Fatalf("package %s err %v", body, err)
	}
	if _, err := os.Stat(filepath.Join(dir, "media", "19")); err != nil {
		t.Fatal(err)
	}
	if !a.reportPaused {
		t.Fatal("reporter not paused")
	}
	if bytes.Contains(buf.Bytes(), []byte("super-secret-token")) {
		t.Fatal("token logged")
	}
}

func TestPollFailureKeepsMedia(t *testing.T) {
	dir := t.TempDir()
	seedReady(t, dir, 19, "/clip.ts")
	h := &fakeHub{pkgErr: errors.New("timeout")}
	a, _ := newTestAgent(t, testCfg(dir), h, &fakePlayer{})
	a.poll(context.Background())
	if _, err := os.Stat(filepath.Join(dir, "media", "19")); err != nil {
		t.Fatal(err)
	}
}

func TestDrainDrops404AndKeepsNetworkError(t *testing.T) {
	dir := t.TempDir()
	h := &fakeHub{post: 404}
	a, _ := newTestAgent(t, testCfg(dir), h, &fakePlayer{})
	ev := report.Event{ScreenID: 7, MediaAssetID: 19, StartedAt: time.Date(2026, 9, 2, 2, 0, 1, 0, time.UTC)}
	if err := a.queue.Enqueue(ev, "7:19:2026-09-02T02:00:00Z"); err != nil {
		t.Fatal(err)
	}
	a.drain(context.Background())
	_, ok, err := a.queue.Peek()
	if err != nil || ok {
		t.Fatalf("404 kept the event ok=%v err=%v", ok, err)
	}

	h.post = 0
	if err := a.queue.Enqueue(ev, "7:19:2026-09-02T02:00:00Z"); err != nil {
		t.Fatal(err)
	}
	a.drain(context.Background())
	_, ok, err = a.queue.Peek()
	if err != nil || !ok {
		t.Fatalf("network error dropped the event ok=%v err=%v", ok, err)
	}
}

func ptrPkg(pkg hub.Package) *hub.Package { return &pkg }
```

- [ ] **Step 2: Run the test and confirm it fails**

```bash
go test ./internal/agent -count=1
```

Expected: FAIL, `newAgent` undefined.

- [ ] **Step 3: Write the implementation**

`internal/agent/agent.go`

```go
package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"time"

	"mediateca-station/internal/cache"
	"mediateca-station/internal/config"
	"mediateca-station/internal/fsutil"
	"mediateca-station/internal/httpmedia"
	"mediateca-station/internal/hub"
	"mediateca-station/internal/player"
	"mediateca-station/internal/report"
	"mediateca-station/internal/schedule"
)

type Hub interface {
	FetchConfig(ctx context.Context) (hub.Config, error)
	FetchPackage(ctx context.Context, ifNoneMatch string) (hub.Package, bool, error)
	PostPlayEvent(ctx context.Context, screenID, mediaID int, startedAt time.Time) (int, string, error)
}

type Player interface {
	Start(ctx context.Context, serial, mediaURL, mime string) error
	Stop(ctx context.Context, serial string) error
}

type Agent struct {
	cfg           config.File
	hub           Hub
	player        Player
	cache         *cache.Cache
	queue         *report.Queue
	log           *slog.Logger
	now           func() time.Time
	http          *http.Server
	hubCfg        *hub.Config
	hubRaw        []byte
	pkg           *hub.Package
	etag          string
	onScreen      map[int]schedule.Key
	logged        map[string]struct{}
	reportPaused  bool
}

func Run(ctx context.Context, cfg config.File) error {
	a, err := newAgent(cfg, hub.New(cfg.HubBaseURL, cfg.AgentToken), player.New(), slog.Default())
	if err != nil {
		return err
	}
	ln, err := net.Listen("tcp", cfg.HTTPListen)
	if err != nil {
		return err
	}
	a.http = &http.Server{Handler: httpmedia.Handler(cfg.HTTPSecret, a.cache.Open)}
	go a.http.Serve(ln)
	defer a.http.Shutdown(context.Background())
	return a.loop(ctx)
}

func newAgent(cfg config.File, h Hub, p Player, log *slog.Logger) (*Agent, error) {
	if err := os.MkdirAll(cfg.DataDir, 0o700); err != nil {
		return nil, err
	}
	if err := os.Chmod(cfg.DataDir, 0o700); err != nil {
		return nil, err
	}
	media := filepath.Join(cfg.DataDir, "media")
	if err := os.MkdirAll(media, 0o700); err != nil {
		return nil, err
	}
	q, err := report.Open(cfg.DataDir)
	if err != nil {
		return nil, err
	}
	a := &Agent{
		cfg:      cfg,
		hub:      h,
		player:   p,
		cache:    cache.New(media),
		queue:    q,
		log:      log,
		now:      func() time.Time { return time.Now().UTC() },
		onScreen: map[int]schedule.Key{},
		logged:   map[string]struct{}{},
	}
	a.loadDisk()
	return a, nil
}

func (a *Agent) loop(ctx context.Context) error {
	poll := time.NewTicker(a.cfg.PollInterval)
	defer poll.Stop()
	for {
		a.poll(ctx)
		a.tick(ctx)
		a.drain(ctx)
		timer := time.NewTimer(time.Until(a.wake(a.now())))
		select {
		case <-ctx.Done():
			timer.Stop()
			return nil
		case <-poll.C:
			timer.Stop()
		case <-timer.C:
		}
	}
}

func (a *Agent) poll(ctx context.Context) {
	cfg, err := a.hub.FetchConfig(ctx)
	if errors.Is(err, hub.ErrUnauthorized) {
		a.pauseToken()
		return
	}
	var cfgOK *hub.Config
	if err != nil {
		a.log.Error("hub config", "err", err.Error())
	} else {
		cfgOK = &cfg
	}
	pkg, notMod, err := a.hub.FetchPackage(ctx, a.etag)
	if errors.Is(err, hub.ErrUnauthorized) {
		a.pauseToken()
		return
	}
	if err != nil {
		a.log.Error("hub package", "err", err.Error())
		if cfgOK != nil {
			_ = a.storeConfig(*cfgOK)
		}
		return
	}
	if cfgOK != nil {
		_ = a.storeConfig(*cfgOK)
	}
	if notMod {
		a.reportPaused = false
		a.log.Info("poll", "result", "not_modified")
		return
	}
	if err := a.storePackage(pkg); err != nil {
		a.log.Error("store package", "err", err.Error())
		return
	}
	a.reportPaused = false
	a.log.Info("poll", "result", "package")
	if err := a.cache.Reconcile(ctx, a.cfg.HubBaseURL, a.mediaItems()); err != nil {
		a.log.Error("cache", "err", err.Error())
	}
}

func (a *Agent) tick(ctx context.Context) {
	now := a.now()
	entries := a.entries()
	a.logMapping()
	for _, screenID := range a.playable() {
		serial, ok := a.cfg.Serial(screenID)
		if !ok {
			continue
		}
		var on *schedule.Key
		if key, found := a.onScreen[screenID]; found {
			on = &key
		}
		cmds := schedule.Commands(now, a.cfg.LateThreshold, screenID, entries, a.queue.Played, a.cache.Ready, on)
		for _, cmd := range cmds {
			a.apply(ctx, serial, cmd)
		}
	}
}

func (a *Agent) apply(ctx context.Context, serial string, cmd schedule.Command) {
	switch cmd.Kind {
	case schedule.KindStop:
		if err := a.player.Stop(ctx, serial); err != nil {
			a.log.Error("adb stop failed", "screen_id", cmd.ScreenID)
			return
		}
		delete(a.onScreen, cmd.ScreenID)
		a.log.Info("stop", "screen_id", cmd.ScreenID)
	case schedule.KindStart:
		started := a.now().UTC()
		if err := a.player.Start(ctx, serial, a.mediaURL(cmd.Entry.Media.ID), cmd.Entry.Media.MimeType); err != nil {
			a.log.Error("adb start failed", "screen_id", cmd.ScreenID, "media_id", cmd.Entry.Media.ID)
			_ = a.player.Stop(ctx, serial)
			return
		}
		key := schedule.EntryKey(cmd.ScreenID, cmd.Entry).String()
		if err := a.queue.Enqueue(report.Event{
			ScreenID:     cmd.ScreenID,
			MediaAssetID: cmd.Entry.Media.ID,
			StartedAt:    started,
		}, key); err != nil {
			a.log.Error("enqueue play event", "screen_id", cmd.ScreenID)
			_ = a.player.Stop(ctx, serial)
			return
		}
		a.onScreen[cmd.ScreenID] = schedule.EntryKey(cmd.ScreenID, cmd.Entry)
		a.log.Info("start", "screen_id", cmd.ScreenID, "media_id", cmd.Entry.Media.ID, "starts_at", cmd.Entry.StartsAtRaw)
	case schedule.KindSkip:
		id := "skip:" + schedule.EntryKey(cmd.ScreenID, cmd.Entry).String() + ":" + cmd.Reason
		a.logOnce(id, func() {
			a.log.Info("skip", "reason", cmd.Reason, "screen_id", cmd.ScreenID, "media_id", cmd.Entry.Media.ID, "starts_at", cmd.Entry.StartsAtRaw)
		})
	}
}

func (a *Agent) drain(ctx context.Context) {
	if a.reportPaused {
		return
	}
	for {
		ev, ok, err := a.queue.Peek()
		if err != nil || !ok {
			return
		}
		status, body, err := a.hub.PostPlayEvent(ctx, ev.ScreenID, ev.MediaAssetID, ev.StartedAt)
		switch report.OutcomeFor(status, err != nil) {
		case report.OutcomeRemove:
			_ = a.queue.RemoveFirst()
		case report.OutcomeDrop:
			a.log.Error("drop play event", "status", status, "body", body)
			_ = a.queue.RemoveFirst()
		case report.OutcomePause:
			a.pauseToken()
			return
		default:
			return
		}
	}
}

func (a *Agent) pauseToken() {
	a.reportPaused = true
	a.logOnce("token", func() {
		a.log.Error("hub token rejected")
	})
	a.log.Info("poll", "result", "unauthorized")
}

func (a *Agent) playable() []int {
	if a.hubCfg == nil {
		ids := make([]int, 0, len(a.cfg.Screens))
		for _, screen := range a.cfg.Screens {
			ids = append(ids, screen.ScreenID)
		}
		return ids
	}
	allow := map[int]struct{}{}
	for _, screen := range a.hubCfg.Screens {
		allow[screen.ID] = struct{}{}
	}
	var ids []int
	for _, screen := range a.cfg.Screens {
		if _, ok := allow[screen.ScreenID]; ok {
			ids = append(ids, screen.ScreenID)
		}
	}
	return ids
}

func (a *Agent) logMapping() {
	if a.hubCfg == nil {
		return
	}
	local := map[int]struct{}{}
	for _, screen := range a.cfg.Screens {
		local[screen.ScreenID] = struct{}{}
	}
	hubIDs := map[int]struct{}{}
	for _, screen := range a.hubCfg.Screens {
		hubIDs[screen.ID] = struct{}{}
		if _, ok := local[screen.ID]; !ok {
			id := screen.ID
			a.logOnce(fmt.Sprintf("map:%d:no_serial", id), func() {
				a.log.Error("screen mapping", "screen_id", id, "reason", "no_serial")
			})
		}
	}
	for _, screen := range a.cfg.Screens {
		if _, ok := hubIDs[screen.ScreenID]; !ok {
			id := screen.ScreenID
			a.logOnce(fmt.Sprintf("map:%d:not_in_hub_config", id), func() {
				a.log.Error("screen mapping", "screen_id", id, "reason", "not_in_hub_config")
			})
		}
	}
}

func (a *Agent) entries() []schedule.Entry {
	if a.pkg == nil {
		return nil
	}
	var out []schedule.Entry
	for _, raw := range a.pkg.Entries {
		entry, err := schedule.ParseEntry(raw.StartsAt, raw.DurationSeconds, raw.ScreenIDs, raw.Media.ID, raw.Media.URL, raw.Media.MimeType, raw.Position)
		if err != nil {
			pos := raw.Position
			mediaID := raw.Media.ID
			a.logOnce(fmt.Sprintf("ignored:%d:%d", pos, mediaID), func() {
				a.log.Info("skip", "reason", "ignored_entry", "position", pos, "media_id", mediaID)
			})
			continue
		}
		out = append(out, entry)
	}
	return out
}

func (a *Agent) mediaItems() []cache.Item {
	seen := map[int]cache.Item{}
	var order []int
	for _, entry := range a.entries() {
		if _, ok := seen[entry.Media.ID]; !ok {
			order = append(order, entry.Media.ID)
		}
		seen[entry.Media.ID] = cache.Item{ID: entry.Media.ID, URL: entry.Media.URL, Mime: entry.Media.MimeType}
	}
	out := make([]cache.Item, 0, len(order))
	for _, id := range order {
		out = append(out, seen[id])
	}
	return out
}

func (a *Agent) mediaURL(id int) string {
	return "http://" + a.cfg.HTTPListen + "/m/" + a.cfg.HTTPSecret + "/" + strconv.Itoa(id)
}

func (a *Agent) wake(now time.Time) time.Time {
	wake := now.Add(24 * time.Hour)
	entries := a.entries()
	for _, screenID := range a.playable() {
		at := schedule.WakeAt(now, a.cfg.LateThreshold, screenID, entries, a.queue.Played, a.cache.Ready)
		if at.Before(wake) {
			wake = at
		}
	}
	if !wake.After(now) {
		return now
	}
	return wake
}

func (a *Agent) storeConfig(cfg hub.Config) error {
	raw, err := json.Marshal(cfg)
	if err != nil {
		return err
	}
	if a.hubRaw != nil && bytes.Equal(a.hubRaw, raw) {
		a.hubCfg = &cfg
		return nil
	}
	if err := fsutil.WriteAtomic(filepath.Join(a.cfg.DataDir, "config.json"), raw); err != nil {
		return err
	}
	a.hubRaw = append([]byte(nil), raw...)
	a.hubCfg = &cfg
	a.log.Info("config", "station_id", cfg.StationID, "offline_cache_hours", cfg.OfflineCacheHours)
	for _, screen := range cfg.Screens {
		a.log.Info("screen", "screen_id", screen.ID, "orientation", screen.Orientation, "name", screen.Name)
	}
	return nil
}

func (a *Agent) storePackage(pkg hub.Package) error {
	raw, err := json.Marshal(pkg)
	if err != nil {
		return err
	}
	if err := fsutil.WriteAtomic(filepath.Join(a.cfg.DataDir, "package.json"), raw); err != nil {
		return err
	}
	if err := fsutil.WriteAtomic(filepath.Join(a.cfg.DataDir, "package.etagheader"), []byte(pkg.HeaderETag)); err != nil {
		return err
	}
	copied := pkg
	a.pkg = &copied
	a.etag = pkg.HeaderETag
	return nil
}

func (a *Agent) loadDisk() {
	if raw, err := os.ReadFile(filepath.Join(a.cfg.DataDir, "config.json")); err == nil {
		var cfg hub.Config
		if json.Unmarshal(raw, &cfg) == nil {
			a.hubCfg = &cfg
			a.hubRaw = raw
		}
	}
	if raw, err := os.ReadFile(filepath.Join(a.cfg.DataDir, "package.json")); err == nil {
		var pkg hub.Package
		if json.Unmarshal(raw, &pkg) == nil {
			a.pkg = &pkg
		}
	}
	if raw, err := os.ReadFile(filepath.Join(a.cfg.DataDir, "package.etagheader")); err == nil {
		a.etag = string(raw)
	}
}

func (a *Agent) logOnce(key string, fn func()) {
	if _, ok := a.logged[key]; ok {
		return
	}
	a.logged[key] = struct{}{}
	fn()
}
```

`TestPoll401DoesNotReplacePackage` only fails the config call. `poll` returns before `FetchPackage` on config 401, which matches the spec: a 401 writes neither file. The seeded `package.json` stays. `newAgent` does not need the package to parse into entries for this test.

`TestEmptyPackageStops` sets `onScreen` after `newAgent`. `tick` with an empty package and a remembered key must force-stop. `schedule.Commands` does that when the remembered key is not in the new timeline.

- [ ] **Step 4: Run the tests and confirm they pass**

```bash
gofmt -w internal/agent/agent.go internal/agent/agent_test.go
go test ./internal/agent -count=1
go test ./... -count=1
```

Expected: every package `ok`.

If `TestEmptyPackageStops` does not stop, the remembered key's screen must be playable. With `hubCfg == nil`, screen 7 is playable. `Commands` with an empty entry list and a non-nil `onScreen` returns `KindStop`. `apply` calls `Stop`.

If `TestTickStartsAndDoesNotRepeat` starts twice, the first tick did not record the played key or did not set `onScreen`. Both are required.

- [ ] **Step 5: Commit**

```bash
git add internal/agent
git commit -m "$(cat <<'EOF'
feat: poll the hub and play the cached timeline

EOF
)"
```

---

### Task 10: Process entry and operator README

**Files:**
- Create: `cmd/station/main.go`
- Create: `README.md`

- [ ] **Step 1: Write main**

`cmd/station/main.go`

```go
package main

import (
	"context"
	"flag"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"mediateca-station/internal/agent"
	"mediateca-station/internal/config"
)

func main() {
	path := flag.String("config", "", "path to config.yaml")
	flag.Parse()
	if *path == "" {
		slog.Error("missing -config")
		os.Exit(1)
	}
	cfg, err := config.Load(*path)
	if err != nil {
		slog.Error("config", "err", err.Error())
		os.Exit(1)
	}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	if err := agent.Run(ctx, cfg); err != nil {
		slog.Error("station", "err", err.Error())
		os.Exit(1)
	}
}
```

A bad config exits non-zero. An idle station does not. SIGINT returns from `Run` with a nil error and the process exits 0.

- [ ] **Step 2: Write the README**

`README.md`

```markdown
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

## Logs

```bash
journalctl -u mediateca-station -f
```

`skip` lines use `reason=late`, `reason=missing_file`, `reason=conflict`, or
`reason=ignored_entry`. `hub token rejected` means the bearer token was
refused; playback of the stored package continues, and new play reports wait
until a later poll succeeds. The token and the HTTP secret are not written to
the log.
```

- [ ] **Step 3: Build and run the full suite**

```bash
gofmt -w cmd/station/main.go
go test ./... -count=1
go build -o /tmp/mediateca-station ./cmd/station
```

Expected: all tests `ok`, and the binary builds.

Smoke the config gate:

```bash
dir=$(mktemp -d)
printf '%s\n' 'hub_base_url: "https://hub.example"' > "$dir/config.yaml"
chmod 0644 "$dir/config.yaml"
/tmp/mediateca-station -config "$dir/config.yaml"; echo exit:$?
```

Expected: the process exits `1` because the file is group/world readable (and, after a mode fix, because required fields are missing).

- [ ] **Step 4: Commit**

```bash
git add cmd/station/main.go README.md
git commit -m "$(cat <<'EOF'
docs: add the station process entry and mini-PC install

EOF
)"
```

---

## Spec coverage

| Spec rule | Task |
| --- | --- |
| Private YAML, defaults, serial map, 32-character secret | 2 |
| Bearer on hub routes only, raw ETag, 304, 401, one play event | 3, 9 |
| Ignore incomplete entries | 4, 9 |
| Start at `starts_at` and at the late threshold; skip 1ns later | 4 |
| Missing file waits inside the window, then skips | 4, 7 |
| Tight join does not force-stop; a longer gap does | 4 |
| Conflict keeps the smaller position | 4 |
| Other screen never starts | 4 |
| Empty package force-stops | 4, 9 |
| Same covering key does not restart; a different key stops | 4 |
| Restart mid-clip does not resume | 9 |
| Failed adb retries; success does not; `played.json` blocks a replay | 5, 6, 9 |
| Relative URL, redirect, no bearer, partial, changed URL, delete | 7 |
| Failed poll does not delete media; 401 does not replace the package | 9 |
| No stored hub config uses the local map; stored config is an allow-list | 9 |
| Reporter 201 / 404 / 422 / 401 / network | 5, 9 |
| HTTP secret, MIME, `Range` 206, bad range 416, empty 404 | 8 |
| VLC argv and per-serial lock | 6 |
| README, systemd, chrony, journald reasons | 10 |

Out of this plan: heartbeat, TV power, HLS, a companion APK, frame sync, and any change to the hub.
