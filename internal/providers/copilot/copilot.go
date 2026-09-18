// Package copilot reads GitHub Copilot's monthly quotas with a GitHub sign-in already on this machine.
//
// GET https://api.github.com/copilot_internal/user, the endpoint Copilot's own editors ask, answers
// with quota_snapshots (premium_interactions, chat, completions), each an entitlement and what is left
// of it, all resetting at quota_reset_date_utc: the 1st of the month, 00:00 UTC. A quota that is
// unlimited, or whose entitlement is 0 (premium requests on Copilot Free), meters nothing.
//
// The token is borrowed, read-only, from the first of:
//   - ~/.config/github-copilot/apps.json (hosts.json for older plugins): the sign-in of Copilot's own
//     editor plugins, so the ring follows the account Copilot is used with;
//   - gh's ~/.config/gh/hosts.yml, where gh keeps its token when there is no keyring;
//   - `gh auth token`, for a token gh keeps in the keyring.
//
// A token GitHub rejects makes way for the next. VS Code and the Copilot CLI keep theirs in the
// keyring, which gonotch does not open: with only those, gh has to be signed in.
package copilot

import (
	"cmp"
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
	"strconv"
	"strings"
	"time"

	"github.com/leock9/gonotch/internal/providers"
	"github.com/leock9/gonotch/internal/usage"
)

const (
	endpoint   = "https://api.github.com/copilot_internal/user"
	pollEvery  = 5 * time.Minute
	backoffMin = time.Minute
)

type Provider struct {
	copilotDir string // Copilot's editor plugins: apps.json, hosts.json
	ghHosts    string
	// markers: any of these on disk means Copilot is used here, in a plugin, the CLI or VS Code
	markers  []string
	endpoint string
	ghToken  func(ctx context.Context) string
}

func New() *Provider {
	home, _ := os.UserHomeDir()
	cfg := os.Getenv("XDG_CONFIG_HOME")
	if !filepath.IsAbs(cfg) {
		cfg = filepath.Join(home, ".config")
	}
	gh := os.Getenv("GH_CONFIG_DIR")
	if !filepath.IsAbs(gh) {
		gh = filepath.Join(cfg, "gh")
	}
	dir := filepath.Join(cfg, "github-copilot")
	return &Provider{
		copilotDir: dir,
		ghHosts:    filepath.Join(gh, "hosts.yml"),
		markers:    []string{dir, filepath.Join(home, ".copilot"), filepath.Join(home, ".vscode*", "extensions", "github.copilot*")},
		endpoint:   endpoint,
		ghToken:    ghAuthToken,
	}
}

func (*Provider) ID() string        { return "copilot" }
func (*Provider) Name() string      { return "GitHub Copilot" }
func (*Provider) UsagePage() string { return "https://github.com/settings/copilot/features" }

// Present looks for Copilot itself, not for gh: plenty of machines have gh signed in and no Copilot.
func (p *Provider) Present() bool {
	for _, m := range p.markers {
		if found, _ := filepath.Glob(m); len(found) > 0 {
			return true
		}
	}
	return false
}

type credential struct {
	source string
	user   string
	token  string
	// keyring: the token is gh's, asked of `gh auth token` only when its turn comes, so a working
	// plugin sign-in never wakes the keyring
	keyring bool
}

// credentials lists the tokens on hand in the order they are tried, read afresh on every poll.
func (p *Provider) credentials() []credential {
	var out []credential
	add := func(c credential) {
		if c.token != "" && !slices.ContainsFunc(out, func(o credential) bool { return o.token == c.token }) {
			out = append(out, c)
		}
	}
	for _, name := range []string{"apps.json", "hosts.json"} {
		path := filepath.Join(p.copilotDir, name)
		raw, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		for _, c := range parseApps(raw) {
			c.source = path
			add(c)
		}
	}
	raw, _ := os.ReadFile(p.ghHosts)
	user, token := parseGHHosts(string(raw))
	add(credential{source: p.ghHosts, user: user, token: token})
	if token == "" {
		out = append(out, credential{source: "gh auth token", user: user, keyring: true})
	}
	return out
}

// parseApps reads the plugins' files: {"github.com:<client id>": {"user", "oauth_token"}}, or
// "github.com" alone in hosts.json. Other hosts are GitHub Enterprise servers, not api.github.com.
func parseApps(raw []byte) []credential {
	var m map[string]struct {
		User  string `json:"user"`
		Token string `json:"oauth_token"`
	}
	if json.Unmarshal(raw, &m) != nil {
		return nil
	}
	keys := make([]string, 0, len(m))
	for k := range m {
		if host, _, _ := strings.Cut(k, ":"); host == "github.com" {
			keys = append(keys, k)
		}
	}
	slices.Sort(keys)
	out := make([]credential, 0, len(keys))
	for _, k := range keys {
		out = append(out, credential{user: m[k].User, token: strings.TrimSpace(m[k].Token)})
	}
	return out
}

