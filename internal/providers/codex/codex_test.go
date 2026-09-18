package codex

import (
	"context"
	"encoding/base64"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/leock9/gonotch/internal/usage"
)

func TestLiveReplyMainPairThenGroupedExtras(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	raw := []byte(`{
		"plan_type":"plus",
		"rate_limit":{
			"primary_window":{"used_percent":12.5,"limit_window_seconds":18000,"reset_after_seconds":3600},
			"secondary_window":{"used_percent":40,"limit_window_seconds":604800,"reset_at":1800500000}
		},
		"additional_rate_limits":[
			{"limit_name":"GPT-5.3-Codex-Spark","rate_limit":{"primary_window":{"used_percent":5,"limit_window_seconds":18000}}},
			{"limit_name":"other","rate_limit":{"primary_window":{"used_percent":99}}},
			"junk"
		],
		"code_review_rate_limit":{"primary_window":{"used_percent":1,"limit_window_seconds":604800}}
	}`)
	ws, err := windowsFromUsage(raw, now)
	if err != nil {
		t.Fatal(err)
	}
	want := []struct{ id, label, group string }{
		{"primary", "5h limit", ""},
		{"secondary", "Weekly limit", ""},
		{"spark", "5h limit", "Spark"},
		{"code-review", "Weekly limit", "Code review"},
	}
	if len(ws) != len(want) {
		t.Fatalf("got %+v", ws)
	}
	for i, w := range want {
		if ws[i].ID != w.id || ws[i].Label != w.label || ws[i].Group != w.group {
			t.Errorf("window %d = %+v, want %+v", i, ws[i], w)
		}
	}
	if !ws[0].ResetsAt.Equal(now.Add(time.Hour)) || ws[1].ResetsAt.Unix() != 1800500000 {
		t.Errorf("resets: %v %v", ws[0].ResetsAt, ws[1].ResetsAt)
	}
}

func TestLabelsFollowTheWindowLength(t *testing.T) {
	cases := map[float64]string{0: "Current session", 30: "30m limit", 300: "5h limit", 7 * 1440: "Weekly limit", 30 * 1440: "Monthly limit", 3 * 1440: "3d limit"}
	for minutes, want := range cases {
		if got := labelFor(minutes, "primary"); got != want {
			t.Errorf("labelFor(%v) = %q, want %q", minutes, got, want)
		}
	}
}

func TestRolloutTailGivesTheLastSnapshot(t *testing.T) {
	text := []byte(`{"timestamp":"2026-09-18T10:00:00.000Z","type":"event_msg","payload":{"type":"token_count","rate_limits":{"primary":{"used_percent":10,"window_minutes":300,"resets_at":1800000000},"secondary":null,"plan_type":"free"}}}
{"timestamp":"2026-09-18T11:00:00.000Z","type":"event_msg","payload":{"type":"token_count","rate_limits":{"primary":{"used_percent":20,"window_minutes":300,"resets_at":1800000000},"secondary":{"used_percent":3,"window_minutes":10080,"resets_in_seconds":60},"plan_type":"plus"}}}
{"timestamp":"2026-09-18T11:00:01.000Z","type":"response_item","payload":{"type":"message"}}
{"truncated line with rate_limits`)
	ws, recorded, plan, ok := snapshotFromRollout(text, time.Now())
	if !ok || len(ws) != 2 || ws[0].Used != 0.2 || ws[1].Label != "Weekly limit" || plan != "plus" {
		t.Fatalf("got %+v %v %q %v", ws, recorded, plan, ok)
	}
	if recorded.Hour() != 11 {
		t.Errorf("recorded = %v", recorded)
	}
}

func TestAuthFileNeedsBothTokenAndAccount(t *testing.T) {
	claims := base64.RawURLEncoding.EncodeToString([]byte(`{"https://api.openai.com/auth":{"chatgpt_plan_type":"pro"}}`))
	exp := base64.RawURLEncoding.EncodeToString([]byte(`{"exp":1000}`))
	c, ok := parseAuth([]byte(`{"tokens":{"access_token":"h.`+exp+`.s","account_id":"acc","id_token":"h.`+claims+`.s"}}`), time.Unix(2000, 0))
	if !ok || c.plan != "pro" || !c.expired || c.accountID != "acc" {
		t.Fatalf("got %+v %v", c, ok)
	}
	if _, ok := parseAuth([]byte(`{"tokens":{"access_token":"x"}}`), time.Now()); ok {
		t.Fatal("no account id is no credential")
	}
}

func writeRollout(t *testing.T, home, day, name, line string, mtime time.Time) {
	t.Helper()
	dir := filepath.Join(home, "sessions", day)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte(line+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(path, mtime, mtime); err != nil {
		t.Fatal(err)
	}
}

func rolloutLine(ts time.Time, used float64) string {
	return fmt.Sprintf(`{"timestamp":%q,"type":"event_msg","payload":{"type":"token_count","rate_limits":{"primary":{"used_percent":%v,"window_minutes":300,"resets_at":1800000000},"plan_type":"plus"}}}`,
		ts.UTC().Format(time.RFC3339), used)
}

func TestWithoutASignInTheNewestRolloutIsTheReading(t *testing.T) {
	home := t.TempDir()
	now := time.Now()
	writeRollout(t, home, "2026/09/17", "rollout-old.jsonl", rolloutLine(now.Add(-26*time.Hour), 10), now.Add(-26*time.Hour))
	writeRollout(t, home, "2026/09/18", "rollout-a.jsonl", rolloutLine(now.Add(-3*time.Hour), 20), now.Add(-3*time.Hour))
	writeRollout(t, home, "2026/09/18", "rollout-b.jsonl", rolloutLine(now.Add(-2*time.Minute), 35), now.Add(-2*time.Minute))
	writeRollout(t, home, "2025/12/31", "rollout-older-year.jsonl", rolloutLine(now, 99), now.Add(-9000*time.Hour))

	s, _ := (&Provider{home: home}).Poll(context.Background(), usage.Snapshot{})
	if len(s.Windows) != 1 || s.Windows[0].Used != 0.35 || s.Plan != "plus" {
		t.Fatalf("got %+v", s)
	}
	if s.Status != usage.StatusOK {
		t.Errorf("a two-minute-old reading is current, got %s", s.Status)
	}
}

func TestAnOldRolloutIsMarkedStaleAndNoRolloutAsksForSignIn(t *testing.T) {
	home := t.TempDir()
	old := time.Now().Add(-3 * time.Hour)
	writeRollout(t, home, "2026/09/18", "rollout-a.jsonl", rolloutLine(old, 20), old)
	s, _ := (&Provider{home: home}).Poll(context.Background(), usage.Snapshot{})
	if s.Status != usage.StatusStale || !s.FetchedAt.Equal(old.Truncate(time.Second)) {
		t.Fatalf("old rollout: status %s, fetched %v", s.Status, s.FetchedAt)
	}
	empty, _ := (&Provider{home: t.TempDir()}).Poll(context.Background(), usage.Snapshot{})
	if empty.Status != usage.StatusNeedsAuth {
		t.Fatalf("no sign-in and no rollout: %s", empty.Status)
	}
}
