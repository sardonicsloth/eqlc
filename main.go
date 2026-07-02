package main

import (
	"bufio"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
	"time"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/app"
	"fyne.io/fyne/v2/canvas"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/dialog"
	"fyne.io/fyne/v2/widget"
)

func main() {
	cfg := loadConfig()

	logPath := flag.String("log", "", "path to eqlog_*.txt (default: saved/auto-detected)")
	replay := flag.String("replay", "", "parse an existing log and print encounter summaries")
	player := flag.String("player", "", "your character name (default: from filename)")
	gapSec := flag.Float64("gap", cfg.Gap, "seconds of inactivity that ends an encounter")
	list := flag.Bool("list", false, "list detected logs and exit")
	flag.Parse()

	// Explicit CLI flags override the saved config.
	set := map[string]bool{}
	flag.Visit(func(f *flag.Flag) { set[f.Name] = true })
	if set["gap"] {
		cfg.Gap = *gapSec
	}

	if *list {
		logs := discoverLogs()
		if len(logs) == 0 {
			fmt.Println("No eqlog_*.txt files found.")
			return
		}
		for _, p := range logs {
			fmt.Printf("%s  %8.1f MB  %s\n",
				mtime(p).Format("2006-01-02 15:04"), float64(fileSize(p))/1e6, p)
		}
		return
	}

	// Resolve which log to follow: explicit -log wins; otherwise ALWAYS the
	// newest eqlog (so switching characters is automatic). Saved config is only
	// a last-resort fallback if nothing is detected.
	path := *replay
	if path == "" && set["log"] {
		path = *logPath
	}
	if path == "" {
		if logs := discoverLogs(); len(logs) > 0 {
			path = logs[0]
		}
	}
	if path == "" {
		path = cfg.LogPath
	}
	if path == "" {
		fmt.Fprintln(os.Stderr, "No eqlog_*.txt found. Pass -log PATH. (Is /log on in game?)")
		os.Exit(1)
	}
	if _, err := os.Stat(path); err != nil {
		fmt.Fprintf(os.Stderr, "log not found: %s\n", path)
		os.Exit(1)
	}

	// Player name: -player flag wins, else derive from the chosen log's filename
	// (not the saved config, so a new character is picked up automatically).
	name := ""
	if set["player"] {
		name = *player
	}
	if name == "" {
		name = playerFromPath(path)
	}

	if *replay != "" {
		runReplay(path, name, time.Duration(cfg.Gap*float64(time.Second)))
		return
	}

	cfg.LogPath, cfg.Player = path, name
	runGUI(cfg)
}

// ---- live GUI --------------------------------------------------------------

// controller owns the live meter so log/player/gap can be hot-swapped from the
// Settings tab. Reloading stops the old tailer and re-seeds from the chosen log.
type controller struct {
	mu    sync.Mutex
	meter *Meter
	stop  chan struct{}
	mine  bool
}

func (c *controller) get() *Meter {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.meter
}

func (c *controller) mineOnly() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.mine
}

func (c *controller) setMine(b bool) {
	c.mu.Lock()
	c.mine = b
	c.mu.Unlock()
}

func (c *controller) reload(path, player string, gap time.Duration) {
	c.mu.Lock()
	if c.stop != nil {
		close(c.stop)
	}
	m := NewMeter(player, gap)
	stop := make(chan struct{})
	c.meter, c.stop = m, stop
	c.mu.Unlock()

	go func() {
		seedFile(path, m) // bulk-parse existing log to build history
		lines := make(chan string, 1024)
		go func() { _ = tail(path, false, lines, stop) }()
		for {
			select {
			case <-stop:
				return
			case ln, ok := <-lines:
				if !ok {
					return
				}
				m.AddLine(ln)
			}
		}
	}()
}

