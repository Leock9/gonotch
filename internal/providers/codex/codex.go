// Package codex reads the Codex CLI's rate limits.
//
// Live: the session Codex keeps in ~/.codex/auth.json (tokens.access_token + tokens.account_id),
// read only and never refreshed, against GET https://chatgpt.com/backend-api/wham/usage. The reply's
// rate_limit.{primary_window,secondary_window} carry used_percent, limit_window_seconds and reset_at
// (epoch seconds) or reset_after_seconds.
//
// Fallback: every turn Codex writes the limits it saw into its rollout log,
// ~/.codex/sessions/YYYY/MM/DD/rollout-*.jsonl, as payload.rate_limits.{primary,secondary} with
// window_minutes and resets_at. That is the number from the last run, dated by the line itself.
package codex

import (
	"bufio"
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/leock9/gonotch/internal/providers"
	"github.com/leock9/gonotch/internal/usage"
)

const (
	endpoint   = "https://chatgpt.com/backend-api/wham/usage"
	pollEvery  = 5 * time.Minute
	backoffMin = time.Minute
	tailBytes  = 256 * 1024
)

type Provider struct{ home string }

func New() *Provider {
	if h := os.Getenv("CODEX_HOME"); filepath.IsAbs(h) {
		return &Provider{home: h}
	}
	home, _ := os.UserHomeDir()
	return &Provider{home: filepath.Join(home, ".codex")}
}

func (*Provider) ID() string        { return "codex" }
func (*Provider) Name() string      { return "Codex" }
func (*Provider) UsagePage() string { return "https://chatgpt.com/codex/settings/usage" }

func (p *Provider) authPath() string { return filepath.Join(p.home, "auth.json") }

// Present: the CLI is installed, signed in, or has left sessions behind.
func (p *Provider) Present() bool {
	if _, err := exec.LookPath("codex"); err == nil {
		return true
	}
	for _, f := range []string{p.authPath(), filepath.Join(p.home, "sessions")} {
		if _, err := os.Stat(f); err == nil {
			return true
		}
	}
	return false
}

type credential struct {
	accessToken string
	accountID   string
	plan        string
	expired     bool
}

func (p *Provider) readCredential() (credential, bool) {
	raw, err := os.ReadFile(p.authPath())
	if err != nil {
		return credential{}, false
	}
	return parseAuth(raw, time.Now())
}

func parseAuth(raw []byte, now time.Time) (credential, bool) {
	var f struct {
		Tokens *struct {
			AccessToken string `json:"access_token"`
			AccountID   string `json:"account_id"`
			IDToken     string `json:"id_token"`
		} `json:"tokens"`
	}
	if json.Unmarshal(raw, &f) != nil || f.Tokens == nil {
		return credential{}, false
	}
	c := credential{accessToken: strings.TrimSpace(f.Tokens.AccessToken), accountID: strings.TrimSpace(f.Tokens.AccountID)}
	if c.accessToken == "" || c.accountID == "" {
		return credential{}, false
	}
	// Claims are read for a label and an expiry hint only; verifying them is the server's job
	var access struct {
		Exp float64 `json:"exp"`
	}
	if jwtClaims(c.accessToken, &access) && access.Exp > 0 {
		c.expired = float64(now.Unix()) >= access.Exp
	}
	var id struct {
		Auth struct {
			Plan string `json:"chatgpt_plan_type"`
		} `json:"https://api.openai.com/auth"`
	}
	if jwtClaims(f.Tokens.IDToken, &id) {
		c.plan = id.Auth.Plan
	}
	return c, true
}

func jwtClaims(token string, into any) bool {
	parts := strings.Split(token, ".")
	if len(parts) < 2 {
		return false
	}
	raw, err := base64.RawURLEncoding.DecodeString(strings.TrimRight(parts[1], "="))
	return err == nil && json.Unmarshal(raw, into) == nil
}

