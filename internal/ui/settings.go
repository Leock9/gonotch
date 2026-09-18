package ui

import (
	"fmt"
	"html"
	"time"

	"github.com/diamondburned/gotk4/pkg/gtk/v3"
	"github.com/diamondburned/gotk4/pkg/pango"

	"github.com/leock9/gonotch/internal/app"
	"github.com/leock9/gonotch/internal/config"
	"github.com/leock9/gonotch/internal/text"
	"github.com/leock9/gonotch/internal/usage"
)

// settingsWindow edits what the notch shows and where: which rings and in what order, the edge and
// the height along it, and auto-hide. Every change applies at once and is saved; there is no
// "apply" step.
type settingsWindow struct {
	u    *UI
	win  *gtk.Window
	list *gtk.ListBox
	// status is each provider row's second line, updated in place as readings arrive
	status map[string]*gtk.Label
}

func (u *UI) openSettings() {
	if u.settings != nil {
		u.settings.win.Present()
		return
	}
	u.settings = newSettings(u)
}

func (s *settingsWindow) t(key string) string { return text.T(s.u.lang, key) }

func newSettings(u *UI) *settingsWindow {
	s := &settingsWindow{u: u, status: map[string]*gtk.Label{}}
	s.win = gtk.NewWindow(gtk.WindowToplevel)
	s.win.SetTitle("gonotch")
	s.win.SetIconName("preferences-system")
	s.win.SetResizable(false)
	s.win.SetPosition(gtk.WinPosCenter)
	s.win.SetDefaultSize(480, -1)

	bar := gtk.NewHeaderBar()
	bar.SetTitle("gonotch")
	bar.SetSubtitle(s.t("settings_title"))
	bar.SetShowCloseButton(true)
	s.win.SetTitlebar(bar)

	body := gtk.NewBox(gtk.OrientationVertical, 10)
	body.SetMarginStart(20)
	body.SetMarginEnd(20)
	body.SetMarginTop(16)
	body.SetMarginBottom(20)

	// Providers
	body.PackStart(heading(s.t("providers")), false, false, 0)
	s.list = gtk.NewListBox()
	s.list.SetSelectionMode(gtk.SelectionNone)
	frame := gtk.NewFrame("")
	frame.Add(s.list)
	body.PackStart(frame, false, false, 0)
	body.PackStart(hint(s.t("providers_hint")), false, false, 0)

	// Position
	cfg := u.app.Config()
	body.PackStart(spaced(heading(s.t("position"))), false, false, 0)
	edge := gtk.NewComboBoxText()
	edge.Append("right", s.t("right"))
	edge.Append("left", s.t("left"))
	edge.SetActiveID(cfg.Edge)
	edge.ConnectChanged(func() {
		id := edge.ActiveID()
		u.app.UpdateConfig(func(c *config.Config) { c.Edge = id })
	})
	body.PackStart(row(s.t("edge"), edge), false, false, 0)

	height := gtk.NewScaleWithRange(gtk.OrientationHorizontal, 0, 100, 1)
	height.SetDrawValue(false)
	height.SetValue(cfg.Position * 100)
	height.AddMark(0, gtk.PosBottom, s.t("top"))
	height.AddMark(50, gtk.PosBottom, s.t("middle"))
	height.AddMark(100, gtk.PosBottom, s.t("bottom"))
	height.SetHExpand(true)
	height.ConnectValueChanged(func() {
		v := height.Value() / 100
		u.app.UpdateConfig(func(c *config.Config) { c.Position = v })
	})
	body.PackStart(row(s.t("height"), height), false, false, 0)

	// Hiding
	body.PackStart(spaced(heading(s.t("hide"))), false, false, 0)
	auto := gtk.NewSwitch()
	auto.SetActive(cfg.AutoHide)
	auto.ConnectStateSet(func(on bool) bool {
		u.app.UpdateConfig(func(c *config.Config) { c.AutoHide = on })
		return false
	})
	body.PackStart(row(s.t("autohide"), auto), false, false, 0)
	body.PackStart(hint(s.t("autohide_hint")), false, false, 0)

	s.win.Add(body)
	s.win.ConnectDestroy(func() { u.settings = nil })
	s.fillProviders()
	s.win.ShowAll()
	s.win.Present()
	return s
}

// fillProviders builds one row per provider, in ring order.
func (s *settingsWindow) fillProviders() {
	for _, child := range s.list.Children() {
		s.list.Remove(child)
	}
	s.status = map[string]*gtk.Label{}
	catalog := s.u.app.Catalog()
	for i, p := range catalog {
		s.list.Insert(s.providerRow(p, i == 0, i == len(catalog)-1), -1)
	}
	s.list.ShowAll()
}

