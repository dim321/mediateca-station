package schedule

import (
	"fmt"
	"sort"
	"time"
)

type Media struct {
	ID       int
	URL      string
	MimeType string
}

type Entry struct {
	Position    int
	StartsAt    time.Time
	StartsAtRaw string
	Duration    time.Duration
	ScreenIDs   []int
	Media       Media
}

type Key struct {
	ScreenID int
	MediaID  int
	StartsAt string
}

func (k Key) String() string {
	return fmt.Sprintf("%d:%d:%s", k.ScreenID, k.MediaID, k.StartsAt)
}

func EntryKey(screenID int, entry Entry) Key {
	return Key{ScreenID: screenID, MediaID: entry.Media.ID, StartsAt: entry.StartsAtRaw}
}

type Kind int

const (
	KindStart Kind = iota + 1
	KindStop
	KindSkip
)

type Command struct {
	Kind     Kind
	ScreenID int
	Entry    Entry
	Reason   string
}

type PlayedFunc func(key string) bool
type ReadyFunc func(mediaID int, url string) bool

func ParseEntry(startsAt *string, durationSeconds int, screenIDs []int, mediaID int, rawURL, mime string, position int) (Entry, error) {
	if startsAt == nil || *startsAt == "" || durationSeconds <= 0 || mediaID == 0 || rawURL == "" || mime == "" {
		return Entry{}, fmt.Errorf("ignored_entry")
	}
	parsed, err := time.Parse(time.RFC3339, *startsAt)
	if err != nil {
		return Entry{}, fmt.Errorf("ignored_entry")
	}
	return Entry{
		Position:    position,
		StartsAt:    parsed,
		StartsAtRaw: *startsAt,
		Duration:    time.Duration(durationSeconds) * time.Second,
		ScreenIDs:   append([]int(nil), screenIDs...),
		Media:       Media{ID: mediaID, URL: rawURL, MimeType: mime},
	}, nil
}

func Commands(now time.Time, late time.Duration, screenID int, entries []Entry, played PlayedFunc, ready ReadyFunc, onScreen *Key) []Command {
	play, losers := split(entries, screenID)
	var cmds []Command
	for _, loser := range losers {
		if !now.Before(loser.StartsAt) {
			cmds = append(cmds, Command{Kind: KindSkip, ScreenID: screenID, Entry: loser, Reason: "conflict"})
		}
	}
	covering := coveringEntry(play, now)
	if onScreen != nil && !sameCovering(screenID, covering, *onScreen) {
		if !holdForJoin(now, play, *onScreen, covering, screenID) {
			cmds = append(cmds, Command{Kind: KindStop, ScreenID: screenID})
		}
	}
	if covering != nil {
		cmds = append(cmds, startOrSkip(now, late, screenID, *covering, played, ready, onScreen)...)
	}
	for _, entry := range play {
		if covering != nil && EntryKey(screenID, entry) == EntryKey(screenID, *covering) {
			continue
		}
		if !now.Before(entry.StartsAt) && now.After(entry.StartsAt.Add(late)) && !played(EntryKey(screenID, entry).String()) {
			cmds = append(cmds, Command{Kind: KindSkip, ScreenID: screenID, Entry: entry, Reason: "late"})
		}
	}
	return cmds
}

func WakeAt(now time.Time, late time.Duration, screenID int, entries []Entry, played PlayedFunc, ready ReadyFunc) time.Time {
	play, _ := split(entries, screenID)
	wake := now.Add(24 * time.Hour)
	consider := func(at time.Time) {
		if at.After(now) && at.Before(wake) {
			wake = at
		}
	}
	for _, entry := range play {
		consider(entry.StartsAt)
		consider(entry.StartsAt.Add(entry.Duration))
		consider(entry.StartsAt.Add(late))
	}
	if covering := coveringEntry(play, now); covering != nil && !now.After(covering.StartsAt.Add(late)) && !played(EntryKey(screenID, *covering).String()) {
		soon := now.Add(200 * time.Millisecond)
		if soon.Before(wake) {
			wake = soon
		}
		_ = ready
	}
	return wake
}

func startOrSkip(now time.Time, late time.Duration, screenID int, entry Entry, played PlayedFunc, ready ReadyFunc, onScreen *Key) []Command {
	key := EntryKey(screenID, entry)
	if played(key.String()) || (onScreen != nil && *onScreen == key) {
		return nil
	}
	if now.After(entry.StartsAt.Add(late)) {
		reason := "late"
		if !ready(entry.Media.ID, entry.Media.URL) {
			reason = "missing_file"
		}
		return []Command{{Kind: KindSkip, ScreenID: screenID, Entry: entry, Reason: reason}}
	}
	if !ready(entry.Media.ID, entry.Media.URL) {
		return nil
	}
	return []Command{{Kind: KindStart, ScreenID: screenID, Entry: entry}}
}

func sameCovering(screenID int, covering *Entry, onScreen Key) bool {
	return covering != nil && EntryKey(screenID, *covering) == onScreen
}

func holdForJoin(now time.Time, play []Entry, onScreen Key, covering *Entry, screenID int) bool {
	cur, ok := find(play, onScreen)
	if !ok {
		return false
	}
	next, ok := nextAfter(play, cur)
	if !ok || next.StartsAt.After(cur.StartsAt.Add(cur.Duration).Add(time.Second)) {
		return false
	}
	if now.Before(next.StartsAt) {
		return true
	}
	return covering != nil && EntryKey(screenID, *covering) == EntryKey(screenID, next)
}

func split(entries []Entry, screenID int) (play []Entry, losers []Entry) {
	var list []Entry
	for _, entry := range entries {
		if contains(entry.ScreenIDs, screenID) {
			list = append(list, entry)
		}
	}
	sort.Slice(list, func(i, j int) bool {
		if list[i].StartsAt.Equal(list[j].StartsAt) {
			return list[i].Position < list[j].Position
		}
		return list[i].StartsAt.Before(list[j].StartsAt)
	})
	for i, entry := range list {
		if i > 0 && list[i-1].StartsAt.Equal(entry.StartsAt) {
			losers = append(losers, entry)
			continue
		}
		play = append(play, entry)
	}
	return play, losers
}

func coveringEntry(play []Entry, now time.Time) *Entry {
	var found *Entry
	for i := range play {
		end := play[i].StartsAt.Add(play[i].Duration)
		if !now.Before(play[i].StartsAt) && now.Before(end) {
			if found == nil || play[i].StartsAt.After(found.StartsAt) {
				entry := play[i]
				found = &entry
			}
		}
	}
	return found
}

func find(play []Entry, key Key) (Entry, bool) {
	for _, entry := range play {
		if EntryKey(key.ScreenID, entry) == key {
			return entry, true
		}
	}
	return Entry{}, false
}

func nextAfter(play []Entry, cur Entry) (Entry, bool) {
	for i := range play {
		if play[i].StartsAt.Equal(cur.StartsAt) && play[i].Position == cur.Position && play[i].Media.ID == cur.Media.ID {
			if i+1 < len(play) {
				return play[i+1], true
			}
			return Entry{}, false
		}
	}
	return Entry{}, false
}

func contains(ids []int, want int) bool {
	for _, id := range ids {
		if id == want {
			return true
		}
	}
	return false
}
