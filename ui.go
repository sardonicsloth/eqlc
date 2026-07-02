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
	expanded map[string]bool // combatant name -> showing abilities inline
	disp     []drow          // flattened render lines
	toggle   []string        // per disp line: combatant name if clickable, else ""
}

func NewBarList() *BarList {
	b := &BarList{showDPS: true, expanded: map[string]bool{}}
	b.ExtendBaseWidget(b)
	return b
}

// computeDisplay flattens rows + expanded ability sub-rows into render lines.
func (b *BarList) computeDisplay() {
	b.disp = b.disp[:0]
	b.toggle = b.toggle[:0]
	for _, r := range b.rows {
		b.disp = append(b.disp, drow{
			label: marker(r) + r.Name,
			pct:   r.Pct,
			right: valText(r, b.showDPS),
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
	default:
		return "Damage done"
	}
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
