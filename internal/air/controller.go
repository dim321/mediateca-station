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
