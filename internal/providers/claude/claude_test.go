package claude

import (
	"testing"
	"time"
)

func TestLimitsLeadAndTheNamedFallbackIsNotATwin(t *testing.T) {
	raw := []byte(`{
		"limits":[
			{"kind":"weekly_all","percent":24,"resets_at":"2026-09-22T10:00:00Z"},
			{"kind":"session","percent":8.4,"resets_at":"2026-09-18T13:00:00Z"},
			{"kind":"weekly_scoped","percent":0,"resets_at":"2026-09-22T10:00:00Z"},
			{"kind":"no_reset","percent":50}
		],
		"five_hour":{"utilization":8.4,"resets_at":"2026-09-18T13:00:00Z"},
		"seven_day":{"utilization":24,"resets_at":"2026-09-22T10:00:00Z"}
	}`)
	ws, err := parseResponse(raw)
	if err != nil {
		t.Fatal(err)
	}
	var ids []string
	for _, w := range ws {
		ids = append(ids, w.ID)
	}
	want := []string{"session", "weekly_all", "weekly_scoped"}
	if len(ids) != len(want) {
		t.Fatalf("ids = %v, want %v", ids, want)
	}
	for i := range want {
		if ids[i] != want[i] {
			t.Fatalf("ids = %v, want %v", ids, want)
		}
	}
	if ws[0].Used < 0.083 || ws[0].Used > 0.085 {
		t.Errorf("session used = %v", ws[0].Used)
	}
	if weeklyID(ws) != "weekly_all" {
		t.Errorf("weekly = %q", weeklyID(ws))
	}
}

func TestTheNamedFieldsStandInWhenLimitsIsMissing(t *testing.T) {
	ws, err := parseResponse([]byte(`{"five_hour":{"utilization":130,"resets_at":"2026-09-18T13:00:00Z"},"seven_day":{"utilization":10,"resets_at":"2026-09-22T10:00:00Z"}}`))
	if err != nil {
		t.Fatal(err)
	}
	if len(ws) != 2 || ws[0].ID != "session" || ws[0].Used != 1 || ws[1].ID != "seven_day" {
		t.Fatalf("got %+v", ws)
	}
}

func TestCredentialFileShapes(t *testing.T) {
	c, ok := parseCredential([]byte(`{"claudeAiOauth":{"accessToken":"tok","expiresAt":1790000000000}}`))
	if !ok || c.token != "tok" || c.expiresAt.UnixMilli() != 1790000000000 {
		t.Fatalf("nested: %+v %v", c, ok)
	}
	c, ok = parseCredential([]byte(`{"accessToken":"flat"}`))
	if !ok || c.token != "flat" || !c.expiresAt.IsZero() {
		t.Fatalf("flat: %+v %v", c, ok)
	}
	if _, ok := parseCredential([]byte(`{"claudeAiOauth":{}}`)); ok {
		t.Fatal("an empty token is no credential")
	}
}

func TestBackoffDoublesToTheCapAndRetryAfterOnlyLengthens(t *testing.T) {
	cases := []struct {
		n    int
		ra   time.Duration
		want time.Duration
	}{
		{0, 0, time.Minute},
		{1, 0, 2 * time.Minute},
		{4, 0, 15 * time.Minute},
		{9, 0, 15 * time.Minute},
		{0, 30 * time.Second, time.Minute},
		{0, time.Hour, time.Hour},
	}
	for _, c := range cases {
		if got := backoff(c.n, c.ra); got != c.want {
			t.Errorf("backoff(%d, %v) = %v, want %v", c.n, c.ra, got, c.want)
		}
	}
}

func TestRenewsOnlyInsideTheMarginAndBacksOffAfterFailing(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	exp := now.Add(3 * time.Minute)
	var zero time.Time
	if shouldRenew(zero, now, zero, zero, 0) {
		t.Error("no expiry read: never launch on a guess")
	}
	if shouldRenew(now.Add(10*time.Minute), now, zero, zero, 0) {
		t.Error("plenty of time left")
	}
	if !shouldRenew(exp, now, zero, zero, 0) {
		t.Error("inside the margin with no attempt yet")
	}
	last := now.Add(-11 * time.Minute)
	if !shouldRenew(exp, now, exp, last, 0) {
		t.Error("first retry after the cooldown")
	}
	if shouldRenew(exp, now, exp, last, 1) {
		t.Error("second failure doubles the wait to 20 min")
	}
	if !shouldRenew(exp, now, exp, now.Add(-time.Hour), 16) {
		t.Error("the wait never exceeds an hour")
	}
}
