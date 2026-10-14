package commands

import (
	"bytes"
	"context"
	"io"
	"os"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"mediateca-station/internal/config"
	"mediateca-station/internal/hub"
	"mediateca-station/internal/tv"
)

type fakeTV struct {
	starts   int
	restarts int
	stops    int
	lastURL  string
	lastBuf  int
}

func (f *fakeTV) Status(context.Context) (tv.Status, error) { return tv.Status{}, nil }

func (f *fakeTV) StartStream(_ context.Context, url string, bufferMs int) error {
	f.starts++
	f.lastURL = url
	f.lastBuf = bufferMs
	return nil
}

func (f *fakeTV) RestartStream(context.Context) error {
	f.restarts++
	return nil
}

func (f *fakeTV) Stop(context.Context) error {
	f.stops++
	return nil
}

type fakeDev struct {
	wakes       int
	sleeps      int
	screenshots int
	records     int
	recordSec   int
	ensureApps  int
	installs    int
	launches    int
	lastInstall string
	shotData    []byte
	recordDest  string
}

func (f *fakeDev) Wake(context.Context) error {
	f.wakes++
	return nil
}

func (f *fakeDev) Sleep(context.Context) error {
	f.sleeps++
	return nil
}

func (f *fakeDev) Screenshot(context.Context) ([]byte, error) {
	f.screenshots++
	if f.shotData != nil {
		return f.shotData, nil
	}
	return []byte("png"), nil
}

func (f *fakeDev) Record(_ context.Context, seconds int, dest string) error {
	f.records++
	f.recordSec = seconds
	f.recordDest = dest
	return os.WriteFile(dest, []byte("mp4"), 0o644)
}

func (f *fakeDev) EnsureApp(context.Context, AgentClient, string) error {
	f.ensureApps++
	return nil
}

func (f *fakeDev) InstallAPK(_ context.Context, apk string) error {
	f.installs++
	f.lastInstall = apk
	return nil
}

func (f *fakeDev) LaunchMain(context.Context) error {
	f.launches++
	return nil
}

func testExecutor(tv AgentClient, dev DeviceClient) *Executor {
	return &Executor{
		TV:     func(screenID int) AgentClient { return tv },
		Device: func(screenID int) DeviceClient { return dev },
		Screen: func(id int) (config.Screen, bool) {
			return config.Screen{ScreenID: id, ADBSerial: "1.2.3.4:5555", StreamPort: 8091}, true
		},
		StreamURL: func(id int) string { return "http://192.168.33.1:8091/" },
		APKPath:   func() string { return "/apk.apk" },
	}
}

func TestExecutorRestartStream(t *testing.T) {
	tv := &fakeTV{}
	ex := testExecutor(tv, &fakeDev{})
	res := ex.Run(context.Background(), hub.Command{ID: "c1", ScreenID: 7, Type: "restart_stream"})
	if !res.OK || tv.restarts != 1 {
		t.Fatalf("%+v %d", res, tv.restarts)
	}
}

func TestExecutorStartStreamUsesStreamURL(t *testing.T) {
	tv := &fakeTV{}
	ex := testExecutor(tv, &fakeDev{})
	res := ex.Run(context.Background(), hub.Command{ID: "c1", ScreenID: 7, Type: "start_stream"})
	if !res.OK || tv.starts != 1 || tv.lastURL != "http://192.168.33.1:8091/" {
		t.Fatalf("%+v url=%q", res, tv.lastURL)
	}
}

func TestExecutorStartStreamParamURL(t *testing.T) {
	tv := &fakeTV{}
	ex := testExecutor(tv, &fakeDev{})
	res := ex.Run(context.Background(), hub.Command{
		ID: "c1", ScreenID: 7, Type: "start_stream",
		Params: map[string]any{"url": "http://custom/", "buffer_ms": float64(500)},
	})
	if !res.OK || tv.lastURL != "http://custom/" || tv.lastBuf != 500 {
		t.Fatalf("%+v url=%q buf=%d", res, tv.lastURL, tv.lastBuf)
	}
}

