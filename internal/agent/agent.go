package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"mediateca-station/internal/air"
	"mediateca-station/internal/cache"
	"mediateca-station/internal/commands"
	"mediateca-station/internal/config"
	"mediateca-station/internal/fsutil"
	"mediateca-station/internal/health"
	"mediateca-station/internal/httpmedia"
	"mediateca-station/internal/hub"
	"mediateca-station/internal/leases"
	"mediateca-station/internal/player"
	"mediateca-station/internal/report"
	"mediateca-station/internal/schedule"
	"mediateca-station/internal/tv"
	"mediateca-station/internal/voxel"
)

type Hub interface {
	FetchConfig(ctx context.Context) (hub.Config, error)
	FetchPackage(ctx context.Context, ifNoneMatch string) (hub.Package, bool, error)
	PostPlayEvent(ctx context.Context, screenID, mediaID int, startedAt time.Time) (int, string, error)
	PostTelemetry(ctx context.Context, tel hub.Telemetry) error
	FetchCommands(ctx context.Context, ifNoneMatch string) (hub.CommandsResponse, error)
	PostCommandResults(ctx context.Context, results []hub.CommandResult) error
	UploadArtifact(ctx context.Context, uploadURL string, r io.Reader, contentType string) error
}

type commandRunner interface {
	Run(ctx context.Context, cmd hub.Command) hub.CommandResult
}

type Player interface {
	Start(ctx context.Context, serial, mediaURL, mime string) error
	Stop(ctx context.Context, serial string) error
}

type Air interface {
	Start(ctx context.Context, req air.StartReq) error
	Stop(ctx context.Context, serial string, screenID int) error
}

type Agent struct {
	cfg               config.File
	hub               Hub
	player            Player
	air               Air
	voxel             *voxel.Manager
	cache             *cache.Cache
	queue             *report.Queue
	log               *slog.Logger
	now               func() time.Time
	http              *http.Server
	hubCfg            *hub.Config
	hubRaw            []byte
	pkg               *hub.Package
	etag              string
	onScreen          map[int]schedule.Key
	onScreenMu        sync.RWMutex
	lastStart         map[int]time.Time
	lastStartMu       sync.RWMutex
	logged            map[string]struct{}
	reportPaused      bool
	telemetryInterval time.Duration
	healthWatcher     *health.Watcher
	cmdExec           commandRunner
	leaseFile         leases.File
	startedAt         time.Time

	stateMu      sync.RWMutex
	doneMu       sync.Mutex
	doneCmdOrder []string
	doneCmdSet   map[string]struct{}
}

const maxDoneCommands = 100

func Run(ctx context.Context, cfg config.File) error {
	a, err := newAgent(cfg, hub.New(cfg.HubBaseURL, cfg.AgentToken), player.New(), slog.Default())
	if err != nil {
		return err
	}
	if !cfg.VoxelPath() && cfg.HTTPListen != "" {
		ln, err := net.Listen("tcp", cfg.HTTPListen)
		if err != nil {
			return err
		}
		a.http = &http.Server{Handler: httpmedia.Handler(cfg.HTTPSecret, a.cache.Open)}
		go a.http.Serve(ln)
		defer a.http.Shutdown(context.Background())
	}
	a.ensureBroadcasts(ctx)
	if a.air != nil {
		go a.runHealthWatcher(ctx)
	}
	return a.loop(ctx)
}

