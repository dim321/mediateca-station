package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"mediateca-station/internal/air"
	"mediateca-station/internal/config"
	"mediateca-station/internal/hub"
	"mediateca-station/internal/report"
	"mediateca-station/internal/schedule"
)

type fakeHub struct {
	mu sync.Mutex

	cfg    hub.Config
	pkg    hub.Package
	cfgErr error
	pkgErr error
	notMod bool
	posts  []int
	post   int

	commands       []hub.Command
	fetchCmdErr    error
	postResultsErr error
	holdResults    func()
	teleErr        error
	telePosts      int
	lastTelemetry  hub.Telemetry
	commandResults []hub.CommandResult
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

func (f *fakeHub) PostTelemetry(_ context.Context, tel hub.Telemetry) error {
	f.telePosts++
	f.lastTelemetry = tel
	return f.teleErr
}

func (f *fakeHub) FetchCommands(context.Context, string) (hub.CommandsResponse, error) {
	if f.fetchCmdErr != nil {
		return hub.CommandsResponse{}, f.fetchCmdErr
	}
	return hub.CommandsResponse{Commands: append([]hub.Command(nil), f.commands...)}, nil
}

func (f *fakeHub) PostCommandResults(_ context.Context, results []hub.CommandResult) error {
	if f.holdResults != nil {
		f.holdResults()
	}
	f.mu.Lock()
	f.commandResults = append(f.commandResults, results...)
	err := f.postResultsErr
	f.mu.Unlock()
	return err
}

func (f *fakeHub) UploadArtifact(context.Context, string, io.Reader, string) error {
	return nil
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

type fakeAir struct {
	starts []air.StartReq
	stops  int
	err    error
}

func (f *fakeAir) Start(_ context.Context, req air.StartReq) error {
	f.starts = append(f.starts, req)
	return f.err
}

func (f *fakeAir) Stop(context.Context, string, int) error {
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

func voxelTestCfg(dir string) config.File {
	return config.File{
		HubBaseURL:    "https://hub.example",
		AgentToken:    "super-secret-token",
		LateThreshold: 2 * time.Second,
		PollInterval:  time.Minute,
		DataDir:       dir,
		BroadcastBin:  "/usr/local/bin/broadcast",
		BroadcastBind: "192.168.1.10",
		FifoDir:       filepath.Join(dir, "fifo"),
		VoxelAPK:      "/opt/voxel.apk",
		Screens:       []config.Screen{{ScreenID: 7, ADBSerial: "192.168.1.21:5555", StreamPort: 8091}},
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

func TestTickVoxelPathUsesAir(t *testing.T) {
	dir := t.TempDir()
	start := time.Date(2026, 9, 2, 2, 0, 0, 0, time.UTC)
	seedReady(t, dir, 19, "/clip.ts")
	p := &fakePlayer{}
	fa := &fakeAir{}
	a, _ := newTestAgent(t, voxelTestCfg(dir), &fakeHub{}, p)
	a.air = fa
	a.now = func() time.Time { return start }
	a.pkg = ptrPkg(pkgAt(start, 19, []int{7}))
	a.tick(context.Background())
	if p.starts != 0 || p.stops != 0 {
		t.Fatalf("player starts=%d stops=%d", p.starts, p.stops)
	}
	if len(fa.starts) != 1 {
		t.Fatalf("air starts=%d", len(fa.starts))
	}
	req := fa.starts[0]
	if req.ScreenID != 7 || req.Port != 8091 || req.MediaID != 19 || req.Duration != 30*time.Second {
		t.Fatalf("start req %+v", req)
	}
	if !a.queue.Played("7:19:2026-09-02T02:00:00Z") {
		t.Fatal("played key missing")
	}
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

func TestMediaURLUsesHTTPPublic(t *testing.T) {
	dir := t.TempDir()
	start := time.Date(2026, 9, 2, 2, 0, 0, 0, time.UTC)
	seedReady(t, dir, 19, "/clip.ts")
	cfg := testCfg(dir)
	cfg.HTTPListen = "0.0.0.0:8080"
	cfg.HTTPPublic = "10.0.0.8:8080"
	p := &fakePlayer{}
	a, _ := newTestAgent(t, cfg, &fakeHub{}, p)
	a.now = func() time.Time { return start }
	a.pkg = ptrPkg(pkgAt(start, 19, []int{7}))
	a.tick(context.Background())
	if len(p.urls) != 1 || p.urls[0] != "http://10.0.0.8:8080/m/0123456789abcdef0123456789abcdef/19" {
		t.Fatalf("url %v", p.urls)
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

func TestApplyTVMonitorFromPoll(t *testing.T) {
	dir := t.TempDir()
	h := &fakeHub{cfg: hub.Config{
		StationID: 1,
		TVMonitor: &hub.TVMonitor{
			HealthIntervalSeconds:     45,
			AutoRestartWhenNotPlaying: false,
			TelemetryIntervalSeconds:  90,
		},
	}}
	a, _ := newTestAgent(t, voxelTestCfg(dir), h, &fakePlayer{})
	a.poll(context.Background())
	if a.telemetryInterval != 90*time.Second {
		t.Fatalf("telemetry=%v", a.telemetryInterval)
	}
}

func TestApplyTVMonitorDefaultsWithoutHubBlock(t *testing.T) {
	dir := t.TempDir()
	a, _ := newTestAgent(t, voxelTestCfg(dir), &fakeHub{cfg: hub.Config{StationID: 1}}, &fakePlayer{})
	a.applyTVMonitor(hub.Config{})
	if a.telemetryInterval != 60*time.Second {
		t.Fatalf("telemetry=%v", a.telemetryInterval)
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

type fakeCmdExec struct {
	mu   sync.Mutex
	runs []hub.Command
}

func (f *fakeCmdExec) Run(_ context.Context, cmd hub.Command) hub.CommandResult {
	f.mu.Lock()
	f.runs = append(f.runs, cmd)
	f.mu.Unlock()
	return hub.CommandResult{CommandID: cmd.ID, OK: true, FinishedAt: time.Now().UTC()}
}

func TestPollRunsCommandsAndPostsResults(t *testing.T) {
	dir := t.TempDir()
	h := &fakeHub{
		cfg: hub.Config{StationID: 1},
		pkg: pkgAt(time.Now(), 19, []int{7}),
		commands: []hub.Command{{
			ID: "cmd_1", ScreenID: 7, Type: "restart_stream", IssuedAt: "2026-10-09T11:01:00Z",
		}},
	}
	exec := &fakeCmdExec{}
	a, _ := newTestAgent(t, testCfg(dir), h, &fakePlayer{})
	a.cmdExec = exec
	a.poll(context.Background())
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		exec.mu.Lock()
		runs := len(exec.runs)
		exec.mu.Unlock()
		h.mu.Lock()
		results := len(h.commandResults)
		h.mu.Unlock()
		if runs == 1 && results == 1 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	exec.mu.Lock()
	runs := append([]hub.Command(nil), exec.runs...)
	exec.mu.Unlock()
	h.mu.Lock()
	posted := append([]hub.CommandResult(nil), h.commandResults...)
	h.mu.Unlock()
	if len(runs) != 1 || runs[0].ID != "cmd_1" {
		t.Fatalf("runs %+v", runs)
	}
	if len(posted) != 1 || !posted[0].OK {
		t.Fatalf("results %+v", posted)
	}
}

func stationFetch(id string) hub.Command {
	return hub.Command{ID: id, Type: "fetch_screens", ScreenAbsent: true}
}

func leaseLine() []byte {
	return []byte("0 aa:bb:cc:dd:ee:ff 192.168.1.10 living-room-tv *\n")
}

func TestFetchScreensDoesNotRunScreenExecutor(t *testing.T) {
	dir := t.TempDir()
	h := &fakeHub{}
	exec := &fakeCmdExec{}
	a, _ := newTestAgent(t, testCfg(dir), h, &fakePlayer{})
	a.cmdExec = exec
	a.now = func() time.Time { return time.Date(2026, 10, 13, 14, 0, 3, 0, time.UTC) }
	reads := 0
	a.leaseFile.Read = func(string) ([]byte, error) {
		reads++
		return leaseLine(), nil
	}
	raw := []byte(`{"commands":[{"id":"cmd_fetch","screen_id":null,"type":"fetch_screens","params":{},"issued_at":"2026-10-13T14:00:00Z"},{"id":"cmd_unknown","screen_id":7,"type":"nope","issued_at":"2026-10-13T14:00:00Z"}]}`)
	var resp hub.CommandsResponse
	if err := json.Unmarshal(raw, &resp); err != nil {
		t.Fatal(err)
	}
	a.runCommands(context.Background(), resp.Commands)
	exec.mu.Lock()
	runs := append([]hub.Command(nil), exec.runs...)
	exec.mu.Unlock()
	if len(runs) != 1 || runs[0].ID != "cmd_unknown" || runs[0].Type != "nope" {
		t.Fatalf("runs %+v", runs)
	}
	if reads != 1 {
		t.Fatalf("reads %d", reads)
	}
	h.mu.Lock()
	posted := append([]hub.CommandResult(nil), h.commandResults...)
	h.mu.Unlock()
	if len(posted) != 2 || !posted[0].OK || posted[0].Result == nil || len(posted[0].Result.Screens) != 1 {
		t.Fatalf("%+v", posted)
	}
	if posted[0].Result.Screens[0].MACAddress != "aa:bb:cc:dd:ee:ff" || posted[0].Result.Screens[0].IPAddress != "192.168.1.10" {
		t.Fatalf("%+v", posted[0].Result.Screens)
	}
	if !a.commandAlreadyDone("cmd_fetch") {
		t.Fatal("successful fetch was not marked delivered")
	}
}

func TestRunCommandsFetchScreensDelivery(t *testing.T) {
	dir := t.TempDir()
	h := &fakeHub{postResultsErr: errors.New("dial tcp")}
	exec := &fakeCmdExec{}
	a, buf := newTestAgent(t, testCfg(dir), h, &fakePlayer{})
	a.cmdExec = exec
	reads := 0
	a.leaseFile.Read = func(string) ([]byte, error) {
		reads++
		return leaseLine(), nil
	}
	a.runCommands(context.Background(), []hub.Command{stationFetch("cmd_1")})
	if a.commandAlreadyDone("cmd_1") || reads != 1 {
		t.Fatalf("done=%v reads=%d", a.commandAlreadyDone("cmd_1"), reads)
	}

	h.mu.Lock()
	h.postResultsErr = &hub.HTTPStatusError{Status: 503, Op: "command_results"}
	h.mu.Unlock()
	a.runCommands(context.Background(), []hub.Command{stationFetch("cmd_1")})
	if a.commandAlreadyDone("cmd_1") || reads != 2 {
		t.Fatalf("done=%v reads=%d", a.commandAlreadyDone("cmd_1"), reads)
	}

	buf.Reset()
	h.mu.Lock()
	h.postResultsErr = &hub.HTTPStatusError{Status: 422, Op: "command_results"}
	h.mu.Unlock()
	a.runCommands(context.Background(), []hub.Command{stationFetch("cmd_1")})
	if a.commandAlreadyDone("cmd_1") || reads != 3 {
		t.Fatalf("done=%v reads=%d", a.commandAlreadyDone("cmd_1"), reads)
	}
	logText := buf.String()
	if !strings.Contains(logText, "422") || !strings.Contains(logText, "cmd_1") {
		t.Fatalf("log %s", logText)
	}
	if strings.Contains(logText, "super-secret-token") || strings.Contains(logText, "/var/lib/misc/dnsmasq.leases") || strings.Contains(logText, "aa:bb:cc:dd:ee:ff") {
		t.Fatalf("log leaked %s", logText)
	}

	h.mu.Lock()
	h.postResultsErr = nil
	h.mu.Unlock()
	a.runCommands(context.Background(), []hub.Command{stationFetch("cmd_1")})
	if !a.commandAlreadyDone("cmd_1") || reads != 4 {
		t.Fatalf("done=%v reads=%d", a.commandAlreadyDone("cmd_1"), reads)
	}
	a.runCommands(context.Background(), []hub.Command{stationFetch("cmd_1")})
	if reads != 4 {
		t.Fatalf("delivered command re-read the lease file, reads=%d", reads)
	}
	exec.mu.Lock()
	if len(exec.runs) != 0 {
		t.Fatalf("executor %+v", exec.runs)
	}
	exec.mu.Unlock()
}

func TestFetchScreensResult401Pauses(t *testing.T) {
	dir := t.TempDir()
	h := &fakeHub{postResultsErr: hub.ErrUnauthorized}
	a, _ := newTestAgent(t, testCfg(dir), h, &fakePlayer{})
	a.leaseFile.Read = func(string) ([]byte, error) { return leaseLine(), nil }
	a.runCommands(context.Background(), []hub.Command{stationFetch("cmd_1")})
	if !a.reportPaused || a.commandAlreadyDone("cmd_1") {
		t.Fatalf("paused=%v done=%v", a.reportPaused, a.commandAlreadyDone("cmd_1"))
	}
}

func TestFetchScreensRetryDoesNotBlockPlayEvents(t *testing.T) {
	dir := t.TempDir()
	entered := make(chan struct{})
	release := make(chan struct{})
	h := &fakeHub{
		cfg:            hub.Config{StationID: 1},
		notMod:         true,
		post:           201,
		postResultsErr: errors.New("dial tcp"),
		commands:       []hub.Command{stationFetch("cmd_1")},
		holdResults: func() {
			close(entered)
			<-release
		},
	}
	a, _ := newTestAgent(t, testCfg(dir), h, &fakePlayer{})
	a.cmdExec = &fakeCmdExec{}
	a.leaseFile.Read = func(string) ([]byte, error) { return leaseLine(), nil }
	ev := report.Event{ScreenID: 7, MediaAssetID: 19, StartedAt: time.Date(2026, 10, 13, 14, 0, 3, 0, time.UTC)}
	if err := a.queue.Enqueue(ev, "7:19:2026-10-13T14:00:03Z"); err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	go func() {
		a.poll(context.Background())
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("poll blocked on command_results")
	}
	if len(h.posts) != 1 || h.posts[0] != 201 {
		t.Fatalf("play posts %+v", h.posts)
	}
	close(release)
	select {
	case <-entered:
	case <-time.After(2 * time.Second):
		t.Fatal("command result was not attempted")
	}
}

func TestRunCommandsSkipsAlreadyDone(t *testing.T) {
	dir := t.TempDir()
	h := &fakeHub{}
	exec := &fakeCmdExec{}
	a, _ := newTestAgent(t, testCfg(dir), h, &fakePlayer{})
	a.cmdExec = exec
	a.markCommandDone("cmd_1")
	a.runCommands(context.Background(), []hub.Command{{ID: "cmd_1", ScreenID: 7, Type: "wake"}})
	exec.mu.Lock()
	runs := len(exec.runs)
	exec.mu.Unlock()
	if runs != 0 {
		t.Fatalf("runs %d", runs)
	}
	h.mu.Lock()
	results := h.commandResults
	h.mu.Unlock()
	if len(results) != 1 || !results[0].OK || results[0].Error == nil || *results[0].Error != "already_done" {
		t.Fatalf("results %+v", results)
	}
}

func TestPollCommands401PausesReporting(t *testing.T) {
	dir := t.TempDir()
	h := &fakeHub{
		cfg:         hub.Config{StationID: 1},
		notMod:      true,
		fetchCmdErr: hub.ErrUnauthorized,
		post:        201,
	}
	a, _ := newTestAgent(t, testCfg(dir), h, &fakePlayer{})
	ev := report.Event{ScreenID: 7, MediaAssetID: 19, StartedAt: time.Now().UTC()}
	if err := a.queue.Enqueue(ev, "7:19:2026-09-02T02:00:00Z"); err != nil {
		t.Fatal(err)
	}
	a.poll(context.Background())
	if !a.reportPaused {
		t.Fatal("expected paused")
	}
	if len(h.posts) != 0 {
		t.Fatalf("drain ran while paused posts=%d", len(h.posts))
	}
}

func TestPostTelemetry401Pauses(t *testing.T) {
	dir := t.TempDir()
	h := &fakeHub{teleErr: hub.ErrUnauthorized}
	a, _ := newTestAgent(t, testCfg(dir), h, &fakePlayer{})
	a.postTelemetry(context.Background())
	if !a.reportPaused {
		t.Fatal("expected paused")
	}
}

func TestPostTelemetrySkipsWhenPaused(t *testing.T) {
	dir := t.TempDir()
	h := &fakeHub{}
	a, _ := newTestAgent(t, testCfg(dir), h, &fakePlayer{})
	a.reportPaused = true
	a.postTelemetry(context.Background())
	if h.telePosts != 0 {
		t.Fatalf("telePosts=%d", h.telePosts)
	}
}

func TestBuildTelemetryIncludesScreens(t *testing.T) {
	dir := t.TempDir()
	a, _ := newTestAgent(t, testCfg(dir), &fakeHub{}, &fakePlayer{})
	a.etag = `"v1"`
	a.now = func() time.Time { return time.Date(2026, 10, 9, 12, 0, 0, 0, time.UTC) }
	a.startedAt = a.now().Add(-10 * time.Second)
	tel := a.buildTelemetry(context.Background())
	if tel.Station.PackageETag != `"v1"` || tel.Station.UptimeSeconds != 10 {
		t.Fatalf("station %+v", tel.Station)
	}
	if len(tel.Screens) != 1 || tel.Screens[0].ScreenID != 7 {
		t.Fatalf("screens %+v", tel.Screens)
	}
}

func ptrPkg(pkg hub.Package) *hub.Package { return &pkg }
