package air

import (
	"context"
	"fmt"
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

type channel struct {
	mediaID int
	writer  io.WriteCloser
	cancel  context.CancelFunc
	done    chan struct{}
	viewers map[int]string // screenID -> serial
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
	channels map[int]*channel // port -> shared stream channel
}

func (c *Controller) Start(ctx context.Context, req StartReq) error {
	path := c.MediaPath(req.MediaID)
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	_ = f.Close()

	c.mu.Lock()
	if c.channels == nil {
		c.channels = map[int]*channel{}
	}
	ch := c.channels[req.Port]
	if ch != nil && ch.mediaID == req.MediaID {
		if _, ok := ch.viewers[req.ScreenID]; ok {
			c.mu.Unlock()
			return nil
		}
		ch.viewers[req.ScreenID] = req.Serial
		c.mu.Unlock()
		tv := c.NewTV(req.Serial)
		url := c.StreamURL(req.ScreenID)
		return tv.StartStream(ctx, url, 300)
	}

	var old *channel
	if ch != nil {
		old = ch
		delete(c.channels, req.Port)
	}
	c.mu.Unlock()

	var waitDone chan struct{}
	var oldWriter io.WriteCloser
	if old != nil {
		waitDone = old.done
		old.cancel()
		oldWriter = old.writer
	}
	if waitDone != nil {
		<-waitDone
	}
	if oldWriter != nil {
		_ = oldWriter.Close()
	}

	w, err := c.OpenWriter(req.Port)
	if err != nil {
		return err
	}

	feedCtx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	newCh := &channel{
		mediaID: req.MediaID,
		writer:  w,
		cancel:  cancel,
		done:    done,
		viewers: map[int]string{req.ScreenID: req.Serial},
	}

	c.mu.Lock()
	existing := c.channels[req.Port]
	if existing != nil {
		if existing.mediaID == req.MediaID {
			cancel()
			_ = w.Close()
			if _, ok := existing.viewers[req.ScreenID]; ok {
				c.mu.Unlock()
				return nil
			}
			existing.viewers[req.ScreenID] = req.Serial
			c.mu.Unlock()
			tv := c.NewTV(req.Serial)
			url := c.StreamURL(req.ScreenID)
			return tv.StartStream(ctx, url, 300)
		}
		cancel()
		_ = w.Close()
		c.mu.Unlock()
		return fmt.Errorf("stream port %d busy", req.Port)
	}
	c.channels[req.Port] = newCh
	c.mu.Unlock()

	tv := c.NewTV(req.Serial)
	url := c.StreamURL(req.ScreenID)
	if err := tv.StartStream(ctx, url, 300); err != nil {
		cancel()
		_ = w.Close()
		c.mu.Lock()
		if c.channels[req.Port] == newCh {
			delete(c.channels, req.Port)
		}
		c.mu.Unlock()
		return err
	}

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
	var ch *channel
	var port int
	for p, cch := range c.channels {
		if _, ok := cch.viewers[screenID]; ok {
			ch = cch
			port = p
			break
		}
	}
	if ch != nil {
		delete(ch.viewers, screenID)
	}
	remaining := ch != nil && len(ch.viewers) > 0
	var waitDone chan struct{}
	var w io.WriteCloser
	var cancel context.CancelFunc
	if ch != nil && !remaining {
		waitDone = ch.done
		cancel = ch.cancel
		w = ch.writer
		delete(c.channels, port)
	}
	c.mu.Unlock()

	if cancel != nil {
		cancel()
	}
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
	var w io.WriteCloser
	for _, ch := range c.channels {
		if _, ok := ch.viewers[screenID]; ok {
			w = ch.writer
			break
		}
	}
	c.mu.Unlock()
	if w == nil {
		return nil
	}
	f := &feeder.Feeder{}
	return f.WriteNullPackets(w, count)
}
