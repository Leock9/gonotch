// Package app wires the providers, the session store and the local server together, and tells
// whoever subscribes (the notch, the server's /state) when anything changed.
package app

import (
	"context"
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
	usage.Snapshot
}

// State is everything the notch draws.
type State struct {
	Providers []ProviderState    `json:"providers"`
	Sessions  []sessions.Session `json:"sessions"`
	Aggregate sessions.State     `json:"aggregate"`
}

type App struct {
	Cfg   config.Config
	Store *sessions.Store

	providers []providers.Provider
	runners   map[string]*providers.Runner

	mu    sync.Mutex
	snaps map[string]usage.Snapshot
	subs  []chan struct{}
}

func New(cfg config.Config) *App {
	a := &App{Cfg: cfg, Store: sessions.NewStore(), runners: map[string]*providers.Runner{}, snaps: map[string]usage.Snapshot{}}
	a.providers = []providers.Provider{claude.New(a.claudeActive), codex.New(), cursor.New()}
	return a
}

// All providers, including the ones not installed here (for doctor).
func (a *App) Providers() []providers.Provider { return a.providers }

func snapshotPath(id string) string { return filepath.Join(config.StateDir(), id+".json") }

// claudeActive: a session is running or waiting, so Claude's ring is worth reading every minute.
func (a *App) claudeActive() bool {
	agg := sessions.Aggregate(a.Store.List())
	return agg == sessions.Running || agg == sessions.Attention
}

// Start runs the providers, the transcript watcher and the sweeper until ctx ends.
func (a *App) Start(ctx context.Context) {
	for _, p := range a.providers {
		r := providers.NewRunner(p, a.publish)
		a.runners[p.ID()] = r
		go r.Run(ctx, usage.Load(snapshotPath(p.ID())))
	}
	w := sessions.NewWatcher(func(ev sessions.Event) { a.Apply(ev) })
	go w.Run(ctx)
	go func() {
		t := time.NewTicker(30 * time.Second)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				if a.Store.Sweep() {
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
	if a.Store.Apply(ev) {
		a.notify()
	}
}

// Changed is called by the UI after it changes the store itself (a dismissed session).
func (a *App) Changed() { a.notify() }

// Refresh asks one provider (or every one, for "") to read now.
func (a *App) Refresh(id string) {
	for pid, r := range a.runners {
		if id == "" || id == pid {
			r.RequestRefresh()
		}
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

// State returns the providers in their fixed order (absent ones left out) and the sessions.
func (a *App) State() State {
	a.mu.Lock()
	var st State
	for _, p := range a.providers {
		s, ok := a.snaps[p.ID()]
		if !ok || s.Status == usage.StatusAbsent || a.Cfg.IsHidden(p.ID()) {
			continue
		}
		st.Providers = append(st.Providers, ProviderState{ID: p.ID(), Name: p.Name(), UsagePage: p.UsagePage(), Snapshot: s})
	}
	a.mu.Unlock()
	st.Sessions = a.Store.List()
	st.Aggregate = sessions.Aggregate(st.Sessions)
	return st
}
