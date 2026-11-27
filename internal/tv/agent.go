package tv

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
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

func (a *Agent) StartStream(ctx context.Context, streamURL string, bufferMs int) error {
	// Voxel APK reads only the request line / query; JSON body is ignored.
	q := url.Values{}
	q.Set("url", streamURL)
	q.Set("buffer_ms", strconv.Itoa(bufferMs))
	return a.json(ctx, http.MethodPost, "/api/start_stream?"+q.Encode(), nil, nil)
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
	bodyBytes, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
	if resp.StatusCode >= 300 {
		msg := strings.TrimSpace(string(bodyBytes))
		if msg == "" {
			return fmt.Errorf("tv agent %s: %s", path, resp.Status)
		}
		return fmt.Errorf("tv agent %s: %s: %s", path, resp.Status, msg)
	}
	if out != nil {
		return json.Unmarshal(bodyBytes, out)
	}
	return nil
}
