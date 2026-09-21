// Package demo stands in for the real providers in `gonotch demo`: fixed readings and sessions, no
// network and no credentials, so the notch can be tried, and its screenshots retaken, anywhere.
package demo

import (
	"context"
	"time"

	"github.com/leock9/gonotch/internal/providers"
	"github.com/leock9/gonotch/internal/sessions"
	"github.com/leock9/gonotch/internal/usage"
)

type provider struct {
	id, name, page string
	reading        func(now time.Time) usage.Snapshot
}

func (p provider) ID() string        { return p.id }
func (p provider) Name() string      { return p.name }
func (p provider) UsagePage() string { return p.page }
func (provider) Present() bool       { return true }
func (provider) Probe() string       { return "demo data" }

func (p provider) Poll(_ context.Context, _ usage.Snapshot) (usage.Snapshot, time.Duration) {
	s := p.reading(time.Now())
	s.Status, s.FetchedAt = usage.StatusOK, time.Now()
	return s, time.Minute
}

// Providers returns the four rings, their readings spread over every colour band.
func Providers() []providers.Provider {
	return []providers.Provider{
		provider{"claude", "Claude", "https://claude.ai/settings/usage", func(now time.Time) usage.Snapshot {
			return usage.Snapshot{Headline: "session", Weekly: "weekly_all", Plan: "Max", Windows: []usage.Window{
				{ID: "session", Label: "Current session", Used: 0.34, ResetsAt: now.Add(2*time.Hour + 10*time.Minute)},
				{ID: "weekly_all", Label: "Weekly (all models)", Used: 0.58, ResetsAt: now.Add(76 * time.Hour)},
				{ID: "weekly_scoped", Label: "Weekly (model-scoped)", Used: 0.12, ResetsAt: now.Add(76 * time.Hour)},
			}}
		}},
		provider{"codex", "Codex", "https://chatgpt.com/codex/settings/usage", func(now time.Time) usage.Snapshot {
			return usage.Snapshot{Headline: "primary", Weekly: "secondary", Plan: "plus", Windows: []usage.Window{
				{ID: "primary", Label: "5h limit", Used: 0.62, ResetsAt: now.Add(3*time.Hour + 25*time.Minute)},
				{ID: "secondary", Label: "Weekly limit", Used: 0.18, ResetsAt: now.Add(130 * time.Hour)},
			}}
		}},
		provider{"cursor", "Cursor", "https://cursor.com/dashboard?tab=usage", func(now time.Time) usage.Snapshot {
			return usage.Snapshot{Headline: "included", Plan: "Pro", Windows: []usage.Window{
				{ID: "included", Label: "Included usage", Used: 0.87, ResetsAt: now.Add(11 * 24 * time.Hour)},
			}}
		}},
		provider{"copilot", "GitHub Copilot", "https://github.com/settings/copilot/features", func(now time.Time) usage.Snapshot {
			// A paid plan meters premium requests only; they reset on the 1st, 00:00 UTC
			reset := time.Date(now.Year(), now.Month()+1, 1, 0, 0, 0, 0, time.UTC)
			return usage.Snapshot{Headline: "premium_interactions", Plan: "Individual", Windows: []usage.Window{
				{ID: "premium_interactions", Label: "Premium requests", Used: 0.46, ResetsAt: reset},
			}}
		}},
	}
}

// Sessions are the Claude Code sessions the demo starts with: one working, one finished.
func Sessions() []sessions.Event {
	return []sessions.Event{
		{Kind: sessions.EvDone, SessionID: "4b1e9c", CWD: "/home/you/docs", Prompt: "update the changelog", FromHook: true},
		{Kind: sessions.EvRunning, SessionID: "7f3a21", CWD: "/home/you/gonotch", Prompt: "add the settings window", ToolName: "Bash", ToolCmd: "go test ./...", FromHook: true},
	}
}