func (s *settingsWindow) providerRow(p app.ProviderState, first, last bool) *gtk.ListBoxRow {
	id := p.ID
	box := gtk.NewBox(gtk.OrientationHorizontal, 10)
	box.SetMarginStart(12)
	box.SetMarginEnd(10)
	box.SetMarginTop(8)
	box.SetMarginBottom(8)

	// The window follows the desktop's theme, light or dark, so the marks take its text colour
	if pb := s.u.glyphs.get(id, 20, themeInk(s.win)); pb != nil {
		box.PackStart(gtk.NewImageFromPixbuf(pb), false, false, 0)
	}
	texts := gtk.NewBox(gtk.OrientationVertical, 2)
	name := gtk.NewLabel("")
	name.SetMarkup("<b>" + html.EscapeString(p.Name) + "</b>")
	name.SetXAlign(0)
	status := gtk.NewLabel("")
	status.SetXAlign(0)
	status.SetEllipsize(pango.EllipsizeEnd)
	status.SetMaxWidthChars(34)
	status.StyleContext().AddClass("dim-label")
	s.status[id] = status
	s.setStatus(p)
	texts.PackStart(name, false, false, 0)
	texts.PackStart(status, false, false, 0)
	box.PackStart(texts, true, true, 0)

	up := gtk.NewButtonFromIconName("go-up-symbolic", int(gtk.IconSizeButton))
	up.SetTooltipText(s.t("move_up"))
	up.SetSensitive(!first)
	up.SetRelief(gtk.ReliefNone)
	up.ConnectClicked(func() { s.move(id, -1) })
	down := gtk.NewButtonFromIconName("go-down-symbolic", int(gtk.IconSizeButton))
	down.SetTooltipText(s.t("move_down"))
	down.SetSensitive(!last)
	down.SetRelief(gtk.ReliefNone)
	down.ConnectClicked(func() { s.move(id, +1) })

	show := gtk.NewSwitch()
	show.SetActive(!p.Hidden)
	show.SetVAlign(gtk.AlignCenter)
	show.SetTooltipText(s.t("show_ring"))
	show.ConnectStateSet(func(on bool) bool {
		s.u.app.UpdateConfig(func(c *config.Config) { c.SetHidden(id, !on) })
		return false
	})

	box.PackEnd(show, false, false, 0)
	box.PackEnd(down, false, false, 0)
	box.PackEnd(up, false, false, 0)
	row := gtk.NewListBoxRow()
	row.Add(box)
	return row
}

func (s *settingsWindow) move(id string, delta int) {
	order := s.u.app.Order()
	s.u.app.UpdateConfig(func(c *config.Config) { c.Move(order, id, delta) })
	s.fillProviders()
}

// refresh updates the status lines after a new reading, without rebuilding the rows.
func (s *settingsWindow) refresh() {
	for _, p := range s.u.app.Catalog() {
		s.setStatus(p)
	}
}

func (s *settingsWindow) setStatus(p app.ProviderState) {
	l, ok := s.status[p.ID]
	if !ok {
		return
	}
	l.SetText(s.statusText(p))
}

// statusText is one line on what a provider is showing, or why it shows nothing.
func (s *settingsWindow) statusText(p app.ProviderState) string {
	switch {
	case p.Status == usage.StatusAbsent:
		return s.t("not_installed")
	case p.Hidden && len(p.Windows) == 0:
		return s.t("off")
	case p.Status == usage.StatusLoading:
		return s.t("loading")
	}
	if h, ok := p.HeadlineWindow(); ok {
		line := text.Pct(h.Used) + " · " + text.Label(s.u.lang, h.Label)
		if p.Hidden {
			return s.t("off") + " · " + line
		}
		if p.IsStale(time.Now()) {
			line += " · " + s.t("stale")
		}
		return line
	}
	if p.Note != "" {
		return p.Note
	}
	return "—"
}

// themeInk is the theme's text colour as #rrggbb.
func themeInk(w *gtk.Window) string {
	c := w.StyleContext().Color(gtk.StateFlagNormal)
	return fmt.Sprintf("#%02x%02x%02x", int(c.Red()*255), int(c.Green()*255), int(c.Blue()*255))
}

func heading(label string) *gtk.Label {
	l := gtk.NewLabel("")
	l.SetMarkup("<b>" + html.EscapeString(label) + "</b>")
	l.SetXAlign(0)
	return l
}

func hint(label string) *gtk.Label {
	l := gtk.NewLabel(label)
	l.SetXAlign(0)
	l.SetLineWrap(true)
	l.SetMaxWidthChars(56)
	l.StyleContext().AddClass("dim-label")
	return l
}

func spaced(w *gtk.Label) *gtk.Label {
	w.SetMarginTop(10)
	return w
}

// row is a label on the left and a control on the right.
func row(label string, control gtk.Widgetter) *gtk.Box {
	box := gtk.NewBox(gtk.OrientationHorizontal, 16)
	l := gtk.NewLabel(label)
	l.SetXAlign(0)
	box.PackStart(l, false, false, 0)
	box.PackEnd(control, gtk.BaseWidget(control).HExpand(), true, 0)
	return box
}
