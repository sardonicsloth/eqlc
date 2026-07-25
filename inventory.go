package main

import (
	"bufio"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
)

// InvItem is one line of a /outputfile inventory dump.
type InvItem struct {
	Location string
	Name     string
	ID       int
	Count    int
}

// FindInventoryDumps returns <Char>-Inventory.txt files, newest first.
// /outputfile writes them to the game's install root; we also check next to
// any discovered log locations (the game root is the Logs dir's parent) and
// common copy-out folders for VM setups.
func FindInventoryDumps() []string {
	home, _ := os.UserHomeDir()
	pats := []string{
		filepath.Join(home, "Documents/eql-gamedata/*-Inventory.txt"),
		filepath.Join(home, "Documents/eqlogs/*-Inventory.txt"),
	}
	if runtime.GOOS == "windows" {
		pats = append(pats,
			`C:\Users\Public\Daybreak Game Company\Installed Games\*\*-Inventory.txt`)
	}
	seen := map[string]bool{}
	var out []string
	add := func(paths []string) {
		for _, p := range paths {
			if !seen[p] {
				seen[p] = true
				out = append(out, p)
			}
		}
	}
	for _, pat := range pats {
		m, _ := filepath.Glob(pat)
		add(m)
	}
	for _, lg := range discoverLogs() { // game root = parent of the Logs dir
		root := filepath.Dir(filepath.Dir(lg))
		m, _ := filepath.Glob(filepath.Join(root, "*-Inventory.txt"))
		add(m)
	}
	sort.Slice(out, func(i, j int) bool { return mtime(out[i]).After(mtime(out[j])) })
	return out
}

// LoadInventory parses a tab-separated inventory dump, skipping empty slots.
func LoadInventory(path string) ([]InvItem, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	var items []InvItem
	sc := bufio.NewScanner(f)
	first := true
	for sc.Scan() {
		ln := strings.TrimRight(sc.Text(), "\r\n")
		if first { // header row
			first = false
			if strings.HasPrefix(strings.ToLower(ln), "location") {
				continue
			}
		}
		p := strings.Split(ln, "\t")
		if len(p) < 4 || p[1] == "Empty" || strings.TrimSpace(p[1]) == "" {
			continue
		}
		id, _ := strconv.Atoi(p[2])
		cnt, _ := strconv.Atoi(p[3])
		if cnt < 1 {
			cnt = 1
		}
		items = append(items, InvItem{Location: p[0], Name: p[1], ID: id, Count: cnt})
	}
	return items, sc.Err()
}

// QuestGroup is quest-item progress inferred from the loot ledger and/or inventory.
type QuestGroup struct {
	Quest  string
	TurnIn string
	Note   string
	Pages  []string // eqlwiki quest page titles
	Items  []string // "Gnoll's Eye ×1 (kept)" style lines
	Warn   bool     // any item sold/merged
}

// QuestProgress groups seen quest items by quest. Inventory items (if provided)
// count as "carried"; loot-ledger items show their auto-loot disposition.
func (m *Meter) QuestProgress(inv []InvItem) []QuestGroup {
	type acc struct {
		qi    QuestInfo
		items map[string]string // item -> status text
		warn  bool
	}
	groups := map[string]*acc{}
	touch := func(item, status string, warn bool) {
		qi, ok := questLookup(item)
		if !ok {
			return
		}
		g := groups[qi.Quest]
		if g == nil {
			g = &acc{qi: qi, items: map[string]string{}}
			groups[qi.Quest] = g
		}
		// prefer "carried" over ledger states; never downgrade a warning
		if cur, exists := g.items[item]; !exists || status == "carried" || cur == "" {
			g.items[item] = status
		}
		if warn {
			g.warn = true
		}
	}
	for _, it := range inv {
		touch(it.Name, "carried", false)
	}
	m.mu.Lock()
	for _, le := range m.loot {
		warn := le.Disposition == "sold" || le.Disposition == "merged"
		touch(le.Item, le.Disposition, warn)
	}
	m.mu.Unlock()

	out := make([]QuestGroup, 0, len(groups))
	for quest, g := range groups {
		items := make([]string, 0, len(g.items))
		for it, st := range g.items {
			items = append(items, it+" ("+st+")")
		}
		sort.Strings(items)
		out = append(out, QuestGroup{Quest: quest, TurnIn: g.qi.TurnIn, Note: g.qi.Note,
			Pages: g.qi.Pages, Items: items, Warn: g.warn})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Warn != out[j].Warn {
			return out[i].Warn // warnings first
		}
		return out[i].Quest < out[j].Quest
	})
	return out
}
