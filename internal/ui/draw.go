package ui

import (
	"math"
	"time"

	"github.com/diamondburned/gotk4/pkg/cairo"
	"github.com/diamondburned/gotk4/pkg/pango"
	"github.com/diamondburned/gotk4/pkg/pangocairo"

	"github.com/leock9/gonotch/internal/app"
	"github.com/leock9/gonotch/internal/sessions"
	"github.com/leock9/gonotch/internal/text"
	"github.com/leock9/gonotch/internal/ui/layout"
	"github.com/leock9/gonotch/internal/usage"
)

type rgb struct{ r, g, b float64 }

func hex(v uint32) rgb {
	return rgb{float64(v>>16&0xff) / 255, float64(v>>8&0xff) / 255, float64(v&0xff) / 255}
}

// Codenotch's palette: the ring is coloured by how much is gone; the status arc stays neutral so it
// never reads as part of that scale, and waiting borrows the warning yellow.
var (
	colAmple  = hex(0x00FF88)
	colWatch  = hex(0xF2FF00)
	colCrit   = hex(0xFF3F00)
	colTrack  = hex(0x303030)
	colDisc   = hex(0x2a2a2a)
	colStroke = hex(0x2e2e2e)
	colInk    = hex(0xffffff)
	colText   = hex(0xe8e8ea)
	colDim    = hex(0x808080)
	colNote   = hex(0xc8c8c8)
	colCard   = hex(0x0a0a0a)
	colBar    = hex(0x2d2d2d)
	colRule   = hex(0x1e1e1e)
)

func tone(used float64) rgb {
	switch usage.BandOf(used) {
	case usage.BandCritical:
		return colCrit
	case usage.BandWatch:
		return colWatch
	}
	return colAmple
}

func set(cr *cairo.Context, c rgb, a float64) { cr.SetSourceRGBA(c.r, c.g, c.b, a) }

func (u *UI) draw(cr *cairo.Context) {
	cr.SetOperator(cairo.OperatorSource)
	cr.SetSourceRGBA(0, 0, 0, 0)
	cr.Paint()
	cr.SetOperator(cairo.OperatorOver)
	u.drawPill(cr)
	now := time.Now()
	for i, p := range u.state.Providers {
		u.drawCell(cr, i, p, now)
	}
	if u.hover >= 0 && u.cardModel != nil {
		u.drawCard(cr)
	}
}

// pillPath traces the silhouette: the concave fillet above, the pill with its outer corners
// rounded, the fillet below. The screen-edge side is left open, so the same path can be stroked.
func (u *UI) pillPath(cr *cairo.Context, closeAlongEdge bool) {
	p, r, f := u.lay.Pill, layout.PillRadius, layout.Fillet
	edge, inner := p.X+p.W, p.X
	top, bottom := p.Y, p.Y+p.H
	cr.NewPath()
	if u.lay.Right {
		cr.MoveTo(edge, top-f)
		cr.Arc(edge-f, top-f, f, 0, math.Pi/2)
		cr.LineTo(inner+r, top)
		cr.ArcNegative(inner+r, top+r, r, -math.Pi/2, -math.Pi)
		cr.LineTo(inner, bottom-r)
		cr.ArcNegative(inner+r, bottom-r, r, math.Pi, math.Pi/2)
		cr.LineTo(edge-f, bottom)
		cr.Arc(edge-f, bottom+f, f, -math.Pi/2, 0)
		if closeAlongEdge {
			cr.LineTo(edge, top-f)
			cr.ClosePath()
		}
		return
	}
	// Left edge: the same shape mirrored
	edge, inner = p.X, p.X+p.W
	cr.MoveTo(edge, top-f)
	cr.ArcNegative(edge+f, top-f, f, math.Pi, math.Pi/2)
	cr.LineTo(inner-r, top)
	cr.Arc(inner-r, top+r, r, -math.Pi/2, 0)
	cr.LineTo(inner, bottom-r)
	cr.Arc(inner-r, bottom-r, r, 0, math.Pi/2)
	cr.LineTo(edge+f, bottom)
	cr.ArcNegative(edge+f, bottom+f, f, -math.Pi/2, math.Pi)
	if closeAlongEdge {
		cr.LineTo(edge, top-f)
		cr.ClosePath()
	}
}

func (u *UI) drawPill(cr *cairo.Context) {
	u.pillPath(cr, true)
	cr.SetSourceRGBA(0, 0, 0, 1)
	cr.Fill()
	// A thin outline, the only cue when black sits on a black wallpaper
	u.pillPath(cr, false)
	set(cr, colStroke, 1)
	cr.SetLineWidth(1)
	cr.Stroke()
}

