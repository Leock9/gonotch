// Package ui draws the notch with GTK 3 and Cairo: a transparent override-redirect window against
// the screen edge. The window's input shape is the pill (plus the card while it is open), so
// every click anywhere else goes to the window behind — no pointer polling needed.
package ui

import (
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sync"
	"time"

	"github.com/diamondburned/gotk4/pkg/cairo"
	"github.com/diamondburned/gotk4/pkg/core/glib"
	"github.com/diamondburned/gotk4/pkg/gdk/v3"
	"github.com/diamondburned/gotk4/pkg/gtk/v3"
	"github.com/diamondburned/gotk4/pkg/pango"

	"github.com/leock9/gonotch/internal/app"
	"github.com/leock9/gonotch/internal/hooks"
	"github.com/leock9/gonotch/internal/sessions"
	"github.com/leock9/gonotch/internal/text"
	"github.com/leock9/gonotch/internal/ui/layout"
	"github.com/leock9/gonotch/internal/x11"
)

const (
	collapseAfter = 250 * time.Millisecond
	frame         = 50 * time.Millisecond
	pressFor      = 180 * time.Millisecond
)

type UI struct {
	app  *app.App
	lang text.Lang

	win *gtk.Window
	da  *gtk.DrawingArea

	state app.State
	lay   layout.Layout

	hover     int // provider whose card is open, -1 for none
	card      layout.Rect
	tail      layout.Rect
	cardModel *cardModel

	collapse glib.SourceHandle
	anim     glib.SourceHandle
	pressed  map[string]time.Time
	glyphs   *glyphCache
	// pcts are the rings' percentage texts, laid out once per update rather than every frame
	pcts []*pango.Layout
}

// Run shows the notch and blocks in the GTK main loop until Quit.
func Run(a *app.App) {
	gtk.Init()
	u := &UI{app: a, lang: text.Detect(), hover: -1, pressed: map[string]time.Time{}}
	u.build()
	u.update()

	// Changes arrive from provider goroutines; GTK is touched only on its own thread
	var pending sync.Mutex
	go func() {
		for range a.Subscribe() {
			pending.Lock()
			glib.IdleAdd(func() {
				pending.Unlock()
				u.update()
			})
		}
	}()
	gtk.Main()
}

func (u *UI) build() {
	u.win = gtk.NewWindow(gtk.WindowPopup)
	u.win.SetTitle("gonotch")
	u.win.SetAcceptFocus(false)
	if v := u.win.Screen().RGBAVisual(); v != nil {
		u.win.SetVisual(v)
	}
	u.win.SetAppPaintable(true)
	u.win.SetDefaultSize(int(layout.WinW), int(layout.WinH))
	u.win.Resize(int(layout.WinW), int(layout.WinH))

	u.da = gtk.NewDrawingArea()
	u.da.SetSizeRequest(int(layout.WinW), int(layout.WinH))
	u.da.AddEvents(int(gdk.PointerMotionMask | gdk.EnterNotifyMask | gdk.LeaveNotifyMask | gdk.ButtonPressMask))
	u.da.ConnectDraw(func(cr *cairo.Context) bool {
		u.draw(cr)
		release(cr)
		return true
	})
	u.da.ConnectMotionNotifyEvent(func(ev *gdk.EventMotion) bool {
		u.motion(ev.X(), ev.Y())
		return false
	})
	u.da.ConnectLeaveNotifyEvent(func(*gdk.EventCrossing) bool {
		u.scheduleCollapse()
		return false
	})
	u.da.ConnectButtonPressEvent(func(ev *gdk.EventButton) bool {
		u.press(ev.Button(), ev.X(), ev.Y())
		return true
	})
	u.win.Add(u.da)
	u.glyphs = newGlyphCache(u.win.ScaleFactor())
	u.win.ShowAll()
	u.place()
	u.win.Screen().ConnectMonitorsChanged(u.place)
}

