package demo

import (
	"context"
	"testing"

	"github.com/leock9/gonotch/internal/usage"
)

func TestEveryDemoRingHasItsDeclaredWindow(t *testing.T) {
	bands := map[usage.Band]bool{}
	for _, p := range Providers() {
		s, _ := p.Poll(context.Background(), usage.Snapshot{})
		h, ok := s.HeadlineWindow()
		if !ok || s.Status != usage.StatusOK {
			t.Fatalf("%s: headline %q missing or status %s", p.ID(), s.Headline, s.Status)
		}
		bands[usage.BandOf(h.Used)] = true
	}
	if len(bands) != 3 {
		t.Errorf("the demo should show all three colour bands, got %d", len(bands))
	}
	if len(Sessions()) == 0 {
		t.Error("the demo needs a session to show the working arc")
	}
}
