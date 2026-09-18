// Package claude reads Claude Code's plan usage the way its own /usage does.
//
//   - The credential is Claude Code's, read from ~/.claude/.credentials.json and never written.
//   - GET https://api.anthropic.com/api/oauth/usage with `anthropic-beta: oauth-2025-04-20`.
//   - 401/403: read the credential again and retry once (Claude Code may have just rotated it).
//   - 429: back off 60 s × 2^n up to 15 min; Retry-After only ever lengthens the wait.
//   - An expired token is never sent: the endpoint answers it with 429 and Retry-After ≈ 3600, which
//     would read as an hour's rate limit. Instead the standalone CLI is run as `claude -p` with an
//     empty stdin shortly before expiry; starting up is where it renews its token.
package claude

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/leock9/gonotch/internal/providers"
	"github.com/leock9/gonotch/internal/usage"
)

const (
	endpoint     = "https://api.anthropic.com/api/oauth/usage"
	pollActive   = time.Minute
	pollIdle     = 5 * time.Minute
	backoffBase  = time.Minute
	backoffCap   = 15 * time.Minute
	expiredNote  = "Credential expired — run claude once in a terminal to renew it"
	missingNote  = "No Claude Code credential found — sign in with `claude`"
	rejectedNote = "Credential rejected (switched accounts?) — run `claude` and sign in"

	// Renew when this close to expiry. It must stay under Claude Code's own five minutes: its start-up
	// renews only when now + 300 s >= expiresAt, so launching earlier does nothing.
	renewMargin   = 4 * time.Minute
	renewCooldown = 10 * time.Minute
	renewRetryCap = time.Hour
	renewTimeout  = 30 * time.Second
)

type Provider struct {
	// Active reports whether a Claude Code session is running: then the ring is read every minute.
	Active func() bool

	home           string
	consecutive429 int
	renew          renewer
}

func New(active func() bool) *Provider {
	home, _ := os.UserHomeDir()
	return &Provider{Active: active, home: home}
}

func (*Provider) ID() string        { return "claude" }
func (*Provider) Name() string      { return "Claude" }
func (*Provider) UsagePage() string { return "https://claude.ai/settings/usage" }

func (p *Provider) Present() bool {
	if _, ok := p.readCredential(); ok {
		return true
	}
	_, err := os.Stat(filepath.Join(p.home, ".claude"))
	return err == nil
}

type credential struct {
	token     string
	expiresAt time.Time // zero = the file names no expiry
}

func (c credential) expired(now time.Time) bool {
	return !c.expiresAt.IsZero() && !now.Before(c.expiresAt)
}

func (p *Provider) readCredential() (credential, bool) {
	for _, name := range []string{".credentials.json", "credentials.json"} {
		raw, err := os.ReadFile(filepath.Join(p.home, ".claude", name))
		if err != nil {
			continue
		}
		c, ok := parseCredential(raw)
		if ok {
			return c, true
		}
	}
	return credential{}, false
}

func parseCredential(raw []byte) (credential, bool) {
	var file struct {
		OAuth *struct {
			AccessToken string  `json:"accessToken"`
			ExpiresAt   float64 `json:"expiresAt"`
		} `json:"claudeAiOauth"`
		AccessToken string  `json:"accessToken"`
		ExpiresAt   float64 `json:"expiresAt"`
	}
	if json.Unmarshal(raw, &file) != nil {
		return credential{}, false
	}
	tok, exp := file.AccessToken, file.ExpiresAt
	if file.OAuth != nil {
		tok, exp = file.OAuth.AccessToken, file.OAuth.ExpiresAt
	}
	if tok == "" {
		return credential{}, false
	}
	c := credential{token: tok}
	if exp > 0 {
		c.expiresAt = time.UnixMilli(int64(exp))
	}
	return c, true
}