func newAgent(cfg config.File, h Hub, p Player, log *slog.Logger) (*Agent, error) {
	if err := os.MkdirAll(cfg.DataDir, 0o700); err != nil {
		return nil, err
	}
	if err := os.Chmod(cfg.DataDir, 0o700); err != nil {
		return nil, err
	}
	media := filepath.Join(cfg.DataDir, "media")
	if err := os.MkdirAll(media, 0o700); err != nil {
		return nil, err
	}
	q, err := report.Open(cfg.DataDir)
	if err != nil {
		return nil, err
	}
	a := &Agent{
		cfg:       cfg,
		hub:       h,
		player:    p,
		cache:     cache.New(media),
		queue:     q,
		log:       log,
		now:       func() time.Time { return time.Now().UTC() },
		onScreen:  map[int]schedule.Key{},
		lastStart: map[int]time.Time{},
		logged:    map[string]struct{}{},
		startedAt: time.Now().UTC(),
	}
	_, _, a.telemetryInterval = health.TVMonitorSettings(nil)
	if cfg.VoxelPath() {
		if err := os.MkdirAll(cfg.FifoDir, 0o700); err != nil {
			return nil, err
		}
		vm := &voxel.Manager{Bin: cfg.BroadcastBin, FifoDir: cfg.FifoDir, DataDir: cfg.DataDir}
		a.voxel = vm
		ctrl := &air.Controller{
			Pace:      cfg.FeederPace,
			ChunkSize: cfg.ChunkSize,
			Log:       log,
			NewTV: func(serial string) air.TV {
				return tv.NewAgent(serial)
			},
			MediaPath: a.cache.Path,
			StreamURL: func(screenID int) string {
				u, ok := cfg.StreamURL(screenID)
				if !ok {
					return ""
				}
				return u
			},
			OpenWriter: func(port int) (io.WriteCloser, error) {
				return vm.OpenFifo(port)
			},
		}
		a.air = ctrl
	}
	a.loadDisk()
	a.leaseFile = leases.File{
		Path: leases.DefaultPath,
		Now:  func() time.Time { return a.now() },
	}
	return a, nil
}

func (a *Agent) ensureBroadcasts(ctx context.Context) {
	if a.voxel == nil {
		return
	}
	for _, screen := range a.cfg.Screens {
		if screen.StreamPort <= 0 {
			continue
		}
		if err := a.voxel.Ensure(ctx, screen.StreamPort); err != nil {
			a.log.Error("broadcast ensure", "screen_id", screen.ScreenID, "port", screen.StreamPort, "err", err.Error())
		}
	}
}

func (a *Agent) loop(ctx context.Context) error {
	poll := time.NewTicker(a.cfg.PollInterval)
	defer poll.Stop()
	teleInterval := a.effectiveTelemetryInterval()
	tele := time.NewTicker(teleInterval)
	defer tele.Stop()

	schedule := time.NewTimer(time.Until(a.wake(a.now())))
	defer schedule.Stop()

	a.poll(ctx)
	a.tick(ctx)
	a.drain(ctx)

	for {
		select {
		case <-ctx.Done():
			return nil
		case <-poll.C:
			a.poll(ctx)
			if next := a.effectiveTelemetryInterval(); next != teleInterval {
				tele.Stop()
				teleInterval = next
				tele = time.NewTicker(teleInterval)
			}
			a.tick(ctx)
			a.drain(ctx)
			resetScheduleTimer(schedule, a.wake(a.now()))
		case <-tele.C:
			go a.postTelemetry(ctx)
		case <-schedule.C:
			a.tick(ctx)
			a.drain(ctx)
			resetScheduleTimer(schedule, a.wake(a.now()))
		}
	}
}

func resetScheduleTimer(t *time.Timer, wakeAt time.Time) {
	next := time.Until(wakeAt)
	if !t.Stop() {
		select {
		case <-t.C:
		default:
		}
	}
	t.Reset(next)
}

func (a *Agent) effectiveTelemetryInterval() time.Duration {
	a.stateMu.RLock()
	interval := a.telemetryInterval
	a.stateMu.RUnlock()
	if interval > 0 {
		return interval
	}
	_, _, d := health.TVMonitorSettings(nil)
	return d
}

func (a *Agent) poll(ctx context.Context) {
	cfg, err := a.hub.FetchConfig(ctx)
	if errors.Is(err, hub.ErrUnauthorized) {
		a.pauseToken()
		return
	}
	var cfgOK *hub.Config
	if err != nil {
		a.log.Error("hub config", "err", err.Error())
	} else {
		cfgOK = &cfg
	}
	pkg, notMod, err := a.hub.FetchPackage(ctx, a.etag)
	if errors.Is(err, hub.ErrUnauthorized) {
		a.pauseToken()
		return
	}
	if err != nil {
		a.log.Error("hub package", "err", err.Error())
		if cfgOK != nil {
			_ = a.storeConfig(*cfgOK)
		}
		return
	}
	if cfgOK != nil {
		_ = a.storeConfig(*cfgOK)
	}
	if notMod {
		a.reportPaused = false
		a.log.Info("poll", "result", "not_modified")
		a.afterPoll(ctx)
		return
	}
	if err := a.storePackage(pkg); err != nil {
		a.log.Error("store package", "err", err.Error())
		a.afterPoll(ctx)
		return
	}
	a.reportPaused = false
	a.log.Info("poll", "result", "package")
	if err := a.cache.Reconcile(ctx, a.cfg.HubBaseURL, a.mediaItems()); err != nil {
		a.log.Error("cache", "err", err.Error())
	}
	a.afterPoll(ctx)
}

