package hub

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestFetchConfig(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/agent/v1/config" {
			t.Errorf("path %s", r.URL.Path)
		}
		if r.Header.Get("Authorization") != "Bearer tok" {
			t.Errorf("auth %q", r.Header.Get("Authorization"))
		}
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"station_id":3,"offline_cache_hours":24,"screens":[{"id":7,"name":"Entrance","orientation":"landscape"}]}`)
	}))
	defer srv.Close()
	cfg, err := New(srv.URL, "tok").FetchConfig(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if cfg.StationID != 3 || cfg.OfflineCacheHours != 24 || cfg.Screens[0].Orientation != "landscape" {
		t.Fatalf("%+v", cfg)
	}
}

func TestFetchPackageStoresHeaderETagAnd304(t *testing.T) {
	var sawNoneMatch string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/agent/v2/package" {
			t.Errorf("path %s", r.URL.Path)
		}
		sawNoneMatch = r.Header.Get("If-None-Match")
		if sawNoneMatch == `"abc"` {
			w.WriteHeader(http.StatusNotModified)
			return
		}
		w.Header().Set("ETag", `"abc"`)
		io.WriteString(w, `{"version":"abc","etag":"abc","generated_at":"2026-09-02T12:00:00Z","valid_until":"2026-09-03T12:00:00Z","entries":[{"position":1,"starts_at":"2026-09-02T02:00:00Z","duration_seconds":10,"screen_ids":[7],"media":{"id":19,"url":"/rails/active_storage/blobs/redirect/x","mime_type":"video/mp2t"}}],"screen_map":{"7":[0]}}`)
	}))
	defer srv.Close()
	client := New(srv.URL, "tok")
	pkg, notMod, err := client.FetchPackage(context.Background(), "")
	if err != nil || notMod {
		t.Fatalf("notMod=%v err=%v", notMod, err)
	}
	if pkg.HeaderETag != `"abc"` || pkg.Entries[0].Media.ID != 19 || pkg.Entries[0].Media.MimeType != "video/mp2t" {
		t.Fatalf("%+v", pkg)
	}
	_, notMod, err = client.FetchPackage(context.Background(), pkg.HeaderETag)
	if err != nil || !notMod {
		t.Fatalf("second notMod=%v err=%v", notMod, err)
	}
	if sawNoneMatch != `"abc"` {
		t.Fatalf("If-None-Match %q", sawNoneMatch)
	}
}

func TestUnauthorized(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		io.WriteString(w, `{"error":"unauthorized"}`)
	}))
	defer srv.Close()
	_, err := New(srv.URL, "tok").FetchConfig(context.Background())
	if !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("err=%v", err)
	}
}

func TestPostPlayEvent(t *testing.T) {
	var body string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/agent/v1/play_events" || r.Method != http.MethodPost {
			t.Errorf("request %s %s", r.Method, r.URL.Path)
		}
		raw, _ := io.ReadAll(r.Body)
		body = string(raw)
		w.WriteHeader(http.StatusCreated)
		io.WriteString(w, `{"play_log_ids":[101]}`)
	}))
	defer srv.Close()
	started := time.Date(2026, 9, 2, 2, 0, 1, 0, time.UTC)
	status, resp, err := New(srv.URL, "tok").PostPlayEvent(context.Background(), 7, 19, started)
	if err != nil || status != 201 {
		t.Fatalf("status=%d err=%v", status, err)
	}
	if !strings.Contains(body, `"screen_id":7`) || !strings.Contains(body, `"media_asset_id":19`) || !strings.Contains(body, `"started_at":"2026-09-02T02:00:01Z"`) {
		t.Fatalf("body %s", body)
	}
	if !strings.Contains(resp, "101") {
		t.Fatalf("resp %s", resp)
	}
}

func TestPostPlayEventTransportError(t *testing.T) {
	client := New("http://127.0.0.1:1", "tok")
	status, _, err := client.PostPlayEvent(context.Background(), 7, 19, time.Now().UTC())
	if err == nil || status != 0 {
		t.Fatalf("status=%d err=%v", status, err)
	}
}

func TestFetchConfigTVMonitor(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, `{"station_id":3,"offline_cache_hours":24,"screens":[],"tv_monitor":{"health_interval_seconds":45,"auto_restart_when_not_playing":false,"telemetry_interval_seconds":90}}`)
	}))
	defer srv.Close()
	cfg, err := New(srv.URL, "tok").FetchConfig(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if cfg.TVMonitor == nil || cfg.TVMonitor.HealthIntervalSeconds != 45 ||
		cfg.TVMonitor.AutoRestartWhenNotPlaying || cfg.TVMonitor.TelemetryIntervalSeconds != 90 {
		t.Fatalf("%+v", cfg.TVMonitor)
	}
}

func TestPostTelemetry(t *testing.T) {
	var got Telemetry
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/agent/v1/telemetry" {
			t.Fatalf("%s", r.URL.Path)
		}
		_ = json.NewDecoder(r.Body).Decode(&got)
		w.WriteHeader(http.StatusAccepted)
	}))
	defer srv.Close()
	c := New(srv.URL, "tok")
	c.HTTP = srv.Client()
	err := c.PostTelemetry(context.Background(), Telemetry{
		ReportedAt: time.Date(2026, 10, 9, 11, 0, 0, 0, time.UTC),
		Station:    StationTelemetry{UptimeSeconds: 10, BroadcastOK: true},
		Screens:    []ScreenTelemetry{{ScreenID: 7, Playing: true}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if got.Screens[0].ScreenID != 7 {
		t.Fatalf("%+v", got)
	}
}

func TestPostTelemetry204(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()
	c := New(srv.URL, "tok")
	c.HTTP = srv.Client()
	if err := c.PostTelemetry(context.Background(), Telemetry{}); err != nil {
		t.Fatal(err)
	}
}

func TestFetchCommands(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/agent/v1/commands" {
			t.Fatal(r.URL.Path)
		}
		_ = json.NewEncoder(w).Encode(CommandsResponse{Commands: []Command{{
			ID: "cmd_1", ScreenID: 7, Type: "restart_stream", IssuedAt: "2026-10-09T11:01:00Z",
		}}})
	}))
	defer srv.Close()
	c := New(srv.URL, "tok")
	c.HTTP = srv.Client()
	list, err := c.FetchCommands(context.Background(), "")
	if err != nil || len(list.Commands) != 1 || list.Commands[0].Type != "restart_stream" {
		t.Fatalf("%v %+v", err, list)
	}
}

func TestPostCommandResults(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/agent/v1/command_results" {
			t.Fatal(r.URL.Path)
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()
	c := New(srv.URL, "tok")
	c.HTTP = srv.Client()
	if err := c.PostCommandResults(context.Background(), []CommandResult{{
		CommandID: "cmd_1", OK: true, FinishedAt: time.Now().UTC(),
	}}); err != nil {
		t.Fatal(err)
	}
}

func TestUploadArtifact(t *testing.T) {
	var method, ct string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		method = r.Method
		ct = r.Header.Get("Content-Type")
		io.Copy(io.Discard, r.Body)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()
	c := New(srv.URL, "tok")
	c.HTTP = srv.Client()
	if err := c.UploadArtifact(context.Background(), srv.URL+"/upload", strings.NewReader("png-bytes"), "image/png"); err != nil {
		t.Fatal(err)
	}
	if method != http.MethodPut || ct != "image/png" {
		t.Fatalf("method=%s ct=%s", method, ct)
	}
}
