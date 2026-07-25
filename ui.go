package main

import (
	"fmt"
	"image/color"
	"strconv"
	"strings"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/canvas"
	"fyne.io/fyne/v2/theme"
	"fyne.io/fyne/v2/widget"
)

// ---- colors / theme --------------------------------------------------------

var (
	rowBg    = color.NRGBA{R: 0x22, G: 0x24, B: 0x2b, A: 0xff}
	barYou   = color.NRGBA{R: 0x21, G: 0x6b, B: 0x66, A: 0xff} // muted teal = you
	barOther = color.NRGBA{R: 0x2e, G: 0x52, B: 0x76, A: 0xff} // muted steel blue = others
	barEnemy = color.NRGBA{R: 0x6e, G: 0x33, B: 0x33, A: 0xff} // muted red = enemies
	subBar   = color.NRGBA{R: 0x3b, G: 0x44, B: 0x52, A: 0xff} // dim = ability sub-rows
	textCol  = color.NRGBA{R: 0xf3, G: 0xf4, B: 0xf6, A: 0xff}
	subCol   = color.NRGBA{R: 0xb6, G: 0xbd, B: 0xc8, A: 0xff}
)

// Dark, desaturated bars so white text stays readable across the whole bar.
func barColor(r Row) color.Color {
	switch {
	case r.IsEnemy:
		return barEnemy
	case r.IsYou:
		return barYou
	default:
		return barOther
	}
}

// marker distinguishes you from group/pets without a loud full-row color.
func marker(r Row) string {
	if r.IsYou {
		return "▸ "
	}
	return "   "
}

// darkTheme forces the dark variant regardless of the OS setting.
type darkTheme struct{}

func (darkTheme) Color(n fyne.ThemeColorName, _ fyne.ThemeVariant) color.Color {
	return theme.DefaultTheme().Color(n, theme.VariantDark)
}
func (darkTheme) Font(s fyne.TextStyle) fyne.Resource     { return theme.DefaultTheme().Font(s) }
func (darkTheme) Icon(n fyne.ThemeIconName) fyne.Resource { return theme.DefaultTheme().Icon(n) }
func (darkTheme) Size(n fyne.ThemeSizeName) float32       { return theme.DefaultTheme().Size(n) }

// ---- formatting ------------------------------------------------------------

func human(n int) string {
	s := strconv.Itoa(n)
	neg := strings.HasPrefix(s, "-")
	if neg {
		s = s[1:]
	}
	var b strings.Builder
	for i, c := range s {
		if i > 0 && (len(s)-i)%3 == 0 {
			b.WriteByte(',')
		}
		b.WriteRune(c)
	}
	if neg {
		return "-" + b.String()
	}
	return b.String()
}

// ---- DashCard: a tappable dashboard card (EQBuddy-style glanceable row) -----

var (
	cardBg   = color.NRGBA{R: 0x1c, G: 0x1f, B: 0x26, A: 0xff}
	noteCol  = color.NRGBA{R: 0xe8, G: 0x6c, B: 0x5c, A: 0xff}
	titleCol = color.NRGBA{R: 0xd8, G: 0xdd, B: 0xe6, A: 0xff}
)

type DashCard struct {
	widget.BaseWidget
	title   string
	summary string
	note    string // red alert line; hidden when empty
	OnTap   func()
}

func NewDashCard(title string, onTap func()) *DashCard {
	c := &DashCard{title: title, summary: "—", OnTap: onTap}
	c.ExtendBaseWidget(c)
	return c
}

func (c *DashCard) Set(summary, note string) {
	if c.summary == summary && c.note == note {
		return
	}
	c.summary, c.note = summary, note
	c.Refresh()
}

func (c *DashCard) Tapped(*fyne.PointEvent) {
	if c.OnTap != nil {
		c.OnTap()
	}
}
func (c *DashCard) TappedSecondary(*fyne.PointEvent) {}

func (c *DashCard) CreateRenderer() fyne.WidgetRenderer {
	bg := canvas.NewRectangle(cardBg)
	bg.CornerRadius = 8
	title := canvas.NewText(c.title, titleCol)
	title.TextStyle = fyne.TextStyle{Bold: true}
	title.TextSize = 14
	sum := canvas.NewText(c.summary, textCol)
	sum.TextSize = 13
	note := canvas.NewText(c.note, noteCol)
	note.TextSize = 12
	note.TextStyle = fyne.TextStyle{Bold: true}
	chev := canvas.NewText("›", subCol)
	chev.TextSize = 20
	return &dashRenderer{c: c, bg: bg, title: title, sum: sum, note: note, chev: chev}
}

type dashRenderer struct {
	c     *DashCard
	bg    *canvas.Rectangle
	title *canvas.Text
	sum   *canvas.Text
	note  *canvas.Text
	chev  *canvas.Text
}

func (r *dashRenderer) height() float32 {
	if r.c.note != "" {
		return 78
	}
	return 60
}

