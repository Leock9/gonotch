package ui

import (
	"fmt"
	"math"
	"time"

	"github.com/diamondburned/gotk4/pkg/cairo"
	"github.com/diamondburned/gotk4/pkg/pango"
	"github.com/diamondburned/gotk4/pkg/pangocairo"

	"github.com/leock9/gonotch/internal/sessions"
	"github.com/leock9/gonotch/internal/text"
	"github.com/leock9/gonotch/internal/ui/layout"
	"github.com/leock9/gonotch/internal/usage"
)

// cardModel is the hover card laid out once per open or change: text blocks with their positions
// relative to the card's top-left, measured with the widget's own Pango context.
type cardModel struct {
	provider string
	items    []cardItem
	height   float64
	rows     []sessionRow // clickable session rows, card-relative
}

type cardItem struct {
	x, y   float64
	layout *pango.Layout
	color  rgb
	// bar, when set, draws a usage bar instead of text
	bar   bool
	used  float64
	glyph bool
	dot   *rgb
}

type sessionRow struct {
	rect    layout.Rect
	session sessions.Session
}

const inner = layout.CardW - 2*layout.CardPad

func (u *UI) text(s, font string, width float64, wrap bool) *pango.Layout {
	l := u.da.CreatePangoLayout(s)
	l.SetFontDescription(pango.FontDescriptionFromString(font))
	l.SetWidth(int(width * pango.SCALE))
	if wrap {
		l.SetWrap(pango.WrapWordChar)
	} else {
		l.SetEllipsize(pango.EllipsizeEnd)
	}
	return l
}

func height(l *pango.Layout) float64 {
	_, h := l.PixelSize()
	return float64(h)
}

func width(l *pango.Layout) float64 {
	w, _ := l.PixelSize()
	return float64(w)
}