// parseGHHosts takes github.com's user and oauth_token from gh's hosts.yml. Only the host's direct
// keys count: newer gh also lists each account under users:, and the direct pair is the active one.
func parseGHHosts(raw string) (user, token string) {
	in, indent := false, ""
	for _, line := range strings.Split(raw, "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}
		lead := line[:len(line)-len(strings.TrimLeft(line, " \t"))]
		if lead == "" {
			in, indent = trimmed == "github.com:", ""
			continue
		}
		if !in {
			continue
		}
		if indent == "" {
			indent = lead
		}
		key, val, ok := strings.Cut(trimmed, ":")
		if lead != indent || !ok {
			continue
		}
		val = strings.Trim(strings.TrimSpace(val), `"'`)
		switch key {
		case "user":
			user = val
		case "oauth_token":
			token = val
		}
	}
	return user, token
}

// ghAuthToken asks gh for the token it keeps in the keyring. gh older than 2.17 has no `auth token`,
// but keeps the token in hosts.yml instead.
func ghAuthToken(ctx context.Context) string {
	bin, err := exec.LookPath("gh")
	if err != nil {
		// an autostarted session's PATH can be shorter than a terminal's
		for _, b := range []string{"/usr/local/bin/gh", "/home/linuxbrew/.linuxbrew/bin/gh", "/snap/bin/gh"} {
			if _, err := os.Stat(b); err == nil {
				bin = b
				break
			}
		}
	}
	if bin == "" {
		return ""
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, bin, "auth", "token", "--hostname", "github.com").Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

func (p *Provider) Poll(ctx context.Context, prev usage.Snapshot) (usage.Snapshot, time.Duration) {
	next := prev
	var raw []byte
	err := errNoToken
	for _, c := range p.credentials() {
		token := c.token
		if c.keyring {
			token = p.ghToken(ctx)
		}
		if token == "" {
			continue
		}
		if raw, err = p.fetch(ctx, token); !errors.Is(err, errAuth) {
			break
		}
	}
	var rl rateLimited
	switch {
	case errors.Is(err, errNoToken):
		next.Status = usage.StatusNeedsAuth
		next.Note = "Sign in with `gh auth login` to see Copilot's quota"
		return next, pollEvery
	case errors.Is(err, errAuth):
		next.Status = usage.StatusNeedsAuth
		next.Note = "GitHub rejected the sign-in — run `gh auth login` again"
		return next, pollEvery
	case errors.As(err, &rl):
		next = next.Failed(fmt.Sprintf("Rate limited, retrying in %s", rl.wait))
		next.BackoffUntil = time.Now().Add(rl.wait)
		return next, pollEvery
	case err != nil:
		return next.Failed(err.Error()), pollEvery
	}
	r, err := parseUser(raw)
	if err != nil {
		return next.Failed(err.Error()), pollEvery
	}
	next.FetchedAt = time.Now()
	next.BackoffUntil = time.Time{}
	next.Plan = r.plan
	next.Windows = r.windows
	next.Headline, next.Weekly = "", ""
	if len(r.windows) == 0 {
		next.Status, next.Note = usage.StatusNone, r.note
		return next, pollEvery
	}
	next.Status, next.Note = usage.StatusOK, ""
	next.Headline = r.windows[0].ID
	if len(r.windows) > 1 {
		next.Weekly = r.windows[1].ID
	}
	return next, pollEvery
}

var (
	errAuth    = errors.New("unauthorized")
	errNoToken = errors.New("no token")
)

type rateLimited struct{ wait time.Duration }

func (rateLimited) Error() string { return "rate limited" }

func (p *Provider) fetch(ctx context.Context, token string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, p.endpoint, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Accept", "application/json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	req.Header.Set("User-Agent", providers.UserAgent)
	resp, err := providers.Client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if wait, limited := rateLimit(resp, time.Now()); limited {
		return nil, rateLimited{wait}
	}
	switch {
	case resp.StatusCode == 401 || resp.StatusCode == 403:
		return nil, errAuth
	case resp.StatusCode != 200:
		return nil, fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	return io.ReadAll(io.LimitReader(resp.Body, 1<<20))
}

// rateLimit reads GitHub's two kinds of limit, which can come as a 403 as well as a 429: the primary
// one, with x-ratelimit-remaining 0 until x-ratelimit-reset, and the secondary one, with Retry-After.
func rateLimit(resp *http.Response, now time.Time) (time.Duration, bool) {
	if resp.StatusCode != 403 && resp.StatusCode != 429 {
		return 0, false
	}
	if secs, err := strconv.Atoi(strings.TrimSpace(resp.Header.Get("Retry-After"))); err == nil {
		return max(time.Duration(secs)*time.Second, backoffMin), true
	}
	if resp.Header.Get("X-Ratelimit-Remaining") == "0" {
		reset, _ := strconv.ParseInt(resp.Header.Get("X-Ratelimit-Reset"), 10, 64)
		return max(time.Unix(reset, 0).Sub(now), backoffMin), true
	}
	return backoffMin, resp.StatusCode == 429
}

type reading struct {
	windows []usage.Window
	plan    string
	note    string
}

// order puts the scarce quota first: premium requests are what a paid plan runs out of.
var order = []string{"premium_interactions", "chat", "completions"}

var labels = map[string]string{
	"premium_interactions": "Premium requests",
	"chat":                 "Chat requests",
	"completions":          "Code completions",
}

func parseUser(raw []byte) (reading, error) {
	var r struct {
		Login        string `json:"login"`
		Plan         string `json:"copilot_plan"`
		SKU          string `json:"access_type_sku"`
		ResetDateUTC string `json:"quota_reset_date_utc"`
		ResetDate    string `json:"quota_reset_date"`
		Quotas       map[string]struct {
			Entitlement      *float64 `json:"entitlement"`
			Remaining        *float64 `json:"remaining"`
			PercentRemaining *float64 `json:"percent_remaining"`
			Unlimited        bool     `json:"unlimited"`
		} `json:"quota_snapshots"`
	}
	if err := json.Unmarshal(raw, &r); err != nil {
		return reading{}, fmt.Errorf("parse: %w", err)
	}
	reset, err := time.Parse(time.RFC3339, r.ResetDateUTC)
	if err != nil {
		reset, _ = time.Parse(time.DateOnly, r.ResetDate)
	}
	ids := make([]string, 0, len(r.Quotas))
	for id := range r.Quotas {
		if !slices.Contains(order, id) {
			ids = append(ids, id)
		}
	}
	slices.Sort(ids)
	var out reading
	unlimited := 0
	for _, id := range append(slices.Clone(order), ids...) {
		q, ok := r.Quotas[id]
		switch {
		case !ok:
			continue
		case q.Unlimited:
			unlimited++
			continue
		case q.Entitlement == nil || *q.Entitlement <= 0:
			continue
		}
		var used float64
		switch {
		case q.Remaining != nil:
			used = 1 - *q.Remaining / *q.Entitlement
		case q.PercentRemaining != nil:
			used = 1 - *q.PercentRemaining/100
		default:
			continue
		}
		out.windows = append(out.windows, usage.Window{ID: id, Label: label(id), Used: min(max(used, 0), 1), ResetsAt: reset})
	}
	out.plan = planName(r.SKU, r.Plan)
	if r.Login != "" {
		if out.plan != "" {
			out.plan += " · "
		}
		out.plan += r.Login
	}
	if len(out.windows) == 0 {
		out.note = "Copilot meters nothing on this account"
		if unlimited > 0 {
			out.note = "Unlimited on this plan — nothing to meter"
		}
	}
	return out, nil
}

func label(id string) string {
	if l, ok := labels[id]; ok {
		return l
	}
	if id == "" {
		return id
	}
	s := strings.ReplaceAll(id, "_", " ")
	return strings.ToUpper(s[:1]) + s[1:]
}

// planName says Free where the SKU says so; copilot_plan alone reads "individual" on Free and paid
// plans alike, so it is shown as it comes.
func planName(sku, plan string) string {
	if strings.HasPrefix(sku, "free_") {
		return "Free"
	}
	if plan == "" {
		return ""
	}
	return strings.ToUpper(plan[:1]) + plan[1:]
}

func (p *Provider) Probe() string {
	if !p.Present() {
		return "no Copilot plugin, CLI or VS Code extension found (not installed)"
	}
	var parts []string
	for _, c := range p.credentials() {
		if c.keyring {
			parts = append(parts, "`gh auth token` (the keyring), if gh is signed in")
			continue
		}
		parts = append(parts, fmt.Sprintf("%s (user=%s, token %d chars)", c.source, cmp.Or(c.user, "?"), len(c.token)))
	}
	return "tokens, in the order tried: " + strings.Join(parts, "; ")
}