func (r *dashRenderer) Layout(size fyne.Size) {
	r.bg.Resize(fyne.NewSize(size.Width, r.height()-6))
	r.bg.Move(fyne.NewPos(0, 0))
	r.title.Move(fyne.NewPos(14, 6))
	r.sum.Move(fyne.NewPos(14, 28))
	r.note.Move(fyne.NewPos(14, 50))
	r.chev.Move(fyne.NewPos(size.Width-26, r.height()/2-16))
}

func (r *dashRenderer) MinSize() fyne.Size { return fyne.NewSize(320, r.height()) }

func (r *dashRenderer) Refresh() {
	r.title.Text = r.c.title
	r.sum.Text = r.c.summary
	r.note.Text = r.c.note
	canvas.Refresh(r.c)
}

func (r *dashRenderer) Objects() []fyne.CanvasObject {
	return []fyne.CanvasObject{r.bg, r.title, r.sum, r.note, r.chev}
}
func (r *dashRenderer) Destroy() {}

// ---- BarList: a custom widget that draws one colored bar per combatant ------

const (
	rowH   = 28
	rowPad = 5
)

// drow is one rendered line: a combatant, or an expanded ability sub-row.
type drow struct {
	label string
	pct   float64
	right string
	col   color.Color
	bold  bool
}

type BarList struct {
	widget.BaseWidget
	rows     []Row
	showDPS  bool
	mode     ViewMode        // controls the right-hand value formatting
	expanded map[string]bool // combatant name -> showing abilities inline
	disp     []drow          // flattened render lines
	toggle   []string        // per disp line: combatant name if clickable, else ""
}

func NewBarList() *BarList {
	b := &BarList{showDPS: true, expanded: map[string]bool{}}
	b.ExtendBaseWidget(b)
	return b
}

func (b *BarList) SetMode(m ViewMode) { b.mode = m }

// computeDisplay flattens rows + expanded ability sub-rows into render lines.
func (b *BarList) computeDisplay() {
	b.disp = b.disp[:0]
	b.toggle = b.toggle[:0]
	for _, r := range b.rows {
		b.disp = append(b.disp, drow{
			label: marker(r) + r.Name,
			pct:   r.Pct,
			right: valTextMode(r, b.mode, b.showDPS),
			col:   barColor(r),
			bold:  r.IsYou,
		})
		b.toggle = append(b.toggle, r.Name)
		if b.expanded[r.Name] {
			for _, a := range r.Abilities {
				b.disp = append(b.disp, drow{
					label: "       " + a.Name,
					pct:   a.Pct,
					right: fmt.Sprintf("%s   %.0f%%", human(a.Total), a.Pct),
					col:   subBar,
				})
				b.toggle = append(b.toggle, "")
			}
		}
	}
}

// Tapped toggles the clicked combatant's inline ability breakdown.
func (b *BarList) Tapped(e *fyne.PointEvent) {
	idx := int(e.Position.Y / (rowH + rowPad))
	if idx < 0 || idx >= len(b.toggle) {
		return
	}
	name := b.toggle[idx]
	if name == "" {
		return // tapped an ability sub-row
	}
	if b.expanded[name] {
		delete(b.expanded, name)
	} else {
		b.expanded[name] = true
	}
	b.Refresh()
}

func (b *BarList) TappedSecondary(*fyne.PointEvent) {}

func (b *BarList) SetRows(rows []Row) {
	b.rows = rows
	b.Refresh()
}

func (b *BarList) CreateRenderer() fyne.WidgetRenderer {
	r := &barRenderer{b: b}
	r.build()
	return r
}

type barRenderer struct {
	b       *BarList
	bgs     []*canvas.Rectangle
	bars    []*canvas.Rectangle
	names   []*canvas.Text
	vals    []*canvas.Text
	objects []fyne.CanvasObject
}

func (r *barRenderer) build() {
	r.b.computeDisplay()
	r.bgs = r.bgs[:0]
	r.bars = r.bars[:0]
	r.names = r.names[:0]
	r.vals = r.vals[:0]
	r.objects = r.objects[:0]
	for _, d := range r.b.disp {
		bg := canvas.NewRectangle(rowBg)
		bg.CornerRadius = 4
		bar := canvas.NewRectangle(d.col)
		bar.CornerRadius = 4
		name := canvas.NewText(d.label, textCol)
		name.TextStyle = fyne.TextStyle{Bold: d.bold}
		name.TextSize = 13
		val := canvas.NewText(d.right, textCol)
		val.Alignment = fyne.TextAlignTrailing
		val.TextSize = 13
		r.bgs = append(r.bgs, bg)
		r.bars = append(r.bars, bar)
		r.names = append(r.names, name)
		r.vals = append(r.vals, val)
		r.objects = append(r.objects, bg, bar, name, val)
	}
}

func valText(row Row, showDPS bool) string {
	if showDPS {
		return fmt.Sprintf("%.0f dps   %s   %.0f%%", row.DPS, human(row.Total), row.Pct)
	}
	return fmt.Sprintf("%s   %.0f%%", human(row.Total), row.Pct)
}

