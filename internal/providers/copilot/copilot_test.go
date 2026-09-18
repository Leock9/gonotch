package copilot

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/leock9/gonotch/internal/usage"
)

// The shape of a Copilot Free reply, as the endpoint gave it (identifiers left out)
const freeReply = `{
  "login": "octocat", "copilot_plan": "individual", "access_type_sku": "free_limited_copilot",
  "quota_reset_date": "2026-10-01", "quota_reset_date_utc": "2026-10-01T00:00:00.000Z",
  "quota_snapshots": {
    "chat": {"entitlement": 200, "remaining": 200, "percent_remaining": 100.0, "unlimited": false, "quota_id": "chat"},
    "completions": {"entitlement": 2000, "remaining": 1958, "percent_remaining": 97.9, "unlimited": false, "quota_id": "completions"},
    "premium_interactions": {"entitlement": 0, "remaining": 0, "percent_remaining": 0.0, "unlimited": false, "has_quota": false}
  }
}`

func TestParseFreePlan(t *testing.T) {
	r, err := parseUser([]byte(freeReply))
	if err != nil {
		t.Fatal(err)
	}
	if len(r.windows) != 2 || r.windows[0].ID != "chat" || r.windows[1].ID != "completions" {
		t.Fatalf("windows = %+v, want chat then completions, and no premium requests (entitlement 0)", r.windows)
	}
	if got := r.windows[1].Used; got < 0.0209 || got > 0.0211 {
		t.Errorf("completions used = %v, want 42/2000", got)
	}
	if want := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC); !r.windows[0].ResetsAt.Equal(want) {
		t.Errorf("reset = %v, want %v", r.windows[0].ResetsAt, want)
	}
	if r.plan != "Free · octocat" {
		t.Errorf("plan = %q", r.plan)
	}
}

func TestParsePaidPlan(t *testing.T) {
	r, err := parseUser([]byte(`{"copilot_plan": "business", "quota_reset_date": "2026-10-01", "quota_snapshots": {
	  "premium_interactions": {"entitlement": 300, "remaining": -20, "overage_count": 20},
	  "chat": {"entitlement": 0, "unlimited": true}, "completions": {"unlimited": true},
	  "extra_thing": {"entitlement": 10, "percent_remaining": 40}}}`))
	if err != nil {
		t.Fatal(err)
	}
	if len(r.windows) != 2 || r.windows[0].ID != "premium_interactions" || r.windows[1].ID != "extra_thing" {
		t.Fatalf("windows = %+v", r.windows)
	}
	if r.windows[0].Used != 1 {
		t.Errorf("premium used = %v, want 1 once past the allowance", r.windows[0].Used)
	}
	if got := r.windows[1].Used; got < 0.599 || got > 0.601 {
		t.Errorf("extra used = %v, want 0.6 from percent_remaining", got)
	}
	if r.windows[1].Label != "Extra thing" || r.plan != "Business" {
		t.Errorf("label %q, plan %q", r.windows[1].Label, r.plan)
	}
	if !r.windows[0].ResetsAt.Equal(time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)) {
		t.Errorf("reset from quota_reset_date = %v", r.windows[0].ResetsAt)
	}
}

func TestParseNothingMetered(t *testing.T) {
	r, err := parseUser([]byte(`{"quota_snapshots": {"chat": {"unlimited": true}, "completions": {"unlimited": true}}}`))
	if err != nil || len(r.windows) != 0 || r.note == "" {
		t.Fatalf("got %+v, %v; want no windows and a note", r, err)
	}
}

func TestParseGHHosts(t *testing.T) {
	cases := []struct{ name, yml, user, token string }{
		{"gh before 2.40", "github.com:\n    oauth_token: gho_old\n    user: octocat\n    git_protocol: https\n", "octocat", "gho_old"},
		{"several accounts", "ghe.example.com:\n    oauth_token: gho_ghe\n    user: other\ngithub.com:\n    users:\n        work:\n            oauth_token: gho_work\n        octocat:\n            oauth_token: gho_mine\n    git_protocol: https\n    user: octocat\n    oauth_token: gho_mine\n", "octocat", "gho_mine"},
		{"token in the keyring", "github.com:\n    users:\n        octocat:\n    git_protocol: https\n    user: octocat\n", "octocat", ""},
		{"not signed in to github.com", "ghe.example.com:\n    oauth_token: gho_ghe\n", "", ""},
	}
	for _, c := range cases {
		if user, token := parseGHHosts(c.yml); user != c.user || token != c.token {
			t.Errorf("%s: got (%q, %q), want (%q, %q)", c.name, user, token, c.user, c.token)
		}
	}
}

func TestParseAppsSkipsEnterprise(t *testing.T) {
	got := parseApps([]byte(`{"ghe.example.com:Iv1": {"user": "x", "oauth_token": "ghu_ghe"},
	  "github.com:Ov2": {"user": "b", "oauth_token": "gho_b"}, "github.com:Iv2": {"user": "a", "oauth_token": "ghu_a"}}`))
	if len(got) != 2 || got[0].token != "ghu_a" || got[1].token != "gho_b" {
		t.Fatalf("got %+v, want github.com's two tokens in key order", got)
	}
}

