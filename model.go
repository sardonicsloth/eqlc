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

	// heals: "You healed Josh for 5 hit points by Lifetap." /
	//        "Soandso healed you for 20 hit points." /
	//        "You have been healed for 30 points."
	healRe     = regexp.MustCompile(`^(.+?) (?:have |has )?healed (.+?) for (\d+) (?:hit )?points?(?: of damage)?(?: by (.+?))?\.(?: \(([^)]*)\))?\s*$`)
	healSelfRe = regexp.MustCompile(`^You have been healed for (\d+) points?\.\s*$`)

	// stuns / CC events (the log rarely names the source; attributed by last-hit)
	// loot lines (EQL auto-loot writes the disposition into the log)
	lootKeptRe   = regexp.MustCompile(`^--You have looted (?:a |an )?(\d+ )?(.+?)(?: from (.+?))?\.--\s*$`)
	lootSoldRe   = regexp.MustCompile(`^You looted (?:a |an )?(\d+ )?(.+?) from (.+?) and sold it for (.+?)\.\s*$`)
	lootStoredRe = regexp.MustCompile(`^You looted (?:a |an )?(\d+ )?(.+?) from (.+?) and stored it in your (.+?)\.?\s*$`)
	lootMergedRe = regexp.MustCompile(`^You looted (?:a |an )?(\d+ )?(.+?) from (.+?) to create (?:a |an )?(.+?)\.?\s*$`)

	// session trackers
	coinRe    = regexp.MustCompile(`^You receive (.+?)(?: from the corpse| as your split)?\.\s*$`)
	// EQL: "You gain experience! (2.347%)" — percent optional (client setting)
	xpRe      = regexp.MustCompile(`^You gain (?:party )?experience!+(?:\s*\((\d+(?:\.\d+)?)%\))?\s*$`)
	levelRe   = regexp.MustCompile(`^You have gained a level! Welcome to level (\d+)!\s*$`)
	skillUpRe = regexp.MustCompile(`^You have become better at (.+?)! \((\d+)\)\s*$`)
	zoneRe    = regexp.MustCompile(`^You have entered (.+?)\.\s*$`)
	factionRe = regexp.MustCompile(`^Your faction standing with (.+?) (?:got worse|got better|has been adjusted by (-?\d+))\.?\s*$`)

	stunRe      = regexp.MustCompile(`^(.+?) (?:(?:is|are) stunned|staggers)[.!]?\s*$`)
	mezRe       = regexp.MustCompile(`^(.+?) (?:has been|is|are) mesmerized[.!]?\s*$`)
	rootRe      = regexp.MustCompile(`^(.+?) (?:has been|is|are) rooted(?: in place)?[.!]?\s*$`)
	fearRe      = regexp.MustCompile(`^(.+?) flees? in terror[.!]?\s*$`)
	interruptRe = regexp.MustCompile(`^(.+?)'s casting is interrupted[.!]\s*$`)
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

// EventKind classifies a parsed line.
type EventKind int

const (
	KDamage EventKind = iota
	KHeal
	KCC // stun/mez/root/fear/interrupt; Amount=1, Ability=cc type
)

// Event is a single damage tick, heal, or CC application.
type Event struct {
	TS       time.Time
	Kind     EventKind
	Attacker string // for KCC this may be "" (attributed later by last-hit)
	Target   string
	Ability  string
	Amount   int
	Crit     bool
}

// Death is a recorded kill: who died and (if known) to whom.
type Death struct {
	TS     time.Time
	Victim string
	Killer string
}

// LootEvent is one looted item and what the auto-loot did with it.
type LootEvent struct {
	TS          time.Time
	Item        string
	Count       int
	Source      string // corpse it came from
	Disposition string // kept | sold | stored | merged
	Detail      string // price / storage / merged-into
}

// parseCoin converts "1 gold, 7 silver and 9 copper" (or "free") to copper.
func parseCoin(s string) int {
	total := 0
	for _, m := range regexp.MustCompile(`(\d+) (platinum|gold|silver|copper)`).FindAllStringSubmatch(s, -1) {
		n, _ := strconv.Atoi(m[1])
		switch m[2] {
		case "platinum":
			total += n * 1000
		case "gold":
			total += n * 100
		case "silver":
			total += n * 10
		default:
			total += n
		}
	}
	return total
}

