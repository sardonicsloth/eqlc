package main

import (
	"bufio"
	"flag"
	"fmt"
	"image/color"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
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
	w := a.NewWindow("EQLC — EverQuest Legends Companion")
	w.Resize(fyne.NewSize(680, 480))

	// ---- Live tab ----
	headline := canvas.NewText(player+" — loading…", textCol)
	headline.TextSize = 17
	headline.TextStyle = fyne.TextStyle{Bold: true}
	sub := widget.NewLabel("log: " + filepath.Base(path))
	sub.Truncation = fyne.TextTruncateEllipsis
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
	histHeadline := widget.NewLabelWithStyle("← select a fight", fyne.TextAlignLeading, fyne.TextStyle{Bold: true})
	histHeadline.Truncation = fyne.TextTruncateEllipsis // long fight names must not jam the split divider
	histBars := NewBarList()

	showHist := func() {
		if histSel < 0 {
			return
		}
		snap := ctrl.get().SnapshotIndex(histSel, mode, sortBy)
		histHeadline.Text = fmt.Sprintf("%s  —  %s · %s · %.1fs",
			modeName(mode), headlineVal(mode, snap), orQ(snap.Target), snap.Duration)
		histHeadline.Refresh()
		rows := snap.Rows
		if ctrl.mineOnly() && mode == ModeDamage {
			rows = onlyMine(rows)
		}
		histBars.SetMode(mode)
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

	// ---- Loot tab (quest-item tracker) ----
	lootQuery := ""
	lootQuestOnly := false
	var lootRows []LootEvent
	lootAlert := canvas.NewText("", color.NRGBA{R: 0xe8, G: 0x6c, B: 0x5c, A: 0xff})
	lootAlert.TextSize = 13
	lootAlert.TextStyle = fyne.TextStyle{Bold: true}
	lootList := widget.NewList(
		func() int { return len(lootRows) },
		func() fyne.CanvasObject {
			l := widget.NewLabel("")
			l.Truncation = fyne.TextTruncateEllipsis
			return l
		},
		func(id widget.ListItemID, o fyne.CanvasObject) {
			if id >= 0 && id < len(lootRows) {
				o.(*widget.Label).SetText(lootLineText(lootRows[id]))
			}
		},
	)
	lootList.OnSelected = func(id widget.ListItemID) {
		if id >= 0 && id < len(lootRows) {
			le := lootRows[id]
			if qi, ok := questLookup(le.Item); ok {
				showQuestDetail(w, le.Item+" — "+qi.Quest, qi,
					[]string{le.Item + " (" + le.Disposition + ")"})
			}
		}
		lootList.UnselectAll()
	}
	refreshLoot := func() {
		lootRows = ctrl.get().LootSnapshot(lootQuery, lootQuestOnly, 400)
		warn := ""
		for _, le := range lootRows {
			if le.Disposition == "sold" || le.Disposition == "merged" {
				if _, ok := questLookup(le.Item); ok {
					warn = "⚠ quest item " + le.Disposition + ": " + le.Item +
						" (" + le.TS.Format("15:04") + ") — check your loot filter!"
					break
				}
			}
		}
		lootAlert.Text = warn
		lootAlert.Refresh()
		lootList.Refresh()
	}
	lootSearch := widget.NewEntry()
	lootSearch.SetPlaceHolder("filter loot by item or mob…")
	lootSearch.OnChanged = func(s string) { lootQuery = s; refreshLoot() }
	lootQuestChk := widget.NewCheck("Quest items only", func(b bool) { lootQuestOnly = b; refreshLoot() })
	lootTab := container.NewBorder(
		container.NewVBox(container.NewBorder(nil, nil, nil, lootQuestChk, lootSearch), lootAlert),
		nil, nil, nil, lootList)

	// dashboard-facing loot digests, refreshed alongside the Loot tab
	questSum, questWarn, lootLast := "no quest items yet", "", ""
	baseRefreshLoot := refreshLoot
	refreshLoot = func() {
		baseRefreshLoot()
		q := ctrl.get().LootSnapshot("", true, 400)
		if len(q) > 0 {
			questSum = fmt.Sprintf("%d recent · last: %s", len(q), q[0].Item)
		}
		questWarn = ""
		for _, le := range q {
			if le.Disposition == "sold" || le.Disposition == "merged" {
				questWarn = "⚠ " + le.Item + " was " + le.Disposition + "!"
				break
			}
		}
		if all := ctrl.get().LootSnapshot("", false, 1); len(all) > 0 {
			lootLast = all[0].Item
		}
	}

	// ---- Session tab ----
	mkSection := func(t string) *widget.Label {
		l := widget.NewLabel("—")
		l.Wrapping = fyne.TextWrapWord
		_ = t
		return l
	}
	sessMoney := mkSection("money")
	sessSkills := mkSection("skills")
	sessZones := mkSection("zones")
	sessFaction := mkSection("faction")
	sessionTab := container.NewVScroll(container.NewPadded(container.NewVBox(
		widget.NewLabelWithStyle("Money", fyne.TextAlignLeading, fyne.TextStyle{Bold: true}), sessMoney,
		widget.NewLabelWithStyle("Skill-ups", fyne.TextAlignLeading, fyne.TextStyle{Bold: true}), sessSkills,
		widget.NewLabelWithStyle("Zones visited", fyne.TextAlignLeading, fyne.TextStyle{Bold: true}), sessZones,
		widget.NewLabelWithStyle("Recent faction hits", fyne.TextAlignLeading, fyne.TextStyle{Bold: true}), sessFaction,
	)))

	// ---- Quests tab ----
	var questGroups []QuestGroup
	var invItems []InvItem
	questList := widget.NewList(
		func() int { return len(questGroups) },
		func() fyne.CanvasObject {
			l := widget.NewLabel("")
			l.Truncation = fyne.TextTruncateEllipsis
			return l
		},
		func(id widget.ListItemID, o fyne.CanvasObject) {
			if id >= 0 && id < len(questGroups) {
				g := questGroups[id]
				mark := "     "
				if g.Warn {
					mark = "⚠ "
				}
				o.(*widget.Label).SetText(mark + g.Quest + "  —  " + strings.Join(g.Items, ", "))
			}
		})
	questList.OnSelected = func(id widget.ListItemID) {
		if id >= 0 && id < len(questGroups) {
			g := questGroups[id]
			showQuestDetail(w, g.Quest,
				QuestInfo{Quest: g.Quest, TurnIn: g.TurnIn, Note: g.Note, Pages: g.Pages},
				g.Items)
		}
		questList.UnselectAll()
	}
	refreshQuests := func() { questGroups = ctrl.get().QuestProgress(invItems); questList.Refresh() }
	questsTab := container.NewBorder(
		widget.NewLabel("Quest items from your log + inventory dumps · tap for turn-in info"),
		nil, nil, nil, questList)

	// ---- Inventory tab ----
	var invRows []InvItem
	invInfo := widget.NewLabel("No inventory dump found. In game: /outputfile inventory — then Sync from VM and Refresh.")
	invInfo.Wrapping = fyne.TextWrapWord
	invList := widget.NewList(
		func() int { return len(invRows) },
		func() fyne.CanvasObject {
			l := widget.NewLabel("")
			l.Truncation = fyne.TextTruncateEllipsis
			return l
		},
		func(id widget.ListItemID, o fyne.CanvasObject) {
			if id >= 0 && id < len(invRows) {
				it := invRows[id]
				star := "     "
				if _, ok := questLookup(it.Name); ok {
					star = "★ "
				}
				cnt := ""
				if it.Count > 1 {
					cnt = fmt.Sprintf(" ×%d", it.Count)
				}
				o.(*widget.Label).SetText(star + it.Name + cnt + "   [" + it.Location + "]")
			}
		})
	loadInv := func(p string) {
		items, err := LoadInventory(p)
		if err != nil {
			invInfo.SetText("couldn't read " + filepath.Base(p) + ": " + err.Error())
			return
		}
		invItems, invRows = items, items
		qc := 0
		for _, it := range items {
			if _, ok := questLookup(it.Name); ok {
				qc++
			}
		}
		invInfo.SetText(fmt.Sprintf("%s — %d items · %d quest items (★)", filepath.Base(p), len(items), qc))
		invList.Refresh()
		refreshQuests()
	}
	invSel := widget.NewSelect(nil, nil)
	refreshDumps := func() {
		dumps := FindInventoryDumps()
		opts := make([]string, len(dumps))
		byLbl := map[string]string{}
		for i, d := range dumps {
			opts[i] = filepath.Base(d)
			byLbl[opts[i]] = d
		}
		invSel.Options = opts
		invSel.OnChanged = func(lbl string) {
			if p, ok := byLbl[lbl]; ok {
				loadInv(p)
			}
		}
		invSel.Refresh()
		if len(dumps) > 0 {
			invSel.SetSelected(opts[0]) // newest — triggers load
		}
	}
	refreshInvBtn := widget.NewButton("Refresh", refreshDumps)
	invBar := container.NewHBox(invSel, refreshInvBtn)
	// optional user-configured sync hook (e.g. a script that copies game files
	// out of a VM); only shown when config.json sets sync_cmd.
	if cmd := strings.TrimSpace(cfg.SyncCmd); cmd != "" {
		invBar.Add(widget.NewButton("Sync", func() {
			sh, flag := "/bin/sh", "-c"
			if runtime.GOOS == "windows" {
				sh, flag = "cmd", "/C"
			}
			_ = exec.Command(sh, flag, cmd).Start()
			invInfo.SetText("Sync command started — give it a moment, then hit Refresh.")
		}))
	}
	invTab := container.NewBorder(
		container.NewVBox(invBar, invInfo),
		nil, nil, nil, invList)
	refreshDumps()

	// ---- XP tab ----
	xpDays := widget.NewLabel("—")
	xpDays.Wrapping = fyne.TextWrapWord
	xpSessions := widget.NewLabel("—")
	xpSessions.Wrapping = fyne.TextWrapWord
	xpTab := container.NewVScroll(container.NewPadded(container.NewVBox(
		widget.NewLabelWithStyle("By day", fyne.TextAlignLeading, fyne.TextStyle{Bold: true}),
		xpDays,
		widget.NewLabelWithStyle("By session (30min+ quiet = logout)", fyne.TextAlignLeading, fyne.TextStyle{Bold: true}),
		xpSessions,
	)))
	prevXPKey := ""
	refreshXP := func() {
		days, sess := ctrl.get().XPReport()
		key := fmt.Sprintf("%d/%d", len(days), len(sess))
		if len(sess) > 0 {
			key += sess[0].End.String() + strconv.Itoa(sess[0].XP)
		}
		if key == prevXPKey {
			return
		}
		prevXPKey = key
		var b strings.Builder
		for i, d := range days {
			if i >= 30 {
				b.WriteString(fmt.Sprintf("… and %d more days\n", len(days)-30))
				break
			}
			lv := ""
			if len(d.Levels) > 0 {
				lv = fmt.Sprintf(" · dinged %v", d.Levels)
			}
			hrs := d.Active.Hours()
			xp := fmt.Sprintf("XP ×%d", d.XP)
			rate := ""
			if d.XPPct > 0 {
				xp = fmt.Sprintf("XP %.1f%%", d.XPPct)
				if hrs > 0.05 {
					rate = fmt.Sprintf(" · %.1f%%/hr", d.XPPct/hrs)
				}
			} else if hrs > 0.05 {
				rate = fmt.Sprintf(" · %.0f xp/hr", float64(d.XP)/hrs)
			}
			b.WriteString(fmt.Sprintf("%s   %s · %d kills · %.1fh%s%s\n",
				d.Day, xp, d.Kills, hrs, rate, lv))
		}
		xpDays.SetText(orDash(b.String()))
		b.Reset()
		for i, s := range sess {
			if i >= 40 {
				b.WriteString(fmt.Sprintf("… and %d more sessions\n", len(sess)-40))
				break
			}
			dur := s.End.Sub(s.Start).Hours()
			lv := ""
			if len(s.Levels) > 0 {
				lv = fmt.Sprintf(" · dinged %v", s.Levels)
			}
			xp := fmt.Sprintf("XP ×%d", s.XP)
			rate := ""
			if s.XPPct > 0 {
				xp = fmt.Sprintf("XP %.1f%%", s.XPPct)
				if dur > 0.05 {
					rate = fmt.Sprintf(" · %.1f%%/hr", s.XPPct/dur)
				}
			} else if dur > 0.05 {
				rate = fmt.Sprintf(" · %.0f xp/hr", float64(s.XP)/dur)
			}
			b.WriteString(fmt.Sprintf("%s %s–%s (%.1fh)   %s · %d kills%s%s\n",
				s.Start.Format("Jan 2"), s.Start.Format("15:04"), s.End.Format("15:04"),
				dur, xp, s.Kills, rate, lv))
		}
		xpSessions.SetText(orDash(b.String()))
	}

	// ---- Home dashboard ----
	var tabs *container.AppTabs
	sel := func(i int) func() { return func() { tabs.SelectIndex(i) } }
	cardCombat := NewDashCard("⚔  Combat", sel(1))
	cardKills := NewDashCard("💀  Kills & Deaths", sel(2))
	cardLoot := NewDashCard("🎒  Loot & Money", sel(3))
	cardQuest := NewDashCard("★  Quest Items", sel(4))
	cardXP := NewDashCard("📈  XP", sel(6))
	cardSess := NewDashCard("📜  Session", sel(7))
	homeSub := widget.NewLabel(player + " · " + filepath.Base(path))
	homeSub.Truncation = fyne.TextTruncateEllipsis
	homeTab := container.NewVScroll(container.NewPadded(container.NewVBox(
		homeSub, cardCombat, cardKills, cardLoot, cardQuest, cardXP, cardSess)))

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

	var onLogPick func(string) // wired below once player/gap widgets exist
	logSel := widget.NewSelect(logOpts, func(lbl string) {
		if p, ok := logByLabel[lbl]; ok {
			chosenPath = p
		}
		if onLogPick != nil {
			onLogPick(lbl)
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

	// The live log switcher lives in the toolbar (logSel + browse); wire its
	// auto-apply now that the player/gap widgets exist.
	onLogPick = func(lbl string) {
		p, ok := logByLabel[lbl]
		if !ok {
			return
		}
		pl := playerFromPath(p)
		playerEntry.SetText(pl)
		ctrl.reload(p, pl, time.Duration(gapVal*float64(time.Second)))
		player, path = pl, p
		cfg.LogPath, cfg.Player = p, pl
		cfg.save()
		status.SetText("Switched to " + lbl + " as " + pl)
	}

	settingsTab := container.NewVScroll(container.NewPadded(container.NewVBox(
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

	modeSel := widget.NewSelect([]string{
		"Damage Done", "Enemy Damage", "Damage Taken",
		"Healing Done", "CC / Stuns", "Deaths",
	}, func(s string) {
		switch s {
		case "Enemy Damage":
			mode = ModeEnemy
		case "Damage Taken":
			mode = ModeTaken
		case "Healing Done":
			mode = ModeHealing
		case "CC / Stuns":
			mode = ModeCC
		case "Deaths":
			mode = ModeDeaths
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
		widget.NewLabel("Log:"), logSel, browse,
	)

	tabs = container.NewAppTabs(
		container.NewTabItem("Home", homeTab),
		container.NewTabItem("Live", liveTab),
		container.NewTabItem("History", split),
		container.NewTabItem("Loot", lootTab),
		container.NewTabItem("Quests", questsTab),
		container.NewTabItem("Inventory", invTab),
		container.NewTabItem("XP", xpTab),
		container.NewTabItem("Session", sessionTab),
		container.NewTabItem("Settings", settingsTab),
	)

	// ---- mini / pill mode ----
	pillText := canvas.NewText("EQLC", textCol)
	pillText.TextSize = 14
	pillText.TextStyle = fyne.TextStyle{Bold: true}
	var fullContent fyne.CanvasObject
	restoreBtn := widget.NewButton("⤢", func() {
		w.SetContent(fullContent)
		w.Resize(fyne.NewSize(680, 480))
	})
	pillBar := container.NewBorder(nil, nil, nil, restoreBtn, container.NewCenter(pillText))
	miniBtn := widget.NewButton("Mini", func() {
		w.SetContent(pillBar)
		w.Resize(fyne.NewSize(430, 52))
	})
	toolbar.Add(miniBtn)

	fullContent = container.NewBorder(container.NewPadded(toolbar), nil, nil, nil, tabs)
	w.SetContent(fullContent)

	// ---- refresh loop ----
	a.Lifecycle().SetOnStarted(func() {
		if cfg.OnTop {
			go func() { time.Sleep(600 * time.Millisecond); setOnTop(true) }()
		}
		var lastMeter *Meter
		prevCount := -1
		prevLoot := -1
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
						headline.Text = fmt.Sprintf("%s — %s", modeName(mode), headlineVal(mode, snap))
						sub.Text = fmt.Sprintf("%s   ·   %.1fs   ·   total %s   ·   %d fights",
							orQ(snap.Target), snap.Duration, human(snap.RaidTotal), snap.Fights)
					}
					headline.Refresh()
					sub.Refresh()
					liveBars.SetMode(mode)
					liveBars.SetRows(rows)

					summaries = sums
					if len(sums) != prevCount {
						prevCount = len(sums)
						applyFilter()
					}
					if lc := m.LootCount(); lc != prevLoot {
						prevLoot = lc
						refreshLoot()
						refreshQuests()
					}

					// ---- dashboard cards + session tab ----
					sess := m.SessionSnapshot()
					combat := "idle"
					if snap.RaidTotal > 0 {
						combat = headlineVal(mode, snap) + " · vs " + orQ(snap.Target)
					}
					cardCombat.Set(fmt.Sprintf("%s · %d fights", combat, len(sums)), "")
					cardKills.Set(fmt.Sprintf("%d kills · %d deaths", sess.Kills, sess.MyDeaths),
						tern(sess.MyDeaths > 0, fmt.Sprintf("%d death(s) this session", sess.MyDeaths), ""))
					lootSum := fmt.Sprintf("%d items · vendor %s · coin %s",
						m.LootCount(), fmtCoin(sess.VendorCopper), fmtCoin(sess.LootCopper))
					if lootLast != "" {
						lootSum += " · last: " + lootLast
					}
					cardLoot.Set(lootSum, "")
					cardQuest.Set(questSum, questWarn)
					zs := sess.LastZone
					if zs == "" {
						zs = "?"
					}
					xpSum := fmt.Sprintf("XP ×%d this log", sess.XPCount)
					if sess.XPPct > 0 {
						xpSum = fmt.Sprintf("%.0f%% total xp this log (%.1f levels' worth) · ×%d gains",
							sess.XPPct, sess.XPPct/100, sess.XPCount)
					}
					cardXP.Set(xpSum+" · tap for breakdown", "")
					refreshXP()
					cardSess.Set(fmt.Sprintf("XP ×%d · %d skill-ups · %d zones · in %s",
						sess.XPCount, len(sess.SkillUps), len(sess.Zones), zs), "")
					homeSub.SetText(player + " · " + filepath.Base(path) + " · " + zs)

					warnFlag := ""
					if questWarn != "" {
						warnFlag = "  ⚠"
					}
					pillText.Text = fmt.Sprintf("⚔ %s   💀 %d/%d   🎒 %d%s",
						combat, sess.Kills, sess.MyDeaths, m.LootCount(), warnFlag)
					pillText.Refresh()

					sessMoney.SetText("vendor sales: " + fmtCoin(sess.VendorCopper) +
						"\ncorpse/quest coin: " + fmtCoin(sess.LootCopper))
					sessSkills.SetText(orDash(strings.Join(lastN(sess.SkillUps, 25), ", ")))
					sessZones.SetText(orDash(strings.Join(sess.Zones, " → ")))
					sessFaction.SetText(orDash(strings.Join(lastN(sess.Faction, 20), "\n")))
				})
			}
		}()
	})

	w.ShowAndRun()
}

// showQuestDetail opens a rich quest dialog: turn-in info, item status, wiki
// links, and the fetched walkthrough text (auto-loaded from the first page).
func showQuestDetail(w fyne.Window, title string, qi QuestInfo, itemLines []string) {
	hdr := container.NewVBox()
	addLbl := func(s string) {
		if s == "" {
			return
		}
		l := widget.NewLabel(s)
		l.Wrapping = fyne.TextWrapWord
		hdr.Add(l)
	}
	if qi.TurnIn != "" {
		addLbl("Turn in: " + qi.TurnIn)
	}
	if len(itemLines) > 0 {
		addLbl("Items: " + strings.Join(itemLines, " · "))
	}
	addLbl(qi.Note)
	if qi.Unverified {
		addLbl("(unverified — test the turn-in)")
	}

	body := widget.NewLabel("")
	body.Wrapping = fyne.TextWrapWord
	loadPage := func(pg string) {
		body.SetText("loading “" + pg + "” from eqlwiki…")
		go func() {
			txt, err := FetchWikiText(pg)
			if err != nil {
				txt = "fetch failed (" + err.Error() + ") — use the link above."
			}
			fyne.Do(func() { body.SetText(txt) })
		}()
	}
	if len(qi.Pages) == 0 {
		body.SetText("No eqlwiki article linked for this one.")
	}
	for i, pg := range qi.Pages {
		pg := pg
		u, _ := url.Parse(wikiURL(pg))
		row := container.NewHBox(
			widget.NewHyperlink(pg+"  ↗", u),
			widget.NewButton("Show walkthrough", func() { loadPage(pg) }),
		)
		hdr.Add(row)
		if i == 0 {
			loadPage(pg) // auto-load the first article's text
		}
	}

	inner := container.NewBorder(hdr, nil, nil, nil, container.NewVScroll(body))
	wrap := container.NewGridWrap(fyne.NewSize(580, 440), inner)
	dialog.ShowCustom(title, "Close", wrap, w)
}

// Reset starts a fresh session (clears fights, loot ledger, and session stats).
func (m *Meter) Reset() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.cur = newEncounter(m.player)
	m.history = nil
	m.loot = nil
	m.sess = Session{}
	m.zonesSeen = map[string]bool{}
	m.spans = nil
	m.xpEvents = nil
	m.killTimes = nil
	m.levelTimes = nil
	m.lastTS = time.Time{}
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
