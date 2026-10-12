package tv

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
)

type recRunner struct {
	calls [][]string
	out   []byte
	err   error
}

func (r *recRunner) Run(ctx context.Context, argv ...string) error {
	cp := append([]string{}, argv...)
	r.calls = append(r.calls, cp)
	return r.err
}

func (r *recRunner) RunOut(ctx context.Context, argv ...string) ([]byte, error) {
	cp := append([]string{}, argv...)
	r.calls = append(r.calls, cp)
	if r.err != nil {
		return nil, r.err
	}
	if r.out != nil {
		return r.out, nil
	}
	return []byte{0x89, 0x50, 0x4e, 0x47}, nil
}

func (r *recRunner) hasSubslice(parts ...string) bool {
	for _, call := range r.calls {
		if subslice(call, parts) {
			return true
		}
	}
	return false
}

func subslice(haystack, needle []string) bool {
	if len(needle) > len(haystack) {
		return false
	}
	for i := 0; i <= len(haystack)-len(needle); i++ {
		ok := true
		for j := range needle {
			if haystack[i+j] != needle[j] {
				ok = false
				break
			}
		}
		if ok {
			return true
		}
	}
	return false
}

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

func TestSleepUsesKeyevent(t *testing.T) {
	r := &recRunner{}
	d := &Device{Serial: "192.168.33.182:5555", Runner: r}
	if err := d.Sleep(context.Background()); err != nil {
		t.Fatal(err)
	}
	if !r.hasSubslice("keyevent", "KEYCODE_SLEEP") {
		t.Fatalf("%v", r.calls)
	}
}

func TestScreenshotScreencap(t *testing.T) {
	r := &recRunner{}
	d := &Device{Serial: "192.168.33.182:5555", Runner: r}
	b, err := d.Screenshot(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(b) < 4 {
		t.Fatalf("short png %d", len(b))
	}
	if !r.hasSubslice("exec-out", "screencap", "-p") {
		t.Fatalf("%v", r.calls)
	}
}

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

func TestLaunchMain(t *testing.T) {
	r := &recRunner{}
	d := &Device{Serial: "1.2.3.4:5555", Runner: r}
	if err := d.LaunchMain(context.Background()); err != nil {
		t.Fatal(err)
	}
	if !r.hasSubslice("am", "start", "-n", "com.voxel.mediastream/.MainActivity") {
		t.Fatalf("%v", r.calls)
	}
}

func TestRecordScreenrecordAndPull(t *testing.T) {
	r := &recRunner{}
	d := &Device{Serial: "10.0.0.5:5555", Runner: r}
	if err := d.Record(context.Background(), 5, "/tmp/cap.mp4"); err != nil {
		t.Fatal(err)
	}
	if !r.hasSubslice("screenrecord", "--time-limit", "5") {
		t.Fatalf("screenrecord %v", r.calls)
	}
	if !r.hasSubslice("pull") {
		t.Fatalf("pull %v", r.calls)
	}
}

func TestWakeCallsWOLWhenStillAsleep(t *testing.T) {
	r := &recRunner{out: []byte("mWakefulness=Asleep\n")}
	var wolMAC string
	d := &Device{
		Serial: "192.168.33.182:5555",
		Runner: r,
		MAC:    "78:53:33:4b:2c:29",
		WOL: func(mac string) error {
			wolMAC = mac
			return nil
		},
	}
	if err := d.Wake(context.Background()); err != nil {
		t.Fatal(err)
	}
	if wolMAC != "78:53:33:4b:2c:29" {
		t.Fatalf("wol %q", wolMAC)
	}
}

func TestEnsureAppSkipsWhenAgentUp(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = jsonStatus(w)
	}))
	defer srv.Close()
	r := &recRunner{}
	d := &Device{Serial: "1.2.3.4:5555", Runner: r}
	a := &Agent{BaseURL: srv.URL, HTTP: srv.Client()}
	if err := d.EnsureApp(context.Background(), a, "/apk/x.apk"); err != nil {
		t.Fatal(err)
	}
	if r.hasSubslice("install") {
		t.Fatalf("installed while agent up: %v", r.calls)
	}
}

func TestEnsureAppInstallsWhenAgentDown(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "down", http.StatusServiceUnavailable)
	}))
	defer srv.Close()
	r := &recRunner{}
	d := &Device{Serial: "1.2.3.4:5555", Runner: r}
	a := &Agent{BaseURL: srv.URL, HTTP: srv.Client()}
	if err := d.EnsureApp(context.Background(), a, "/home/stream/apk/voxel-debug.apk"); err != nil {
		t.Fatal(err)
	}
	if !r.hasSubslice("install", "-r", "/home/stream/apk/voxel-debug.apk") {
		t.Fatalf("install %v", r.calls)
	}
	if !r.hasSubslice("am", "start", "-n", "com.voxel.mediastream/.MainActivity") {
		t.Fatalf("launch %v", r.calls)
	}
}

func TestConnect(t *testing.T) {
	r := &recRunner{}
	d := &Device{Serial: "192.168.1.5:5555", Runner: r}
	if err := d.Connect(context.Background()); err != nil {
		t.Fatal(err)
	}
	if !r.hasSubslice("connect", "192.168.1.5:5555") {
		t.Fatalf("%v", r.calls)
	}
}

func TestWakePropagatesRunnerError(t *testing.T) {
	r := &recRunner{err: errors.New("adb down")}
	d := &Device{Serial: "x:5555", Runner: r}
	if err := d.Wake(context.Background()); err == nil {
		t.Fatal("expected error")
	}
}

func TestWakeWOLOnConnectFailure(t *testing.T) {
	connects := 0
	r := &recRunner{}
	r.err = errors.New("connect failed")
	d := &Device{
		Serial: "192.168.33.182:5555",
		Runner: &connectCountRunner{inner: r, onConnect: func() { connects++ }},
		MAC:    "78:53:33:4b:2c:29",
		WOL: func(mac string) error {
			r.err = nil
			return nil
		},
	}
	if err := d.Wake(context.Background()); err != nil {
		t.Fatal(err)
	}
	if connects < 2 {
		t.Fatalf("connect attempts=%d want >=2", connects)
	}
}

type connectCountRunner struct {
	inner     *recRunner
	onConnect func()
}

func (c *connectCountRunner) Run(ctx context.Context, argv ...string) error {
	if len(argv) >= 2 && argv[1] == "connect" {
		c.onConnect()
	}
	return c.inner.Run(ctx, argv...)
}

func (c *connectCountRunner) RunOut(ctx context.Context, argv ...string) ([]byte, error) {
	return c.inner.RunOut(ctx, argv...)
}

func jsonStatus(w http.ResponseWriter) error {
	w.Header().Set("Content-Type", "application/json")
	_, err := w.Write([]byte(`{"playing":false,"url":"","buffer_ms":0}`))
	return err
}
