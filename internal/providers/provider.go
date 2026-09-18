// Package providers runs the usage readers. Each provider borrows a credential some tool already
// keeps on this machine and asks that vendor's own usage endpoint; none of them signs in by itself.
package providers

import (
	"context"
	"net/http"
	"time"

	"github.com/leock9/gonotch/internal/usage"
)

// Provider reads one vendor's usage.
type Provider interface {
	ID() string
	Name() string
	// UsagePage is the vendor's own usage page, for the notch's menu.
	UsagePage() string
	// Present reports whether the tool is on this machine at all; absent providers get no ring.
	Present() bool
	// Poll makes one reading from the previous snapshot and says how long to wait before the next.
	Poll(ctx context.Context, prev usage.Snapshot) (usage.Snapshot, time.Duration)
	// Probe describes the credential and data sources for `gonotch doctor`, never printing secrets.
	Probe() string
}

// Client is the HTTP client every provider uses. Go verifies against the system's roots, so a
// TLS-inspecting proxy whose CA the machine trusts works as it does for curl.
var Client = &http.Client{Timeout: 15 * time.Second}

// UserAgent goes on every request.
const UserAgent = "gonotch/0.1 (Linux)"

// Runner polls one provider until the context ends, handing each snapshot to publish.
type Runner struct {
	P       Provider
	Refresh chan struct{}
	Publish func(usage.Snapshot)
}

func NewRunner(p Provider, publish func(usage.Snapshot)) *Runner {
	return &Runner{P: p, Refresh: make(chan struct{}, 1), Publish: publish}
}

// RequestRefresh asks for a reading now. It never cuts a rate-limit wait short: asking early only
// spends a request and earns a longer wait.
func (r *Runner) RequestRefresh() {
	select {
	case r.Refresh <- struct{}{}:
	default:
	}
}

// absentRecheck is how often a missing tool is looked for again.
const absentRecheck = 10 * time.Minute

func (r *Runner) Run(ctx context.Context, prev usage.Snapshot) {
	prev.Provider = r.P.ID()
	r.Publish(prev)
	for {
		if !r.P.Present() {
			r.Publish(usage.Snapshot{Provider: r.P.ID(), Status: usage.StatusAbsent})
			if !r.sleep(ctx, absentRecheck) {
				return
			}
			continue
		}
		if wait := time.Until(prev.BackoffUntil); wait > 0 {
			if !sleepCtx(ctx, wait) {
				return
			}
			continue
		}
		next, wait := r.P.Poll(ctx, prev)
		next.Provider = r.P.ID()
		r.Publish(next)
		prev = next
		if !r.sleep(ctx, wait) {
			return
		}
	}
}

// sleep waits, or less if a refresh is asked for; false once the context ends.
func (r *Runner) sleep(ctx context.Context, d time.Duration) bool {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-t.C:
	case <-r.Refresh:
	}
	return true
}

func sleepCtx(ctx context.Context, d time.Duration) bool {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-t.C:
		return true
	}
}