// release drops the reference gotk4 takes on each frame's cairo_t, here on the GTK thread. Left
// to the finalizer gotk4 attaches, the drop happens on the garbage collector's thread; when it is
// the last one, the window's Xlib surface is finished from there, racing GTK for the X connection,
// and the main loop ends up waiting forever in XSync for a reply the other thread took.
func release(cr *cairo.Context) {
	runtime.SetFinalizer(cr, nil)
	cr.Close()
}

// place pins the window to the configured edge of the primary monitor.
func (u *UI) place() {
	display := gdk.DisplayGetDefault()
	mon := display.PrimaryMonitor()
	if mon == nil {
		mon = display.Monitor(0)
	}
	if mon == nil {
		return
	}
	geo, work := mon.Geometry(), mon.Workarea()
	cfg := u.app.Cfg
	x := geo.X() + geo.Width() - int(layout.WinW)
	if cfg.Edge == "left" {
		x = geo.X()
	}
	y := work.Y() + int(float64(work.Height())*cfg.Position) - int(layout.WinH)/2
	y = min(max(y, work.Y()), work.Y()+work.Height()-int(layout.WinH))
	u.win.Move(x, y)
}

// update takes a fresh State and redraws; the geometry only changes when a ring appears or goes.
func (u *UI) update() {
	u.state = u.app.State()
	if len(u.lay.Cells) != len(u.state.Providers) || u.lay.Right != (u.app.Cfg.Edge != "left") {
		u.lay = layout.Compute(len(u.state.Providers), u.app.Cfg.Edge != "left")
		if u.hover >= len(u.state.Providers) {
			u.hover = -1
		}
	}
	u.pcts = u.pcts[:0]
	for _, p := range u.state.Providers {
		u.pcts = append(u.pcts, u.pctLayout(p))
	}
	if u.hover >= 0 {
		u.buildCard()
	}
	u.reshape()
	u.animate()
	u.da.QueueDraw()
}

// reshape sets the input region to what is drawn: the pill, and the card and its tail when open.
func (u *UI) reshape() {
	rects := []layout.Rect{u.lay.Silhouette()}
	if u.hover >= 0 {
		rects = append(rects, u.card, u.tail)
	}
	var crs []*cairo.Rectangle
	for _, r := range rects {
		crs = append(crs, cairo.RectangleNew(int(r.X), int(r.Y), int(r.W+1), int(r.H+1)))
	}
	region, err := (*cairo.Region)(nil).CreateRectangles(crs...)
	if err == nil {
		u.win.InputShapeCombineRegion(region)
	}
}

func (u *UI) motion(x, y float64) {
	if i := u.lay.CellAt(x, y); i >= 0 {
		u.cancelCollapse()
		if i != u.hover {
			u.hover = i
			u.buildCard()
			u.reshape()
			u.da.QueueDraw()
		}
		return
	}
	if u.hover >= 0 && (u.card.Contains(x, y) || u.tail.Contains(x, y) || u.lay.Silhouette().Contains(x, y)) {
		u.cancelCollapse()
		return
	}
	u.scheduleCollapse()
}

func (u *UI) scheduleCollapse() {
	if u.hover < 0 || u.collapse != 0 {
		return
	}
	u.collapse = glib.TimeoutAdd(uint(collapseAfter/time.Millisecond), func() bool {
		u.collapse = 0
		u.hover = -1
		u.cardModel = nil
		u.reshape()
		u.da.QueueDraw()
		return false
	})
}

func (u *UI) cancelCollapse() {
	if u.collapse != 0 {
		glib.SourceRemove(u.collapse)
		u.collapse = 0
	}
}

