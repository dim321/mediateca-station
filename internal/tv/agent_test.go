package tv

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestAgentStartStream(t *testing.T) {
	var gotURL string
	var gotBuf string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/start_stream" || r.Method != http.MethodPost {
			t.Fatalf("%s %s", r.Method, r.URL.Path)
		}
		gotURL = r.URL.Query().Get("url")
		gotBuf = r.URL.Query().Get("buffer_ms")
		_ = json.NewEncoder(w).Encode(map[string]any{"ok": true, "url": gotURL})
	}))
	defer srv.Close()
	a := &Agent{BaseURL: srv.URL, HTTP: srv.Client()}
	if err := a.StartStream(context.Background(), "http://192.168.33.1:8091/", 300); err != nil {
		t.Fatal(err)
	}
	if gotURL != "http://192.168.33.1:8091/" || gotBuf != "300" {
		t.Fatalf("url=%q buffer_ms=%q", gotURL, gotBuf)
	}
}

func TestAgentStatus(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"playing": true, "url": "http://x/", "buffer_ms": 300})
	}))
	defer srv.Close()
	a := &Agent{BaseURL: srv.URL, HTTP: srv.Client()}
	st, err := a.Status(context.Background())
	if err != nil || !st.Playing {
		t.Fatalf("%v %#v", err, st)
	}
}
