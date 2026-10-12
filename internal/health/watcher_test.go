package health

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"mediateca-station/internal/hub"
)

type fakeClient struct {
	restarts atomic.Int32
}

func (f *fakeClient) Status(context.Context) (TvStatus, error) {
	return TvStatus{Playing: false}, nil
}

func (f *fakeClient) RestartStream(context.Context) error {
	f.restarts.Add(1)
	return nil
}

func TestWatcherSkipsRecentlyStarted(t *testing.T) {
	client := &fakeClient{}
	w := &Watcher{
		AutoRestart: true,
		OnAir:       func(int) bool { return true },
		StartedRecently: func(int) bool {
			return true
		},
		Client: func(int) ScreenClient { return client },
	}
	w.tick(context.Background(), []int{7})
	if client.restarts.Load() != 0 {
		t.Fatalf("restarts=%d want 0", client.restarts.Load())
	}
}

func TestWatcherRestartsWhenNotRecent(t *testing.T) {
	client := &fakeClient{}
	w := &Watcher{
		AutoRestart:     true,
		OnAir:           func(int) bool { return true },
		StartedRecently: func(int) bool { return false },
		Client:          func(int) ScreenClient { return client },
	}
	w.tick(context.Background(), []int{7})
	if client.restarts.Load() != 1 {
		t.Fatalf("restarts=%d want 1", client.restarts.Load())
	}
}

func TestTVMonitorSettingsDefaults(t *testing.T) {
	h, auto, telem := TVMonitorSettings(nil)
	if h != DefaultHealthInterval || !auto || telem != DefaultTelemetryInterval {
		t.Fatalf("h=%v auto=%v telem=%v", h, auto, telem)
	}
}

func TestTVMonitorSettingsOverrideAndClamp(t *testing.T) {
	h, auto, telem := TVMonitorSettings(&hub.TVMonitor{
		HealthIntervalSeconds:     2,
		AutoRestartWhenNotPlaying: false,
		TelemetryIntervalSeconds:  90,
	})
	if h != 5*time.Second || auto || telem != 90*time.Second {
		t.Fatalf("h=%v auto=%v telem=%v", h, auto, telem)
	}
	h, auto, telem = TVMonitorSettings(&hub.TVMonitor{
		HealthIntervalSeconds:     45,
		AutoRestartWhenNotPlaying: true,
		TelemetryIntervalSeconds:  90,
	})
	if h != 45*time.Second || !auto || telem != 90*time.Second {
		t.Fatalf("h=%v auto=%v telem=%v", h, auto, telem)
	}
}

func TestApplyTVMonitorDisablesRestart(t *testing.T) {
	client := &fakeClient{}
	w := &Watcher{
		OnAir:           func(int) bool { return true },
		StartedRecently: func(int) bool { return false },
		Client:          func(int) ScreenClient { return client },
	}
	w.ApplyTVMonitor(&hub.TVMonitor{AutoRestartWhenNotPlaying: false})
	w.tick(context.Background(), []int{7})
	if client.restarts.Load() != 0 {
		t.Fatalf("restarts=%d want 0", client.restarts.Load())
	}
}

func TestStartedRecentlyGrace(t *testing.T) {
	now := time.Now()
	recent := map[int]time.Time{7: now.Add(-5 * time.Second)}
	check := func(id int) bool {
		at, ok := recent[id]
		return ok && now.Sub(at) < 10*time.Second
	}
	if !check(7) {
		t.Fatal("expected grace")
	}
	recent[7] = now.Add(-11 * time.Second)
	if check(7) {
		t.Fatal("expected outside grace")
	}
}
