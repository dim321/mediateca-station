package health

import (
	"context"
	"sync"
	"time"

	"mediateca-station/internal/hub"
)

const (
	DefaultHealthInterval    = 30 * time.Second
	DefaultTelemetryInterval = 60 * time.Second
	minHealthInterval        = 5 * time.Second
	defaultInterval          = DefaultHealthInterval
)

// TVMonitorSettings resolves health, auto-restart, and telemetry intervals from Hub tv_monitor.
func TVMonitorSettings(tm *hub.TVMonitor) (healthInterval time.Duration, autoRestart bool, telemetryInterval time.Duration) {
	healthInterval = DefaultHealthInterval
	autoRestart = true
	telemetryInterval = DefaultTelemetryInterval
	if tm == nil {
		return healthInterval, autoRestart, telemetryInterval
	}
	if tm.HealthIntervalSeconds > 0 {
		healthInterval = time.Duration(tm.HealthIntervalSeconds) * time.Second
	}
	if healthInterval < minHealthInterval {
		healthInterval = minHealthInterval
	}
	autoRestart = tm.AutoRestartWhenNotPlaying
	if tm.TelemetryIntervalSeconds > 0 {
		telemetryInterval = time.Duration(tm.TelemetryIntervalSeconds) * time.Second
	}
	return healthInterval, autoRestart, telemetryInterval
}

type TvStatus struct {
	Playing bool
}

type ScreenClient interface {
	Status(ctx context.Context) (TvStatus, error)
	RestartStream(ctx context.Context) error
}

type Watcher struct {
	Interval        time.Duration
	AutoRestart     bool
	OnAir           func(screenID int) bool
	StartedRecently func(screenID int) bool
	Client          func(screenID int) ScreenClient

	mu       sync.Mutex
	reconfig chan struct{}
}

func NewWatcher() *Watcher {
	return &Watcher{reconfig: make(chan struct{}, 1)}
}

func (w *Watcher) ApplyTVMonitor(tm *hub.TVMonitor) {
	interval, auto, _ := TVMonitorSettings(tm)
	w.mu.Lock()
	changed := w.Interval != interval || w.AutoRestart != auto
	w.Interval = interval
	w.AutoRestart = auto
	reconfig := w.reconfig
	w.mu.Unlock()
	if changed && reconfig != nil {
		select {
		case reconfig <- struct{}{}:
		default:
		}
	}
}

func (w *Watcher) Run(ctx context.Context, screenIDs []int) error {
	if w.reconfig == nil {
		w.reconfig = make(chan struct{}, 1)
	}
	for {
		if err := w.runCycle(ctx, screenIDs); err != nil {
			return err
		}
	}
}

func (w *Watcher) runCycle(ctx context.Context, screenIDs []int) error {
	w.mu.Lock()
	interval := w.Interval
	if interval <= 0 {
		interval = defaultInterval
	}
	reconfig := w.reconfig
	w.mu.Unlock()
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		w.tick(ctx, screenIDs)
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		case <-reconfig:
			return nil
		}
	}
}

func (w *Watcher) tick(ctx context.Context, screenIDs []int) {
	w.mu.Lock()
	auto := w.AutoRestart
	w.mu.Unlock()
	if !auto {
		return
	}
	for _, screenID := range screenIDs {
		if w.OnAir != nil && !w.OnAir(screenID) {
			continue
		}
		if w.StartedRecently != nil && w.StartedRecently(screenID) {
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
