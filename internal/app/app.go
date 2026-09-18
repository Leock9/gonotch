// Package app wires the providers, the session store and the local server together, and tells
// whoever subscribes (the notch, the server's /state) when anything changed.
package app

import (
	"context"
	"log"
	"path/filepath"
	"sync"
	"time"

	"github.com/leock9/gonotch/internal/config"
	"github.com/leock9/gonotch/internal/providers"
	"github.com/leock9/gonotch/internal/providers/claude"
	"github.com/leock9/gonotch/internal/providers/codex"
	"github.com/leock9/gonotch/internal/providers/cursor"
	"github.com/leock9/gonotch/internal/sessions"
	"github.com/leock9/gonotch/internal/usage"
)

// ProviderState is one ring: who it is and what it last read.
type ProviderState struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	UsagePage string `json:"usage_page"`
	// Hidden is only ever set in Catalog: State leaves hidden providers out
	Hidden bool `json:"hidden,omitempty"`
	usage.Snapshot
}

// State is everything the notch draws.
type State struct {
	Providers []ProviderState    `json:"providers"`
	Sessions  []sessions.Session `json:"sessions"`
	Aggregate sessions.State     `json:"aggregate"`
}

// saveAfter: a slider drag changes the settings dozens of times a second; the file is written once
// they settle.
const saveAfter = 400 * time.Millisecond

type App struct {
	// store is reached only through App, so every change to it notifies the subscribers
	store *sessions.Store

	providers []providers.Provider
	runners   map[string]*providers.Runner

	mu         sync.Mutex
	cfg        config.Config
	unsaved    bool
	saveTimer  *time.Timer
	snaps      map[string]usage.Snapshot
	subs       []chan struct{}
	onSettings func()
	// transcripts: infer sessions from Claude Code's transcripts (off in the demo)
	transcripts bool
}

func New(cfg config.Config) *App {
	a := newApp(cfg)
	a.providers = []providers.Provider{claude.New(a.claudeActive), codex.New(), cursor.New()}
	a.transcripts = true
	return a
}

// NewWith runs the given providers instead of the real ones and reads no transcripts: `gonotch demo`.
func NewWith(cfg config.Config, ps []providers.Provider) *App {
	a := newApp(cfg)
	a.providers = ps
	return a
}

func newApp(cfg config.Config) *App {
	return &App{cfg: cfg.Clone(), store: sessions.NewStore(), runners: map[string]*providers.Runner{}, snaps: map[string]usage.Snapshot{}}
}

// Providers lists every provider, including the ones not installed here (for doctor).
func (a *App) Providers() []providers.Provider { return a.providers }

func (a *App) ids() []string {
	ids := make([]string, len(a.providers))
	for i, p := range a.providers {
		ids[i] = p.ID()
	}
	return ids
}

// Config returns a copy of the current settings.
func (a *App) Config() config.Config {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.cfg.Clone()
}

// UpdateConfig changes the settings, wakes providers that were just switched on and tells the
// subscribers at once; the file follows once the changes settle (see Flush).
func (a *App) UpdateConfig(change func(c *config.Config)) {
	a.mu.Lock()
	before := a.cfg.Clone()
	change(&a.cfg)
	after := a.cfg.Clone()
	a.unsaved = true
	if a.saveTimer == nil {
		a.saveTimer = time.AfterFunc(saveAfter, a.Flush)
	} else {
		a.saveTimer.Reset(saveAfter)
	}
	a.mu.Unlock()
	for _, id := range a.ids() {
		if before.IsHidden(id) && !after.IsHidden(id) {
			a.Refresh(id)
		}
	}
	a.notify()
}

// Flush writes settings not yet saved; called on quit so a change made just before is not lost.
func (a *App) Flush() {
	a.mu.Lock()
	if a.saveTimer != nil {
		a.saveTimer.Stop()
	}
	unsaved, cfg := a.unsaved, a.cfg.Clone()
	a.unsaved = false
	a.mu.Unlock()
	if !unsaved {
		return
	}
	if err := config.Save(cfg); err != nil {
		log.Printf("saving settings: %v", err)
	}
}

// Order is every provider id in the configured ring order.
func (a *App) Order() []string { return a.Config().Ordered(a.ids()) }

func snapshotPath(id string) string { return filepath.Join(config.StateDir(), id+".json") }

