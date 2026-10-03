package schedule

import (
	"testing"
	"time"
)

func entry(start time.Time, duration time.Duration, position, screenID, mediaID int) Entry {
	raw := start.UTC().Format(time.RFC3339)
	return Entry{
		Position:    position,
		StartsAt:    start,
		StartsAtRaw: raw,
		Duration:    duration,
		ScreenIDs:   []int{screenID},
		Media:       Media{ID: mediaID, URL: "/clip", MimeType: "video/mp2t"},
	}
}

func nonePlayed(string) bool       { return false }
func alwaysReady(int, string) bool { return true }
func neverReady(int, string) bool  { return false }

func kinds(cmds []Command) []Kind {
	out := make([]Kind, len(cmds))
	for i, cmd := range cmds {
		out[i] = cmd.Kind
	}
	return out
}

func TestParseEntryRejectsIncomplete(t *testing.T) {
	raw := "2026-09-02T02:00:00Z"
	if _, err := ParseEntry(&raw, 0, []int{7}, 19, "/clip", "video/mp2t", 1); err == nil {
		t.Fatal("zero duration")
	}
	if _, err := ParseEntry(nil, 10, []int{7}, 19, "/clip", "video/mp2t", 1); err == nil {
		t.Fatal("nil start")
	}
	if _, err := ParseEntry(&raw, 10, []int{7}, 19, "", "video/mp2t", 1); err == nil {
		t.Fatal("empty url")
	}
	got, err := ParseEntry(&raw, 10, []int{7}, 19, "/clip", "video/mp2t", 1)
	if err != nil || got.StartsAtRaw != raw || got.Media.ID != 19 {
		t.Fatalf("got %+v err=%v", got, err)
	}
}

func TestStartAtExactAndAtThreshold(t *testing.T) {
	start := time.Date(2026, 9, 2, 2, 0, 0, 0, time.UTC)
	clip := entry(start, 10*time.Second, 1, 7, 19)
	late := 2 * time.Second
	for _, now := range []time.Time{start, start.Add(late)} {
		cmds := Commands(now, late, 7, []Entry{clip}, nonePlayed, alwaysReady, nil)
		if len(cmds) != 1 || cmds[0].Kind != KindStart || cmds[0].Entry.Media.ID != 19 {
			t.Fatalf("now=%s cmds=%+v", now, cmds)
		}
	}
}

func TestSkipOneNanosecondPastThreshold(t *testing.T) {
	start := time.Date(2026, 9, 2, 2, 0, 0, 0, time.UTC)
	clip := entry(start, 30*time.Second, 1, 7, 19)
	now := start.Add(2*time.Second + time.Nanosecond)
	cmds := Commands(now, 2*time.Second, 7, []Entry{clip}, nonePlayed, alwaysReady, nil)
	if len(cmds) != 1 || cmds[0].Kind != KindSkip || cmds[0].Reason != "late" {
		t.Fatalf("%+v", cmds)
	}
}

func TestMissingFileWaitsInsideWindowAndSkipsAfter(t *testing.T) {
	start := time.Date(2026, 9, 2, 2, 0, 0, 0, time.UTC)
	clip := entry(start, 30*time.Second, 1, 7, 19)
	cmds := Commands(start, 2*time.Second, 7, []Entry{clip}, nonePlayed, neverReady, nil)
	if len(cmds) != 0 {
		t.Fatalf("inside window %+v", cmds)
	}
	now := start.Add(3 * time.Second)
	cmds = Commands(now, 2*time.Second, 7, []Entry{clip}, nonePlayed, neverReady, nil)
	if len(cmds) != 1 || cmds[0].Reason != "missing_file" {
		t.Fatalf("%+v", cmds)
	}
}

func TestOtherScreenNeverStarts(t *testing.T) {
	start := time.Date(2026, 9, 2, 2, 0, 0, 0, time.UTC)
	clip := entry(start, 10*time.Second, 1, 8, 19)
	cmds := Commands(start, 2*time.Second, 7, []Entry{clip}, nonePlayed, alwaysReady, nil)
	if len(cmds) != 0 {
		t.Fatalf("%+v", cmds)
	}
}

