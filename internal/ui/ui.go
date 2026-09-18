// Package ui draws the notch with GTK 3 and Cairo: a transparent override-redirect window against
// the screen edge. The window's input shape is the pill (plus the card while it is open), so
// every click anywhere else goes to the window behind — no pointer polling needed.
package ui

import (
	"fmt"
	"log/slog"
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
	"github.com/leock9/gonotch/internal/config"
	"github.com/leock9/gonotch/internal/hooks"
	"github.com/leock9/gonotch/internal/logs"
	"github.com/leock9/gonotch/internal/sessions"
	"github.com/leock9/gonotch/internal/text"
	"github.com/leock9/gonotch/internal/ui/layout"
	"github.com/leock9/gonotch/internal/x11"
)

const (
	collapseAfter = 250 * time.Millisecond
	frame         = 66 * time.Millisecond // 15 fps: smooth enough for a slow arc, and wakeups scale with it
	arcBox        = 46                    // the square the status arc turns in, inside the ring
	pressFor      = 180 * time.Millisecond

	// Auto-hide: the strip left on the edge, how long the pill takes to slide, and how long the
	// pointer may be away before it slides back
	stripW    = 7.0
	revealFor = 160 * time.Millisecond
	hideAfter = 700 * time.Millisecond
)

type UI struct {
	app  *app.App
	lang text.Lang
	cfg  config.Config // the settings as of the last update

	win *gtk.Window
	da  *gtk.DrawingArea

	state app.State
	lay   layout.Layout
	// The on-screen part of the window, in its own coordinates: it hangs off the monitor when the
	// pill sits near an end of the edge, and the card must stay out of that part
	visTop, visBottom float64

	hover     int // provider whose card is open, -1 for none
	card      layout.Rect
	tail      layout.Rect
	cardModel *cardModel

	// reveal is how far the pill is out, 0 (tucked into the strip) to 1; revealTo is where it is going
	reveal, revealTo float64
	lastFrame        time.Time

	collapse glib.SourceHandle
	hide     glib.SourceHandle
	anim     glib.SourceHandle
	pressed  map[string]time.Time
	glyphs   *glyphCache
	// pcts are the rings' percentage texts, laid out once per update rather than every frame
	pcts []*pango.Layout

	settings *settingsWindow
	// now is the clock the animations read; nil is the wall clock (snapshot frames set their own)
	now func() time.Time
}

func (u *UI) clock() time.Time {
	if u.now != nil {
		return u.now()
	}
	return time.Now()
}

// Run shows the notch and blocks in the GTK main loop until Quit.
func Run(a *app.App) {
	gtk.Init()
	u := &UI{app: a, lang: text.Detect(), hover: -1, pressed: map[string]time.Time{}, cfg: a.Config()}
	u.reveal, u.revealTo = 1, 1
	if u.cfg.AutoHide {
		u.reveal, u.revealTo = 0, 0
	}
	u.build()
	u.update()
	a.OnSettings(func() { glib.IdleAdd(u.openSettings) })

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

// Quit ends the main loop; safe from any goroutine.
func Quit() {
	glib.IdleAdd(gtk.MainQuit)
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
		u.scheduleHide()
		return false
	})
	u.da.ConnectButtonPressEvent(func(ev *gdk.EventButton) bool {
		u.press(ev.Button(), ev.X(), ev.Y())
		return true
	})
	u.win.Add(u.da)
	u.glyphs = newGlyphCache(u.win.ScaleFactor())
	u.lay = layout.Compute(0, u.cfg.Edge != "left")
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

// place pins the window to the configured edge of the primary monitor, with the pill's centre at
// the configured height. The pill stays on screen; the transparent rest of the window may not.
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
	x := geo.X() + geo.Width() - int(layout.WinW)
	if u.cfg.Edge == "left" {
		x = geo.X()
	}
	half := u.lay.Silhouette().H / 2
	top, bottom := float64(work.Y())+half, float64(work.Y()+work.Height())-half
	centre := float64(work.Y()) + float64(work.Height())*u.cfg.Position
	centre = min(max(centre, top), max(bottom, top))
	y := int(centre - layout.WinH/2)
	u.win.Move(x, y)
	u.visTop = float64(work.Y() - y)
	u.visBottom = float64(work.Y() + work.Height() - y)
}

