package air

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
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
	time.Sleep(50 * time.Millisecond)
	tv.mu.Lock()
	defer tv.mu.Unlock()
	if len(tv.urls) != 1 {
		t.Fatalf("urls=%v", tv.urls)
	}
}

func TestControllerJoinReusesWriterAndSkipsSecondStartStream(t *testing.T) {
	dir := t.TempDir()
	media := filepath.Join(dir, "19")
	if err := os.WriteFile(media, []byte{0x47, 0x00, 0x01}, 0o600); err != nil {
		t.Fatal(err)
	}
	var openCalls atomic.Int32
	var closeCalls atomic.Int32
	r, pipeW, _ := os.Pipe()
	go io.Copy(io.Discard, r)
	tracked := &countCloser{WriteCloser: pipeW, onClose: func() { closeCalls.Add(1) }}
	tv := &memTV{}
	c := &Controller{
		OpenWriter: func(port int) (io.WriteCloser, error) {
			openCalls.Add(1)
			return tracked, nil
		},
		NewTV:     func(serial string) TV { return tv },
		MediaPath: func(id int) string { return media },
		StreamURL: func(screenID int) string { return "http://192.168.33.1:8091/" },
		Pace:      "max",
		ChunkSize: 18800,
	}

	ctx := context.Background()
	req := StartReq{
		ScreenID: 7, Serial: "192.168.33.182:5555", Port: 8091,
		MediaID: 19, Duration: 200 * time.Millisecond,
	}
	if err := c.Start(ctx, req); err != nil {
		t.Fatal(err)
	}
	time.Sleep(30 * time.Millisecond)
	if err := c.Start(ctx, req); err != nil {
		t.Fatal(err)
	}
	time.Sleep(30 * time.Millisecond)
	tv.mu.Lock()
	urls := len(tv.urls)
	tv.mu.Unlock()
	if openCalls.Load() != 1 {
		t.Fatalf("OpenWriter calls=%d want 1", openCalls.Load())
	}
	if urls != 1 {
		t.Fatalf("StartStream calls=%d want 1", urls)
	}
	if err := c.Stop(ctx, req.Serial, req.ScreenID); err != nil {
		t.Fatal(err)
	}
	if closeCalls.Load() != 1 {
		t.Fatalf("Close calls=%d want 1", closeCalls.Load())
	}
}

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
			if tv, ok := tvs[serial]; ok {
				return tv
			}
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

type countCloser struct {
	io.WriteCloser
	onClose func()
}

func (c *countCloser) Close() error {
	if c.onClose != nil {
		c.onClose()
	}
	return c.WriteCloser.Close()
}