func runGUI(cfg *Config) {
	path := cfg.LogPath
	player := cfg.Player
	if player == "" {
		player = playerFromPath(path)
	}
	gap := time.Duration(cfg.Gap * float64(time.Second))
	ctrl := &controller{}
	ctrl.setMine(cfg.MineOnly)
	ctrl.reload(path, player, gap)

	a := app.New()
	a.Settings().SetTheme(darkTheme{})
	w := a.NewWindow("eqdps")
	w.Resize(fyne.NewSize(620, 470))

	// ---- Live tab ----
	headline := canvas.NewText(player+" — loading…", textCol)
	headline.TextSize = 17
	headline.TextStyle = fyne.TextStyle{Bold: true}
	sub := canvas.NewText("log: "+filepath.Base(path), subCol)
	sub.TextSize = 12
	liveBars := NewBarList()
	liveTab := container.NewBorder(
		container.NewPadded(container.NewVBox(headline, sub)),
		nil, nil, nil,
		container.NewVScroll(liveBars),
	)

	// ---- shared view state (Recount-style mode + sort) ----
	mode := ModeDamage
	sortBy := SortTotal

	// ---- History tab ----
	var summaries []EncSummary // all fights (kept in sync each tick)
	var filtered []EncSummary  // summaries after the search filter
	query := ""
	histSel := -1 // currently shown encounter Index, or -1
	histHeadline := canvas.NewText("← select a fight", textCol)
	histHeadline.TextSize = 15
	histHeadline.TextStyle = fyne.TextStyle{Bold: true}
	histBars := NewBarList()

	showHist := func() {
		if histSel < 0 {
			return
		}
		snap := ctrl.get().SnapshotIndex(histSel, mode, sortBy)
		histHeadline.Text = fmt.Sprintf("%s  —  %.0f dps · %s · %.1fs",
			modeName(mode), snap.RaidDPS, orQ(snap.Target), snap.Duration)
		histHeadline.Refresh()
		rows := snap.Rows
		if ctrl.mineOnly() && mode == ModeDamage {
			rows = onlyMine(rows)
		}
		histBars.SetRows(rows)
	}

	encList := widget.NewList(
		func() int { return len(filtered) },
		func() fyne.CanvasObject { return widget.NewLabel("") },
		func(id widget.ListItemID, o fyne.CanvasObject) {
			if id >= 0 && id < len(filtered) {
				o.(*widget.Label).SetText(summaryText(filtered[id]))
			}
		},
	)
	encList.OnSelected = func(id widget.ListItemID) {
		if id < 0 || id >= len(filtered) {
			return
		}
		histSel = filtered[id].Index
		showHist()
	}

	applyFilter := func() {
		q := strings.ToLower(strings.TrimSpace(query))
		out := summaries[:0:0] // fresh backing array (don't alias summaries)
		if q == "" {
			out = append(out, summaries...)
		} else {
			for _, s := range summaries {
				if strings.Contains(strings.ToLower(s.Target), q) ||
					strings.Contains(s.Start.Format("15:04:05"), q) {
					out = append(out, s)
				}
			}
		}
		filtered = out
		encList.Refresh()
	}

	search := widget.NewEntry()
	search.SetPlaceHolder("filter fights by mob or time…")
	search.OnChanged = func(s string) { query = s; applyFilter() }
	histRight := container.NewBorder(
		container.NewPadded(histHeadline), nil, nil, nil,
		container.NewVScroll(histBars),
	)
	leftPane := container.NewBorder(search, nil, nil, nil, encList)
	split := container.NewHSplit(leftPane, histRight)
	split.SetOffset(0.45)

	// ---- Settings tab ----
	logByLabel := map[string]string{}
	var logOpts []string
	addLog := func(p string) string {
		lbl := filepath.Base(p)
		if _, ok := logByLabel[lbl]; !ok {
			logByLabel[lbl] = p
			logOpts = append(logOpts, lbl)
		}
		return lbl
	}
	for _, p := range discoverLogs() {
		addLog(p)
	}
	addLog(path)
	chosenPath := path

	logSel := widget.NewSelect(logOpts, func(lbl string) {
		if p, ok := logByLabel[lbl]; ok {
			chosenPath = p
		}
	})
	logSel.SetSelected(filepath.Base(path))
	browse := widget.NewButton("Browse…", func() {
		dialog.ShowFileOpen(func(rc fyne.URIReadCloser, err error) {
			if err != nil || rc == nil {
				return
			}
			p := rc.URI().Path()
			rc.Close()
			lbl := addLog(p)
			logSel.SetOptions(logOpts)
			logSel.SetSelected(lbl)
			chosenPath = p
		}, w)
	})

	playerEntry := widget.NewEntry()
	playerEntry.SetText(player)
	playerEntry.SetPlaceHolder("(auto-detected from log filename)")

	gapVal := gap.Seconds()
	gapLabel := widget.NewLabel(fmt.Sprintf("Encounter gap: %.0f s", gapVal))
	gapSlider := widget.NewSlider(1, 30)
	gapSlider.Step = 1
	gapSlider.SetValue(gapVal)
	gapSlider.OnChanged = func(v float64) {
		gapVal = v
		gapLabel.SetText(fmt.Sprintf("Encounter gap: %.0f s", v))
	}

	mineCheck := widget.NewCheck("Show only my damage", func(b bool) {
		ctrl.setMine(b)
		cfg.MineOnly = b
		cfg.save()
	})
	mineCheck.SetChecked(cfg.MineOnly)
	pinCheck := widget.NewCheck("Keep window on top", func(b bool) {
		setOnTop(b)
		cfg.OnTop = b
		cfg.save()
	})
	pinCheck.SetChecked(cfg.OnTop)

	status := widget.NewLabel("")
	apply := widget.NewButton("Apply & reload log", func() {
		pl := strings.TrimSpace(playerEntry.Text)
		if pl == "" {
			pl = playerFromPath(chosenPath)
			playerEntry.SetText(pl)
		}
		gp := gapVal
		ctrl.reload(chosenPath, pl, time.Duration(gp*float64(time.Second)))
		player, path = pl, chosenPath
		cfg.LogPath, cfg.Player, cfg.Gap = chosenPath, pl, gp
		cfg.save()
		status.SetText("Reloaded " + filepath.Base(chosenPath) + " as " + pl +
			" (gap " + fmt.Sprintf("%.0fs", gp) + ")")
	})
	resetBtn := widget.NewButton("Reset live session", func() { ctrl.get().Reset() })

	settingsTab := container.NewVScroll(container.NewPadded(container.NewVBox(
		widget.NewLabelWithStyle("Log file", fyne.TextAlignLeading, fyne.TextStyle{Bold: true}),
		container.NewBorder(nil, nil, nil, browse, logSel),
		widget.NewLabelWithStyle("Character name", fyne.TextAlignLeading, fyne.TextStyle{Bold: true}),
		playerEntry,
		gapLabel, gapSlider,
		widget.NewSeparator(),
		mineCheck, pinCheck,
		widget.NewSeparator(),
		container.NewHBox(apply, resetBtn),
		status,
		widget.NewLabel("Note: changing log / character / gap re-parses the whole log."),
	)))

	modeSel := widget.NewSelect([]string{"Damage Done", "Enemy Damage", "Damage Taken"}, func(s string) {
		switch s {
		case "Enemy Damage":
			mode = ModeEnemy
		case "Damage Taken":
			mode = ModeTaken
		default:
			mode = ModeDamage
		}
		showHist() // History updates now; Live updates on the next tick
	})
	modeSel.SetSelected("Damage Done")
	sortSel := widget.NewSelect([]string{"by Total", "by DPS", "by Name"}, func(s string) {
		switch s {
		case "by DPS":
			sortBy = SortDPS
		case "by Name":
			sortBy = SortName
		default:
			sortBy = SortTotal
		}
		showHist()
	})
	sortSel.SetSelected("by Total")
	toolbar := container.NewHBox(
		widget.NewLabel("View:"), modeSel,
		widget.NewLabel("Sort:"), sortSel,
	)

	tabs := container.NewAppTabs(
		container.NewTabItem("Live", liveTab),
		container.NewTabItem("History", split),
		container.NewTabItem("Settings", settingsTab),
	)
	w.SetContent(container.NewBorder(container.NewPadded(toolbar), nil, nil, nil, tabs))

	// ---- refresh loop ----
	a.Lifecycle().SetOnStarted(func() {
		if cfg.OnTop {
			go func() { time.Sleep(600 * time.Millisecond); setOnTop(true) }()
		}
		var lastMeter *Meter
		prevCount := -1
		go func() {
			t := time.NewTicker(300 * time.Millisecond)
			defer t.Stop()
			for range t.C {
				fyne.Do(func() {
					m := ctrl.get()
					snap := m.Snapshot(mode, sortBy)
					sums := m.Summaries()
					if m != lastMeter { // a reload happened: clear the history view
						lastMeter = m
						prevCount = -1
						histSel = -1
						histHeadline.Text = "← select a fight"
						histHeadline.Refresh()
						histBars.SetRows(nil)
					}
					rows := snap.Rows
					if ctrl.mineOnly() && mode == ModeDamage {
						rows = onlyMine(rows)
					}
					if snap.RaidTotal == 0 {
						headline.Text = player + " — waiting for combat…"
						sub.Text = fmt.Sprintf("log: %s   ·   %d fights logged",
							filepath.Base(path), len(sums))
					} else {
						headline.Text = fmt.Sprintf("%s — %.0f dps", modeName(mode), snap.RaidDPS)
						sub.Text = fmt.Sprintf("%s   ·   %.1fs   ·   total %s   ·   %d fights",
							orQ(snap.Target), snap.Duration, human(snap.RaidTotal), snap.Fights)
					}
					headline.Refresh()
					sub.Refresh()
					liveBars.SetRows(rows)

					summaries = sums
					if len(sums) != prevCount {
						prevCount = len(sums)
						applyFilter()
					}
				})
			}
		}()
	})

	w.ShowAndRun()
}

