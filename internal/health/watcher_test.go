package health

import (
	"context"
	"sync/atomic"
	"testing"
	"time"
)

type stub struct {
	playing  bool
	restarts int32
}

func (s *stub) Status(ctx context.Context) (TvStatus, error) {
	return TvStatus{Playing: s.playing}, nil
}

func (s *stub) RestartStream(ctx context.Context) error {
	atomic.AddInt32(&s.restarts, 1)
	return nil
}

func TestWatcherRestartsWhenNotPlayingOnAir(t *testing.T) {
	s := &stub{playing: false}
	w := &Watcher{
		Interval:    time.Millisecond,
		AutoRestart: true,
		OnAir:       func(screenID int) bool { return true },
		Client:      func(screenID int) ScreenClient { return s },
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	_ = w.Run(ctx, []int{7})
	if atomic.LoadInt32(&s.restarts) < 1 {
		t.Fatal("expected restart")
	}
}

func TestWatcherIdleNoRestart(t *testing.T) {
	s := &stub{playing: false}
	w := &Watcher{
		Interval:    time.Millisecond,
		AutoRestart: true,
		OnAir:       func(screenID int) bool { return false },
		Client:      func(screenID int) ScreenClient { return s },
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	_ = w.Run(ctx, []int{7})
	if atomic.LoadInt32(&s.restarts) != 0 {
		t.Fatalf("expected no restarts, got %d", s.restarts)
	}
}