func TestExecutorStop(t *testing.T) {
	tv := &fakeTV{}
	ex := testExecutor(tv, &fakeDev{})
	res := ex.Run(context.Background(), hub.Command{ID: "c1", ScreenID: 7, Type: "stop"})
	if !res.OK || tv.stops != 1 {
		t.Fatalf("%+v", res)
	}
}

func TestExecutorWakeSleep(t *testing.T) {
	dev := &fakeDev{}
	ex := testExecutor(&fakeTV{}, dev)
	if res := ex.Run(context.Background(), hub.Command{ID: "w", ScreenID: 7, Type: "wake"}); !res.OK || dev.wakes != 1 {
		t.Fatalf("wake %+v", res)
	}
	if res := ex.Run(context.Background(), hub.Command{ID: "s", ScreenID: 7, Type: "sleep"}); !res.OK || dev.sleeps != 1 {
		t.Fatalf("sleep %+v", res)
	}
}

func TestExecutorRecordMissingUploadURL(t *testing.T) {
	dev := &fakeDev{}
	ex := testExecutor(&fakeTV{}, dev)
	res := ex.Run(context.Background(), hub.Command{
		ID: "c1", ScreenID: 7, Type: "record",
		Params: map[string]any{"seconds": float64(5)},
	})
	if res.OK || res.Error == nil || *res.Error != "missing_upload_url" || dev.records != 0 {
		t.Fatalf("%+v records=%d", res, dev.records)
	}
}

func TestExecutorRecordClampsSeconds(t *testing.T) {
	dev := &fakeDev{}
	ex := testExecutor(&fakeTV{}, dev)
	ex.Upload = func(context.Context, string, io.Reader, string) error { return nil }
	res := ex.Run(context.Background(), hub.Command{
		ID: "c1", ScreenID: 7, Type: "record",
		Params: map[string]any{"upload_url": "https://hub/video", "seconds": float64(500)},
	})
	if !res.OK || dev.recordSec != 120 {
		t.Fatalf("%+v sec=%d", res, dev.recordSec)
	}
}

func TestExecutorRecordUpload(t *testing.T) {
	dev := &fakeDev{}
	var uploaded bytes.Buffer
	ex := testExecutor(&fakeTV{}, dev)
	ex.Upload = func(_ context.Context, url string, r io.Reader, ct string) error {
		if url != "https://hub/video" || ct != "video/mp4" {
			t.Fatalf("url=%q ct=%q", url, ct)
		}
		_, err := io.Copy(&uploaded, r)
		return err
	}
	res := ex.Run(context.Background(), hub.Command{
		ID: "c1", ScreenID: 7, Type: "record",
		Params: map[string]any{"upload_url": "https://hub/video", "seconds": 3},
	})
	if !res.OK || dev.records != 1 || uploaded.String() != "mp4" {
		t.Fatalf("%+v body=%q", res, uploaded.String())
	}
}

func TestExecutorScreenshotMissingUploadURL(t *testing.T) {
	dev := &fakeDev{}
	ex := testExecutor(&fakeTV{}, dev)
	res := ex.Run(context.Background(), hub.Command{ID: "c1", ScreenID: 7, Type: "screenshot"})
	if res.OK || res.Error == nil || *res.Error != "missing_upload_url" || dev.screenshots != 0 {
		t.Fatalf("%+v shots=%d", res, dev.screenshots)
	}
}

func TestExecutorInstallAPKRejectsHTTPURL(t *testing.T) {
	dev := &fakeDev{}
	ex := testExecutor(&fakeTV{}, dev)
	res := ex.Run(context.Background(), hub.Command{
		ID: "c1", ScreenID: 7, Type: "install_apk",
		Params: map[string]any{"url": "http://evil/apk.apk"},
	})
	if res.OK || dev.installs != 0 {
		t.Fatalf("%+v", res)
	}
}

