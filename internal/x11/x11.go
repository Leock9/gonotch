// Package x11 finds and raises top-level windows through the window manager's EWMH properties,
// over a connection of its own (pure Go, no Xlib: a window that closes mid-query is an error value,
// not Xlib's default handler ending the process). It is what jumps back to a session's terminal.
package x11

import (
	"encoding/binary"
	"fmt"
	"os"
	"strings"
	"sync"

	"github.com/jezek/xgb"
	"github.com/jezek/xgb/xproto"
)

type conn struct {
	c     *xgb.Conn
	root  xproto.Window
	atoms map[string]xproto.Atom
}

var (
	once   sync.Once
	shared *conn
	mu     sync.Mutex
)

func get() (*conn, error) {
	once.Do(func() {
		c, err := xgb.NewConn()
		if err != nil {
			return
		}
		shared = &conn{c: c, root: xproto.Setup(c).DefaultScreen(c).Root, atoms: map[string]xproto.Atom{}}
	})
	if shared == nil {
		return nil, fmt.Errorf("no X server")
	}
	return shared, nil
}

func (x *conn) atom(name string) (xproto.Atom, error) {
	if a, ok := x.atoms[name]; ok {
		return a, nil
	}
	r, err := xproto.InternAtom(x.c, false, uint16(len(name)), name).Reply()
	if err != nil {
		return 0, err
	}
	x.atoms[name] = r.Atom
	return r.Atom, nil
}

func (x *conn) prop32(w xproto.Window, name string, typ xproto.Atom) []uint32 {
	a, err := x.atom(name)
	if err != nil {
		return nil
	}
	r, err := xproto.GetProperty(x.c, false, w, a, typ, 0, 4096).Reply()
	if err != nil || r.Format != 32 {
		return nil
	}
	out := make([]uint32, 0, r.ValueLen)
	for i := 0; i+4 <= len(r.Value) && len(out) < int(r.ValueLen); i += 4 {
		out = append(out, binary.LittleEndian.Uint32(r.Value[i:]))
	}
	return out
}

// Window is a top-level window and the process that owns it.
type Window struct {
	ID  uint32
	PID int
}

// Windows lists the window manager's clients that name their process.
func Windows() []Window {
	mu.Lock()
	defer mu.Unlock()
	x, err := get()
	if err != nil {
		return nil
	}
	var out []Window
	for _, w := range x.prop32(x.root, "_NET_CLIENT_LIST", xproto.AtomWindow) {
		if pid := x.prop32(xproto.Window(w), "_NET_WM_PID", xproto.AtomCardinal); len(pid) > 0 && pid[0] != 0 {
			out = append(out, Window{ID: w, PID: int(pid[0])})
		}
	}
	return out
}

// ActivePID is the process behind the focused window, 0 if unknown.
func ActivePID() int {
	mu.Lock()
	defer mu.Unlock()
	x, err := get()
	if err != nil {
		return 0
	}
	w := x.prop32(x.root, "_NET_ACTIVE_WINDOW", xproto.AtomWindow)
	if len(w) == 0 || w[0] == 0 {
		return 0
	}
	if pid := x.prop32(xproto.Window(w[0]), "_NET_WM_PID", xproto.AtomCardinal); len(pid) > 0 {
		return int(pid[0])
	}
	return 0
}

// Activate raises a window the way a pager does (_NET_ACTIVE_WINDOW, source 2), which
// focus-stealing prevention lets through; the window manager restores it if minimised.
func Activate(id uint32) error {
	mu.Lock()
	defer mu.Unlock()
	x, err := get()
	if err != nil {
		return err
	}
	a, err := x.atom("_NET_ACTIVE_WINDOW")
	if err != nil {
		return err
	}
	ev := xproto.ClientMessageEvent{
		Format: 32,
		Window: xproto.Window(id),
		Type:   a,
		Data:   xproto.ClientMessageDataUnionData32New([]uint32{2, 0, 0, 0, 0}),
	}
	mask := uint32(xproto.EventMaskSubstructureRedirect | xproto.EventMaskSubstructureNotify)
	return xproto.SendEventChecked(x.c, false, x.root, mask, string(ev.Bytes())).Check()
}

// Parents maps every process to its parent, from /proc.
func Parents() map[int]int {
	out := map[int]int{}
	entries, _ := os.ReadDir("/proc")
	for _, e := range entries {
		var pid int
		if _, err := fmt.Sscan(e.Name(), &pid); err != nil {
			continue
		}
		raw, err := os.ReadFile("/proc/" + e.Name() + "/stat")
		if err != nil {
			continue
		}
		if pp, ok := parentFromStat(string(raw)); ok {
			out[pid] = pp
		}
	}
	return out
}

// parentFromStat reads `pid (comm) state ppid …`; comm may hold spaces and parentheses of its own,
// so the fields start after the last ')'.
func parentFromStat(stat string) (int, bool) {
	i := strings.LastIndexByte(stat, ')')
	if i < 0 {
		return 0, false
	}
	f := strings.Fields(stat[i+1:])
	if len(f) < 2 {
		return 0, false
	}
	var pp int
	_, err := fmt.Sscan(f[1], &pp)
	return pp, err == nil
}

// Chain is a process and its ancestors, nearest first, at most 12 deep.
func Chain(pid int, parents map[int]int) []int {
	chain := []int{pid}
	for range 12 {
		p, ok := parents[chain[len(chain)-1]]
		if !ok || p <= 1 {
			break
		}
		chain = append(chain, p)
	}
	return chain
}

// FocusProcess raises the window of the nearest ancestor of pid that owns one: the terminal a
// Claude Code session runs in. Nearest, because further up sits systemd --user, the parent of
// almost every app. A terminal server hosting several windows cannot say which holds the tab, so
// any of its windows will do.
func FocusProcess(pid int) bool {
	chain := Chain(pid, Parents())
	best, bestRank := uint32(0), len(chain)
	for _, w := range Windows() {
		for rank, p := range chain {
			if p == w.PID && rank < bestRank {
				best, bestRank = w.ID, rank
			}
		}
	}
	return best != 0 && Activate(best) == nil
}