func (p *Provider) Poll(ctx context.Context, prev usage.Snapshot) (usage.Snapshot, time.Duration) {
	next := prev
	next.Headline = "session"
	wait := pollIdle
	if p.Active != nil && p.Active() {
		wait = pollActive
	}

	// Ahead of everything: renewing never touches the usage endpoint, and a fresh token deserves a fresh try
	if cred, ok := p.readCredential(); ok && p.renew.maybeRenew(ctx, p, cred) {
		p.consecutive429 = 0
		next.BackoffUntil = time.Time{}
	}

	now := time.Now()
	cred, ok := p.readCredential()
	switch {
	case !ok:
		next.Status, next.Note = usage.StatusNeedsAuth, missingNote
		return next, wait
	case cred.expired(now):
		// Expired is not signed out: keep the last reading, dimmed and dated, and send nothing
		if len(next.Windows) > 0 {
			next.Status = usage.StatusStale
		} else {
			next.Status = usage.StatusNeedsAuth
		}
		next.Note = expiredNote
		return next, wait
	}

	windows, err := fetch(ctx, cred.token)
	var ae authError
	if errors.As(err, &ae) {
		if again, ok := p.readCredential(); ok && again.token != cred.token {
			windows, err = fetch(ctx, again.token)
		}
	}
	var rl rateLimited
	switch {
	case err == nil:
		p.consecutive429 = 0
		next.Status = usage.StatusOK
		next.Windows = windows
		next.FetchedAt = time.Now()
		next.Note = ""
		next.BackoffUntil = time.Time{}
		next.Weekly = weeklyID(windows)
	case errors.As(err, &ae):
		next.Status, next.Note = usage.StatusNeedsAuth, rejectedNote
	case errors.As(err, &rl):
		p.consecutive429++
		d := backoff(p.consecutive429-1, rl.retryAfter)
		if len(next.Windows) > 0 {
			next.Status = usage.StatusStale
		}
		next.Note = fmt.Sprintf("Rate limited, retrying in %d min", int(d.Round(time.Minute).Minutes()))
		next.BackoffUntil = time.Now().Add(d)
	default:
		next = next.Failed(err.Error())
	}
	return next, wait
}

func weeklyID(ws []usage.Window) string {
	for _, id := range []string{"seven_day", "weekly_all", "weekly"} {
		for _, w := range ws {
			if w.ID == id {
				return id
			}
		}
	}
	return ""
}

type authError struct{ code int }

func (e authError) Error() string { return fmt.Sprintf("HTTP %d", e.code) }

type rateLimited struct{ retryAfter time.Duration }

func (rateLimited) Error() string { return "HTTP 429" }

func fetch(ctx context.Context, token string) ([]usage.Window, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("anthropic-beta", "oauth-2025-04-20")
	req.Header.Set("User-Agent", providers.UserAgent)
	resp, err := providers.Client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	switch {
	case resp.StatusCode == 401 || resp.StatusCode == 403:
		return nil, authError{resp.StatusCode}
	case resp.StatusCode == 429:
		secs, _ := strconv.Atoi(strings.TrimSpace(resp.Header.Get("Retry-After")))
		return nil, rateLimited{time.Duration(secs) * time.Second}
	case resp.StatusCode != 200:
		return nil, fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, err
	}
	return parseResponse(raw)
}

func backoff(consecutive int, retryAfter time.Duration) time.Duration {
	d := backoffBase << min(consecutive, 4)
	d = min(max(d, backoffBase), backoffCap)
	// The server's Retry-After is honoured in full: with expired tokens never sent, a long one is a
	// real rate limit, and retrying early only earns another
	return max(d, retryAfter)
}

func labelFor(kind string) string {
	switch kind {
	case "session":
		return "Current session"
	case "seven_day", "weekly_all":
		return "Weekly (all models)"
	case "seven_day_opus", "weekly_opus":
		return "Weekly (Opus)"
	case "weekly_scoped":
		return "Weekly (model-scoped)"
	}
	s := strings.ReplaceAll(kind, "_", " ")
	if s == "" {
		return s
	}
	return strings.ToUpper(s[:1]) + s[1:]
}

// parseResponse reads `limits` — the forward-compatible main shape — and merges the older named
// fields in as a fallback, since a window that just rolled over drops out of limits first.
func parseResponse(raw []byte) ([]usage.Window, error) {
	type named struct {
		Utilization *float64 `json:"utilization"`
		ResetsAt    string   `json:"resets_at"`
	}
	var r struct {
		Limits []struct {
			Kind     string   `json:"kind"`
			Percent  *float64 `json:"percent"`
			ResetsAt string   `json:"resets_at"`
		} `json:"limits"`
		FiveHour *named `json:"five_hour"`
		SevenDay *named `json:"seven_day"`
	}
	if err := json.Unmarshal(raw, &r); err != nil {
		return nil, fmt.Errorf("parse: %w", err)
	}
	var out []usage.Window
	for _, l := range r.Limits {
		reset, err := time.Parse(time.RFC3339, l.ResetsAt)
		if l.Kind == "" || l.Percent == nil || err != nil {
			continue // a window without a reset time is not shown
		}
		out = append(out, usage.Window{ID: l.Kind, Label: labelFor(l.Kind), Used: clamp01(*l.Percent / 100), ResetsAt: reset})
	}
	// The named fields duplicate limits under other ids (limits says weekly_all where the fallback
	// says seven_day), so a fallback is dropped when an alias, the label, or the same reset and
	// percentage is already there.
	fallbacks := []struct {
		field   *named
		id      string
		aliases []string
	}{
		{r.FiveHour, "session", []string{"session", "five_hour"}},
		{r.SevenDay, "seven_day", []string{"seven_day", "weekly_all", "weekly"}},
	}
	for _, f := range fallbacks {
		if f.field == nil || f.field.Utilization == nil {
			continue
		}
		used := clamp01(*f.field.Utilization / 100)
		reset, _ := time.Parse(time.RFC3339, f.field.ResetsAt)
		label := labelFor(f.id)
		dup := false
		for _, w := range out {
			sameReading := !reset.IsZero() && w.ResetsAt.Unix() == reset.Unix() && abs(w.Used-used) < 0.005
			if slices.Contains(f.aliases, w.ID) || w.Label == label || sameReading {
				dup = true
				break
			}
		}
		if !dup {
			out = append(out, usage.Window{ID: f.id, Label: label, Used: used, ResetsAt: reset})
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].ID == "session" && out[j].ID != "session" })
	return out, nil
}