func (a *Agent) afterPoll(ctx context.Context) {
	if !a.reportPaused {
		a.pollCommands(ctx)
	}
	a.drain(ctx)
}

func (a *Agent) pollCommands(ctx context.Context) {
	resp, err := a.hub.FetchCommands(ctx, "")
	if errors.Is(err, hub.ErrUnauthorized) {
		a.pauseToken()
		return
	}
	if err != nil {
		a.log.Error("hub commands", "err", err.Error())
		return
	}
	if len(resp.Commands) == 0 {
		return
	}
	commands := append([]hub.Command(nil), resp.Commands...)
	go a.runCommands(context.WithoutCancel(ctx), commands)
}

func (a *Agent) runCommands(ctx context.Context, cmds []hub.Command) {
	exec := a.commandExecutor()
	results := make([]hub.CommandResult, 0, len(cmds))
	for _, cmd := range cmds {
		if a.commandAlreadyDone(cmd.ID) {
			msg := "already_done"
			results = append(results, hub.CommandResult{
				CommandID:  cmd.ID,
				OK:         true,
				Error:      &msg,
				FinishedAt: time.Now().UTC(),
			})
			continue
		}
		if cmd.Type == "fetch_screens" {
			results = append(results, commands.FetchScreens(cmd, a.leaseFile.Active, a.now()))
			continue
		}
		results = append(results, exec.Run(ctx, cmd))
	}
	if err := a.hub.PostCommandResults(ctx, results); errors.Is(err, hub.ErrUnauthorized) {
		a.pauseToken()
		return
	} else if err != nil {
		var status *hub.HTTPStatusError
		if errors.As(err, &status) && status.Status == http.StatusUnprocessableEntity {
			a.log.Error("hub command_results rejected", "status", status.Status, "command_ids", commandIDs(results))
			return
		}
		a.log.Error("hub command_results", "err", err.Error(), "command_ids", commandIDs(results))
		return
	}
	for _, res := range results {
		if res.OK && (res.Error == nil || *res.Error != "already_done") {
			a.markCommandDone(res.CommandID)
		}
	}
}

func commandIDs(results []hub.CommandResult) []string {
	ids := make([]string, len(results))
	for i, res := range results {
		ids[i] = res.CommandID
	}
	return ids
}

func (a *Agent) commandAlreadyDone(id string) bool {
	a.doneMu.Lock()
	defer a.doneMu.Unlock()
	if a.doneCmdSet == nil {
		return false
	}
	_, ok := a.doneCmdSet[id]
	return ok
}

func (a *Agent) markCommandDone(id string) {
	if id == "" {
		return
	}
	a.doneMu.Lock()
	defer a.doneMu.Unlock()
	if a.doneCmdSet == nil {
		a.doneCmdSet = map[string]struct{}{}
	}
	if _, ok := a.doneCmdSet[id]; ok {
		return
	}
	a.doneCmdOrder = append(a.doneCmdOrder, id)
	a.doneCmdSet[id] = struct{}{}
	for len(a.doneCmdOrder) > maxDoneCommands {
		oldest := a.doneCmdOrder[0]
		a.doneCmdOrder = a.doneCmdOrder[1:]
		delete(a.doneCmdSet, oldest)
	}
}

func (a *Agent) commandExecutor() commandRunner {
	if a.cmdExec != nil {
		return a.cmdExec
	}
	return &commands.Executor{
		Screen: a.cfg.ScreenByID,
		StreamURL: func(id int) string {
			u, ok := a.cfg.StreamURL(id)
			if !ok {
				return ""
			}
			return u
		},
		APKPath: func() string { return a.cfg.VoxelAPK },
		TV: func(screenID int) commands.AgentClient {
			serial, ok := a.cfg.Serial(screenID)
			if !ok {
				return nil
			}
			return tv.NewAgent(serial)
		},
		Device: func(screenID int) commands.DeviceClient {
			screen, ok := a.cfg.ScreenByID(screenID)
			if !ok {
				return nil
			}
			return &commandDevice{Device: tv.Device{Serial: screen.ADBSerial, MAC: screen.MAC, Runner: tv.ExecRunner{}}}
		},
		Upload: func(ctx context.Context, url string, r io.Reader, contentType string) error {
			return a.hub.UploadArtifact(ctx, url, r, contentType)
		},
	}
}

