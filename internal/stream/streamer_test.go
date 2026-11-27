package stream

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"
)

func freePort(t *testing.T) int {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	return ln.Addr().(*net.TCPAddr).Port
}

func TestPortServesFIFOBytes(t *testing.T) {
	dir := t.TempDir()
	port := freePort(t)
	fifo := filepath.Join(dir, "stream.ts")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	p, err := Start(ctx, "127.0.0.1", port, fifo)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = p.Stop() }()

	payload := make([]byte, 188*3)
	for i := range payload {
		payload[i] = byte(i % 251)
	}

	url := "http://127.0.0.1:" + strconv.Itoa(port) + "/"
	got := make(chan []byte, 1)
	errCh := make(chan error, 1)
	go func() {
		resp, err := http.Get(url)
		if resp != nil {
			defer resp.Body.Close()
		}
		if err != nil {
			errCh <- err
			return
		}
		if ct := resp.Header.Get("Content-Type"); ct != "video/mp2t" {
			errCh <- fmt.Errorf("content-type=%s", ct)
			return
		}
		buf := make([]byte, len(payload))
		if _, err := io.ReadFull(resp.Body, buf); err != nil {
			errCh <- err
			return
		}
		got <- buf
	}()

	deadline := time.Now().Add(2 * time.Second)
	for p.ClientCount() == 0 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if p.ClientCount() == 0 {
		t.Fatal("no HTTP client connected")
	}

	w, err := os.OpenFile(fifo, os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.Write(payload); err != nil {
		t.Fatal(err)
	}
	_ = w.Close()

	select {
	case err := <-errCh:
		t.Fatal(err)
	case buf := <-got:
		if string(buf) != string(payload) {
			t.Fatalf("payload mismatch len=%d", len(buf))
		}
	case <-time.After(3 * time.Second):
		t.Fatal("timeout waiting for stream body")
	}
}

func TestPortFanOutAndDropSlowClient(t *testing.T) {
	dir := t.TempDir()
	port := freePort(t)
	fifo := filepath.Join(dir, "stream.ts")
	ctx := context.Background()
	p, err := Start(ctx, "127.0.0.1", port, fifo)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = p.Stop() }()

	url := "http://127.0.0.1:" + strconv.Itoa(port) + "/"

	slowDone := make(chan struct{})
	go func() {
		defer close(slowDone)
		resp, err := http.Get(url)
		if resp != nil {
			defer resp.Body.Close()
		}
		if err != nil {
			return
		}
		time.Sleep(500 * time.Millisecond)
	}()

	fastGot := make(chan []byte, 1)
	go func() {
		resp, err := http.Get(url)
		if resp != nil {
			defer resp.Body.Close()
		}
		if err != nil {
			return
		}
		buf := make([]byte, 188)
		n, _ := io.ReadFull(resp.Body, buf)
		fastGot <- buf[:n]
	}()

	deadline := time.Now().Add(2 * time.Second)
	for p.ClientCount() < 2 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}

	w, err := os.OpenFile(fifo, os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	chunk := make([]byte, 188)
	for i := 0; i < clientBufPackets+64; i++ {
		chunk[0] = byte(i)
		if _, err := w.Write(chunk); err != nil {
			t.Fatal(err)
		}
	}
	_ = w.Close()

	select {
	case buf := <-fastGot:
		if len(buf) != 188 {
			t.Fatalf("fast client got %d bytes", len(buf))
		}
	case <-time.After(3 * time.Second):
		t.Fatal("fast client blocked by slow peer")
	}

	w2, err := os.OpenFile(fifo, os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = w2.Write(chunk)
	_ = w2.Close()

	<-slowDone
	if !p.Running() {
		t.Fatal("listener should stay alive")
	}
}

func TestPortStopClosesListener(t *testing.T) {
	dir := t.TempDir()
	port := freePort(t)
	fifo := filepath.Join(dir, "stream.ts")
	p, err := Start(context.Background(), "127.0.0.1", port, fifo)
	if err != nil {
		t.Fatal(err)
	}
	if err := p.Stop(); err != nil {
		t.Fatal(err)
	}
	if p.Running() {
		t.Fatal("expected not running")
	}
	_, err = net.DialTimeout("tcp", "127.0.0.1:"+strconv.Itoa(port), 200*time.Millisecond)
	if err == nil {
		t.Fatal("listener still accepting")
	}
}

func TestEnsureIdempotentFifo(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "a.ts")
	if err := ensureFifo(path); err != nil {
		t.Fatal(err)
	}
	if err := ensureFifo(path); err != nil {
		t.Fatal(err)
	}
}
