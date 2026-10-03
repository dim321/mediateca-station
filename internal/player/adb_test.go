package player

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"
)

type fakeRun struct {
	mu    sync.Mutex
	args  [][]string
	fail  string
	wait  chan struct{}
	onRun func()
}

func (f *fakeRun) Run(ctx context.Context, argv ...string) error {
	if f.onRun != nil {
		f.onRun()
	}
	f.mu.Lock()
	cp := append([]string{}, argv...)
	f.args = append(f.args, cp)
	fail := len(argv) > 1 && argv[1] == f.fail
	f.mu.Unlock()
	if f.wait != nil {
		select {
		case <-f.wait:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	if fail {
		return errors.New("fail")
	}
	return nil
}

func TestStartConnectsThenLaunchesVLC(t *testing.T) {
	run := &fakeRun{}
	p := &Player{Runner: run}
	err := p.Start(context.Background(), "192.168.1.21:5555", "http://192.168.1.10:8080/m/secret/19", "video/mp2t")
	if err != nil {
		t.Fatal(err)
	}
	if len(run.args) != 2 {
		t.Fatalf("%v", run.args)
	}
	if run.args[0][0] != "adb" || run.args[0][1] != "connect" || run.args[0][2] != "192.168.1.21:5555" {
		t.Fatalf("connect %v", run.args[0])
	}
	want := []string{"adb", "-s", "192.168.1.21:5555", "shell", "am", "start", "-a", "android.intent.action.VIEW", "-d", "http://192.168.1.10:8080/m/secret/19", "-t", "video/mp2t", "-p", "org.videolan.vlc"}
	if stringsJoin(run.args[1]) != stringsJoin(want) {
		t.Fatalf("start %v", run.args[1])
	}
}

func TestStartStopsWhenConnectFails(t *testing.T) {
	run := &fakeRun{fail: "connect"}
	p := &Player{Runner: run}
	if err := p.Start(context.Background(), "192.168.1.21:5555", "http://host/m/s/1", "video/mp2t"); err == nil {
		t.Fatal("expected error")
	}
	if len(run.args) != 1 {
		t.Fatalf("launched VLC after connect failure: %v", run.args)
	}
}

func TestStopForceStopsPackage(t *testing.T) {
	run := &fakeRun{}
	p := &Player{Runner: run}
	if err := p.Stop(context.Background(), "192.168.1.21:5555"); err != nil {
		t.Fatal(err)
	}
	want := []string{"adb", "-s", "192.168.1.21:5555", "shell", "am", "force-stop", "org.videolan.vlc"}
	if stringsJoin(run.args[0]) != stringsJoin(want) {
		t.Fatalf("%v", run.args[0])
	}
}

func TestSerialDoesNotOverlap(t *testing.T) {
	entered := make(chan struct{})
	release := make(chan struct{})
	var once sync.Once
	run := &fakeRun{
		wait: release,
		onRun: func() {
			once.Do(func() { close(entered) })
		},
	}
	p := &Player{Runner: run}
	go func() {
		_ = p.Start(context.Background(), "192.168.1.21:5555", "http://host/a", "video/mp2t")
	}()
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("start did not reach adb")
	}
	done := make(chan struct{})
	go func() {
		_ = p.Stop(context.Background(), "192.168.1.21:5555")
		close(done)
	}()
	select {
	case <-done:
		t.Fatal("stop overlapped start")
	case <-time.After(40 * time.Millisecond):
	}
	close(release)
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("stop did not finish")
	}
}

func stringsJoin(parts []string) string {
	out := ""
	for i, part := range parts {
		if i > 0 {
			out += "\x00"
		}
		out += part
	}
	return out
}