func (a *Agent) postTelemetry(ctx context.Context) {
	if a.reportPaused {
		return
	}
	tel := a.buildTelemetry(ctx)
	if err := a.hub.PostTelemetry(ctx, tel); errors.Is(err, hub.ErrUnauthorized) {
		a.pauseToken()
		return
	} else if err != nil {
		a.log.Error("hub telemetry", "err", err.Error())
	}
}

func (a *Agent) buildTelemetry(ctx context.Context) hub.Telemetry {
	now := a.now().UTC()
	uptime := int(now.Sub(a.startedAt).Seconds())
	if uptime < 0 {
		uptime = 0
	}
	station := hub.StationTelemetry{
		UptimeSeconds: uptime,
		PackageETag:   a.etag,
		BroadcastOK:   a.broadcastOK(ctx),
	}
	screens := make([]hub.ScreenTelemetry, 0, len(a.cfg.Screens))
	for _, screen := range a.cfg.Screens {
		screens = append(screens, a.screenTelemetry(ctx, screen))
	}
	return hub.Telemetry{
		ReportedAt: now,
		Station:    station,
		Screens:    screens,
	}
}

func (a *Agent) broadcastOK(ctx context.Context) bool {
	if a.voxel == nil {
		return true
	}
	for _, screen := range a.cfg.Screens {
		if screen.StreamPort <= 0 {
			continue
		}
		if err := a.voxel.Ensure(ctx, screen.StreamPort); err != nil {
			return false
		}
	}
	return true
}

func (a *Agent) screenTelemetry(ctx context.Context, screen config.Screen) hub.ScreenTelemetry {
	out := hub.ScreenTelemetry{
		ScreenID:   screen.ScreenID,
		StreamPort: screen.StreamPort,
	}
	out.Reachable = screenReachable(ctx, screen.ADBSerial)
	serial, ok := a.cfg.Serial(screen.ScreenID)
	if !ok {
		return out
	}
	agent := tv.NewAgent(serial)
	st, err := agent.Status(ctx)
	if err != nil {
		msg := err.Error()
		out.LastError = &msg
		return out
	}
	out.AgentOK = true
	out.Playing = st.Playing
	out.URL = st.URL
	return out
}

type commandDevice struct {
	tv.Device
}

func (d *commandDevice) EnsureApp(ctx context.Context, agent commands.AgentClient, apk string) error {
	tvAgent, ok := agent.(*tv.Agent)
	if !ok {
		return fmt.Errorf("unexpected tv agent type")
	}
	return d.Device.EnsureApp(ctx, tvAgent, apk)
}

func screenReachable(ctx context.Context, adbSerial string) bool {
	if adbSerial == "" {
		return false
	}
	dial := (&net.Dialer{Timeout: 2 * time.Second}).DialContext
	if conn, err := dial(ctx, "tcp", adbSerial); err == nil {
		conn.Close()
		return true
	}
	host := adbSerial
	if i := strings.IndexByte(adbSerial, ':'); i >= 0 {
		host = adbSerial[:i]
	}
	if conn, err := dial(ctx, "tcp", net.JoinHostPort(host, "8989")); err == nil {
		conn.Close()
		return true
	}
	return false
}

func (a *Agent) tick(ctx context.Context) {
	now := a.now()
	entries := a.entries()
	a.logMapping()
	for _, screenID := range a.playable() {
		serial, ok := a.cfg.Serial(screenID)
		if !ok {
			continue
		}
		var on *schedule.Key
		a.onScreenMu.RLock()
		key, found := a.onScreen[screenID]
		a.onScreenMu.RUnlock()
		if found {
			on = &key
		}
		cmds := schedule.Commands(now, a.cfg.LateThreshold, screenID, entries, a.queue.Played, a.cache.Ready, on)
		for _, cmd := range cmds {
			a.apply(ctx, serial, cmd)
		}
	}
}