// update takes fresh settings and State and redraws.
func (u *UI) update() {
	prev := u.cfg
	u.cfg = u.app.Config()
	u.state = u.app.State()
	right := u.cfg.Edge != "left"
	relaid := len(u.lay.Cells) != len(u.state.Providers) || u.lay.Right != right
	if relaid {
		u.lay = layout.Compute(len(u.state.Providers), right)
		if u.hover >= len(u.state.Providers) {
			u.hover = -1
		}
	}
	if relaid || prev.Edge != u.cfg.Edge || prev.Position != u.cfg.Position {
		u.place()
	}
	if prev.AutoHide != u.cfg.AutoHide {
		u.cancelHide()
		u.revealTo = 1
		if u.cfg.AutoHide {
			u.revealTo = 0
			u.hover, u.cardModel = -1, nil
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
	if u.settings != nil {
		u.settings.refresh()
	}
}

// tucked: auto-hide is on and the pill is in, or on its way in.
func (u *UI) tucked() bool { return u.cfg.AutoHide && u.revealTo == 0 }

// reshape sets the input region to what is there to touch: the strip while tucked, else the pill,
// and the card and its tail when open.
func (u *UI) reshape() {
	rects := []layout.Rect{u.lay.Silhouette()}
	switch {
	case u.tucked():
		rects = []layout.Rect{u.lay.Strip(stripW)}
	case u.hover >= 0:
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
	u.cancelHide()
	if u.tucked() {
		// The pointer reached the strip: slide the pill out
		u.revealTo = 1
		u.reshape()
		u.animate()
		return
	}
	if u.reveal < 1 {
		return // no card until the pill is all the way out
	}
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

// scheduleHide tucks an auto-hidden notch back in once the pointer has been away a moment.
func (u *UI) scheduleHide() {
	if !u.cfg.AutoHide || u.revealTo == 0 || u.hide != 0 {
		return
	}
	u.hide = glib.TimeoutAdd(uint(hideAfter/time.Millisecond), func() bool {
		u.hide = 0
		u.cancelCollapse()
		u.hover, u.cardModel = -1, nil
		u.revealTo = 0
		u.reshape()
		u.animate()
		return false
	})
}

func (u *UI) cancelHide() {
	if u.hide != 0 {
		glib.SourceRemove(u.hide)
		u.hide = 0
	}
}

func (u *UI) working() bool {
	return u.state.Aggregate == sessions.Running || u.state.Aggregate == sessions.Attention
}

// animate runs a frame timer only while something moves: the pill sliding, a working session's
// arc, a waiting one's pulse, or a ring being pressed.
func (u *UI) animate() {
	if u.anim != 0 {
		return
	}
	u.lastFrame = time.Now()
	if !u.tick() {
		return
	}
	u.anim = glib.TimeoutAdd(uint(frame/time.Millisecond), func() bool {
		if u.tick() {
			return true
		}
		u.anim = 0
		return false
	})
}

// tick advances one frame and redraws only what moves; false once nothing does.
func (u *UI) tick() bool {
	now := time.Now()
	dt := now.Sub(u.lastFrame)
	u.lastFrame = now
	if u.reveal != u.revealTo {
		step := float64(dt) / float64(revealFor)
		if u.revealTo > u.reveal {
			u.reveal = min(u.reveal+step, u.revealTo)
		} else {
			u.reveal = max(u.reveal-step, u.revealTo)
		}
		u.da.QueueDraw()
		return true
	}
	moving := false
	if u.reveal == 0 {
		// Tucked: only the strip shows, and it pulses while a session waits on you
		if u.state.Aggregate == sessions.Attention {
			s := u.lay.Strip(stripW)
			u.da.QueueDrawArea(int(s.X)-1, int(s.Y)-1, int(s.W)+2, int(s.H)+2)
			moving = true
		}
		return moving
	}
	for i, p := range u.state.Providers {
		c := u.lay.Cells[i]
		switch {
		case now.Sub(u.pressed[p.ID]) < pressFor+frame:
			// A press scales the whole ring
			u.da.QueueDrawArea(int(c.CX-layout.Ring/2)-4, int(c.CY-layout.Ring/2)-4, int(layout.Ring)+8, int(layout.Ring)+8)
			moving = true
		case p.ID == "claude" && u.working():
			// Only the status arc moves, and it turns inside the ring
			u.da.QueueDrawArea(int(c.CX)-arcBox/2, int(c.CY)-arcBox/2, arcBox, arcBox)
			moving = true
		}
	}
	return moving
}

func (u *UI) press(button uint, x, y float64) {
	if u.reveal < 1 {
		return
	}
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
	add(text.T(u.lang, "settings"), u.openSettings)
	if hooks.IsInstalled() {
		add(text.T(u.lang, "uninstall_hooks"), func() { logHooks(hooks.Uninstall()) })
	} else {
		add(text.T(u.lang, "install_hooks"), func() { logHooks(hooks.Install(HookBinary())) })
	}
	add(text.T(u.lang, "open_log"), func() { openURL(logs.Path()) })
	add(text.T(u.lang, "quit"), gtk.MainQuit)
	m.ShowAll()
	m.PopupAtPointer(nil)
}

func logHooks(msg string, err error) {
	if err != nil {
		slog.Error("Claude Code hooks", "err", err)
		return
	}
	slog.Info(msg)
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
	if s.PID == 0 {
		return
	}
	if !x11.FocusProcess(s.PID) {
		slog.Warn("no terminal window found for the session", "pid", s.PID, "session", s.ID)
		return
	}
	if s.State == sessions.Done {
		u.app.AckSession(s.ID)
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
	if err := cmd.Start(); err != nil {
		slog.Warn("xdg-open", "url", u, "err", err)
		return
	}
	go cmd.Wait()
}
