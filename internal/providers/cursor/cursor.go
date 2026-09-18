// Package cursor reads the Cursor editor's plan usage with the editor's own session.
//
// The editor keeps its sign-in in the state database it inherited from VS Code,
// ~/.config/Cursor/User/globalStorage/state.vscdb (SQLite, ItemTable(key, value)). The session cookie
// is WorkosCursorSessionToken=<user id>::<access token>; the user id comes from
// cursorAuth/stripeMembershipAuthId where older builds wrote it, and otherwise from the access
// token's own `sub` claim (`<provider>|user_…`), which is all newer builds keep.
//
// GET https://cursor.com/api/usage-summary answers with a percentage of the allowance, not a request
// count: individualUsage.plan.totalPercentUsed is the dashboard's "Included usage · N% used".
package cursor

import (
	"context"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/leock9/gonotch/internal/providers"
	"github.com/leock9/gonotch/internal/usage"
	_ "modernc.org/sqlite"
)

const (
	endpoint  = "https://cursor.com/api/usage-summary"
	pollEvery = 5 * time.Minute
)

type Provider struct{ db string }

func New() *Provider {
	base := os.Getenv("XDG_CONFIG_HOME")
	if !filepath.IsAbs(base) {
		home, _ := os.UserHomeDir()
		base = filepath.Join(home, ".config")
	}
	return &Provider{db: filepath.Join(base, "Cursor", "User", "globalStorage", "state.vscdb")}
}

func (*Provider) ID() string        { return "cursor" }
func (*Provider) Name() string      { return "Cursor" }
func (*Provider) UsagePage() string { return "https://cursor.com/dashboard?tab=usage" }

func (p *Provider) Present() bool {
	_, err := os.Stat(p.db)
	return err == nil
}

// open tries mode=ro first, which sees a token the editor just rotated into the WAL, then
// immutable=1, which is what still opens once the editor has quit and taken its -shm with it.
func (p *Provider) open() (*sql.DB, error) {
	var lastErr error
	for _, q := range []string{"mode=ro", "immutable=1"} {
		u := url.URL{Scheme: "file", Path: p.db, RawQuery: q}
		db, err := sql.Open("sqlite", u.String())
		if err != nil {
			lastErr = err
			continue
		}
		if _, err = db.Exec("SELECT 1 FROM ItemTable LIMIT 1"); err == nil {
			return db, nil
		}
		lastErr = err
		db.Close()
	}
	return nil, lastErr
}

type credential struct {
	cookie string
	plan   string
}

// readCredential reads the session afresh every time: the editor rotates the token, and holding an
// old one signs us out.
func (p *Provider) readCredential() (credential, error) {
	db, err := p.open()
	if err != nil {
		return credential{}, err
	}
	defer db.Close()
	item := func(key string) string {
		var v string
		_ = db.QueryRow("SELECT value FROM ItemTable WHERE key = ?", key).Scan(&v)
		return strings.TrimSpace(v)
	}
	token := item("cursorAuth/accessToken")
	if token == "" {
		return credential{}, fmt.Errorf("not signed in")
	}
	userID := item("cursorAuth/stripeMembershipAuthId")
	if userID == "" {
		userID = userIDFromToken(token)
	}
	if userID == "" {
		return credential{}, fmt.Errorf("no user id in the session")
	}
	return credential{
		cookie: "WorkosCursorSessionToken=" + url.QueryEscape(userID+"::"+token),
		plan:   item("cursorAuth/stripeMembershipType"),
	}, nil
}

// userIDFromToken takes the part after '|' in the token's sub claim ("google-oauth2|user_…").
func userIDFromToken(token string) string {
	parts := strings.Split(token, ".")
	if len(parts) < 2 {
		return ""
	}
	raw, err := base64.RawURLEncoding.DecodeString(strings.TrimRight(parts[1], "="))
	if err != nil {
		return ""
	}
	var c struct {
		Sub string `json:"sub"`
	}
	if json.Unmarshal(raw, &c) != nil {
		return ""
	}
	if _, id, ok := strings.Cut(c.Sub, "|"); ok {
		return id
	}
	return c.Sub
}

