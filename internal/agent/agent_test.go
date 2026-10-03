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