func (a *Agent) apply(ctx context.Context, serial string, cmd schedule.Command) {
	switch cmd.Kind {
	case schedule.KindStop:
		if a.air != nil {
			if err := a.air.Stop(ctx, serial, cmd.ScreenID); err != nil {
				a.log.Error("air stop failed", "screen_id", cmd.ScreenID)
				return
			}
		} else if err := a.player.Stop(ctx, serial); err != nil {
			a.log.Error("adb stop failed", "screen_id", cmd.ScreenID)
			return
		}
		a.onScreenMu.Lock()
		delete(a.onScreen, cmd.ScreenID)
		a.onScreenMu.Unlock()
		a.log.Info("stop", "screen_id", cmd.ScreenID)
	case schedule.KindStart:
		started := a.now().UTC()
		if a.air != nil {
			screen, ok := a.cfg.ScreenByID(cmd.ScreenID)
			if !ok {
				return
			}
			if err := a.air.Start(ctx, air.StartReq{
				ScreenID: cmd.ScreenID,
				Serial:   serial,
				Port:     screen.StreamPort,
				MediaID:  cmd.Entry.Media.ID,
				Duration: cmd.Entry.Duration,
			}); err != nil {
				a.log.Error("air start failed", "screen_id", cmd.ScreenID, "media_id", cmd.Entry.Media.ID)
				_ = a.air.Stop(ctx, serial, cmd.ScreenID)
				return
			}
			a.markScreenStarted(cmd.ScreenID)
		} else if err := a.player.Start(ctx, serial, a.mediaURL(cmd.Entry.Media.ID), cmd.Entry.Media.MimeType); err != nil {
			a.log.Error("adb start failed", "screen_id", cmd.ScreenID, "media_id", cmd.Entry.Media.ID)
			_ = a.player.Stop(ctx, serial)
			return
		}
		key := schedule.EntryKey(cmd.ScreenID, cmd.Entry).String()
		if err := a.queue.Enqueue(report.Event{
			ScreenID:     cmd.ScreenID,
			MediaAssetID: cmd.Entry.Media.ID,
			StartedAt:    started,
		}, key); err != nil {
			a.log.Error("enqueue play event", "screen_id", cmd.ScreenID)
			a.stopPlayback(ctx, serial, cmd.ScreenID)
			return
		}
		a.onScreenMu.Lock()
		a.onScreen[cmd.ScreenID] = schedule.EntryKey(cmd.ScreenID, cmd.Entry)
		a.onScreenMu.Unlock()
		a.log.Info("start", "screen_id", cmd.ScreenID, "media_id", cmd.Entry.Media.ID, "starts_at", cmd.Entry.StartsAtRaw)
	case schedule.KindSkip:
		id := "skip:" + schedule.EntryKey(cmd.ScreenID, cmd.Entry).String() + ":" + cmd.Reason
		a.logOnce(id, func() {
			a.log.Info("skip", "reason", cmd.Reason, "screen_id", cmd.ScreenID, "media_id", cmd.Entry.Media.ID, "starts_at", cmd.Entry.StartsAtRaw)
		})
	}
}

func (a *Agent) stopPlayback(ctx context.Context, serial string, screenID int) {
	if a.air != nil {
		_ = a.air.Stop(ctx, serial, screenID)
		return
	}
	_ = a.player.Stop(ctx, serial)
}

func (a *Agent) drain(ctx context.Context) {
	if a.reportPaused {
		return
	}
	for {
		ev, ok, err := a.queue.Peek()
		if err != nil || !ok {
			return
		}
		status, body, err := a.hub.PostPlayEvent(ctx, ev.ScreenID, ev.MediaAssetID, ev.StartedAt)
		switch report.OutcomeFor(status, err != nil) {
		case report.OutcomeRemove:
			_ = a.queue.RemoveFirst()
		case report.OutcomeDrop:
			a.log.Error("drop play event", "status", status, "body", body)
			_ = a.queue.RemoveFirst()
		case report.OutcomePause:
			a.pauseToken()
			return
		default:
			return
		}
	}
}

func (a *Agent) pauseToken() {
	a.reportPaused = true
	a.logOnce("token", func() {
		a.log.Error("hub token rejected")
	})
	a.log.Info("poll", "result", "unauthorized")
}

