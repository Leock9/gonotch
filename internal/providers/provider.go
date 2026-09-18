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
	// Enabled, when set and false, pauses polling: a ring switched off costs no requests. A refresh
	// request wakes the runner to look again.
	Enabled func() bool
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
		if r.Enabled != nil && !r.Enabled() {
			if !wait(ctx, absentRecheck, r.Refresh) {
				return
			}
			continue
		}
		if !r.P.Present() {
			r.Publish(usage.Snapshot{Provider: r.P.ID(), Status: usage.StatusAbsent})
			if !wait(ctx, absentRecheck, r.Refresh) {
				return
			}
			continue
		}
		if backoff := time.Until(prev.BackoffUntil); backoff > 0 {
			if !wait(ctx, backoff, nil) {
				return
			}
			continue
		}
		next, after := r.P.Poll(ctx, prev)
		next.Provider = r.P.ID()
		r.Publish(next)
		prev = next
		if !wait(ctx, after, r.Refresh) {
			return
		}
	}
}

// wait sleeps for d; a signal on wake cuts it short, and a nil wake never fires. False once ctx ends.
func wait(ctx context.Context, d time.Duration, wake <-chan struct{}) bool {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-t.C:
	case <-wake:
	}
	return true
}
