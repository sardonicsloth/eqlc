package main

import (
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// ---- line parsing (ported from the validated Python regexes) ----------------

var (
	lineRe = regexp.MustCompile(`^\[(.+?)\] (.*)$`)
	wsRe   = regexp.MustCompile(`\s+`)

	verbs = `hits?|slashe?s?|crushe?s?|pierces?|bashe?s?|kicks?|bites?|mauls?|` +
		`punche?s?|backstabs?|strikes?|gores?|claws?|slams?|rends?|smashe?s?|` +
		`stings?|slices?`
	dmgRe = regexp.MustCompile(`^(.+?) (` + verbs +
		`) (.+?) for (\d+) points? of (non-melee )?damage\.(?: \(([^)]*)\))?\s*$`)
	// typed spell/proc/elemental damage: "X hit Y for N points of fire damage by Spell."
	typedRe = regexp.MustCompile(
		`^(.+?) hits? (.+?) for (\d+) points of (?:[a-z-]+ )?damage by (.+?)\.(?: \(([^)]*)\))?\s*$`)
	dotRe      = regexp.MustCompile(`^(.+?) has taken (\d+) damage from (.+?)\.\s*$`)
	slainByRe  = regexp.MustCompile(`^(.+?) has been slain by (.+?)!\s*$`)
	youSlainRe = regexp.MustCompile(`^You have slain (.+?)!\s*$`)
	diesRe     = regexp.MustCompile(`^(.+?) dies\.\s*$`)
)

func parseTS(s string) (time.Time, bool) {
	s = wsRe.ReplaceAllString(strings.TrimSpace(s), " ")
	t, err := time.ParseInLocation("Mon Jan 2 15:04:05 2006", s, time.Local)
	if err != nil {
		return time.Time{}, false
	}
	return t, true
}

func norm(name, player string) string {
	n := strings.TrimSpace(name)
	switch strings.ToLower(n) {
	case "you", "your", "yourself":
		return player
	}
	return canonArticle(n)
}

// canonArticle lowercases a leading article so the same mob matches whether it
// appears as the sentence subject ("An orc hits YOU") or object ("you hit an orc").
func canonArticle(s string) string {
	for _, a := range []string{"A ", "An ", "The "} {
		if strings.HasPrefix(s, a) {
			return strings.ToLower(s[:1]) + s[1:]
		}
	}
	return s
}

// Event is a single damage tick.
type Event struct {
	TS       time.Time
	Attacker string
	Target   string
	Ability  string
	Amount   int
	Crit     bool
}

// ParseLine returns (event, isDeath). Both zero => line ignored.
func hasCrit(flags string) bool {
	f := strings.ToLower(flags)
	return strings.Contains(f, "critical") || strings.Contains(f, "crippling")
}

// petOwner detects an owned pet/warder attacker ("Soandso`s warder", "YOUR pet")
// and returns the owner plus a category label, so pet damage folds into the owner.
func petOwner(name, player string) (owner, label string, ok bool) {
	if strings.HasPrefix(strings.ToLower(name), "your ") {
		return player, petLabel(name[5:]), true
	}
	if i := strings.Index(name, "`s "); i > 0 {
		return name[:i], petLabel(name[i+3:]), true
	}
	return "", "", false
}

func petLabel(desc string) string {
	desc = strings.TrimSpace(desc)
	if desc == "" || strings.EqualFold(desc, "pet") {
		return "pet"
	}
	return "pet (" + desc + ")"
}

func mkEvent(ts time.Time, rawAtk, rawTgt string, amt int, ability string, crit bool, player string) *Event {
	atk := norm(rawAtk, player)
	if owner, lbl, ok := petOwner(atk, player); ok {
		atk, ability = owner, lbl // fold pet damage into the owner under a "pet" category
	}
	return &Event{TS: ts, Attacker: atk, Target: norm(rawTgt, player), Ability: ability, Amount: amt, Crit: crit}
}

func ParseLine(line, player string) (*Event, bool) {
	m := lineRe.FindStringSubmatch(line)
	if m == nil {
		return nil, false
	}
	ts, ok := parseTS(m[1])
	if !ok {
		return nil, false
	}
	body := m[2]

	// melee / non-melee: "X slashes Y for N points of [non-melee ]damage."
	if d := dmgRe.FindStringSubmatch(body); d != nil {
		ability := d[2]
		if d[5] != "" {
			ability = "spell"
		}
		amt, _ := strconv.Atoi(d[4])
		return mkEvent(ts, d[1], d[3], amt, ability, hasCrit(d[6]), player), false
	}

	// typed spell/proc/elemental: "X hit Y for N points of <element> damage by <Spell>."
	if d := typedRe.FindStringSubmatch(body); d != nil {
		amt, _ := strconv.Atoi(d[3])
		return mkEvent(ts, d[1], d[2], amt, d[4], hasCrit(d[5]), player), false
	}

	// damage-over-time: "Y has taken N damage from <source>."
	if d := dotRe.FindStringSubmatch(body); d != nil {
		src := d[3]
		var rawAtk, ability string
		switch {
		case strings.HasPrefix(strings.ToLower(src), "your "):
			rawAtk, ability = "you", src[5:]
		case strings.Contains(src, "'s "):
			parts := strings.SplitN(src, "'s ", 2)
			rawAtk, ability = parts[0], parts[1]
		default:
			rawAtk, ability = "Unknown", src
		}
		amt, _ := strconv.Atoi(d[2])
		return mkEvent(ts, rawAtk, d[1], amt, ability+" (dot)", false, player), false
	}

	if youSlainRe.MatchString(body) || slainByRe.MatchString(body) || diesRe.MatchString(body) {
		return nil, true
	}
	return nil, false
}

// ---- accumulation ----------------------------------------------------------

type Combatant struct {
	Name      string
	Total     int
	Hits      int
	Crits     int
	Best      int
	First     time.Time
	Last      time.Time
	Targets   map[string]bool
	Abilities map[string]int
}

func (c *Combatant) add(ev *Event) {
	c.Total += ev.Amount
	c.Hits++
	if ev.Crit {
		c.Crits++
	}
	if ev.Amount > c.Best {
		c.Best = ev.Amount
	}
	if c.First.IsZero() {
		c.First = ev.TS
	}
	c.Last = ev.TS
	c.Targets[ev.Target] = true
	c.Abilities[ev.Ability] += ev.Amount
}

// dur is the combatant's active span in seconds (min 1, for per-actor DPS).
func (c *Combatant) dur() float64 {
	if c.First.IsZero() {
		return 1
	}
	if s := c.Last.Sub(c.First).Seconds(); s > 1 {
		return s
	}
	return 1
}

func (c *Combatant) dps() float64 { return float64(c.Total) / c.dur() }

type Encounter struct {
	Player  string
	Start   time.Time
	Last    time.Time
	byName  map[string]*Combatant // outgoing damage, keyed by attacker
	taken   map[string]*Combatant // incoming damage, keyed by victim (abilities = attackers)
	enemies map[string]bool       // things the player damaged
}

func newEncounter(player string) *Encounter {
	return &Encounter{
		Player:  player,
		byName:  map[string]*Combatant{},
		taken:   map[string]*Combatant{},
		enemies: map[string]bool{},
	}
}

func (e *Encounter) empty() bool { return e.Start.IsZero() }

func (e *Encounter) add(ev *Event) {
	if e.Start.IsZero() {
		e.Start = ev.TS
	}
	e.Last = ev.TS

	c := e.byName[ev.Attacker]
	if c == nil {
		c = &Combatant{Name: ev.Attacker, Targets: map[string]bool{}, Abilities: map[string]int{}}
		e.byName[ev.Attacker] = c
	}
	c.add(ev)

	// damage taken, keyed by victim; its "abilities" map records who hit them.
	t := e.taken[ev.Target]
	if t == nil {
		t = &Combatant{Name: ev.Target, Targets: map[string]bool{}, Abilities: map[string]int{}}
		e.taken[ev.Target] = t
	}
	t.Total += ev.Amount
	t.Hits++
	if ev.Crit {
		t.Crits++
	}
	if ev.Amount > t.Best {
		t.Best = ev.Amount
	}
	if t.First.IsZero() {
		t.First = ev.TS
	}
	t.Last = ev.TS
	t.Targets[ev.Attacker] = true
	t.Abilities[ev.Attacker] += ev.Amount

	if ev.Attacker == e.Player {
		e.enemies[ev.Target] = true
	}
}

func (e *Encounter) durationSecs() float64 {
	if e.Start.IsZero() {
		return 0
	}
	d := e.Last.Sub(e.Start).Seconds()
	if d < 0 {
		d = 0
	}
	return d
}

// friendlies: you + anyone who damaged one of your enemies (pets, group);
// excludes the mobs themselves.
func (e *Encounter) friendlies() []*Combatant {
	var out []*Combatant
	for name, c := range e.byName {
		if e.enemies[name] {
			continue
		}
		if name == e.Player || sharesTarget(c.Targets, e.enemies) {
			out = append(out, c)
		}
	}
	return out
}

func sharesTarget(a, b map[string]bool) bool {
	for k := range a {
		if b[k] {
			return true
		}
	}
	return false
}

// ---- snapshot for the UI ---------------------------------------------------

type AbilityRow struct {
	Name  string
	Total int
	Pct   float64
}

type Row struct {
	Name      string
	DPS       float64
	Total     int
	Pct       float64
	CritPct   float64
	Best      int
	IsYou     bool
	IsEnemy   bool
	Abilities []AbilityRow
}

type Snapshot struct {
	Player    string
	Target    string
	Duration  float64
	RaidDPS   float64
	RaidTotal int
	Fights    int
	Rows      []Row
}

// ---- thread-safe meter -----------------------------------------------------

type Meter struct {
	mu      sync.Mutex
	player  string
	gap     time.Duration
	cur     *Encounter
	history []*Encounter
}

func NewMeter(player string, gap time.Duration) *Meter {
	return &Meter{player: player, gap: gap, cur: newEncounter(player)}
}

func (m *Meter) AddLine(line string) {
	ev, _ := ParseLine(line, m.player)
	if ev == nil {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if !m.cur.empty() && ev.TS.Sub(m.cur.Last) > m.gap {
		if len(m.cur.friendlies()) > 0 {
			m.history = append(m.history, m.cur)
		}
		m.cur = newEncounter(m.player)
	}
	m.cur.add(ev)
}

// ViewMode selects which combatants/values to show (Recount-style).
type ViewMode int

const (
	ModeDamage ViewMode = iota // friendlies' outgoing damage
	ModeEnemy                  // enemies' outgoing damage
	ModeTaken                  // damage taken by friendlies
)

// SortBy selects the row ordering.
type SortBy int

const (
	SortTotal SortBy = iota
	SortDPS
	SortName
)

func friendlySet(enc *Encounter) map[string]bool {
	s := map[string]bool{}
	for _, c := range enc.friendlies() {
		s[c.Name] = true
	}
	return s
}

func sortCrew(crew []*Combatant, by SortBy) {
	switch by {
	case SortName:
		sort.Slice(crew, func(i, j int) bool {
			return strings.ToLower(crew[i].Name) < strings.ToLower(crew[j].Name)
		})
	case SortDPS:
		sort.Slice(crew, func(i, j int) bool { return crew[i].dps() > crew[j].dps() })
	default:
		sort.Slice(crew, func(i, j int) bool { return crew[i].Total > crew[j].Total })
	}
}

// snapshotEncounterView builds a Snapshot for one encounter under a view mode +
// sort. Callers hold the lock for the live encounter; history is immutable.
func snapshotEncounterView(enc *Encounter, mode ViewMode, by SortBy) Snapshot {
	dur := enc.durationSecs()
	d := dur
	if d <= 0 {
		d = 1
	}

	fset := friendlySet(enc)
	isEnemy := func(name string) bool { return name != enc.Player && !fset[name] }

	var crew []*Combatant
	switch mode {
	case ModeEnemy:
		for name, c := range enc.byName {
			if isEnemy(name) {
				crew = append(crew, c)
			}
		}
	case ModeTaken:
		for name, c := range enc.taken {
			if fset[name] {
				crew = append(crew, c)
			}
		}
	default: // ModeDamage — ALL damage-dealers (you + group + pets + enemies) in one
		// ranked list, color-coded, exactly like Recount's Damage Done.
		for _, c := range enc.byName {
			crew = append(crew, c)
		}
	}
	sortCrew(crew, by)

	total := 0
	for _, c := range crew {
		total += c.Total
	}
	denom := total
	if denom == 0 {
		denom = 1
	}

	rows := make([]Row, 0, len(crew))
	for _, c := range crew {
		cp := 0.0
		if c.Hits > 0 {
			cp = 100 * float64(c.Crits) / float64(c.Hits)
		}
		ctot := c.Total
		if ctot == 0 {
			ctot = 1
		}
		abil := make([]AbilityRow, 0, len(c.Abilities))
		for name, tot := range c.Abilities {
			abil = append(abil, AbilityRow{Name: name, Total: tot, Pct: 100 * float64(tot) / float64(ctot)})
		}
		sort.Slice(abil, func(i, j int) bool { return abil[i].Total > abil[j].Total })
		rows = append(rows, Row{
			Name:      c.Name,
			DPS:       c.dps(),
			Total:     c.Total,
			Pct:       100 * float64(c.Total) / float64(denom),
			CritPct:   cp,
			Best:      c.Best,
			IsYou:     c.Name == enc.Player,
			IsEnemy:   isEnemy(c.Name),
			Abilities: abil,
		})
	}

	return Snapshot{
		Player:    enc.Player,
		Target:    targetLabel(enc),
		Duration:  dur,
		RaidDPS:   float64(total) / d,
		RaidTotal: total,
		Rows:      rows,
	}
}

func targetLabel(enc *Encounter) string {
	tgts := make([]string, 0, len(enc.enemies))
	for t := range enc.enemies {
		tgts = append(tgts, t)
	}
	sort.Strings(tgts)
	if len(tgts) > 3 {
		return strings.Join(tgts[:3], ", ") + " +" + strconv.Itoa(len(tgts)-3)
	}
	return strings.Join(tgts, ", ")
}

// Snapshot of the current (live) encounter under a view mode + sort.
func (m *Meter) Snapshot(mode ViewMode, by SortBy) Snapshot {
	m.mu.Lock()
	defer m.mu.Unlock()
	s := snapshotEncounterView(m.cur, mode, by)
	s.Fights = len(m.history)
	return s
}

// EncSummary is one selectable row in the History list.
type EncSummary struct {
	Index   int // stable id: position in [history..., cur]
	Start   time.Time
	Target  string
	Dur     float64
	YourDPS float64
	RaidDPS float64
	Live    bool
}

// Summaries lists every non-empty encounter, oldest first. Index values are
// stable (history is append-only), so selections stay valid as combat continues.
func (m *Meter) Summaries() []EncSummary {
	m.mu.Lock()
	defer m.mu.Unlock()
	all := make([]*Encounter, 0, len(m.history)+1)
	all = append(all, m.history...)
	liveIdx := -1
	if !m.cur.empty() {
		all = append(all, m.cur)
		liveIdx = len(all) - 1
	}
	out := make([]EncSummary, 0, len(all))
	for idx, e := range all {
		crew := e.friendlies()
		if len(crew) == 0 {
			continue
		}
		dur := e.durationSecs()
		d := dur
		if d <= 0 {
			d = 1
		}
		raid, you := 0, 0
		for _, c := range crew {
			raid += c.Total
			if c.Name == e.Player {
				you = c.Total
			}
		}
		out = append(out, EncSummary{
			Index:   idx,
			Start:   e.Start,
			Target:  targetLabel(e),
			Dur:     dur,
			YourDPS: float64(you) / d,
			RaidDPS: float64(raid) / d,
			Live:    idx == liveIdx,
		})
	}
	return out
}

// SnapshotIndex returns one encounter (EncSummary.Index) under a view mode + sort.
func (m *Meter) SnapshotIndex(i int, mode ViewMode, by SortBy) Snapshot {
	m.mu.Lock()
	defer m.mu.Unlock()
	enc := m.cur
	if i >= 0 && i < len(m.history) {
		enc = m.history[i]
	}
	s := snapshotEncounterView(enc, mode, by)
	s.Fights = len(m.history)
	return s
}
