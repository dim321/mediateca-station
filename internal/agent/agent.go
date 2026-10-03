package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"time"

	"mediateca-station/internal/cache"
	"mediateca-station/internal/config"
	"mediateca-station/internal/fsutil"
	"mediateca-station/internal/httpmedia"
	"mediateca-station/internal/hub"
	"mediateca-station/internal/player"
	"mediateca-station/internal/report"
	"mediateca-station/internal/schedule"
)

type Hub interface {
	FetchConfig(ctx context.Context) (hub.Config, error)
	FetchPackage(ctx context.Context, ifNoneMatch string) (hub.Package, bool, error)
	PostPlayEvent(ctx context.Context, screenID, mediaID int, startedAt time.Time) (int, string, error)
}

type Player interface {
	Start(ctx context.Context, serial, mediaURL, mime string) error
	Stop(ctx context.Context, serial string) error
}

type Agent struct {
	cfg          config.File
	hub          Hub
	player       Player
	cache        *cache.Cache
	queue        *report.Queue
	log          *slog.Logger
	now          func() time.Time
	http         *http.Server
	hubCfg       *hub.Config
	hubRaw       []byte
	pkg          *hub.Package
	etag         string
	onScreen     map[int]schedule.Key
	logged       map[string]struct{}
	reportPaused bool
}

func Run(ctx context.Context, cfg config.File) error {
	a, err := newAgent(cfg, hub.New(cfg.HubBaseURL, cfg.AgentToken), player.New(), slog.Default())
	if err != nil {
		return err
	}
	ln, err := net.Listen("tcp", cfg.HTTPListen)
	if err != nil {
		return err
	}
	a.http = &http.Server{Handler: httpmedia.Handler(cfg.HTTPSecret, a.cache.Open)}
	go a.http.Serve(ln)
	defer a.http.Shutdown(context.Background())
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
		cfg:      cfg,
		hub:      h,
		player:   p,
		cache:    cache.New(media),
		queue:    q,
		log:      log,
		now:      func() time.Time { return time.Now().UTC() },
		onScreen: map[int]schedule.Key{},
		logged:   map[string]struct{}{},
	}
	a.loadDisk()
	return a, nil
}

func (a *Agent) loop(ctx context.Context) error {
	poll := time.NewTicker(a.cfg.PollInterval)
	defer poll.Stop()
	for {
		a.poll(ctx)
		a.tick(ctx)
		a.drain(ctx)
		timer := time.NewTimer(time.Until(a.wake(a.now())))
		select {
		case <-ctx.Done():
			timer.Stop()
			return nil
		case <-poll.C:
			timer.Stop()
		case <-timer.C:
		}
	}
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
		return
	}
	if err := a.storePackage(pkg); err != nil {
		a.log.Error("store package", "err", err.Error())
		return
	}
	a.reportPaused = false
	a.log.Info("poll", "result", "package")
	if err := a.cache.Reconcile(ctx, a.cfg.HubBaseURL, a.mediaItems()); err != nil {
		a.log.Error("cache", "err", err.Error())
	}
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
		if key, found := a.onScreen[screenID]; found {
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
		if err := a.player.Stop(ctx, serial); err != nil {
			a.log.Error("adb stop failed", "screen_id", cmd.ScreenID)
			return
		}
		delete(a.onScreen, cmd.ScreenID)
		a.log.Info("stop", "screen_id", cmd.ScreenID)
	case schedule.KindStart:
		started := a.now().UTC()
		if err := a.player.Start(ctx, serial, a.mediaURL(cmd.Entry.Media.ID), cmd.Entry.Media.MimeType); err != nil {
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
			_ = a.player.Stop(ctx, serial)
			return
		}
		a.onScreen[cmd.ScreenID] = schedule.EntryKey(cmd.ScreenID, cmd.Entry)
		a.log.Info("start", "screen_id", cmd.ScreenID, "media_id", cmd.Entry.Media.ID, "starts_at", cmd.Entry.StartsAtRaw)
	case schedule.KindSkip:
		id := "skip:" + schedule.EntryKey(cmd.ScreenID, cmd.Entry).String() + ":" + cmd.Reason
		a.logOnce(id, func() {
			a.log.Info("skip", "reason", cmd.Reason, "screen_id", cmd.ScreenID, "media_id", cmd.Entry.Media.ID, "starts_at", cmd.Entry.StartsAtRaw)
		})
	}
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
	return "http://" + a.cfg.HTTPListen + "/m/" + a.cfg.HTTPSecret + "/" + strconv.Itoa(id)
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
		return nil
	}
	if err := fsutil.WriteAtomic(filepath.Join(a.cfg.DataDir, "config.json"), raw); err != nil {
		return err
	}
	a.hubRaw = append([]byte(nil), raw...)
	a.hubCfg = &cfg
	a.log.Info("config", "station_id", cfg.StationID, "offline_cache_hours", cfg.OfflineCacheHours)
	for _, screen := range cfg.Screens {
		a.log.Info("screen", "screen_id", screen.ID, "orientation", screen.Orientation, "name", screen.Name)
	}
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