func (p *Provider) Poll(ctx context.Context, prev usage.Snapshot) (usage.Snapshot, time.Duration) {
	next := prev
	next.Headline, next.Weekly = "primary", "secondary"
	cred, ok := p.readCredential()
	if !ok {
		return p.fromRollout(next, "Sign in with `codex login` for live readings"), pollEvery
	}
	next.Plan = cred.plan
	raw, err := fetch(ctx, cred)
	var rl rateLimited
	switch {
	case err == nil:
		windows, perr := windowsFromUsage(raw, time.Now())
		if perr != nil || len(windows) == 0 {
			return p.fromRollout(next, "Codex answered without rate limits"), pollEvery
		}
		next.Status = usage.StatusOK
		next.Windows = windows
		next.FetchedAt = time.Now()
		next.Note = ""
		next.BackoffUntil = time.Time{}
	case err == errAuth:
		note := "Codex session rejected — run `codex login`"
		if cred.expired {
			note = "Codex session expired — run codex once to renew it"
		}
		next = p.fromRollout(next, note)
		if next.Status != usage.StatusStale {
			next.Status, next.Note = usage.StatusNeedsAuth, note
		}
	case asRateLimited(err, &rl):
		next = next.Failed(fmt.Sprintf("Rate limited, retrying in %s", rl.wait))
		next.BackoffUntil = time.Now().Add(rl.wait)
	default:
		next = p.fromRollout(next, err.Error())
	}
	return next, pollEvery
}

// fromRollout falls back to the newest rollout's snapshot, dated by its own timestamp.
func (p *Provider) fromRollout(next usage.Snapshot, why string) usage.Snapshot {
	path := p.newestRollout()
	if path == "" {
		if len(next.Windows) > 0 {
			return next.Failed(why)
		}
		next.Status, next.Note = usage.StatusNeedsAuth, why
		return next
	}
	windows, recorded, plan, ok := snapshotFromRollout(tail(path), time.Now())
	if !ok {
		return next.Failed(why)
	}
	next.Windows = windows
	next.FetchedAt = recorded
	if plan != "" {
		next.Plan = plan
	}
	next.Status = usage.StatusStale
	if time.Since(recorded) < usage.StaleAfter {
		next.Status = usage.StatusOK
	}
	next.Note = "From the last Codex run · " + why
	return next
}

var errAuth = fmt.Errorf("unauthorized")

type rateLimited struct{ wait time.Duration }

func (r rateLimited) Error() string { return "HTTP 429" }

func asRateLimited(err error, into *rateLimited) bool {
	r, ok := err.(rateLimited)
	if ok {
		*into = r
	}
	return ok
}

func fetch(ctx context.Context, c credential) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+c.accessToken)
	req.Header.Set("ChatGPT-Account-Id", c.accountID)
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Cache-Control", "no-cache, no-store")
	req.Header.Set("User-Agent", providers.UserAgent)
	resp, err := providers.Client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	switch {
	case resp.StatusCode == 401 || resp.StatusCode == 403:
		return nil, errAuth
	case resp.StatusCode == 429:
		secs, _ := strconv.Atoi(strings.TrimSpace(resp.Header.Get("Retry-After")))
		return nil, rateLimited{max(time.Duration(secs)*time.Second, backoffMin)}
	case resp.StatusCode != 200:
		return nil, fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	return io.ReadAll(io.LimitReader(resp.Body, 1<<20))
}

// labelFor names a window by its length, since "5h limit" says more than "primary". The primary
// window is not always five hours (a free plan has shown 30 days).
func labelFor(minutes float64, id string) string {
	switch {
	case minutes <= 0:
		if id == "primary" {
			return "Current session"
		}
		return "Longer window"
	case minutes < 60:
		return fmt.Sprintf("%dm limit", int(minutes))
	case minutes < 24*60:
		return fmt.Sprintf("%dh limit", int(minutes/60))
	}
	switch days := int(math.Round(minutes / (24 * 60))); days {
	case 7:
		return "Weekly limit"
	case 30:
		return "Monthly limit"
	default:
		return fmt.Sprintf("%dd limit", days)
	}
}

