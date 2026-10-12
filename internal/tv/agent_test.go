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
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/start_stream" || r.Method != http.MethodPost {
			t.Fatalf("%s %s", r.Method, r.URL.Path)
		}
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		gotURL, _ = body["url"].(string)
		_ = json.NewEncoder(w).Encode(map[string]any{"ok": true, "url": gotURL})
	}))
	defer srv.Close()
	a := &Agent{BaseURL: srv.URL, HTTP: srv.Client()}
	if err := a.StartStream(context.Background(), "http://192.168.33.1:8091/", 300); err != nil {
		t.Fatal(err)
	}
	if gotURL != "http://192.168.33.1:8091/" {
		t.Fatalf("%q", gotURL)
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
