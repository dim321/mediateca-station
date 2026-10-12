package tv

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"
)

type Status struct {
	Playing  bool   `json:"playing"`
	URL      string `json:"url"`
	BufferMs int    `json:"buffer_ms"`
}

type Agent struct {
	BaseURL string // e.g. http://192.168.33.182:8989
	HTTP    *http.Client
}

func NewAgent(tvHost string) *Agent {
	host := tvHost
	if i := strings.IndexByte(tvHost, ':'); i >= 0 {
		// adb_serial is host:5555 — agent is host:8989
		host = tvHost[:i]
	}
	return &Agent{
		BaseURL: fmt.Sprintf("http://%s:8989", host),
		HTTP:    &http.Client{Timeout: 5 * time.Second},
	}
}

func (a *Agent) Status(ctx context.Context) (Status, error) {
	var st Status
	err := a.json(ctx, http.MethodGet, "/api/status", nil, &st)
	return st, err
}

func (a *Agent) StartStream(ctx context.Context, url string, bufferMs int) error {
	body := map[string]any{"url": url, "buffer_ms": bufferMs}
	return a.json(ctx, http.MethodPost, "/api/start_stream", body, nil)
}

func (a *Agent) RestartStream(ctx context.Context) error {
	return a.json(ctx, http.MethodPost, "/api/restart_stream", nil, nil)
}

func (a *Agent) Stop(ctx context.Context) error {
	return a.json(ctx, http.MethodPost, "/api/stop", nil, nil)
}

func (a *Agent) json(ctx context.Context, method, path string, body any, out any) error {
	var rdr *bytes.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return err
		}
		rdr = bytes.NewReader(b)
	} else {
		rdr = bytes.NewReader(nil)
	}
	req, err := http.NewRequestWithContext(ctx, method, strings.TrimRight(a.BaseURL, "/")+path, rdr)
	if err != nil {
		return err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	client := a.HTTP
	if client == nil {
		client = http.DefaultClient
	}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		return fmt.Errorf("tv agent %s: %s", path, resp.Status)
	}
	if out != nil {
		return json.NewDecoder(resp.Body).Decode(out)
	}
	return nil
}
