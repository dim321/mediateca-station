package cache

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestResolveJoinsRelativeURL(t *testing.T) {
	got, err := Resolve("https://hub.example", "/rails/active_storage/blobs/redirect/x")
	if err != nil || got != "https://hub.example/rails/active_storage/blobs/redirect/x" {
		t.Fatalf("%s %v", got, err)
	}
}

func TestDownloadFollowsRedirectWithoutBearer(t *testing.T) {
	file := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "" {
			t.Errorf("bearer leaked: %q", r.Header.Get("Authorization"))
		}
		w.Write([]byte("ts-bytes"))
	}))
	defer file.Close()
	hub := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, file.URL, http.StatusFound)
	}))
	defer hub.Close()
	dir := t.TempDir()
	c := New(dir)
	item := Item{ID: 19, URL: "/rails/active_storage/blobs/redirect/x", Mime: "video/mp2t"}
	if err := c.Reconcile(context.Background(), hub.URL, []Item{item}); err != nil {
		t.Fatal(err)
	}
	if !c.Ready(19, item.URL) {
		t.Fatal("not ready")
	}
	body, err := os.ReadFile(filepath.Join(dir, "19"))
	if err != nil || string(body) != "ts-bytes" {
		t.Fatalf("body %q err %v", body, err)
	}
	if _, err := os.Stat(filepath.Join(dir, "19.partial")); !os.IsNotExist(err) {
		t.Fatal("partial remained")
	}
}

func TestPartialIsNotReady(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "19.partial"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if New(dir).Ready(19, "/clip") {
		t.Fatal("partial was ready")
	}
}

func TestChangedURLDownloadsAgain(t *testing.T) {
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		w.Write([]byte(r.URL.Path))
	}))
	defer srv.Close()
	dir := t.TempDir()
	c := New(dir)
	first := Item{ID: 19, URL: "/a.ts", Mime: "video/mp2t"}
	second := Item{ID: 19, URL: "/b.ts", Mime: "video/mp2t"}
	if err := c.Reconcile(context.Background(), srv.URL, []Item{first}); err != nil {
		t.Fatal(err)
	}
	if err := c.Reconcile(context.Background(), srv.URL, []Item{second}); err != nil {
		t.Fatal(err)
	}
	if !c.Ready(19, "/b.ts") || c.Ready(19, "/a.ts") {
		t.Fatal("url sidecar")
	}
	if hits.Load() != 2 {
		t.Fatalf("hits %d", hits.Load())
	}
}

func TestDeleteUnreferencedEvenWhenAnotherDownloadFails(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/bad" {
			w.WriteHeader(http.StatusBadGateway)
			return
		}
		w.Write([]byte("ok"))
	}))
	defer srv.Close()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "19"), []byte("old"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "19.url"), []byte("/old"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "20"), []byte("keep"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "20.url"), []byte("/previous"), 0o600); err != nil {
		t.Fatal(err)
	}
	err := New(dir).Reconcile(context.Background(), srv.URL, []Item{{ID: 20, URL: "/bad", Mime: "video/mp2t"}})
	if err == nil {
		t.Fatal("expected download error")
	}
	if _, statErr := os.Stat(filepath.Join(dir, "19")); !os.IsNotExist(statErr) {
		t.Fatal("unreferenced file kept")
	}
	if _, statErr := os.Stat(filepath.Join(dir, "20")); statErr != nil {
		t.Fatal("wanted file removed")
	}
}

func TestAtMostTwoDownloads(t *testing.T) {
	var current atomic.Int32
	var max atomic.Int32
	var started atomic.Int32
	release := make(chan struct{})
	var entered sync.WaitGroup
	entered.Add(2)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := current.Add(1)
		arrival := started.Add(1)
		for {
			old := max.Load()
			if n <= old || max.CompareAndSwap(old, n) {
				break
			}
		}
		if arrival <= 2 {
			entered.Done()
		}
		<-release
		current.Add(-1)
		w.Write([]byte("x"))
	}))
	defer srv.Close()
	c := New(t.TempDir())
	done := make(chan struct{})
	go func() {
		_ = c.Reconcile(context.Background(), srv.URL, []Item{
			{ID: 1, URL: "/f", Mime: "video/mp2t"},
			{ID: 2, URL: "/f", Mime: "video/mp2t"},
			{ID: 3, URL: "/f", Mime: "video/mp2t"},
		})
		close(done)
	}()
	select {
	case <-done:
		t.Fatal("reconcile finished before the third download was released")
	case <-waitEntered(&entered):
	case <-time.After(2 * time.Second):
		t.Fatal("two downloads did not start")
	}
	time.Sleep(50 * time.Millisecond)
	if max.Load() != 2 {
		t.Fatalf("max %d", max.Load())
	}
	close(release)
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("reconcile did not finish")
	}
}

func waitEntered(wg *sync.WaitGroup) <-chan struct{} {
	ch := make(chan struct{})
	go func() {
		wg.Wait()
		close(ch)
	}()
	return ch
}