// fmtCoin renders copper as "Xp Yg Zs Wc" (skipping zero denominations).
func fmtCoin(c int) string {
	if c == 0 {
		return "0c"
	}
	parts := []string{}
	for _, d := range []struct {
		div int
		suf string
	}{{1000, "p"}, {100, "g"}, {10, "s"}, {1, "c"}} {
		if v := c / d.div; v > 0 {
			parts = append(parts, strconv.Itoa(v)+d.suf)
			c %= d.div
		}
	}
	return strings.Join(parts, " ")
}

// Session accumulates play-session stats across all encounters (EQBuddy-style).
type Session struct {
	Kills, MyDeaths          int
	VendorCopper, LootCopper int
	XPCount                  int
	XPPct                    float64
	SkillUps                 []string
	Zones                    []string
	Faction                  []string
	LastZone                 string
}

// ParseLoot returns a loot event for auto-loot log lines, or nil.
func ParseLoot(line string) *LootEvent {
	m := lineRe.FindStringSubmatch(line)
	if m == nil {
		return nil
	}
	ts, ok := parseTS(m[1])
	if !ok {
		return nil
	}
	body := m[2]
	mk := func(cnt, item, src, disp, detail string) *LootEvent {
		n := 1
		if c := strings.TrimSpace(cnt); c != "" {
			n, _ = strconv.Atoi(c)
			if n < 1 {
				n = 1
			}
		}
		return &LootEvent{TS: ts, Item: strings.TrimSpace(item), Count: n,
			Source: strings.TrimSuffix(strings.TrimSpace(src), "'s corpse"),
			Disposition: disp, Detail: detail}
	}
	if d := lootSoldRe.FindStringSubmatch(body); d != nil {
		return mk(d[1], d[2], d[3], "sold", d[4])
	}
	if d := lootStoredRe.FindStringSubmatch(body); d != nil {
		return mk(d[1], d[2], d[3], "stored", d[4])
	}
	if d := lootMergedRe.FindStringSubmatch(body); d != nil {
		return mk(d[1], d[2], d[3], "merged", "-> "+d[4])
	}
	if d := lootKeptRe.FindStringSubmatch(body); d != nil {
		return mk(d[1], d[2], d[3], "kept", "")
	}
	return nil
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

func ParseLine(line, player string) (*Event, *Death) {
	m := lineRe.FindStringSubmatch(line)
	if m == nil {
		return nil, nil
	}
	ts, ok := parseTS(m[1])
	if !ok {
		return nil, nil
	}
	body := m[2]

	// melee / non-melee: "X slashes Y for N points of [non-melee ]damage."
	if d := dmgRe.FindStringSubmatch(body); d != nil {
		ability := d[2]
		if d[5] != "" {
			ability = "spell"
		}
		amt, _ := strconv.Atoi(d[4])
		return mkEvent(ts, d[1], d[3], amt, ability, hasCrit(d[6]), player), nil
	}

	// typed spell/proc/elemental: "X hit Y for N points of <element> damage by <Spell>."
	if d := typedRe.FindStringSubmatch(body); d != nil {
		amt, _ := strconv.Atoi(d[3])
		return mkEvent(ts, d[1], d[2], amt, d[4], hasCrit(d[5]), player), nil
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
		return mkEvent(ts, rawAtk, d[1], amt, ability+" (dot)", false, player), nil
	}

	// heals: "You healed Josh for 5 hit points by Lifetap."
	if d := healRe.FindStringSubmatch(body); d != nil {
		amt, _ := strconv.Atoi(d[3])
		ability := strings.TrimSpace(d[4])
		if ability == "" {
			ability = "heal"
		}
		ev := mkEvent(ts, d[1], d[2], amt, ability, hasCrit(d[5]), player)
		ev.Kind = KHeal
		return ev, nil
	}
	if d := healSelfRe.FindStringSubmatch(body); d != nil {
		amt, _ := strconv.Atoi(d[1])
		ev := mkEvent(ts, "Unknown", "you", amt, "heal", false, player)
		ev.Kind = KHeal
		return ev, nil
	}

	// CC: source is usually not named in the line — attributed later by last-hit.
	for _, cc := range []struct {
		re   *regexp.Regexp
		name string
	}{
		{stunRe, "stun"}, {mezRe, "mez"}, {rootRe, "root"}, {fearRe, "fear"}, {interruptRe, "interrupt"},
	} {
		if d := cc.re.FindStringSubmatch(body); d != nil {
			return &Event{TS: ts, Kind: KCC, Target: norm(d[1], player), Ability: cc.name, Amount: 1}, nil
		}
	}

	// deaths
	if d := youSlainRe.FindStringSubmatch(body); d != nil {
		return nil, &Death{TS: ts, Victim: canonArticle(d[1]), Killer: player}
	}
	if d := slainByRe.FindStringSubmatch(body); d != nil {
		return nil, &Death{TS: ts, Victim: norm(d[1], player), Killer: norm(d[2], player)}
	}
	if d := diesRe.FindStringSubmatch(body); d != nil {
		return nil, &Death{TS: ts, Victim: canonArticle(d[1])}
	}
	return nil, nil
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

type lastHit struct {
	who string
	ts  time.Time
}

type Encounter struct {
	Player  string
	Start   time.Time
	Last    time.Time
	byName  map[string]*Combatant // outgoing damage, keyed by attacker
	taken   map[string]*Combatant // incoming damage, keyed by victim (abilities = attackers)
	healers map[string]*Combatant // healing done, keyed by healer (abilities = spells)
	ccBy    map[string]*Combatant // CC events, keyed by (attributed) source; Total = count
	deaths  []Death
	hitBy   map[string]lastHit // target -> most recent attacker (for CC attribution)
	enemies map[string]bool    // things the player damaged
}

func newEncounter(player string) *Encounter {
	return &Encounter{
		Player:  player,
		byName:  map[string]*Combatant{},
		taken:   map[string]*Combatant{},
		healers: map[string]*Combatant{},
		ccBy:    map[string]*Combatant{},
		hitBy:   map[string]lastHit{},
		enemies: map[string]bool{},
	}
}

func getC(m map[string]*Combatant, name string) *Combatant {
	c := m[name]
	if c == nil {
		c = &Combatant{Name: name, Targets: map[string]bool{}, Abilities: map[string]int{}}
		m[name] = c
	}
	return c
}

func (e *Encounter) empty() bool { return e.Start.IsZero() }

func (e *Encounter) add(ev *Event) {
	if e.Start.IsZero() {
		e.Start = ev.TS
	}
	e.Last = ev.TS

	switch ev.Kind {
	case KHeal:
		getC(e.healers, ev.Attacker).add(ev)
		return
	case KCC:
		// the log line names only the victim; credit the most recent attacker
		// of that target (within 4s), else "Unknown".
		src := ev.Attacker
		if src == "" {
			if lh, ok := e.hitBy[ev.Target]; ok && ev.TS.Sub(lh.ts) <= 4*time.Second {
				src = lh.who
			} else {
				src = "Unknown"
			}
		}
		c := getC(e.ccBy, src)
		c.Total++ // Total = number of CC events
		c.Hits++
		if c.First.IsZero() {
			c.First = ev.TS
		}
		c.Last = ev.TS
		c.Targets[ev.Target] = true
		c.Abilities[ev.Ability]++
		return
	}

	c := getC(e.byName, ev.Attacker)
	c.add(ev)
	e.hitBy[ev.Target] = lastHit{who: ev.Attacker, ts: ev.TS}

	// damage taken, keyed by victim; its "abilities" map records who hit them.
	t := getC(e.taken, ev.Target)
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
	loot    []LootEvent
	sess    Session
	zonesSeen map[string]bool

	// XP-tracker raw event streams (log-ordered)
	spans      []Span // contiguous activity; a 30min+ quiet gap starts a new one
	xpEvents   []XPGain
	killTimes  []time.Time
	levelTimes []LevelUp
	lastTS     time.Time
}

// XPGain is one experience message; Pct is 0 when the client omits the number.
type XPGain struct {
	TS  time.Time
	Pct float64
}

// Span is one contiguous stretch of log activity (≈ one login session).
type Span struct{ Start, End time.Time }

// LevelUp is a ding.
type LevelUp struct {
	TS    time.Time
	Level int
}

const sessionGap = 30 * time.Minute

// noteTS extends the current activity span or starts a new one.
func (m *Meter) noteTS(ts time.Time) {
	if ts.IsZero() {
		return
	}
	if len(m.spans) == 0 || ts.Sub(m.lastTS) > sessionGap {
		m.spans = append(m.spans, Span{Start: ts, End: ts})
	} else {
		m.spans[len(m.spans)-1].End = ts
	}
	m.lastTS = ts
}

func NewMeter(player string, gap time.Duration) *Meter {
	return &Meter{player: player, gap: gap, cur: newEncounter(player), zonesSeen: map[string]bool{}}
}

// addSessionLine handles session-stat lines; returns true if the line was one.
func (m *Meter) addSessionLine(ts time.Time, body string) bool {
	switch {
	case xpRe.MatchString(body):
		m.sess.XPCount++
		pct := 0.0
		if d := xpRe.FindStringSubmatch(body); d[1] != "" {
			pct, _ = strconv.ParseFloat(d[1], 64)
		}
		m.sess.XPPct += pct
		m.xpEvents = append(m.xpEvents, XPGain{TS: ts, Pct: pct})
	case levelRe.MatchString(body):
		lv, _ := strconv.Atoi(levelRe.FindStringSubmatch(body)[1])
		m.levelTimes = append(m.levelTimes, LevelUp{TS: ts, Level: lv})
	case zoneRe.MatchString(body):
		z := zoneRe.FindStringSubmatch(body)[1]
		m.sess.LastZone = z
		if !m.zonesSeen[z] {
			m.zonesSeen[z] = true
			m.sess.Zones = append(m.sess.Zones, z)
		}
	case skillUpRe.MatchString(body):
		d := skillUpRe.FindStringSubmatch(body)
		m.sess.SkillUps = append(m.sess.SkillUps, d[1]+" ("+d[2]+")")
	case factionRe.MatchString(body):
		m.sess.Faction = append(m.sess.Faction, factionRe.FindStringSubmatch(body)[0])
		if len(m.sess.Faction) > 500 {
			m.sess.Faction = append(m.sess.Faction[:0:0], m.sess.Faction[len(m.sess.Faction)-250:]...)
		}
	case coinRe.MatchString(body):
		m.sess.LootCopper += parseCoin(coinRe.FindStringSubmatch(body)[1])
	default:
		return false
	}
	return true
}

// SessionSnapshot returns a copy of the session stats.
func (m *Meter) SessionSnapshot() Session {
	m.mu.Lock()
	defer m.mu.Unlock()
	s := m.sess
	s.SkillUps = append([]string(nil), m.sess.SkillUps...)
	s.Zones = append([]string(nil), m.sess.Zones...)
	s.Faction = append([]string(nil), m.sess.Faction...)
	return s
}

func (m *Meter) AddLine(line string) {
	// activity + session-stat lines first: every timestamped line (chat included)
	// feeds session detection, and xp/level/zone/faction lines are handled here.
	if lm := lineRe.FindStringSubmatch(line); lm != nil {
		ts, tok := parseTS(lm[1])
		m.mu.Lock()
		if tok {
			m.noteTS(ts)
		}
		handled := m.addSessionLine(ts, lm[2])
		m.mu.Unlock()
		if handled {
			return
		}
	}
	if le := ParseLoot(line); le != nil {
		m.mu.Lock()
		m.loot = append(m.loot, *le)
		if le.Disposition == "sold" {
			m.sess.VendorCopper += parseCoin(le.Detail)
		}
		if len(m.loot) > 20000 { // keep memory bounded on huge logs
			m.loot = append(m.loot[:0:0], m.loot[len(m.loot)-10000:]...)
		}
		m.mu.Unlock()
		return
	}
	ev, death := ParseLine(line, m.player)
	if ev == nil && death == nil {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	ts := time.Time{}
	if ev != nil {
		ts = ev.TS
	} else {
		ts = death.TS
	}
	if !m.cur.empty() && ts.Sub(m.cur.Last) > m.gap {
		if len(m.cur.friendlies()) > 0 {
			m.history = append(m.history, m.cur)
		}
		m.cur = newEncounter(m.player)
	}
	if death != nil {
		if death.Victim == m.player {
			m.sess.MyDeaths++
		} else if death.Killer == m.player || m.cur.enemies[death.Victim] {
			m.sess.Kills++
			m.killTimes = append(m.killTimes, death.TS)
		}
		if !m.cur.empty() {
			m.cur.deaths = append(m.cur.deaths, *death)
			m.cur.Last = death.TS
		}
		return
	}
	m.cur.add(ev)
}

// ViewMode selects which combatants/values to show (Recount-style).
type ViewMode int

const (
	ModeDamage  ViewMode = iota // friendlies' outgoing damage
	ModeEnemy                   // enemies' outgoing damage
	ModeTaken                   // damage taken by friendlies
	ModeHealing                 // healing done (HPS)
	ModeCC                      // stuns/mez/root/fear/interrupts (event counts)
	ModeDeaths                  // deaths this encounter
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
	case ModeHealing:
		for _, c := range enc.healers {
			crew = append(crew, c)
		}
	case ModeCC:
		for _, c := range enc.ccBy {
			crew = append(crew, c)
		}
	case ModeDeaths:
		// synthesize one Combatant per victim; abilities = killers, Total = death count
		byVictim := map[string]*Combatant{}
		for _, d := range enc.deaths {
			c := byVictim[d.Victim]
			if c == nil {
				c = &Combatant{Name: d.Victim, Targets: map[string]bool{}, Abilities: map[string]int{}}
				byVictim[d.Victim] = c
			}
			c.Total++
			c.Hits++
			if c.First.IsZero() {
				c.First = d.TS
			}
			c.Last = d.TS
			k := d.Killer
			if k == "" {
				k = "unknown"
			}
			c.Abilities["killed by "+k]++
		}
		for _, c := range byVictim {
			crew = append(crew, c)
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

// DayStat aggregates XP activity for one calendar day.
type DayStat struct {
	Day    string
	XP     int
	XPPct  float64
	Kills  int
	Levels []int
	Active time.Duration
}

// SessStat aggregates one login session (activity span).
type SessStat struct {
	Start, End time.Time
	XP, Kills  int
	XPPct      float64
	Levels     []int
}

func countBetween(times []time.Time, a, b time.Time) int {
	n := 0
	for _, t := range times {
		if !t.Before(a) && !t.After(b) {
			n++
		}
	}
	return n
}

// XPReport builds per-day and per-session XP aggregations (newest first).
func (m *Meter) XPReport() ([]DayStat, []SessStat) {
	m.mu.Lock()
	defer m.mu.Unlock()

	var sess []SessStat
	for _, sp := range m.spans {
		s := SessStat{Start: sp.Start, End: sp.End,
			Kills: countBetween(m.killTimes, sp.Start, sp.End)}
		for _, xe := range m.xpEvents {
			if !xe.TS.Before(sp.Start) && !xe.TS.After(sp.End) {
				s.XP++
				s.XPPct += xe.Pct
			}
		}
		for _, lu := range m.levelTimes {
			if !lu.TS.Before(sp.Start) && !lu.TS.After(sp.End) {
				s.Levels = append(s.Levels, lu.Level)
			}
		}
		sess = append(sess, s)
	}

	dayIdx := map[string]int{}
	var days []DayStat
	dayOf := func(t time.Time) *DayStat {
		k := t.Format("2006-01-02")
		if i, ok := dayIdx[k]; ok {
			return &days[i]
		}
		days = append(days, DayStat{Day: k})
		dayIdx[k] = len(days) - 1
		return &days[len(days)-1]
	}
	for _, xe := range m.xpEvents {
		d := dayOf(xe.TS)
		d.XP++
		d.XPPct += xe.Pct
	}
	for _, t := range m.killTimes {
		dayOf(t).Kills++
	}
	for _, lu := range m.levelTimes {
		d := dayOf(lu.TS)
		d.Levels = append(d.Levels, lu.Level)
	}
	for _, sp := range m.spans { // active time credited to the span's start day
		dayOf(sp.Start).Active += sp.End.Sub(sp.Start)
	}

	// newest first
	for i, j := 0, len(sess)-1; i < j; i, j = i+1, j-1 {
		sess[i], sess[j] = sess[j], sess[i]
	}
	sort.Slice(days, func(i, j int) bool { return days[i].Day > days[j].Day })
	return days, sess
}

// LootCount returns how many loot events exist (cheap change-detector for the UI).
func (m *Meter) LootCount() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.loot)
}

// LootSnapshot returns loot events newest-first, filtered by substring and
// (optionally) to known quest items only.
func (m *Meter) LootSnapshot(query string, questOnly bool, limit int) []LootEvent {
	m.mu.Lock()
	defer m.mu.Unlock()
	q := strings.ToLower(strings.TrimSpace(query))
	out := make([]LootEvent, 0, 256)
	for i := len(m.loot) - 1; i >= 0 && len(out) < limit; i-- {
		le := m.loot[i]
		if questOnly {
			if _, ok := questLookup(le.Item); !ok {
				continue
			}
		}
		if q != "" && !strings.Contains(strings.ToLower(le.Item), q) &&
			!strings.Contains(strings.ToLower(le.Source), q) {
			continue
		}
		out = append(out, le)
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
