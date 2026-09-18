package codex

import (
	"encoding/base64"
	"testing"
	"time"
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