func arc(cr *cairo.Context, cx, cy, r, from, frac, width float64, c rgb, a float64) {
	cr.NewPath()
	cr.SetLineWidth(width)
	cr.SetLineCap(cairo.LineCapButt)
	cr.Arc(cx, cy, r, from, from+2*math.Pi*frac)
	set(cr, c, a)
	cr.Stroke()
}

const top = -math.Pi / 2

func (u *UI) drawCell(cr *cairo.Context, i int, p app.ProviderState, now time.Time) {
	c := u.lay.Cells[i]
	alpha := 1.0
	if p.IsStale(now) {
		alpha = 0.45
	}
	cr.Save()
	if t, ok := u.pressed[p.ID]; ok && now.Sub(t) < pressFor {
		k := 1 - 0.07*math.Sin(math.Pi*float64(now.Sub(t))/float64(pressFor))
		cr.Translate(c.CX, c.CY)
		cr.Scale(k, k)
		cr.Translate(-c.CX, -c.CY)
	}
	// Disc, track, reading
	cr.NewPath()
	cr.Arc(c.CX, c.CY, 22, 0, 2*math.Pi)
	set(cr, colDisc, alpha)
	cr.Fill()
	arc(cr, c.CX, c.CY, 25, 0, 1, 5, colTrack, alpha)
	h, hasHeadline := p.HeadlineWindow()
	if hasHeadline {
		arc(cr, c.CX, c.CY, 25, top, min(h.Used, 1), 5, tone(h.Used), alpha)
	}
	// The week, thinner, just outside: a session at 12 % beside a week at 91 % is why it exists
	if w, ok := p.WeeklyWindow(); ok {
		arc(cr, c.CX, c.CY, 31, 0, 1, 2.4, colTrack, 0.7*alpha)
		arc(cr, c.CX, c.CY, 31, top, min(w.Used, 1), 2.4, tone(w.Used), 0.85*alpha)
	}
	// Claude's sessions: a turning white arc while one works, a yellow pulse while one waits on you
	if p.ID == "claude" {
		secs := float64(now.UnixMilli()%100000) / 1000
		switch u.state.Aggregate {
		case sessions.Running:
			arc(cr, c.CX, c.CY, 19, top+2*math.Pi*secs/1.2, 0.28, 2.5, colInk, 1)
		case sessions.Attention:
			arc(cr, c.CX, c.CY, 19, 0, 1, 2.5, colWatch, 0.45+0.45*math.Sin(2*math.Pi*secs/1.4))
		}
	}
	glyphAlpha := alpha
	if hasHeadline && h.Used >= 1 {
		glyphAlpha *= 0.45
	}
	u.glyphs.draw(cr, p.ID, c.CX, c.CY, 26, glyphAlpha)
	cr.Restore()

	if i < len(u.pcts) {
		l := u.pcts[i]
		w, _ := l.PixelSize()
		cr.MoveTo(c.CX-float64(w)/2, c.PctY)
		set(cr, colInk, alpha)
		pangocairo.ShowLayout(cr, l)
	}
}

// pctLayout is the number under a ring: a dash where there is nothing to show, an ellipsis while
// the first reading is on its way.
func (u *UI) pctLayout(p app.ProviderState) *pango.Layout {
	pct := "…"
	h, hasHeadline := p.HeadlineWindow()
	switch {
	case p.Status == usage.StatusNeedsAuth || p.Status == usage.StatusNone:
		pct = "—"
	case hasHeadline:
		pct = text.Pct(h.Used)
	case len(p.Windows) > 0 || p.Status == usage.StatusError:
		pct = "—" // its declared window is missing: a dash, not a wait
	}
	l := u.da.CreatePangoLayout(pct)
	l.SetFontDescription(pango.FontDescriptionFromString("Sans Semi-Bold 11.5"))
	return l
}

func roundedRect(cr *cairo.Context, r layout.Rect, radius float64) {
	cr.NewPath()
	cr.Arc(r.X+r.W-radius, r.Y+radius, radius, -math.Pi/2, 0)
	cr.Arc(r.X+r.W-radius, r.Y+r.H-radius, radius, 0, math.Pi/2)
	cr.Arc(r.X+radius, r.Y+r.H-radius, radius, math.Pi/2, math.Pi)
	cr.Arc(r.X+radius, r.Y+radius, radius, math.Pi, 3*math.Pi/2)
	cr.ClosePath()
}

func (u *UI) drawCard(cr *cairo.Context) {
	roundedRect(cr, u.card, layout.CardRadius)
	set(cr, colCard, 0.97)
	cr.Fill()
	cr.Rectangle(u.tail.X, u.tail.Y, u.tail.W, u.tail.H)
	set(cr, colCard, 0.97)
	cr.Fill()
	u.cardModel.draw(cr, u)
}
