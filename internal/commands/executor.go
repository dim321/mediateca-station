package commands

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"sync"
	"time"

	"mediateca-station/internal/config"
	"mediateca-station/internal/hub"
	"mediateca-station/internal/tv"
)

type AgentClient interface {
	StartStream(ctx context.Context, url string, bufferMs int) error
	RestartStream(ctx context.Context) error
	Stop(ctx context.Context) error
	Status(ctx context.Context) (tv.Status, error)
}

type DeviceClient interface {
	Wake(ctx context.Context) error
	Sleep(ctx context.Context) error
	Screenshot(ctx context.Context) ([]byte, error)
	Record(ctx context.Context, seconds int, dest string) error
	EnsureApp(ctx context.Context, agent AgentClient, apk string) error
	InstallAPK(ctx context.Context, apk string) error
	LaunchMain(ctx context.Context) error
}

type Executor struct {
	TV        func(screenID int) AgentClient
	Device    func(screenID int) DeviceClient
	Screen    func(id int) (config.Screen, bool)
	StreamURL func(id int) string
	APKPath   func() string
	Upload    func(ctx context.Context, url string, r io.Reader, contentType string) error

	screenMu sync.Map // int → *sync.Mutex
}

func (e *Executor) Run(ctx context.Context, cmd hub.Command) (res hub.CommandResult) {
	defer func() {
		if v := recover(); v != nil {
			msg := fmt.Sprintf("panic: %v", v)
			res = failResult(cmd.ID, msg)
		}
	}()

	unlock := e.lockScreen(cmd.ScreenID)
	defer unlock()

	finished := time.Now()
	res = hub.CommandResult{CommandID: cmd.ID, FinishedAt: finished}

	if e.TV == nil || e.Device == nil {
		return failResult(cmd.ID, "not_configured")
	}

	agent := e.TV(cmd.ScreenID)
	dev := e.Device(cmd.ScreenID)

	var err error
	switch cmd.Type {
	case "start_stream":
		url := paramString(cmd.Params, "url")
		if url == "" && e.StreamURL != nil {
			url = e.StreamURL(cmd.ScreenID)
		}
		if url == "" {
			return failResult(cmd.ID, "missing_stream_url")
		}
		buf := paramInt(cmd.Params, "buffer_ms", 300)
		err = agent.StartStream(ctx, url, buf)
	case "restart_stream":
		err = agent.RestartStream(ctx)
	case "stop":
		err = agent.Stop(ctx)
	case "wake":
		err = dev.Wake(ctx)
	case "sleep":
		err = dev.Sleep(ctx)
	case "screenshot":
		var data []byte
		data, err = dev.Screenshot(ctx)
		if err == nil {
			err = e.maybeUpload(ctx, cmd.Params, data, "image/png")
		}
	case "record":
		sec := paramInt(cmd.Params, "seconds", 10)
		dest, tmp, tmpErr := tempFile("record-*.mp4")
		if tmpErr != nil {
			return failResult(cmd.ID, tmpErr.Error())
		}
		defer os.Remove(tmp)
		err = dev.Record(ctx, sec, dest)
		if err == nil {
			err = e.uploadRecordedFile(ctx, cmd.Params, dest)
		}
	case "ensure_app":
		apk := e.apkPath()
		err = dev.EnsureApp(ctx, agent, apk)
	case "install_apk":
		apk, removeAPK, apkErr := e.resolveAPK(ctx, cmd.Params)
		if apkErr != nil {
			return failResult(cmd.ID, apkErr.Error())
		}
		if removeAPK {
			defer os.Remove(apk)
		}
		err = dev.InstallAPK(ctx, apk)
		if err == nil {
			err = dev.LaunchMain(ctx)
		}
	default:
		return failResult(cmd.ID, "unknown_type")
	}

	if err != nil {
		return failResult(cmd.ID, err.Error())
	}
	res.OK = true
	return res
}

func (e *Executor) lockScreen(id int) func() {
	v, _ := e.screenMu.LoadOrStore(id, &sync.Mutex{})
	mu := v.(*sync.Mutex)
	mu.Lock()
	return mu.Unlock
}

func (e *Executor) apkPath() string {
	if e.APKPath != nil {
		return e.APKPath()
	}
	return ""
}

func (e *Executor) uploadRecordedFile(ctx context.Context, params map[string]any, dest string) error {
	url := paramString(params, "upload_url")
	if url == "" {
		return nil
	}
	return e.uploadFile(ctx, url, dest, "video/mp4")
}

func (e *Executor) maybeUpload(ctx context.Context, params map[string]any, data []byte, contentType string) error {
	url := paramString(params, "upload_url")
	if url == "" {
		return nil
	}
	if e.Upload == nil {
		return fmt.Errorf("upload not configured")
	}
	return e.Upload(ctx, url, bytes.NewReader(data), contentType)
}

func (e *Executor) uploadFile(ctx context.Context, uploadURL, path, contentType string) error {
	if e.Upload == nil {
		return fmt.Errorf("upload not configured")
	}
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	return e.Upload(ctx, uploadURL, f, contentType)
}

func (e *Executor) resolveAPK(ctx context.Context, params map[string]any) (path string, remove bool, err error) {
	if u := paramString(params, "url"); u != "" {
		dest, _, err := tempFile("apk-*.apk")
		if err != nil {
			return "", false, err
		}
		if err := downloadFile(ctx, u, dest); err != nil {
			os.Remove(dest)
			return "", false, err
		}
		return dest, true, nil
	}
	return e.apkPath(), false, nil
}

func failResult(id, msg string) hub.CommandResult {
	return hub.CommandResult{
		CommandID:  id,
		OK:         false,
		Error:      &msg,
		FinishedAt: time.Now(),
	}
}

func paramString(params map[string]any, key string) string {
	if params == nil {
		return ""
	}
	v, ok := params[key]
	if !ok || v == nil {
		return ""
	}
	switch s := v.(type) {
	case string:
		return s
	default:
		return fmt.Sprint(v)
	}
}

func paramInt(params map[string]any, key string, def int) int {
	if params == nil {
		return def
	}
	v, ok := params[key]
	if !ok || v == nil {
		return def
	}
	switch n := v.(type) {
	case int:
		return n
	case int64:
		return int(n)
	case float64:
		return int(n)
	case string:
		var i int
		if _, err := fmt.Sscan(n, &i); err == nil {
			return i
		}
	}
	return def
}

func tempFile(pattern string) (path, name string, err error) {
	f, err := os.CreateTemp("", pattern)
	if err != nil {
		return "", "", err
	}
	name = f.Name()
	path = name
	if err := f.Close(); err != nil {
		os.Remove(name)
		return "", "", err
	}
	return path, name, nil
}

func downloadFile(ctx context.Context, url, dest string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("download: status %d", resp.StatusCode)
	}
	f, err := os.Create(dest)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = io.Copy(f, resp.Body)
	return err
}
