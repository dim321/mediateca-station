package hub

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

var ErrUnauthorized = errors.New("unauthorized")

type Screen struct {
	ID          int    `json:"id"`
	Name        string `json:"name"`
	Orientation string `json:"orientation"`
}

type TVMonitor struct {
	HealthIntervalSeconds     int  `json:"health_interval_seconds"`
	AutoRestartWhenNotPlaying bool `json:"auto_restart_when_not_playing"`
	TelemetryIntervalSeconds  int  `json:"telemetry_interval_seconds"`
}

type Config struct {
	StationID         int        `json:"station_id"`
	OfflineCacheHours int        `json:"offline_cache_hours"`
	Screens           []Screen   `json:"screens"`
	TVMonitor         *TVMonitor `json:"tv_monitor"`
}

type Telemetry struct {
	ReportedAt time.Time         `json:"reported_at"`
	Station    StationTelemetry  `json:"station"`
	Screens    []ScreenTelemetry `json:"screens"`
}

type StationTelemetry struct {
	UptimeSeconds int    `json:"uptime_seconds"`
	PackageETag   string `json:"package_etag"`
	BroadcastOK   bool   `json:"broadcast_ok"`
}

type ScreenTelemetry struct {
	ScreenID         int     `json:"screen_id"`
	Reachable        bool    `json:"reachable"`
	AgentOK          bool    `json:"agent_ok"`
	Playing          bool    `json:"playing"`
	URL              string  `json:"url"`
	Wakefulness      string  `json:"wakefulness"`
	StreamPort       int     `json:"stream_port"`
	BroadcastClients int     `json:"broadcast_clients"`
	LastError        *string `json:"last_error"`
}

type Command struct {
	ID       string         `json:"id"`
	ScreenID int            `json:"screen_id"`
	Type     string         `json:"type"`
	Params   map[string]any `json:"params"`
	IssuedAt string         `json:"issued_at"`
}

type CommandsResponse struct {
	Commands []Command `json:"commands"`
}

type CommandResult struct {
	CommandID  string    `json:"command_id"`
	OK         bool      `json:"ok"`
	Error      *string   `json:"error"`
	FinishedAt time.Time `json:"finished_at"`
}

type Media struct {
	ID       int    `json:"id"`
	URL      string `json:"url"`
	MimeType string `json:"mime_type"`
}

type Entry struct {
	ForDate              string  `json:"for_date"`
	BroadcastDayStartsAt string  `json:"broadcast_day_starts_at"`
	Position             int     `json:"position"`
	OffsetSeconds        int     `json:"offset_seconds"`
	StartsAt             *string `json:"starts_at"`
	DurationSeconds      int     `json:"duration_seconds"`
	SourceKind           string  `json:"source_kind"`
	MediaPlanID          *int    `json:"media_plan_id"`
	ScreenIDs            []int   `json:"screen_ids"`
	Media                Media   `json:"media"`
}

type Package struct {
	Version     string           `json:"version"`
	ETag        string           `json:"etag"`
	GeneratedAt string           `json:"generated_at"`
	ValidUntil  string           `json:"valid_until"`
	Entries     []Entry          `json:"entries"`
	ScreenMap   map[string][]int `json:"screen_map"`
	HeaderETag  string           `json:"-"`
}

type Client struct {
	BaseURL string
	Token   string
	HTTP    *http.Client
}

func New(baseURL, token string) *Client {
	return &Client{
		BaseURL: strings.TrimRight(baseURL, "/"),
		Token:   token,
		HTTP:    &http.Client{Timeout: 30 * time.Second},
	}
}

