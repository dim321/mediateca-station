package report

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"time"

	"mediateca-station/internal/fsutil"
)

type Outcome int

const (
	OutcomeRetry Outcome = iota
	OutcomeRemove
	OutcomeDrop
	OutcomePause
)

func OutcomeFor(status int, netErr bool) Outcome {
	if netErr {
		return OutcomeRetry
	}
	switch status {
	case 201:
		return OutcomeRemove
	case 401:
		return OutcomePause
	case 404, 422:
		return OutcomeDrop
	default:
		return OutcomeRetry
	}
}

type Event struct {
	ScreenID     int
	MediaAssetID int
	StartedAt    time.Time
}

type stored struct {
	ScreenID     int    `json:"screen_id"`
	MediaAssetID int    `json:"media_asset_id"`
	StartedAt    string `json:"started_at"`
	PlayedKey    string `json:"played_key"`
}

type Queue struct {
	mu      sync.Mutex
	dir     string
	played  map[string]struct{}
	events  string
	playedP string
}

func Open(dir string) (*Queue, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	q := &Queue{
		dir:     dir,
		played:  map[string]struct{}{},
		events:  filepath.Join(dir, "events.jsonl"),
		playedP: filepath.Join(dir, "played.json"),
	}
	if raw, err := os.ReadFile(q.playedP); err == nil && len(raw) > 0 {
		var keys []string
		if err := json.Unmarshal(raw, &keys); err != nil {
			return nil, err
		}
		for _, key := range keys {
			q.played[key] = struct{}{}
		}
	}
	lines, err := q.readLines()
	if err != nil {
		return nil, err
	}
	for _, line := range lines {
		if line.PlayedKey != "" {
			q.played[line.PlayedKey] = struct{}{}
		}
	}
	return q, nil
}

func (q *Queue) Played(key string) bool {
	q.mu.Lock()
	defer q.mu.Unlock()
	_, ok := q.played[key]
	return ok
}

func (q *Queue) Enqueue(ev Event, playedKey string) error {
	q.mu.Lock()
	defer q.mu.Unlock()
	line, err := json.Marshal(stored{
		ScreenID:     ev.ScreenID,
		MediaAssetID: ev.MediaAssetID,
		StartedAt:    ev.StartedAt.UTC().Format(time.RFC3339),
		PlayedKey:    playedKey,
	})
	if err != nil {
		return err
	}
	f, err := os.OpenFile(q.events, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	if _, err := f.Write(append(line, '\n')); err != nil {
		f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	q.played[playedKey] = struct{}{}
	return q.writePlayed()
}

func (q *Queue) Peek() (Event, bool, error) {
	q.mu.Lock()
	defer q.mu.Unlock()
	lines, err := q.readLines()
	if err != nil || len(lines) == 0 {
		return Event{}, false, err
	}
	started, err := time.Parse(time.RFC3339, lines[0].StartedAt)
	if err != nil {
		return Event{}, false, err
	}
	return Event{ScreenID: lines[0].ScreenID, MediaAssetID: lines[0].MediaAssetID, StartedAt: started}, true, nil
}

func (q *Queue) RemoveFirst() error {
	q.mu.Lock()
	defer q.mu.Unlock()
	lines, err := q.readLines()
	if err != nil || len(lines) == 0 {
		return err
	}
	var buf []byte
	for _, line := range lines[1:] {
		raw, err := json.Marshal(line)
		if err != nil {
			return err
		}
		buf = append(buf, raw...)
		buf = append(buf, '\n')
	}
	return fsutil.WriteAtomic(q.events, buf)
}

func (q *Queue) writePlayed() error {
	keys := make([]string, 0, len(q.played))
	for key := range q.played {
		keys = append(keys, key)
	}
	raw, err := json.Marshal(keys)
	if err != nil {
		return err
	}
	return fsutil.WriteAtomic(q.playedP, raw)
}

func (q *Queue) readLines() ([]stored, error) {
	f, err := os.Open(q.events)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	defer f.Close()
	var lines []stored
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		text := scanner.Bytes()
		if len(text) == 0 {
			continue
		}
		var line stored
		if err := json.Unmarshal(text, &line); err != nil {
			return nil, err
		}
		lines = append(lines, line)
	}
	return lines, scanner.Err()
}