func (a *Agent) playable() []int {
	if a.hubCfg == nil {
		ids := make([]int, 0, len(a.cfg.Screens))
		for _, screen := range a.cfg.Screens {
			ids = append(ids, screen.ScreenID)
		}
		return ids
	}
	allow := map[int]struct{}{}
	for _, screen := range a.hubCfg.Screens {
		allow[screen.ID] = struct{}{}
	}
	var ids []int
	for _, screen := range a.cfg.Screens {
		if _, ok := allow[screen.ScreenID]; ok {
			ids = append(ids, screen.ScreenID)
		}
	}
	return ids
}

func (a *Agent) logMapping() {
	if a.hubCfg == nil {
		return
	}
	local := map[int]struct{}{}
	for _, screen := range a.cfg.Screens {
		local[screen.ScreenID] = struct{}{}
	}
	hubIDs := map[int]struct{}{}
	for _, screen := range a.hubCfg.Screens {
		hubIDs[screen.ID] = struct{}{}
		if _, ok := local[screen.ID]; !ok {
			id := screen.ID
			a.logOnce(fmt.Sprintf("map:%d:no_serial", id), func() {
				a.log.Error("screen mapping", "screen_id", id, "reason", "no_serial")
			})
		}
	}
	for _, screen := range a.cfg.Screens {
		if _, ok := hubIDs[screen.ScreenID]; !ok {
			id := screen.ScreenID
			a.logOnce(fmt.Sprintf("map:%d:not_in_hub_config", id), func() {
				a.log.Error("screen mapping", "screen_id", id, "reason", "not_in_hub_config")
			})
		}
	}
}

func (a *Agent) entries() []schedule.Entry {
	if a.pkg == nil {
		return nil
	}
	var out []schedule.Entry
	for _, raw := range a.pkg.Entries {
		entry, err := schedule.ParseEntry(raw.StartsAt, raw.DurationSeconds, raw.ScreenIDs, raw.Media.ID, raw.Media.URL, raw.Media.MimeType, raw.Position)
		if err != nil {
			pos := raw.Position
			mediaID := raw.Media.ID
			a.logOnce(fmt.Sprintf("ignored:%d:%d", pos, mediaID), func() {
				a.log.Info("skip", "reason", "ignored_entry", "position", pos, "media_id", mediaID)
			})
			continue
		}
		out = append(out, entry)
	}
	return out
}

func (a *Agent) mediaItems() []cache.Item {
	seen := map[int]cache.Item{}
	var order []int
	for _, entry := range a.entries() {
		if _, ok := seen[entry.Media.ID]; !ok {
			order = append(order, entry.Media.ID)
		}
		seen[entry.Media.ID] = cache.Item{ID: entry.Media.ID, URL: entry.Media.URL, Mime: entry.Media.MimeType}
	}
	out := make([]cache.Item, 0, len(order))
	for _, id := range order {
		out = append(out, seen[id])
	}
	return out
}

func (a *Agent) mediaURL(id int) string {
	host := a.cfg.HTTPListen
	if a.cfg.HTTPPublic != "" {
		host = a.cfg.HTTPPublic
	}
	return "http://" + host + "/m/" + a.cfg.HTTPSecret + "/" + strconv.Itoa(id)
}

func (a *Agent) wake(now time.Time) time.Time {
	wake := now.Add(24 * time.Hour)
	entries := a.entries()
	for _, screenID := range a.playable() {
		at := schedule.WakeAt(now, a.cfg.LateThreshold, screenID, entries, a.queue.Played, a.cache.Ready)
		if at.Before(wake) {
			wake = at
		}
	}
	if !wake.After(now) {
		return now
	}
	return wake
}

func (a *Agent) storeConfig(cfg hub.Config) error {
	raw, err := json.Marshal(cfg)
	if err != nil {
		return err
	}
	if a.hubRaw != nil && bytes.Equal(a.hubRaw, raw) {
		a.hubCfg = &cfg
		a.applyTVMonitor(cfg)
		return nil
	}
	if err := fsutil.WriteAtomic(filepath.Join(a.cfg.DataDir, "config.json"), raw); err != nil {
		return err
	}
	a.hubRaw = append([]byte(nil), raw...)
	a.hubCfg = &cfg
	a.applyTVMonitor(cfg)
	a.log.Info("config", "station_id", cfg.StationID, "offline_cache_hours", cfg.OfflineCacheHours)
	for _, screen := range cfg.Screens {
		a.log.Info("screen", "screen_id", screen.ID, "orientation", screen.Orientation, "name", screen.Name)
	}
	a.ensureBroadcasts(context.Background())
	return nil
}