// newTest points a provider at files in a temporary directory and at a test server
func newTest(t *testing.T, apps, hosts string, handler http.HandlerFunc) *Provider {
	t.Helper()
	dir := t.TempDir()
	p := &Provider{copilotDir: filepath.Join(dir, "github-copilot"), ghHosts: filepath.Join(dir, "gh", "hosts.yml"),
		ghToken: func(context.Context) string { return "" }}
	p.markers = []string{p.copilotDir}
	for path, body := range map[string]string{filepath.Join(p.copilotDir, "apps.json"): apps, p.ghHosts: hosts} {
		if body == "" {
			continue
		}
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	p.endpoint = srv.URL
	return p
}

func TestPollFallsBackPastARejectedToken(t *testing.T) {
	p := newTest(t, `{"github.com:Iv1": {"user": "old", "oauth_token": "ghu_revoked"}}`,
		"github.com:\n    oauth_token: gho_good\n    user: octocat\n",
		func(w http.ResponseWriter, r *http.Request) {
			if r.Header.Get("Authorization") != "Bearer gho_good" {
				w.WriteHeader(401)
				return
			}
			w.Write([]byte(freeReply))
		})
	if !p.Present() {
		t.Fatal("the plugin's directory is there, so Copilot is")
	}
	s, _ := p.Poll(context.Background(), usage.Snapshot{})
	if s.Status != usage.StatusOK || s.Headline != "chat" || s.Weekly != "completions" {
		t.Fatalf("got status %q headline %q weekly %q (%s)", s.Status, s.Headline, s.Weekly, s.Note)
	}
}

func TestTheKeyringIsAskedOnlyWhenTheFilesFail(t *testing.T) {
	asked := 0
	good := `{"github.com:Iv1": {"user": "octocat", "oauth_token": "ghu_good"}}`
	p := newTest(t, good, "github.com:\n    user: octocat\n", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer ghu_good" && r.Header.Get("Authorization") != "Bearer gho_keyring" {
			w.WriteHeader(401)
			return
		}
		w.Write([]byte(freeReply))
	})
	p.ghToken = func(context.Context) string { asked++; return "gho_keyring" }
	if s, _ := p.Poll(context.Background(), usage.Snapshot{}); s.Status != usage.StatusOK || asked != 0 {
		t.Fatalf("status %q, keyring asked %d times; want ok without asking", s.Status, asked)
	}
	if err := os.WriteFile(filepath.Join(p.copilotDir, "apps.json"), []byte(`{"github.com:Iv1": {"oauth_token": "ghu_revoked"}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if s, _ := p.Poll(context.Background(), usage.Snapshot{}); s.Status != usage.StatusOK || asked != 1 {
		t.Fatalf("status %q, keyring asked %d times; want ok from the keyring's token", s.Status, asked)
	}
}

func TestPollRateLimited(t *testing.T) {
	p := newTest(t, "", "github.com:\n    oauth_token: gho_good\n", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Retry-After", "120")
		w.WriteHeader(403)
	})
	prev := usage.Snapshot{Status: usage.StatusOK, Windows: []usage.Window{{ID: "chat", Used: 0.3}}, Headline: "chat"}
	s, _ := p.Poll(context.Background(), prev)
	if s.Status != usage.StatusStale || s.Headline != "chat" {
		t.Errorf("a secondary rate limit keeps the last reading, stale: got %q", s.Status)
	}
	if d := time.Until(s.BackoffUntil); d < 110*time.Second || d > 120*time.Second {
		t.Errorf("backoff = %v, want Retry-After's 120 s", d)
	}
}

func TestPollNeedsAuth(t *testing.T) {
	p := newTest(t, "", "", func(w http.ResponseWriter, _ *http.Request) { t.Error("no token, no request") })
	if s, _ := p.Poll(context.Background(), usage.Snapshot{}); s.Status != usage.StatusNeedsAuth {
		t.Errorf("status = %q, want needsAuth", s.Status)
	}
	p = newTest(t, "", "github.com:\n    oauth_token: gho_x\n", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(401) })
	if s, _ := p.Poll(context.Background(), usage.Snapshot{}); s.Status != usage.StatusNeedsAuth {
		t.Errorf("status = %q, want needsAuth after a 401", s.Status)
	}
}

func TestPrimaryRateLimitWaitsForReset(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	resp := &http.Response{StatusCode: 403, Header: http.Header{}}
	resp.Header.Set("X-Ratelimit-Remaining", "0")
	resp.Header.Set("X-Ratelimit-Reset", strconv.FormatInt(now.Add(10*time.Minute).Unix(), 10))
	if d, ok := rateLimit(resp, now); !ok || d != 10*time.Minute {
		t.Errorf("got (%v, %v), want 10 min", d, ok)
	}
	resp.Header.Del("X-Ratelimit-Remaining")
	if _, ok := rateLimit(resp, now); ok {
		t.Error("a plain 403 is a refused token, not a rate limit")
	}
}