// Reset starts a fresh session (clears current + history).
func (m *Meter) Reset() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.cur = newEncounter(m.player)
	m.history = nil
}

// setOnTop pins the window above others. Linux/X11 via wmctrl; no-op elsewhere.
func setOnTop(on bool) {
	if runtime.GOOS != "linux" {
		return
	}
	if _, err := exec.LookPath("wmctrl"); err != nil {
		return
	}
	action := "add"
	if !on {
		action = "remove"
	}
	_ = exec.Command("wmctrl", "-r", "eqdps", "-b", action+",above").Run()
}

// ---- replay (no GUI) -------------------------------------------------------

func runReplay(path, player string, gap time.Duration) {
	m := NewMeter(player, gap)
	f, err := os.Open(path)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 1<<20), 1<<20)
	for sc.Scan() {
		m.AddLine(sc.Text())
	}

	encs := append(m.history, m.cur)
	n := 0
	for _, e := range encs {
		if e.empty() || len(e.friendlies()) == 0 {
			continue
		}
		n++
		printEncounter(n, e)
	}
	fmt.Printf("\n%d encounter(s) in %s\n", n, filepath.Base(path))
}

func printEncounter(idx int, e *Encounter) {
	dur := e.durationSecs()
	d := dur
	if d <= 0 {
		d = 1
	}
	crew := e.friendlies()
	sort.Slice(crew, func(i, j int) bool { return crew[i].Total > crew[j].Total })
	raid := 0
	for _, c := range crew {
		raid += c.Total
	}
	denom := raid
	if denom == 0 {
		denom = 1
	}
	tgts := make([]string, 0, len(e.enemies))
	for t := range e.enemies {
		tgts = append(tgts, t)
	}
	sort.Strings(tgts)
	target := "?"
	if len(tgts) > 0 {
		target = tgts[0]
		if len(tgts) > 1 {
			target += fmt.Sprintf(" +%d", len(tgts)-1)
		}
	}

	fmt.Printf("\n=== Fight #%d  @ %s  (%.0fs)  vs %s ===\n",
		idx, e.Start.Format("15:04:05"), dur, target)
	fmt.Printf("    raid: %s dmg  |  %.0f dps\n", human(raid), float64(raid)/d)
	for _, c := range crew {
		cp := 0.0
		if c.Hits > 0 {
			cp = 100 * float64(c.Crits) / float64(c.Hits)
		}
		top := ""
		best := 0
		for ab, v := range c.Abilities {
			if v > best {
				best, top = v, ab
			}
		}
		fmt.Printf("    %-22s %8.0f dps  %10s  (%3.0f%%)  crit %3.0f%%  top:%s\n",
			c.Name, float64(c.Total)/d, human(c.Total), 100*float64(c.Total)/float64(denom), cp, top)
	}
}
