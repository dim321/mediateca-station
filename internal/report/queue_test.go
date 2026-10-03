package report

import (
	"path/filepath"
	"testing"
	"time"
)

func TestOutcomeFor(t *testing.T) {
	if OutcomeFor(201, false) != OutcomeRemove {
		t.Fatal("201")
	}
	if OutcomeFor(401, false) != OutcomePause {
		t.Fatal("401")
	}
	if OutcomeFor(404, false) != OutcomeDrop || OutcomeFor(422, false) != OutcomeDrop {
		t.Fatal("drop")
	}
	if OutcomeFor(0, true) != OutcomeRetry || OutcomeFor(500, false) != OutcomeRetry {
		t.Fatal("retry")
	}
}

func TestEnqueuePeekAndPlayedSurvivesReopen(t *testing.T) {
	dir := t.TempDir()
	q, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	started := time.Date(2026, 9, 2, 2, 0, 1, 0, time.UTC)
	key := "7:19:2026-09-02T02:00:00Z"
	if err := q.Enqueue(Event{ScreenID: 7, MediaAssetID: 19, StartedAt: started}, key); err != nil {
		t.Fatal(err)
	}
	if !q.Played(key) {
		t.Fatal("not played")
	}
	ev, ok, err := q.Peek()
	if err != nil || !ok || ev.MediaAssetID != 19 || !ev.StartedAt.Equal(started) {
		t.Fatalf("%+v ok=%v err=%v", ev, ok, err)
	}
	reopened, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	if !reopened.Played(key) {
		t.Fatal("played key lost")
	}
	if err := reopened.RemoveFirst(); err != nil {
		t.Fatal(err)
	}
	_, ok, err = reopened.Peek()
	if err != nil || ok {
		t.Fatalf("after remove ok=%v err=%v", ok, err)
	}
	if !reopened.Played(key) {
		t.Fatal("remove cleared played key")
	}
	if _, err := Open(filepath.Join(dir, "missing")); err != nil {
		t.Fatal(err)
	}
}
