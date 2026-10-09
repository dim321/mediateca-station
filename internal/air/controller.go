package air

import (
	"context"
	"io"
	"log/slog"
	"os"
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
	Log        *slog.Logger

	mu       sync.Mutex
	feeds    map[int]context.CancelFunc // screenID
	feedDone map[int]chan struct{}
	writers  map[int]io.WriteCloser
	ports    map[int]int // screenID → stream port
}

func (c *Controller) Start(ctx context.Context, req StartReq) error {
	path := c.MediaPath(req.MediaID)
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	_ = f.Close()

	c.mu.Lock()
	if c.feeds == nil {
		c.feeds = map[int]context.CancelFunc{}
		c.feedDone = map[int]chan struct{}{}
		c.writers = map[int]io.WriteCloser{}
		c.ports = map[int]int{}
	}
	var waitDone chan struct{}
	hadActive := false
	var w io.WriteCloser
	prevPort := -1
	if cancel, ok := c.feeds[req.ScreenID]; ok {
		hadActive = true
		waitDone = c.feedDone[req.ScreenID]
		cancel()
		delete(c.feeds, req.ScreenID)
		w = c.writers[req.ScreenID]
		prevPort = c.ports[req.ScreenID]
	}
	c.mu.Unlock()

	if waitDone != nil {
		<-waitDone
	}

	if w == nil || prevPort != req.Port {
		if w != nil {
			_ = w.Close()
			w = nil
		}
		w, err = c.OpenWriter(req.Port)
		if err != nil {
			return err
		}
		c.mu.Lock()
		c.writers[req.ScreenID] = w
		c.ports[req.ScreenID] = req.Port
		c.mu.Unlock()
	}

	if !hadActive {
		tv := c.NewTV(req.Serial)
		url := c.StreamURL(req.ScreenID)
		if err := tv.StartStream(ctx, url, 300); err != nil {
			return err
		}
	}

	feedCtx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	c.mu.Lock()
	c.feeds[req.ScreenID] = cancel
	c.feedDone[req.ScreenID] = done
	c.mu.Unlock()

	feed := &feeder.Feeder{ChunkSize: c.ChunkSize, Pace: feeder.PaceMode(c.Pace)}
	go func() {
		defer close(done)
		if err := feed.FeedFileDuration(feedCtx, path, w, req.Duration); err != nil && c.Log != nil {
			c.Log.Error("feed", "screen_id", req.ScreenID, "media_id", req.MediaID, "err", err.Error())
		}
	}()
	return nil
}

func (c *Controller) Stop(ctx context.Context, serial string, screenID int) error {
	c.mu.Lock()
	var waitDone chan struct{}
	if cancel, ok := c.feeds[screenID]; ok {
		waitDone = c.feedDone[screenID]
		cancel()
		delete(c.feeds, screenID)
		delete(c.feedDone, screenID)
	}
	var w io.WriteCloser
	if ww, ok := c.writers[screenID]; ok {
		w = ww
		delete(c.writers, screenID)
		delete(c.ports, screenID)
	}
	c.mu.Unlock()

	if waitDone != nil {
		<-waitDone
	}
	if w != nil {
		_ = w.Close()
	}
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