// valTextMode formats the right-hand value appropriately for the view mode.
func valTextMode(row Row, mode ViewMode, showDPS bool) string {
	switch mode {
	case ModeHealing:
		return fmt.Sprintf("%.0f hps   %s   %.0f%%", row.DPS, human(row.Total), row.Pct)
	case ModeCC:
		return fmt.Sprintf("%s event%s   %.0f%%", human(row.Total), tern(row.Total == 1, "", "s"), row.Pct)
	case ModeDeaths:
		return fmt.Sprintf("%s death%s", human(row.Total), tern(row.Total == 1, "", "s"))
	default:
		return valText(row, showDPS)
	}
}

// lootLineText renders one Loot-tab row; quest items get a star + quest tag.
func lootLineText(le LootEvent) string {
	cnt := ""
	if le.Count > 1 {
		cnt = fmt.Sprintf(" ×%d", le.Count)
	}
	star, tag := "     ", ""
	if qi, ok := questLookup(le.Item); ok {
		star = "★ "
		tag = "   → " + qi.Quest
		if qi.Unverified {
			tag += " (unverified)"
		}
	}
	disp := ""
	switch le.Disposition {
	case "sold":
		disp = "  [SOLD " + le.Detail + "]"
	case "stored":
		disp = "  [stored]"
	case "merged":
		disp = "  [merged " + le.Detail + "]"
	}
	src := ""
	if le.Source != "" {
		src = "  ⇐ " + le.Source
	}
	return le.TS.Format("15:04") + "  " + star + le.Item + cnt + src + disp + tag
}

func summaryText(s EncSummary) string {
	tag := "  "
	if s.Live {
		tag = "● "
	}
	return fmt.Sprintf("%s%s   %s   (%.0fs)   you %.0f / raid %.0f",
		tag, s.Start.Format("15:04:05"), orQ(s.Target), s.Dur, s.YourDPS, s.RaidDPS)
}

func modeName(m ViewMode) string {
	switch m {
	case ModeEnemy:
		return "Enemy damage"
	case ModeTaken:
		return "Damage taken"
	case ModeHealing:
		return "Healing done"
	case ModeCC:
		return "CC / stuns"
	case ModeDeaths:
		return "Deaths"
	default:
		return "Damage done"
	}
}

// headlineVal renders the headline number for a snapshot under a view mode.
func headlineVal(mode ViewMode, s Snapshot) string {
	switch mode {
	case ModeHealing:
		return fmt.Sprintf("%.0f hps", s.RaidDPS)
	case ModeCC:
		return fmt.Sprintf("%s events", human(s.RaidTotal))
	case ModeDeaths:
		return fmt.Sprintf("%s deaths", human(s.RaidTotal))
	default:
		return fmt.Sprintf("%.0f dps", s.RaidDPS)
	}
}

func orDash(s string) string {
	if strings.TrimSpace(s) == "" {
		return "—"
	}
	return s
}

// lastN returns the final n elements of a slice (newest-last lists).
func lastN(s []string, n int) []string {
	if len(s) <= n {
		return s
	}
	return s[len(s)-n:]
}

func orQ(s string) string {
	if s == "" {
		return "?"
	}
	return s
}

func tern(b bool, t, f string) string {
	if b {
		return t
	}
	return f
}

// onlyMine filters bar rows to just the player (for the "show only my damage" option).
func onlyMine(rows []Row) []Row {
	var out []Row
	for _, r := range rows {
		if r.IsYou {
			out = append(out, r)
		}
	}
	return out
}

func (r *barRenderer) Layout(size fyne.Size) {
	for i := range r.bgs {
		y := float32(i) * (rowH + rowPad)
		r.bgs[i].Resize(fyne.NewSize(size.Width, rowH))
		r.bgs[i].Move(fyne.NewPos(0, y))

		frac := float32(r.b.disp[i].pct / 100)
		if frac < 0.04 {
			frac = 0.04
		}
		r.bars[i].Resize(fyne.NewSize(size.Width*frac, rowH))
		r.bars[i].Move(fyne.NewPos(0, y))

		nh := r.names[i].MinSize().Height
		r.names[i].Move(fyne.NewPos(10, y+(rowH-nh)/2))
		r.vals[i].Resize(fyne.NewSize(size.Width-10, rowH))
		r.vals[i].Move(fyne.NewPos(0, y+(rowH-r.vals[i].MinSize().Height)/2))
	}
}

func (r *barRenderer) MinSize() fyne.Size {
	h := float32(len(r.b.disp)) * (rowH + rowPad)
	if h == 0 {
		h = rowH
	}
	return fyne.NewSize(280, h)
}

func (r *barRenderer) Refresh() {
	r.build()
	r.Layout(r.b.Size())
	canvas.Refresh(r.b)
}

func (r *barRenderer) Objects() []fyne.CanvasObject { return r.objects }
func (r *barRenderer) Destroy()                     {}
