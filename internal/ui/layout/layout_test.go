package layout

import "testing"

func TestThePillHugsTheEdgeAndIsCentred(t *testing.T) {
	l := Compute(3, true)
	if l.Pill.X+l.Pill.W != WinW {
		t.Errorf("pill ends at %v, not the window's right edge", l.Pill.X+l.Pill.W)
	}
	if top, bottom := l.Pill.Y, WinH-(l.Pill.Y+l.Pill.H); top != bottom {
		t.Errorf("not centred: %v above, %v below", top, bottom)
	}
	if s := l.Silhouette(); s.Y < 0 || s.Y+s.H > WinH {
		t.Errorf("five cells and the fillets must fit: %+v", s)
	}
	if left := Compute(3, false); left.Pill.X != 0 {
		t.Errorf("left edge pill at %v", left.Pill.X)
	}
}

func TestFiveRingsStillFit(t *testing.T) {
	if s := Compute(5, true).Silhouette(); s.Y < 0 || s.Y+s.H > WinH {
		t.Errorf("five rings overflow: %+v", s)
	}
}

func TestEveryPointOnThePillBelongsToOneCell(t *testing.T) {
	l := Compute(3, true)
	for y := l.Pill.Y + Pad; y < l.Pill.Y+l.Pill.H-Pad; y++ {
		if l.CellAt(l.Pill.X+PillW/2, y) < 0 {
			t.Fatalf("no cell at y=%v", y)
		}
	}
	if l.CellAt(10, WinH/2) != -1 {
		t.Error("the transparent area is no cell")
	}
}

func TestTheCardStaysInsideTheWindowAndMeetsThePill(t *testing.T) {
	l := Compute(3, true)
	card, tail := l.Card(0, 600, 0, WinH)
	if card.Y < 0 || card.Y+card.H > WinH || card.X < 0 {
		t.Errorf("card out of the window: %+v", card)
	}
	// The window hangs 200 px off the top of the screen: the card stays below that
	if card, _ := l.Card(0, 200, 200, WinH); card.Y < 200 {
		t.Errorf("card above the visible part: %+v", card)
	}
	if card.X+card.W < tail.X || tail.X+tail.W < l.Pill.X {
		t.Errorf("gap between card %+v, tail %+v and pill %+v", card, tail, l.Pill)
	}
	if c, _ := l.Card(7, 100, 0, WinH); c != (Rect{}) {
		t.Error("no card for a missing cell")
	}
}
