package ui

import (
	"embed"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"

	"github.com/diamondburned/gotk4/pkg/cairo"
	"github.com/diamondburned/gotk4/pkg/gdk/v3"
	"github.com/diamondburned/gotk4/pkg/gdkpixbuf/v2"

	"github.com/leock9/gonotch/internal/config"
)

// The provider marks, from @lobehub/icons-static-svg (MIT); see glyphs/NOTICE.md.
//
//go:embed glyphs/*.svg
var builtinGlyphs embed.FS

// glyphCache renders each mark once per size and colour, at the monitor's scale. The marks are drawn
// in currentColor, which becomes the colour asked for. A file in ~/.config/gonotch/glyphs/<provider>.svg
// replaces the built-in one.
type glyphCache struct {
	scale int
	m     map[string]*gdkpixbuf.Pixbuf
}

func newGlyphCache(scale int) *glyphCache {
	return &glyphCache{scale: max(scale, 1), m: map[string]*gdkpixbuf.Pixbuf{}}
}

func glyphSVG(id string) ([]byte, bool) {
	if raw, err := os.ReadFile(filepath.Join(config.Dir(), "glyphs", id+".svg")); err == nil {
		return raw, true
	}
	raw, err := builtinGlyphs.ReadFile("glyphs/" + id + ".svg")
	return raw, err == nil
}

// notchInk is the marks' colour on the notch, which is always black whatever the desktop theme.
const notchInk = "#e8e8ea"

func (g *glyphCache) get(id string, size int, color string) *gdkpixbuf.Pixbuf {
	key := fmt.Sprintf("%s@%d%s", id, size, color)
	if pb, ok := g.m[key]; ok {
		return pb
	}
	g.m[key] = nil // a mark that fails to load is not retried every frame, nor logged again
	fail := func(err error) *gdkpixbuf.Pixbuf {
		slog.Warn("provider mark not drawn", "provider", id, "err", err)
		return nil
	}
	raw, ok := glyphSVG(id)
	if !ok {
		return fail(errors.New("no SVG for it"))
	}
	svg := strings.ReplaceAll(string(raw), "currentColor", color)
	loader, err := gdkpixbuf.NewPixbufLoaderWithType("svg")
	if err != nil {
		return fail(fmt.Errorf("%w (is librsvg2-common installed?)", err))
	}
	px := size * g.scale
	loader.SetSize(px, px)
	if err := loader.Write([]byte(svg)); err != nil {
		_ = loader.Close()
		return fail(err)
	}
	if err := loader.Close(); err != nil {
		return fail(err)
	}
	g.m[key] = loader.Pixbuf()
	return g.m[key]
}

// draw paints a mark centred on (cx, cy), size logical pixels square.
func (g *glyphCache) draw(cr *cairo.Context, id string, cx, cy, size, alpha float64) {
	pb := g.get(id, int(size), notchInk)
	if pb == nil {
		return
	}
	cr.Save()
	cr.Translate(cx-size/2, cy-size/2)
	cr.Scale(1/float64(g.scale), 1/float64(g.scale))
	gdk.CairoSetSourcePixbuf(cr, pb, 0, 0)
	cr.PaintWithAlpha(alpha)
	cr.Restore()
}
