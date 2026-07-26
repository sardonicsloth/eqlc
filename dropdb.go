package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// The drop database records every credited kill and every looted item with
// zone + difficulty context and a link back to the exact log line. It is
// cumulative across sessions (persisted as JSONL in the config dir) and
// deduplicated by (log file, line number), so re-seeding a log is idempotent.

// DropEvent is one persisted row: a kill (Item == "") or a loot drop.
type DropEvent struct {
	TS   time.Time `json:"ts"`
	NPC  string    `json:"npc"`
	Item string    `json:"item,omitempty"`
	Cnt  int       `json:"cnt,omitempty"`
	Disp string    `json:"disp,omitempty"`
	Zone string    `json:"zone"`
	Diff string    `json:"diff"`
	File string    `json:"file"`
	Line int       `json:"line"`
	Raw  string    `json:"raw"`
}

// LogRef points at a supporting log line for the UI.
type LogRef struct {
	File string
	Line int
	Raw  string
}

type itemAgg struct {
	Count int // total items dropped (sum of counts)
	Drops int // number of drop events
	Refs  []LogRef
}

type npcKey struct{ NPC, Zone, Diff string }

type npcAgg struct {
	Kills    int
	KillRefs []LogRef
	Items    map[string]*itemAgg
}

const maxRefs = 25

type DropDB struct {
	mu    sync.Mutex
	path  string
	w     *bufio.Writer
	f     *os.File
	seen  map[string]bool // "file:line" dedup
	byNPC map[npcKey]*npcAgg
}

func dropDBPath() string {
	dir, err := os.UserConfigDir()
	if err != nil {
		return ""
	}
	return filepath.Join(dir, "eqlc", "dropdb.jsonl")
}

// OpenDropDB loads the existing database and opens it for appending.
func OpenDropDB() *DropDB {
	db := &DropDB{path: dropDBPath(), seen: map[string]bool{}, byNPC: map[npcKey]*npcAgg{}}
	if db.path == "" {
		return db
	}
	_ = os.MkdirAll(filepath.Dir(db.path), 0o755)
	if f, err := os.Open(db.path); err == nil {
		sc := bufio.NewScanner(f)
		sc.Buffer(make([]byte, 1<<20), 1<<20)
		for sc.Scan() {
			var ev DropEvent
			if json.Unmarshal(sc.Bytes(), &ev) == nil {
				db.apply(&ev, false)
			}
		}
		f.Close()
	}
	if f, err := os.OpenFile(db.path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644); err == nil {
		db.f = f
		db.w = bufio.NewWriter(f)
	}
	return db
}

func (db *DropDB) key(ev *DropEvent) string {
	kind := "k"
	if ev.Item != "" {
		kind = "d"
	}
	return fmt.Sprintf("%s:%d:%s", ev.File, ev.Line, kind)
}

// apply folds an event into the aggregates; returns false if it was a dupe.
func (db *DropDB) apply(ev *DropEvent, persist bool) bool {
	k := db.key(ev)
	if db.seen[k] {
		return false
	}
	db.seen[k] = true

	nk := npcKey{NPC: ev.NPC, Zone: ev.Zone, Diff: ev.Diff}
	agg := db.byNPC[nk]
	if agg == nil {
		agg = &npcAgg{Items: map[string]*itemAgg{}}
		db.byNPC[nk] = agg
	}
	ref := LogRef{File: ev.File, Line: ev.Line, Raw: ev.Raw}
	if ev.Item == "" {
		agg.Kills++
		if len(agg.KillRefs) < maxRefs {
			agg.KillRefs = append(agg.KillRefs, ref)
		}
	} else {
		ia := agg.Items[ev.Item]
		if ia == nil {
			ia = &itemAgg{}
			agg.Items[ev.Item] = ia
		}
		ia.Drops++
		ia.Count += ev.Cnt
		if len(ia.Refs) < maxRefs {
			ia.Refs = append(ia.Refs, ref)
		}
	}
	if persist && db.w != nil {
		if b, err := json.Marshal(ev); err == nil {
			db.w.Write(b)
			db.w.WriteByte('\n')
			db.w.Flush()
		}
	}
	return true
}

// Add records a new event (thread-safe, persisted).
func (db *DropDB) Add(ev *DropEvent) {
	db.mu.Lock()
	db.apply(ev, true)
	db.mu.Unlock()
}

// ---- reporting ----

// DropItemRow is one item's rate for an NPC.
type DropItemRow struct {
	Item  string
	Drops int // events
	Count int // total items
	Rate  float64
	Refs  []LogRef
}

// DropRow is one NPC @ zone/difficulty.
type DropRow struct {
	NPC, Zone, Diff string
	Kills           int
	KillRefs        []LogRef
	Items           []DropItemRow
}

// Report returns aggregated rows filtered by substring (npc or item) and
// difficulty ("" = all), sorted by kill count descending.
func (db *DropDB) Report(query, diff string) []DropRow {
	db.mu.Lock()
	defer db.mu.Unlock()
	q := strings.ToLower(strings.TrimSpace(query))
	var out []DropRow
	for k, agg := range db.byNPC {
		if diff != "" && k.Diff != diff {
			continue
		}
		row := DropRow{NPC: k.NPC, Zone: k.Zone, Diff: k.Diff, Kills: agg.Kills, KillRefs: agg.KillRefs}
		denom := agg.Kills
		if denom == 0 {
			denom = 1
		}
		itemMatch := false
		for item, ia := range agg.Items {
			ir := DropItemRow{Item: item, Drops: ia.Drops, Count: ia.Count,
				Rate: 100 * float64(ia.Drops) / float64(denom), Refs: ia.Refs}
			row.Items = append(row.Items, ir)
			if q != "" && strings.Contains(strings.ToLower(item), q) {
				itemMatch = true
			}
		}
		if q != "" && !itemMatch && !strings.Contains(strings.ToLower(k.NPC), q) &&
			!strings.Contains(strings.ToLower(k.Zone), q) {
			continue
		}
		sort.Slice(row.Items, func(i, j int) bool { return row.Items[i].Drops > row.Items[j].Drops })
		out = append(out, row)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Kills > out[j].Kills })
	return out
}

// Difficulties returns the distinct difficulty labels present in the data.
func (db *DropDB) Difficulties() []string {
	db.mu.Lock()
	defer db.mu.Unlock()
	set := map[string]bool{}
	for k := range db.byNPC {
		set[k.Diff] = true
	}
	var out []string
	for d := range set {
		out = append(out, d)
	}
	sort.Strings(out)
	return out
}

// Count returns total aggregated NPC rows (cheap UI change detector).
func (db *DropDB) Count() int {
	db.mu.Lock()
	defer db.mu.Unlock()
	return len(db.seen)
}

// FetchContext reads lines around a LogRef for display (the "log link").
func FetchContext(ref LogRef, around int) string {
	f, err := os.Open(ref.File)
	if err != nil {
		return ref.Raw + "\n\n(log file not available: " + err.Error() + ")"
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 1<<20), 1<<20)
	var b strings.Builder
	n := 0
	for sc.Scan() {
		n++
		if n >= ref.Line-around && n <= ref.Line+around {
			if n == ref.Line {
				b.WriteString("» ")
			} else {
				b.WriteString("  ")
			}
			b.WriteString(sc.Text())
			b.WriteByte('\n')
		}
		if n > ref.Line+around {
			break
		}
	}
	if b.Len() == 0 {
		return ref.Raw
	}
	return b.String()
}
