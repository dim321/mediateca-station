package health

import (
	"context"
	"time"
)

const defaultInterval = 30 * time.Second

type TvStatus struct {
	Playing bool
}

type ScreenClient interface {
	Status(ctx context.Context) (TvStatus, error)
	RestartStream(ctx context.Context) error
}

type Watcher struct {
	Interval    time.Duration
	AutoRestart bool
	OnAir       func(screenID int) bool
	Client      func(screenID int) ScreenClient
}

func (w *Watcher) Run(ctx context.Context, screenIDs []int) error {
	interval := w.Interval
	if interval <= 0 {
		interval = defaultInterval
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		w.tick(ctx, screenIDs)
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
	}
}

func (w *Watcher) tick(ctx context.Context, screenIDs []int) {
	if !w.AutoRestart {
		return
	}
	for _, screenID := range screenIDs {
		if w.OnAir != nil && !w.OnAir(screenID) {
			continue
		}
		client := w.Client(screenID)
		if client == nil {
			continue
		}
		st, err := client.Status(ctx)
		if err != nil || st.Playing {
			continue
		}
		_ = client.RestartStream(ctx)
	}
}