func (u *UI) buildCard() {
	if u.hover < 0 || u.hover >= len(u.state.Providers) {
		u.cardModel = nil
		return
	}
	p := u.state.Providers[u.hover]
	m := &cardModel{provider: p.ID}
	now := time.Now()
	y := layout.CardPad
	add := func(it cardItem) { m.items = append(m.items, it) }

	// Header: mark, name
	add(cardItem{x: layout.CardPad, y: y + 1, glyph: true})
	title := u.text(fmt.Sprintf("%s · %s", p.Name, text.T(u.lang, "usage")), "Sans Bold 11", inner-24, false)
	add(cardItem{x: layout.CardPad + 24, y: y, layout: title, color: colText})
	y += math.Max(height(title), 18) + 2

	// Plan and age
	sub := p.Plan
	if !p.FetchedAt.IsZero() && (p.IsStale(now) || now.Sub(p.FetchedAt) > time.Minute) {
		ago := fmt.Sprintf(text.T(u.lang, "updated"), text.Ago(u.lang, now.Sub(p.FetchedAt)))
		if sub != "" {
			sub += " · "
		}
		sub += ago
	}
	if sub != "" {
		l := u.text(sub, "Sans 8.5", inner, false)
		add(cardItem{x: layout.CardPad, y: y, layout: l, color: colDim})
		y += height(l)
	}
	y += 8

	window := func(w usage.Window) {
		label := u.text(text.Label(u.lang, w.Label), "Sans Bold 9", inner, false)
		reset := u.text(text.Reset(u.lang, w.ResetsAt, now), "Sans 8.5", inner, false)
		add(cardItem{x: layout.CardPad, y: y, layout: label, color: colText})
		// The reset sits beside the label when both fit, and wraps under it as a whole otherwise
		if rw := width(reset); width(label)+8+rw <= inner {
			add(cardItem{x: layout.CardPad + inner - rw, y: y + 1, layout: reset, color: colDim})
			y += math.Max(height(label), height(reset)) + 5
		} else {
			y += height(label) + 1
			add(cardItem{x: layout.CardPad, y: y, layout: reset, color: colDim})
			y += height(reset) + 5
		}
		add(cardItem{x: layout.CardPad, y: y, bar: true, used: w.Used})
		y += 4 + 4
		used := u.text(text.UsedLeft(u.lang, w.Used), "Sans 8.5", inner, false)
		add(cardItem{x: layout.CardPad, y: y, layout: used, color: colDim})
		y += height(used) + 10
	}
	var groups []string
	for _, w := range p.Windows {
		if w.Group == "" {
			window(w)
		} else if len(groups) == 0 || groups[len(groups)-1] != w.Group {
			groups = append(groups, w.Group)
		}
	}
	for _, g := range groups {
		h := u.text(text.Label(u.lang, g), "Sans Bold 9.5", inner, false)
		add(cardItem{x: layout.CardPad, y: y, layout: h, color: colText})
		y += height(h) + 6
		for _, w := range p.Windows {
			if w.Group == g {
				window(w)
			}
		}
	}

	note := p.Note
	if note == "" && len(p.Windows) == 0 && (p.Status == usage.StatusLoading) {
		note = text.T(u.lang, "loading")
	}
	if note != "" {
		l := u.text(note, "Sans 9", inner, true)
		add(cardItem{x: layout.CardPad, y: y, layout: l, color: colNote})
		y += height(l) + 10
	}

	// Claude's card lists its sessions; a click jumps to the session's terminal
	if p.ID == "claude" && len(u.state.Sessions) > 0 {
		y += 2
		add(cardItem{x: layout.CardPad, y: y, bar: true, used: -1})
		y += 8
		for i, s := range u.state.Sessions {
			if i == 6 {
				more := u.text(fmt.Sprintf("+%d", len(u.state.Sessions)-6), "Sans 8.5", inner, false)
				add(cardItem{x: layout.CardPad, y: y, layout: more, color: colDim})
				y += height(more) + 4
				break
			}
			dot := stateColor(s.State)
			state := u.text(text.T(u.lang, string(s.State)), "Sans 8.5", inner, false)
			sw := width(state)
			title := u.text(s.Title, "Sans 8.5", inner-14-sw-8, false)
			rowH := math.Max(height(title), 14)
			add(cardItem{x: layout.CardPad, y: y + rowH/2 - 3, dot: &dot})
			add(cardItem{x: layout.CardPad + 12, y: y, layout: title, color: hex(0xb0b0b3)})
			add(cardItem{x: layout.CardPad + inner - sw, y: y, layout: state, color: colDim})
			m.rows = append(m.rows, sessionRow{rect: layout.Rect{X: 0, Y: y - 2, W: layout.CardW, H: rowH + 4}, session: s})
			y += rowH + 4
			if detail := sessionDetail(s); detail != "" {
				l := u.text(detail, "Sans 8", inner-12, false)
				add(cardItem{x: layout.CardPad + 12, y: y - 2, layout: l, color: colDim})
				y += height(l) + 2
			}
		}
	}
	m.height = y + layout.CardPad - 8
	u.cardModel = m
	u.card, u.tail = u.lay.Card(u.hover, m.height)
}

func sessionDetail(s sessions.Session) string {
	switch {
	case s.State == sessions.Attention && s.Attn != "":
		return s.Attn
	case s.Prompt != "":
		return "“" + s.Prompt + "”"
	}
	return s.Last
}

func stateColor(s sessions.State) rgb {
	switch s {
	case sessions.Running:
		return colInk
	case sessions.Attention:
		return colWatch
	case sessions.Done:
		return colAmple
	}
	return colDim
}

func (m *cardModel) draw(cr *cairo.Context, u *UI) {
	ox, oy := u.card.X, u.card.Y
	for _, it := range m.items {
		x, y := ox+it.x, oy+it.y
		switch {
		case it.glyph:
			u.glyphs.draw(cr, m.provider, x+8, y+8, 16, 1)
		case it.dot != nil:
			cr.NewPath()
			cr.Arc(x+3, y+3, 3, 0, 2*math.Pi)
			set(cr, *it.dot, 1)
			cr.Fill()
		case it.bar && it.used < 0: // a rule
			cr.Rectangle(ox+layout.CardPad, y, inner, 1)
			set(cr, colRule, 1)
			cr.Fill()
		case it.bar:
			roundedRect(cr, layout.Rect{X: x, Y: y, W: inner, H: 4}, 2)
			set(cr, colBar, 1)
			cr.Fill()
			if it.used > 0 {
				roundedRect(cr, layout.Rect{X: x, Y: y, W: math.Max(inner*min(it.used, 1), 4), H: 4}, 2)
				set(cr, tone(it.used), 1)
				cr.Fill()
			}
		case it.layout != nil:
			cr.MoveTo(x, y)
			set(cr, it.color, 1)
			pangocairo.ShowLayout(cr, it.layout)
		}
	}
}
