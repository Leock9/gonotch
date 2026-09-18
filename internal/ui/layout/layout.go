// Package layout is the notch's geometry, in logical pixels of its window, with no GTK in it: the
// pill against the screen edge, a cell per ring, and the hover card beside the pill.
//
// The measures are Codenotch's: a 70 px pill, 56 px rings, and concave fillets of 38.7 px where
// the pill meets the screen edge.
package layout

const (
	WinW = 360.0
	WinH = 650.0

	PillW      = 70.0
	PillRadius = 20.0
	Fillet     = 38.7
	Pad        = 18.0 // above the first ring and below the last cell
	Ring       = 56.0
	RingToPct  = 6.0
	PctH       = 21.0
	CellGap    = 14.0

	CardW      = 246.0
	CardPad    = 16.0
	CardRadius = 16.0
	CardGap    = 10.0 // between the card and the pill; the tail bridges it
	TailH      = 36.0

	// HotPad widens the pill's hit area, so a pointer arriving at the edge of a ring counts
	HotPad = 4.0
)

type Rect struct{ X, Y, W, H float64 }

func (r Rect) Contains(x, y float64) bool {
	return x >= r.X && y >= r.Y && x < r.X+r.W && y < r.Y+r.H
}

func (r Rect) Inset(d float64) Rect { return Rect{r.X + d, r.Y + d, r.W - 2*d, r.H - 2*d} }

// Cell is one ring: its centre, its hit area, and the baseline of its percentage.
type Cell struct {
	CX, CY float64
	Hit    Rect
	PctY   float64 // top of the percentage text
}

type Layout struct {
	// Right is true for the right screen edge; the left edge mirrors everything.
	Right bool
	Pill  Rect
	Cells []Cell
}

// Compute lays out n rings, the pill centred in the window.
func Compute(n int, right bool) Layout {
	cells := max(n, 1)
	h := 2*Pad + float64(cells)*(Ring+RingToPct+PctH) + float64(cells-1)*CellGap
	x := WinW - PillW
	if !right {
		x = 0
	}
	l := Layout{Right: right, Pill: Rect{x, (WinH - h) / 2, PillW, h}}
	y := l.Pill.Y + Pad
	for i := 0; i < n; i++ {
		c := Cell{CX: x + PillW/2, CY: y + Ring/2, PctY: y + Ring + RingToPct}
		c.Hit = Rect{x, y - CellGap/2, PillW, Ring + RingToPct + PctH + CellGap}
		l.Cells = append(l.Cells, c)
		y += Ring + RingToPct + PctH + CellGap
	}
	return l
}

// Silhouette is the pill plus its fillets: what the window must catch clicks on when closed.
func (l Layout) Silhouette() Rect {
	return Rect{l.Pill.X, l.Pill.Y - Fillet, l.Pill.W, l.Pill.H + 2*Fillet}
}

// CellAt is the ring under a point, or -1.
func (l Layout) CellAt(x, y float64) int {
	for i, c := range l.Cells {
		if c.Hit.Contains(x, y) {
			return i
		}
	}
	return -1
}

// Card places a card of height h beside cell i: centred on the ring, kept inside the window.
func (l Layout) Card(i int, h float64) (card, tail Rect) {
	if i < 0 || i >= len(l.Cells) {
		return Rect{}, Rect{}
	}
	c := l.Cells[i]
	h = min(h, WinH)
	y := min(max(c.CY-h/2, 0), WinH-h)
	x := l.Pill.X - CardGap - CardW
	tail = Rect{l.Pill.X - CardGap - 1, c.CY - TailH/2, CardGap + 2, TailH}
	if !l.Right {
		x = l.Pill.X + l.Pill.W + CardGap
		tail.X = l.Pill.X + l.Pill.W - 1
	}
	return Rect{x, y, CardW, h}, tail
}