func (p *Provider) Poll(ctx context.Context, prev usage.Snapshot) (usage.Snapshot, time.Duration) {
	next := prev
	next.Headline = "included"
	cred, err := p.readCredential()
	if err != nil {
		next.Status = usage.StatusNeedsAuth
		next.Note = "Sign in to the Cursor editor to see its usage"
		return next, pollEvery
	}
	raw, err := fetch(ctx, cred.cookie)
	switch {
	case err == errAuth:
		next.Status = usage.StatusNeedsAuth
		next.Note = "Cursor rejected the session — sign in again in the editor"
		return next, pollEvery
	case err != nil:
		return next.Failed(err.Error()), pollEvery
	}
	windows, note, plan, err := parseSummary(raw)
	if err != nil {
		return next.Failed(err.Error()), pollEvery
	}
	next.FetchedAt = time.Now()
	next.Plan = firstNonEmpty(plan, cred.plan)
	next.Note = note
	next.Windows = windows
	if len(windows) == 0 {
		next.Status = usage.StatusNone
		return next, pollEvery
	}
	next.Status = usage.StatusOK
	if _, ok := next.Window("included"); !ok {
		next.Headline = windows[0].ID
	}
	return next, pollEvery
}

var errAuth = fmt.Errorf("unauthorized")

func fetch(ctx context.Context, cookie string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Cookie", cookie)
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", providers.UserAgent)
	resp, err := providers.Client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	switch {
	case resp.StatusCode == 401 || resp.StatusCode == 403:
		return nil, errAuth
	case resp.StatusCode != 200:
		return nil, fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	return io.ReadAll(io.LimitReader(resp.Body, 1<<20))
}

// parseSummary turns usage-summary into windows. 0 % is a reading too; on the free plan used/limit
// are always 0, so only the percentages are trusted.
func parseSummary(raw []byte) (windows []usage.Window, note, plan string, err error) {
	var r struct {
		BillingCycleEnd string `json:"billingCycleEnd"`
		MembershipType  string `json:"membershipType"`
		IsUnlimited     bool   `json:"isUnlimited"`
		IndividualUsage struct {
			Plan struct {
				TotalPercentUsed *float64 `json:"totalPercentUsed"`
				APIPercentUsed   *float64 `json:"apiPercentUsed"`
			} `json:"plan"`
			OnDemand *struct {
				Enabled bool     `json:"enabled"`
				Used    *float64 `json:"used"`
				Limit   float64  `json:"limit"`
			} `json:"onDemand"`
		} `json:"individualUsage"`
	}
	if err := json.Unmarshal(raw, &r); err != nil {
		return nil, "", "", fmt.Errorf("parse: %w", err)
	}
	reset, _ := time.Parse(time.RFC3339, r.BillingCycleEnd)
	u := r.IndividualUsage
	if u.Plan.TotalPercentUsed != nil {
		windows = append(windows, usage.Window{ID: "included", Label: "Included usage", Used: pct(*u.Plan.TotalPercentUsed), ResetsAt: reset})
	}
	if u.Plan.APIPercentUsed != nil && *u.Plan.APIPercentUsed > 0 {
		windows = append(windows, usage.Window{ID: "api", Label: "API usage", Used: pct(*u.Plan.APIPercentUsed), ResetsAt: reset})
	}
	if od := u.OnDemand; od != nil && od.Enabled && od.Limit > 0 && od.Used != nil {
		windows = append(windows, usage.Window{ID: "on_demand", Label: "On demand", Used: min(max(*od.Used/od.Limit, 0), 1), ResetsAt: reset})
	}
	plan = capitalize(r.MembershipType)
	if len(windows) > 0 {
		return windows, "", plan, nil
	}
	membership := firstNonEmpty(r.MembershipType, "this")
	if r.IsUnlimited {
		return nil, fmt.Sprintf("Unlimited on the %s plan — nothing to meter", membership), plan, nil
	}
	return nil, fmt.Sprintf("The %s plan has nothing for Cursor to meter yet", membership), plan, nil
}

func (p *Provider) Probe() string {
	if !p.Present() {
		return p.db + " not found (not installed)"
	}
	c, err := p.readCredential()
	if err != nil {
		return fmt.Sprintf("%s: session not readable (%v)", p.db, err)
	}
	return fmt.Sprintf("session borrowed from the editor (cookie %d chars, plan=%s)", len(c.cookie), firstNonEmpty(c.plan, "?"))
}

func pct(v float64) float64 { return min(max(v/100, 0), 1) }

func capitalize(s string) string {
	if s == "" {
		return s
	}
	return strings.ToUpper(s[:1]) + s[1:]
}

func firstNonEmpty(xs ...string) string {
	for _, x := range xs {
		if x != "" {
			return x
		}
	}
	return ""
}