func (c *Client) FetchConfig(ctx context.Context) (Config, error) {
	resp, err := c.do(ctx, http.MethodGet, "/api/agent/v1/config", "", nil)
	if err != nil {
		return Config{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusUnauthorized {
		return Config{}, ErrUnauthorized
	}
	if resp.StatusCode != http.StatusOK {
		return Config{}, fmt.Errorf("hub config: status %d", resp.StatusCode)
	}
	var cfg Config
	if err := json.NewDecoder(resp.Body).Decode(&cfg); err != nil {
		return Config{}, err
	}
	return cfg, nil
}

func (c *Client) FetchPackage(ctx context.Context, ifNoneMatch string) (Package, bool, error) {
	resp, err := c.do(ctx, http.MethodGet, "/api/agent/v2/package", ifNoneMatch, nil)
	if err != nil {
		return Package{}, false, err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusUnauthorized {
		return Package{}, false, ErrUnauthorized
	}
	if resp.StatusCode == http.StatusNotModified {
		return Package{}, true, nil
	}
	if resp.StatusCode != http.StatusOK {
		return Package{}, false, fmt.Errorf("hub package: status %d", resp.StatusCode)
	}
	var pkg Package
	if err := json.NewDecoder(resp.Body).Decode(&pkg); err != nil {
		return Package{}, false, err
	}
	pkg.HeaderETag = resp.Header.Get("ETag")
	return pkg, false, nil
}

func (c *Client) PostPlayEvent(ctx context.Context, screenID, mediaID int, startedAt time.Time) (int, string, error) {
	payload := map[string]any{
		"events": []map[string]any{{
			"screen_id":      screenID,
			"media_asset_id": mediaID,
			"started_at":     startedAt.UTC().Format(time.RFC3339),
		}},
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		return 0, "", err
	}
	resp, err := c.do(ctx, http.MethodPost, "/api/agent/v1/play_events", "", raw)
	if err != nil {
		return 0, "", err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return resp.StatusCode, "", err
	}
	return resp.StatusCode, string(body), nil
}

func (c *Client) PostTelemetry(ctx context.Context, tel Telemetry) error {
	raw, err := json.Marshal(tel)
	if err != nil {
		return err
	}
	resp, err := c.do(ctx, http.MethodPost, "/api/agent/v1/telemetry", "", raw)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusUnauthorized {
		return ErrUnauthorized
	}
	if resp.StatusCode != http.StatusAccepted {
		return fmt.Errorf("hub telemetry: status %d", resp.StatusCode)
	}
	return nil
}

func (c *Client) FetchCommands(ctx context.Context, ifNoneMatch string) (CommandsResponse, error) {
	resp, err := c.do(ctx, http.MethodGet, "/api/agent/v1/commands", ifNoneMatch, nil)
	if err != nil {
		return CommandsResponse{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusUnauthorized {
		return CommandsResponse{}, ErrUnauthorized
	}
	if resp.StatusCode == http.StatusNotModified {
		return CommandsResponse{}, nil
	}
	if resp.StatusCode != http.StatusOK {
		return CommandsResponse{}, fmt.Errorf("hub commands: status %d", resp.StatusCode)
	}
	var out CommandsResponse
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return CommandsResponse{}, err
	}
	return out, nil
}

func (c *Client) PostCommandResults(ctx context.Context, results []CommandResult) error {
	raw, err := json.Marshal(map[string]any{"results": results})
	if err != nil {
		return err
	}
	resp, err := c.do(ctx, http.MethodPost, "/api/agent/v1/command_results", "", raw)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusUnauthorized {
		return ErrUnauthorized
	}
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("hub command_results: status %d", resp.StatusCode)
	}
	return nil
}

func (c *Client) UploadArtifact(ctx context.Context, uploadURL string, r io.Reader, contentType string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodPut, uploadURL, r)
	if err != nil {
		return err
	}
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusUnauthorized {
		return ErrUnauthorized
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("hub upload: status %d", resp.StatusCode)
	}
	return nil
}

func (c *Client) do(ctx context.Context, method, path, ifNoneMatch string, body []byte) (*http.Response, error) {
	var reader io.Reader
	if body != nil {
		reader = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.BaseURL+path, reader)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+c.Token)
	req.Header.Set("Accept", "application/json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if ifNoneMatch != "" {
		req.Header.Set("If-None-Match", ifNoneMatch)
	}
	return c.HTTP.Do(req)
}
