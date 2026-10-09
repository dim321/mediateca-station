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
