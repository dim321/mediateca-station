package voxel

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"sync"
	"syscall"
	"testing"
	"time"
)

type fakeRunner struct {
	mu   sync.Mutex
	argv [][]string
	last *fakeProc
}

func (f *fakeRunner) Start(ctx context.Context, argv ...string) (Process, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	cp := append([]string(nil), argv...)
	f.argv = append(f.argv, cp)
	p := &fakeProc{alive: true}
	f.last = p
	return p, nil
}

func (f *fakeRunner) lastProc() *fakeProc {
	if len(f.argv) == 0 {
		return nil
	}
	return f.last
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

func TestManagerRestartsDeadBroadcast(t *testing.T) {
	dir := t.TempDir()
	runner := &fakeRunner{}
	m := &Manager{Bin: "/bin/broadcast", FifoDir: dir, DataDir: dir, Runner: runner}
	ctx := context.Background()
	if err := m.Ensure(ctx, 8091); err != nil {
		t.Fatal(err)
	}
	runner.mu.Lock()
	p := runner.lastProc()
	runner.mu.Unlock()
	p.alive = false
	if err := m.Ensure(ctx, 8091); err != nil {
		t.Fatal(err)
	}
	runner.mu.Lock()
	n := len(runner.argv)
	runner.mu.Unlock()
	if n != 2 {
		t.Fatalf("starts=%d want 2", n)
	}
}

func TestOsProcessRunningDetectsExit(t *testing.T) {
	cmd := exec.Command("true")
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	p := &osProcess{cmd: cmd, done: make(chan struct{})}
	go func() {
		_ = cmd.Wait()
		close(p.done)
	}()
	deadline := time.Now().Add(2 * time.Second)
	for p.Running() && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if p.Running() {
		t.Fatal("process still running after exit")
	}
}

func TestOpenFifoWithoutReaderUsesHold(t *testing.T) {
	dir := t.TempDir()
	m := &Manager{FifoDir: dir}
	port := 8092
	if err := ensureFifo(m.FifoPath(port)); err != nil {
		t.Fatal(err)
	}
	f, err := m.OpenFifo(port)
	if err != nil {
		t.Fatal(err)
	}
	_ = f.Close()
}

func TestManagerBuiltinStartsHTTP(t *testing.T) {
	dir := t.TempDir()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := ln.Addr().(*net.TCPAddr).Port
	_ = ln.Close()

	m := &Manager{
		FifoDir: dir,
		DataDir: dir,
		Bind:    "127.0.0.1",
		Builtin: true,
	}
	ctx := context.Background()
	if err := m.Ensure(ctx, port); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = m.Stop(port) }()

	req, err := http.NewRequest(http.MethodGet, "http://127.0.0.1:"+strconv.Itoa(port)+"/", nil)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.Header.Get("Content-Type") != "video/mp2t" {
		t.Fatalf("content-type=%s", resp.Header.Get("Content-Type"))
	}

	payload := []byte{0x47, 0x1f, 0xff, 0x10}
	w, err := m.OpenFifo(port)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.Write(payload); err != nil {
		t.Fatal(err)
	}
	_ = w.Close()

	buf := make([]byte, len(payload))
	done := make(chan error, 1)
	go func() {
		_, err := io.ReadFull(resp.Body, buf)
		done <- err
	}()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("timeout reading stream")
	}
	if string(buf) != string(payload) {
		t.Fatalf("got %v", buf)
	}

	// Ensure is idempotent while running.
	if err := m.Ensure(ctx, port); err != nil {
		t.Fatal(err)
	}
}

func TestOpenFifoWithReaderSucceeds(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "stream_8093.ts")
	if err := ensureFifo(path); err != nil {
		t.Fatal(err)
	}
	opened := make(chan *os.File, 1)
	go func() {
		f, err := openFifoWriter(path, 2*time.Second)
		opened <- f
		if err != nil {
			t.Error(err)
		}
	}()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		r, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NONBLOCK, 0)
		if err == nil {
			f := <-opened
			if f == nil {
				t.Fatal("writer failed")
			}
			_ = f.Close()
			_ = r.Close()
			return
		}
		if errors.Is(err, syscall.ENXIO) {
			time.Sleep(5 * time.Millisecond)
			continue
		}
		t.Fatal(err)
	}
	t.Fatal("timed out waiting for fifo pair")
}