// animate runs a frame timer only while something moves: a working session's spinning arc, a
// waiting one's pulse, or a ring being pressed.
func (u *UI) animate() {
	moving := u.state.Aggregate == sessions.Running || u.state.Aggregate == sessions.Attention
	for _, t := range u.pressed {
		if time.Since(t) < pressFor {
			moving = true
		}
	}
	if !moving || u.anim != 0 {
		return
	}
	u.anim = glib.TimeoutAdd(uint(frame/time.Millisecond), func() bool {
		still := false
		// Only the rings that move are redrawn; the rest of the window stays as it is
		for i, p := range u.state.Providers {
			working := p.ID == "claude" && (u.state.Aggregate == sessions.Running || u.state.Aggregate == sessions.Attention)
			if working || time.Since(u.pressed[p.ID]) < pressFor+frame {
				c := u.lay.Cells[i]
				u.da.QueueDrawArea(int(c.CX-layout.Ring/2)-4, int(c.CY-layout.Ring/2)-4, int(layout.Ring)+8, int(layout.Ring)+8)
				still = true
			}
		}
		if !still {
			u.anim = 0
		}
		return still
	})
}

func (u *UI) press(button uint, x, y float64) {
	i := u.lay.CellAt(x, y)
	switch button {
	case 1:
		if i >= 0 {
			p := u.state.Providers[i]
			u.pressed[p.ID] = time.Now()
			u.app.Refresh(p.ID)
			u.animate()
			return
		}
		if s, ok := u.sessionAt(x, y); ok {
			u.jumpTo(s)
		}
	case 3:
		provider := -1
		if i >= 0 {
			provider = i
		} else if u.hover >= 0 {
			provider = u.hover
		}
		u.menu(provider)
	}
}

func (u *UI) menu(provider int) {
	m := gtk.NewMenu()
	add := func(label string, f func()) {
		item := gtk.NewMenuItemWithLabel(label)
		item.ConnectActivate(f)
		m.Append(item)
	}
	add(text.T(u.lang, "refresh"), func() { u.app.Refresh("") })
	if provider >= 0 && provider < len(u.state.Providers) {
		page := u.state.Providers[provider].UsagePage
		if host, err := url.Parse(page); err == nil {
			add(fmt.Sprintf(text.T(u.lang, "open"), host.Host), func() { openURL(page) })
		}
	}
	m.Append(&gtk.NewSeparatorMenuItem().MenuItem)
	if hooks.IsInstalled() {
		add(text.T(u.lang, "uninstall_hooks"), func() { _, _ = hooks.Uninstall() })
	} else {
		add(text.T(u.lang, "install_hooks"), func() { _, _ = hooks.Install(HookBinary()) })
	}
	add(text.T(u.lang, "quit"), gtk.MainQuit)
	m.ShowAll()
	m.PopupAtPointer(nil)
}

// sessionAt is the session row of the open card under a window point.
func (u *UI) sessionAt(x, y float64) (sessions.Session, bool) {
	if u.cardModel == nil || !u.card.Contains(x, y) {
		return sessions.Session{}, false
	}
	for _, r := range u.cardModel.rows {
		if r.rect.Contains(x-u.card.X, y-u.card.Y) {
			return r.session, true
		}
	}
	return sessions.Session{}, false
}

// jumpTo brings the session's terminal forward; looking at a finished session acknowledges it.
func (u *UI) jumpTo(s sessions.Session) {
	if s.PID == 0 || !x11.FocusProcess(s.PID) {
		return
	}
	if s.State == sessions.Done {
		u.app.Store.AckDone(func(x sessions.Session) bool { return x.ID == s.ID })
		u.app.Changed()
	}
}

// HookBinary is gonotch-hook: next to this binary, else on PATH.
func HookBinary() string {
	if self, err := os.Executable(); err == nil {
		sibling := filepath.Join(filepath.Dir(self), "gonotch-hook")
		if _, err := os.Stat(sibling); err == nil {
			return sibling
		}
	}
	if exe, err := exec.LookPath("gonotch-hook"); err == nil {
		return exe
	}
	return "gonotch-hook"
}

func openURL(u string) {
	cmd := exec.Command("xdg-open", u)
	if cmd.Start() == nil {
		go cmd.Wait()
	}
}

// Quit ends the main loop; safe from any goroutine.
func Quit() {
	glib.IdleAdd(gtk.MainQuit)
}
