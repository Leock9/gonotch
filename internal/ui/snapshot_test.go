//go:build snapshot

package ui

// Renders the notch into transparent PNGs for the README, frame by frame, with the demo data:
//
//	GONOTCH_SNAPSHOTS=/out/frames go test -tags snapshot -run Snapshots ./internal/ui/
//
// It needs a display (Xvfb will do) and writes nothing unless GONOTCH_SNAPSHOTS is set.

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/diamondburned/gotk4/pkg/cairo"
	"github.com/diamondburned/gotk4/pkg/gtk/v3"

	"github.com/leock9/gonotch/internal/app"
	"github.com/leock9/gonotch/internal/config"
	"github.com/leock9/gonotch/internal/providers/demo"
	"github.com/leock9/gonotch/internal/sessions"
	"github.com/leock9/gonotch/internal/text"
	"github.com/leock9/gonotch/internal/ui/layout"
)

func TestSnapshots(t *testing.T) {
	dir := os.Getenv("GONOTCH_SNAPSHOTS")
	if dir == "" {
		t.Skip("set GONOTCH_SNAPSHOTS to a directory")
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	gtk.Init()
	a := app.NewWith(config.Default(), demo.Providers())
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	a.Start(ctx)
	for _, ev := range demo.Sessions() {
		a.Apply(ev)
	}
	for deadline := time.Now().Add(5 * time.Second); len(a.State().Providers) < len(demo.Providers()); {
		if time.Now().After(deadline) {
			t.Fatal("the demo providers never published")
		}
		time.Sleep(20 * time.Millisecond)
	}
	u := &UI{app: a, lang: text.EN, hover: -1, pressed: map[string]time.Time{}, cfg: a.Config(), reveal: 1, revealTo: 1}
	u.build()
	u.update()

	t0 := time.Date(2026, 9, 18, 12, 0, 0, 0, time.Local)
	render := func(name string, at time.Time) {
		t.Helper()
		s := cairo.CreateImageSurface(cairo.FormatARGB32, int(layout.WinW), int(layout.WinH))
		cr := cairo.Create(s)
		u.now = func() time.Time { return at }
		u.draw(cr)
		s.Flush()
		if err := s.WriteToPNG(filepath.Join(dir, name)); err != nil {
			t.Fatal(err)
		}
	}
	frames := func(prefix string, n int, from time.Time) {
		for i := range n {
			render(fmt.Sprintf("%s-%02d.png", prefix, i), from.Add(time.Duration(i)*frame))
		}
	}

	render("notch.png", t0)
	frames("spin", 22, t0) // about 1.4 s: one turn of the arc
	for i, name := range []string{"claude", "codex", "cursor", "copilot"} {
		u.hover = i
		u.buildCard()
		render("card-"+name+".png", t0)
		frames("card-"+name, 15, t0.Add(time.Duration(i+2)*time.Second))
	}
	u.hover, u.cardModel = -1, nil
	a.Apply(sessions.Event{Kind: sessions.EvAttention, SessionID: "7f3a21", Message: "Claude needs your permission to use Bash", FromHook: true})
	u.update()
	render("waiting.png", t0.Add(350*time.Millisecond))
	u.cfg.AutoHide, u.reveal, u.revealTo = true, 0, 0
	render("tucked.png", t0.Add(350*time.Millisecond))
	for i := 0; i <= 8; i++ {
		u.reveal = float64(i) / 8
		render(fmt.Sprintf("slide-%02d.png", i), t0.Add(350*time.Millisecond))
	}
}