type liveWindow struct {
	UsedPercent        *float64 `json:"used_percent"`
	LimitWindowSeconds float64  `json:"limit_window_seconds"`
	ResetAt            float64  `json:"reset_at"`
	ResetAfterSeconds  float64  `json:"reset_after_seconds"`
}

type livePair struct {
	Primary   *liveWindow `json:"primary_window"`
	Secondary *liveWindow `json:"secondary_window"`
}

func (w *liveWindow) window(id, labelID, group string, now time.Time) (usage.Window, bool) {
	if w == nil || w.UsedPercent == nil {
		return usage.Window{}, false
	}
	out := usage.Window{ID: id, Label: labelFor(w.LimitWindowSeconds/60, labelID), Used: clamp01(*w.UsedPercent / 100), Group: group}
	switch {
	case w.ResetAt > 0:
		out.ResetsAt = time.Unix(int64(w.ResetAt), 0)
	case w.ResetAfterSeconds > 0:
		out.ResetsAt = now.Add(time.Duration(w.ResetAfterSeconds * float64(time.Second)))
	}
	return out, true
}

// windowsFromUsage: primary and secondary feed the ring; Spark and Code review go on the card,
// grouped, after the main pair.
func windowsFromUsage(raw []byte, now time.Time) ([]usage.Window, error) {
	var r struct {
		RateLimit  *livePair `json:"rate_limit"`
		Additional []struct {
			LimitName      string    `json:"limit_name"`
			MeteredFeature string    `json:"metered_feature"`
			RateLimit      *livePair `json:"rate_limit"`
		} `json:"additional_rate_limits"`
		CodeReview *livePair `json:"code_review_rate_limit"`
	}
	// A junk extra must not cost the main pair, so the extras are decoded leniently on their own
	var main struct {
		RateLimit  *livePair `json:"rate_limit"`
		CodeReview *livePair `json:"code_review_rate_limit"`
	}
	if err := json.Unmarshal(raw, &main); err != nil {
		return nil, fmt.Errorf("parse: %w", err)
	}
	_ = json.Unmarshal(raw, &r)
	var out []usage.Window
	add := func(w usage.Window, ok bool) {
		if !ok {
			return
		}
		for _, x := range out {
			if x.ID == w.ID {
				return
			}
		}
		out = append(out, w)
	}
	if main.RateLimit != nil {
		add(main.RateLimit.Primary.window("primary", "primary", "", now))
		add(main.RateLimit.Secondary.window("secondary", "secondary", "", now))
	}
	for _, x := range r.Additional {
		name := strings.ToLower(x.LimitName + " " + x.MeteredFeature)
		if !strings.Contains(name, "spark") || x.RateLimit == nil {
			continue
		}
		add(x.RateLimit.Primary.window("spark", "primary", "Spark", now))
		add(x.RateLimit.Secondary.window("spark-secondary", "secondary", "Spark", now))
	}
	if main.CodeReview != nil {
		add(main.CodeReview.Primary.window("code-review", "primary", "Code review", now))
		add(main.CodeReview.Secondary.window("code-review-secondary", "secondary", "Code review", now))
	}
	return out, nil
}

// newestRollout picks the most recently written rollout from the three newest dated directories.
func (p *Provider) newestRollout() string {
	var days []string
	years := dirs(filepath.Join(p.home, "sessions"))
outer:
	for _, y := range years {
		for _, m := range dirs(y) {
			for _, d := range dirs(m) {
				days = append(days, d)
				if len(days) >= 3 {
					break outer
				}
			}
		}
	}
	best, bestTime := "", time.Time{}
	for _, d := range days {
		entries, _ := os.ReadDir(d)
		for _, e := range entries {
			n := e.Name()
			if !strings.HasPrefix(n, "rollout-") || !strings.HasSuffix(n, ".jsonl") {
				continue
			}
			info, err := e.Info()
			if err == nil && info.ModTime().After(bestTime) {
				best, bestTime = filepath.Join(d, n), info.ModTime()
			}
		}
	}
	return best
}

