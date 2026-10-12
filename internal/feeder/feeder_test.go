package feeder

import (
	"bytes"
	"context"
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestFeedFileRealtimeWritesAllBytes(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "clip.ts")
	payload := bytes.Repeat([]byte{0x47}, 18800*2)
	if err := os.WriteFile(src, payload, 0o600); err != nil {
		t.Fatal(err)
	}
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	done := make(chan []byte, 1)
	go func() {
		b, _ := io.ReadAll(r)
		done <- b
	}()
	f := &Feeder{ChunkSize: 18800, Pace: PaceMax}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := f.FeedFile(ctx, src, w); err != nil {
		t.Fatal(err)
	}
	_ = w.Close()
	got := <-done
	if !bytes.Equal(got, payload) {
		t.Fatalf("len got=%d want=%d", len(got), len(payload))
	}
}

func TestWriteNullPackets(t *testing.T) {
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	buf := make([]byte, 188*100)
	done := make(chan int, 1)
	go func() {
		n, _ := io.ReadFull(r, buf)
		done <- n
	}()
	f := &Feeder{ChunkSize: 18800}
	if err := f.WriteNullPackets(w, 100); err != nil {
		t.Fatal(err)
	}
	_ = w.Close()
	n := <-done
	if n != 188*100 {
		t.Fatalf("n=%d", n)
	}
	if buf[0] != 0x47 || buf[1] != 0x1f || buf[2] != 0xff {
		t.Fatalf("not a null packet header: %x %x %x", buf[0], buf[1], buf[2])
	}
}