func (a *Agent) storePackage(pkg hub.Package) error {
	raw, err := json.Marshal(pkg)
	if err != nil {
		return err
	}
	if err := fsutil.WriteAtomic(filepath.Join(a.cfg.DataDir, "package.json"), raw); err != nil {
		return err
	}
	if err := fsutil.WriteAtomic(filepath.Join(a.cfg.DataDir, "package.etagheader"), []byte(pkg.HeaderETag)); err != nil {
		return err
	}
	copied := pkg
	a.pkg = &copied
	a.etag = pkg.HeaderETag
	return nil
}

func (a *Agent) loadDisk() {
	if raw, err := os.ReadFile(filepath.Join(a.cfg.DataDir, "config.json")); err == nil {
		var cfg hub.Config
		if json.Unmarshal(raw, &cfg) == nil {
			a.hubCfg = &cfg
			a.hubRaw = raw
		}
	}
	if raw, err := os.ReadFile(filepath.Join(a.cfg.DataDir, "package.json")); err == nil {
		var pkg hub.Package
		if json.Unmarshal(raw, &pkg) == nil {
			a.pkg = &pkg
		}
	}
	if raw, err := os.ReadFile(filepath.Join(a.cfg.DataDir, "package.etagheader")); err == nil {
		a.etag = string(raw)
	}
}

func (a *Agent) logOnce(key string, fn func()) {
	if _, ok := a.logged[key]; ok {
		return
	}
	a.logged[key] = struct{}{}
	fn()
}

func (a *Agent) applyTVMonitor(cfg hub.Config) {
	_, _, telemetry := health.TVMonitorSettings(cfg.TVMonitor)
	a.stateMu.Lock()
	a.telemetryInterval = telemetry
	hw := a.healthWatcher
	a.stateMu.Unlock()
	if hw != nil {
		hw.ApplyTVMonitor(cfg.TVMonitor)
	}
}

func (a *Agent) runHealthWatcher(ctx context.Context) {
	ids := make([]int, 0, len(a.cfg.Screens))
	for _, screen := range a.cfg.Screens {
		ids = append(ids, screen.ScreenID)
	}
	w := health.NewWatcher()
	w.OnAir = a.screenOnAir
	w.StartedRecently = a.screenStartedRecently
	w.Client = a.tvHealthClient
	a.stateMu.Lock()
	a.healthWatcher = w
	a.stateMu.Unlock()
	if a.hubCfg != nil {
		a.applyTVMonitor(*a.hubCfg)
	} else {
		_, _, telemetry := health.TVMonitorSettings(nil)
		a.stateMu.Lock()
		a.telemetryInterval = telemetry
		a.stateMu.Unlock()
		w.ApplyTVMonitor(nil)
	}
	if err := w.Run(ctx, ids); err != nil && ctx.Err() == nil {
		a.log.Error("health watcher", "err", err.Error())
	}
}

func (a *Agent) screenOnAir(screenID int) bool {
	a.onScreenMu.RLock()
	_, ok := a.onScreen[screenID]
	a.onScreenMu.RUnlock()
	return ok
}

func (a *Agent) markScreenStarted(screenID int) {
	a.lastStartMu.Lock()
	a.lastStart[screenID] = a.now()
	a.lastStartMu.Unlock()
}

func (a *Agent) screenStartedRecently(screenID int) bool {
	a.lastStartMu.RLock()
	at, ok := a.lastStart[screenID]
	a.lastStartMu.RUnlock()
	return ok && a.now().Sub(at) < 10*time.Second
}

func (a *Agent) tvHealthClient(screenID int) health.ScreenClient {
	serial, ok := a.cfg.Serial(screenID)
	if !ok {
		return nil
	}
	return &tvScreenClient{tv: tv.NewAgent(serial)}
}

type tvScreenClient struct {
	tv *tv.Agent
}

func (c *tvScreenClient) Status(ctx context.Context) (health.TvStatus, error) {
	st, err := c.tv.Status(ctx)
	return health.TvStatus{Playing: st.Playing}, err
}

func (c *tvScreenClient) RestartStream(ctx context.Context) error {
	return c.tv.RestartStream(ctx)
}