// dirs lists subdirectories newest name first (the tree is YYYY/MM/DD).
func dirs(p string) []string {
	entries, _ := os.ReadDir(p)
	var out []string
	for _, e := range entries {
		if e.IsDir() {
			out = append(out, filepath.Join(p, e.Name()))
		}
	}
	sort.Sort(sort.Reverse(sort.StringSlice(out)))
	return out
}

func tail(path string) []byte {
	f, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer f.Close()
	if st, err := f.Stat(); err == nil && st.Size() > tailBytes {
		_, _ = f.Seek(st.Size()-tailBytes, io.SeekStart)
	}
	raw, _ := io.ReadAll(f)
	return raw
}

// snapshotFromRollout finds the last rate_limits line in a rollout's tail.
func snapshotFromRollout(text []byte, now time.Time) (windows []usage.Window, recorded time.Time, plan string, ok bool) {
	type rolloutWindow struct {
		UsedPercent     *float64 `json:"used_percent"`
		WindowMinutes   float64  `json:"window_minutes"`
		ResetsAt        float64  `json:"resets_at"`
		ResetsInSeconds float64  `json:"resets_in_seconds"`
	}
	type limits struct {
		Primary   *rolloutWindow `json:"primary"`
		Secondary *rolloutWindow `json:"secondary"`
		PlanType  string         `json:"plan_type"`
	}
	var lines [][]byte
	sc := bufio.NewScanner(bytes.NewReader(text))
	sc.Buffer(make([]byte, 0, 64*1024), tailBytes)
	for sc.Scan() {
		if bytes.Contains(sc.Bytes(), []byte("rate_limits")) {
			lines = append(lines, append([]byte(nil), sc.Bytes()...))
		}
	}
	for i := len(lines) - 1; i >= 0; i-- {
		var v struct {
			Timestamp  string  `json:"timestamp"`
			RateLimits *limits `json:"rate_limits"`
			Payload    struct {
				RateLimits *limits `json:"rate_limits"`
			} `json:"payload"`
		}
		if json.Unmarshal(lines[i], &v) != nil {
			continue
		}
		rl := v.RateLimits
		if rl == nil {
			rl = v.Payload.RateLimits
		}
		if rl == nil {
			continue
		}
		var out []usage.Window
		for _, x := range []struct {
			id string
			w  *rolloutWindow
		}{{"primary", rl.Primary}, {"secondary", rl.Secondary}} {
			if x.w == nil || x.w.UsedPercent == nil {
				continue
			}
			w := usage.Window{ID: x.id, Label: labelFor(x.w.WindowMinutes, x.id), Used: clamp01(*x.w.UsedPercent / 100)}
			switch {
			case x.w.ResetsAt > 0:
				w.ResetsAt = time.Unix(int64(x.w.ResetsAt), 0)
			case x.w.ResetsInSeconds > 0:
				w.ResetsAt = now.Add(time.Duration(x.w.ResetsInSeconds * float64(time.Second)))
			}
			out = append(out, w)
		}
		if len(out) == 0 {
			continue
		}
		ts, _ := time.Parse(time.RFC3339Nano, v.Timestamp)
		return out, ts, rl.PlanType, true
	}
	return nil, time.Time{}, "", false
}

func (p *Provider) Probe() string {
	auth := "auth.json not found"
	if c, ok := p.readCredential(); ok {
		auth = "auth.json usable"
		if c.expired {
			auth += " (access token expired)"
		}
		if c.plan != "" {
			auth += ", plan=" + c.plan
		}
	} else if _, err := os.Stat(p.authPath()); err == nil {
		auth = "auth.json present but holds no token"
	}
	exe := "not found"
	if e, err := exec.LookPath("codex"); err == nil {
		exe = e
	}
	roll := p.newestRollout()
	if roll == "" {
		roll = "none"
	}
	return fmt.Sprintf("%s | executable %s | newest rollout %s", auth, exe, roll)
}

func clamp01(v float64) float64 { return min(max(v, 0), 1) }
