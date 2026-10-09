package voxel

import (
	"context"
	"os"
	"path/filepath"
	"sync"
	"testing"
)

type fakeRunner struct {
	mu   sync.Mutex
	argv [][]string
}

func (f *fakeRunner) Start(ctx context.Context, argv ...string) (Process, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	cp := append([]string(nil), argv...)
	f.argv = append(f.argv, cp)
	return &fakeProc{alive: true}, nil
}

type fakeProc struct{ alive bool }

func (p *fakeProc) Pid() int      { return 42 }
func (p *fakeProc) Wait() error   { return nil }
func (p *fakeProc) Kill() error   { p.alive = false; return nil }
func (p *fakeProc) Running() bool { return p.alive }

func TestManagerStartsBroadcast(t *testing.T) {
	dir := t.TempDir()
	fifo := filepath.Join(dir, "stream_8091.ts")
	runner := &fakeRunner{}
	m := &Manager{
		Bin:     "/home/stream/bin/broadcast",
		FifoDir: dir,
		DataDir: dir,
		Runner:  runner,
	}
	ctx := context.Background()
	if err := m.Ensure(ctx, 8091); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(fifo); err != nil {
		t.Fatal(err)
	}
	if len(runner.argv) != 1 {
		t.Fatalf("starts=%d", len(runner.argv))
	}
	if runner.argv[0][0] != "/home/stream/bin/broadcast" {
		t.Fatalf("%v", runner.argv[0])
	}
	if runner.argv[0][1] != fifo || runner.argv[0][2] != ":8091" {
		t.Fatalf("%v", runner.argv[0])
	}
}