func TestConflictPlaysSmallerPosition(t *testing.T) {
	start := time.Date(2026, 9, 2, 2, 0, 0, 0, time.UTC)
	laterPos := entry(start, 10*time.Second, 2, 7, 20)
	earlierPos := entry(start, 10*time.Second, 1, 7, 19)
	cmds := Commands(start, 2*time.Second, 7, []Entry{laterPos, earlierPos}, nonePlayed, alwaysReady, nil)
	var started, skipped int
	for _, cmd := range cmds {
		if cmd.Kind == KindStart && cmd.Entry.Media.ID == 19 {
			started++
		}
		if cmd.Kind == KindSkip && cmd.Reason == "conflict" && cmd.Entry.Media.ID == 20 {
			skipped++
		}
	}
	if started != 1 || skipped != 1 {
		t.Fatalf("%+v", cmds)
	}
}

func TestTightJoinDoesNotStop(t *testing.T) {
	start := time.Date(2026, 9, 2, 2, 0, 0, 0, time.UTC)
	first := entry(start, 10*time.Second, 1, 7, 19)
	second := entry(start.Add(10*time.Second), 10*time.Second, 2, 7, 20)
	on := EntryKey(7, first)
	played := func(key string) bool { return key == on.String() }
	cmds := Commands(start.Add(10*time.Second), 2*time.Second, 7, []Entry{first, second}, played, alwaysReady, &on)
	for _, cmd := range cmds {
		if cmd.Kind == KindStop {
			t.Fatalf("stopped on a tight join: %+v", cmds)
		}
	}
	if len(cmds) != 1 || cmds[0].Kind != KindStart || cmds[0].Entry.Media.ID != 20 {
		t.Fatalf("%+v", cmds)
	}
}

func TestGapForceStops(t *testing.T) {
	start := time.Date(2026, 9, 2, 2, 0, 0, 0, time.UTC)
	first := entry(start, 10*time.Second, 1, 7, 19)
	second := entry(start.Add(12*time.Second), 10*time.Second, 2, 7, 20)
	on := EntryKey(7, first)
	played := func(key string) bool { return key == on.String() }
	cmds := Commands(start.Add(10*time.Second), 2*time.Second, 7, []Entry{first, second}, played, alwaysReady, &on)
	if len(kinds(cmds)) != 1 || cmds[0].Kind != KindStop {
		t.Fatalf("%+v", cmds)
	}
}

func TestEmptyPackageStops(t *testing.T) {
	on := Key{ScreenID: 7, MediaID: 19, StartsAt: "2026-09-02T02:00:00Z"}
	now := time.Date(2026, 9, 2, 2, 0, 5, 0, time.UTC)
	cmds := Commands(now, 2*time.Second, 7, nil, nonePlayed, alwaysReady, &on)
	if len(cmds) != 1 || cmds[0].Kind != KindStop {
		t.Fatalf("%+v", cmds)
	}
}

func TestSameCoveringKeyDoesNotRestart(t *testing.T) {
	start := time.Date(2026, 9, 2, 2, 0, 0, 0, time.UTC)
	clip := entry(start, 30*time.Second, 1, 7, 19)
	on := EntryKey(7, clip)
	played := func(key string) bool { return key == on.String() }
	cmds := Commands(start.Add(5*time.Second), 2*time.Second, 7, []Entry{clip}, played, alwaysReady, &on)
	if len(cmds) != 0 {
		t.Fatalf("%+v", cmds)
	}
}

func TestDifferentCoveringKeyStops(t *testing.T) {
	start := time.Date(2026, 9, 2, 2, 0, 0, 0, time.UTC)
	old := entry(start, 60*time.Second, 1, 7, 19)
	neu := entry(start, 60*time.Second, 1, 7, 30)
	on := EntryKey(7, old)
	now := start.Add(30 * time.Second)
	cmds := Commands(now, 2*time.Second, 7, []Entry{neu}, nonePlayed, alwaysReady, &on)
	if len(cmds) < 1 || cmds[0].Kind != KindStop {
		t.Fatalf("%+v", cmds)
	}
	for _, cmd := range cmds {
		if cmd.Kind == KindStart {
			t.Fatalf("started a late replacement: %+v", cmds)
		}
	}
}

func TestWakeRetriesUnplayedClipInsideWindow(t *testing.T) {
	start := time.Date(2026, 9, 2, 2, 0, 0, 0, time.UTC)
	clip := entry(start, 30*time.Second, 1, 7, 19)
	wake := WakeAt(start, 2*time.Second, 7, []Entry{clip}, nonePlayed, neverReady)
	if !wake.Equal(start.Add(200 * time.Millisecond)) {
		t.Fatalf("wake %s", wake)
	}
}