// claudeActive: a session is running or waiting, so Claude's ring is worth reading every minute.
func (a *App) claudeActive() bool {
	agg := sessions.Aggregate(a.store.List())
	return agg == sessions.Running || agg == sessions.Attention
}

// Start runs the providers, the transcript watcher and the sweeper until ctx ends.
func (a *App) Start(ctx context.Context) {
	for _, p := range a.providers {
		id := p.ID()
		r := providers.NewRunner(p, a.publish)
		r.Enabled = func() bool { return !a.Config().IsHidden(id) }
		a.runners[id] = r
		go r.Run(ctx, usage.Load(snapshotPath(id)))
	}
	if a.transcripts {
		w := sessions.NewWatcher(func(ev sessions.Event) { a.Apply(ev) })
		go w.Run(ctx)
	}
	go func() {
		t := time.NewTicker(30 * time.Second)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				if a.store.Sweep() {
					a.notify()
				}
			}
		}
	}()
}

func (a *App) publish(s usage.Snapshot) {
	a.mu.Lock()
	a.snaps[s.Provider] = s
	a.mu.Unlock()
	if s.Status != usage.StatusAbsent && s.Status != usage.StatusLoading {
		_ = usage.Save(snapshotPath(s.Provider), s)
	}
	a.notify()
}

// Apply folds a session event in, notifying only on a visible change.
func (a *App) Apply(ev sessions.Event) {
	if a.store.Apply(ev) {
		a.notify()
	}
}

// AckSession marks a finished session as seen, so the notch stops announcing it.
func (a *App) AckSession(id string) {
	if a.store.AckDone(func(s sessions.Session) bool { return s.ID == id }) {
		a.notify()
	}
}

// Refresh asks one provider (or every one, for "") to read now.
func (a *App) Refresh(id string) {
	for pid, r := range a.runners {
		if id == "" || id == pid {
			r.RequestRefresh()
		}
	}
}

// OnSettings registers what opens the settings window; RequestSettings calls it (the server does,
// when `gonotch settings` or a second `gonotch` asks).
func (a *App) OnSettings(f func()) {
	a.mu.Lock()
	a.onSettings = f
	a.mu.Unlock()
}

func (a *App) RequestSettings() {
	a.mu.Lock()
	f := a.onSettings
	a.mu.Unlock()
	if f != nil {
		f()
	}
}

// Subscribe returns a channel that receives a signal after every change. Signals coalesce: a slow
// reader sees one, then reads State.
func (a *App) Subscribe() <-chan struct{} {
	ch := make(chan struct{}, 1)
	a.mu.Lock()
	a.subs = append(a.subs, ch)
	a.mu.Unlock()
	return ch
}

func (a *App) notify() {
	a.mu.Lock()
	defer a.mu.Unlock()
	for _, ch := range a.subs {
		select {
		case ch <- struct{}{}:
		default:
		}
	}
}

// Catalog is every provider in ring order, hidden and absent ones included, for the settings.
func (a *App) Catalog() []ProviderState {
	byID := map[string]providers.Provider{}
	for _, p := range a.providers {
		byID[p.ID()] = p
	}
	a.mu.Lock()
	cfg := a.cfg.Clone()
	snaps := make(map[string]usage.Snapshot, len(a.snaps))
	for id, s := range a.snaps {
		snaps[id] = s
	}
	a.mu.Unlock()
	order := cfg.Ordered(a.ids())
	out := make([]ProviderState, 0, len(order))
	for _, id := range order {
		p := byID[id]
		s, ok := snaps[id]
		// Present touches the filesystem, so it runs outside the lock the pollers publish under
		if !ok && !p.Present() {
			s.Status = usage.StatusAbsent
		}
		out = append(out, ProviderState{ID: id, Name: p.Name(), UsagePage: p.UsagePage(), Hidden: cfg.IsHidden(id), Snapshot: s})
	}
	return out
}

// State returns the rings to draw, in order, and the sessions.
func (a *App) State() State {
	var st State
	for _, p := range a.Catalog() {
		if p.Hidden || p.Status == usage.StatusAbsent {
			continue
		}
		p.Hidden = false
		st.Providers = append(st.Providers, p)
	}
	st.Sessions = a.store.List()
	st.Aggregate = sessions.Aggregate(st.Sessions)
	return st
}