func TestExecutorScreenshotUpload(t *testing.T) {
	dev := &fakeDev{shotData: []byte("img")}
	var uploaded bytes.Buffer
	ex := testExecutor(&fakeTV{}, dev)
	ex.Upload = func(_ context.Context, url string, r io.Reader, ct string) error {
		if url != "https://hub/upload" || ct != "image/png" {
			t.Fatalf("url=%q ct=%q", url, ct)
		}
		_, err := io.Copy(&uploaded, r)
		return err
	}
	res := ex.Run(context.Background(), hub.Command{
		ID: "c1", ScreenID: 7, Type: "screenshot",
		Params: map[string]any{"upload_url": "https://hub/upload"},
	})
	if !res.OK || dev.screenshots != 1 || uploaded.String() != "img" {
		t.Fatalf("%+v shots=%d body=%q", res, dev.screenshots, uploaded.String())
	}
}

func TestExecutorEnsureApp(t *testing.T) {
	dev := &fakeDev{}
	ex := testExecutor(&fakeTV{}, dev)
	res := ex.Run(context.Background(), hub.Command{ID: "c1", ScreenID: 7, Type: "ensure_app"})
	if !res.OK || dev.ensureApps != 1 {
		t.Fatalf("%+v", res)
	}
}

func TestExecutorInstallAPKLocal(t *testing.T) {
	dev := &fakeDev{}
	ex := testExecutor(&fakeTV{}, dev)
	res := ex.Run(context.Background(), hub.Command{ID: "c1", ScreenID: 7, Type: "install_apk"})
	if !res.OK || dev.installs != 1 || dev.lastInstall != "/apk.apk" || dev.launches != 1 {
		t.Fatalf("%+v install=%q launches=%d", res, dev.lastInstall, dev.launches)
	}
}

func TestExecutorUnknownType(t *testing.T) {
	ex := testExecutor(&fakeTV{}, &fakeDev{})
	res := ex.Run(context.Background(), hub.Command{ID: "c1", ScreenID: 7, Type: "nope"})
	if res.OK || res.Error == nil || *res.Error != "unknown_type" {
		t.Fatalf("%+v", res)
	}
}

type slowDev struct {
	delay time.Duration

	active int32
	max    int32
}

func (d *slowDev) Wake(context.Context) error {
	n := atomic.AddInt32(&d.active, 1)
	for {
		old := atomic.LoadInt32(&d.max)
		if n > old {
			if atomic.CompareAndSwapInt32(&d.max, old, n) {
				break
			}
			continue
		}
		break
	}
	time.Sleep(d.delay)
	atomic.AddInt32(&d.active, -1)
	return nil
}

func (d *slowDev) Sleep(context.Context) error { return nil }
func (d *slowDev) Screenshot(context.Context) ([]byte, error) {
	return nil, nil
}
func (d *slowDev) Record(context.Context, int, string) error { return nil }
func (d *slowDev) EnsureApp(context.Context, AgentClient, string) error {
	return nil
}
func (d *slowDev) InstallAPK(context.Context, string) error { return nil }
func (d *slowDev) LaunchMain(context.Context) error         { return nil }

func TestExecutorPerScreenMutex(t *testing.T) {
	dev := &slowDev{delay: 80 * time.Millisecond}
	ex := testExecutor(&fakeTV{}, dev)

	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		ex.Run(context.Background(), hub.Command{ID: "a", ScreenID: 7, Type: "wake"})
	}()
	go func() {
		defer wg.Done()
		ex.Run(context.Background(), hub.Command{ID: "b", ScreenID: 7, Type: "wake"})
	}()
	wg.Wait()
	if atomic.LoadInt32(&dev.max) != 1 {
		t.Fatalf("expected max concurrent 1 on same screen, got %d", dev.max)
	}

	dev2 := &slowDev{delay: 80 * time.Millisecond}
	ex2 := testExecutor(&fakeTV{}, dev2)
	var wg2 sync.WaitGroup
	wg2.Add(2)
	go func() {
		defer wg2.Done()
		ex2.Run(context.Background(), hub.Command{ID: "a", ScreenID: 1, Type: "wake"})
	}()
	go func() {
		defer wg2.Done()
		ex2.Run(context.Background(), hub.Command{ID: "b", ScreenID: 2, Type: "wake"})
	}()
	wg2.Wait()
	if atomic.LoadInt32(&dev2.max) < 2 {
		t.Fatalf("expected parallel runs on different screens, max=%d", dev2.max)
	}
}