// ---------------- token renewal ----------------

type renewer struct {
	attemptedFor time.Time // the expiry the last launch tried to move
	lastAttempt  time.Time
	failures     int
}

// shouldRenew decides whether a launch is worth making. Pure, so every branch is testable.
func shouldRenew(expiresAt, now, attemptedFor, lastAttempt time.Time, failures int) bool {
	if expiresAt.IsZero() || expiresAt.After(now.Add(renewMargin)) {
		return false
	}
	if lastAttempt.IsZero() {
		return true
	}
	// A launch that failed to move the expiry leaves the same value: try that token again on a
	// doubling wait, so one bad moment (asleep, offline) does not freeze the ring until someone
	// opens a terminal, and a token that cannot renew does not become a launch every tick
	wait := renewCooldown
	if attemptedFor.Equal(expiresAt) {
		wait = min(renewCooldown<<min(failures, 16), renewRetryCap)
	}
	return now.Sub(lastAttempt) >= wait
}

func (r *renewer) maybeRenew(ctx context.Context, p *Provider, cred credential) bool {
	now := time.Now()
	if !shouldRenew(cred.expiresAt, now, r.attemptedFor, r.lastAttempt, r.failures) {
		return false
	}
	cli := p.findCLI()
	if cli == "" {
		return false
	}
	if r.attemptedFor.Equal(cred.expiresAt) {
		r.failures++
	} else {
		r.failures = 0
	}
	r.attemptedFor, r.lastAttempt = cred.expiresAt, now
	runRenewal(ctx, cli)
	after, ok := p.readCredential()
	return ok && after.expiresAt.After(cred.expiresAt)
}

// findCLI finds the standalone Claude Code: the native installer's ~/.local/bin, the older
// ~/.claude/local, then PATH (npm, Homebrew).
func (p *Provider) findCLI() string {
	for _, c := range []string{
		filepath.Join(p.home, ".local", "bin", "claude"),
		filepath.Join(p.home, ".claude", "local", "claude"),
	} {
		if st, err := os.Stat(c); err == nil && !st.IsDir() {
			return c
		}
	}
	if c, err := exec.LookPath("claude"); err == nil {
		return c
	}
	return ""
}

// runRenewal runs `claude -p` with a null stdin: it starts up (renewing an aged token) and exits for
// want of a prompt, with no conversation and no transcript. Its output goes nowhere, since a token
// could in principle be echoed into it.
func runRenewal(ctx context.Context, cli string) {
	ctx, cancel := context.WithTimeout(ctx, renewTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, cli, "-p")
	cmd.Stdin, cmd.Stdout, cmd.Stderr = nil, nil, nil
	// Launched from inside a Claude Code session the child would use the host's auth and leave the file alone
	for _, kv := range os.Environ() {
		k, _, _ := strings.Cut(kv, "=")
		if k == "CLAUDECODE" || strings.HasPrefix(k, "CLAUDE_CODE_") {
			continue
		}
		cmd.Env = append(cmd.Env, kv)
	}
	// Its own process group, so a timeout takes down whatever it started too
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error { return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL) }
	_ = cmd.Run()
}

func (p *Provider) Probe() string {
	cli := p.findCLI()
	renews := "no standalone claude CLI found to renew it"
	if cli != "" {
		renews = "renews via " + cli
	}
	cred, ok := p.readCredential()
	if !ok {
		return "credential: ~/.claude/.credentials.json not found (sign in once with the Claude Code CLI)"
	}
	state := "valid"
	if cred.expired(time.Now()) {
		state = "expired"
	}
	exp := "no expiry"
	if !cred.expiresAt.IsZero() {
		exp = "expires " + cred.expiresAt.Local().Format("2006-01-02 15:04")
	}
	return fmt.Sprintf("credential: found (%s, %s; %s)", state, exp, renews)
}

func clamp01(v float64) float64 { return min(max(v, 0), 1) }

func abs(v float64) float64 {
	if v < 0 {
		return -v
	}
	return v
}
