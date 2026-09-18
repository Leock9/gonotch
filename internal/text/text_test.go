package text

import (
	"testing"
	"time"
)

func TestPctNeverReadsAsNothingUsed(t *testing.T) {
	for used, want := range map[float64]string{0: "0%", 0.004: "0.4%", 0.0004: "<0.1%", 0.12: "12%", 0.995: "100%", 1: "100%"} {
		if got := Pct(used); got != want {
			t.Errorf("Pct(%v) = %q, want %q", used, got, want)
		}
	}
}

func TestUsedLeftAddsUpAfterRounding(t *testing.T) {
	if got := UsedLeft(EN, 0.126); got != "13% used · 87% left" {
		t.Errorf("got %q", got)
	}
	if got := UsedLeft(PT, 0.995); got != "99.5% usado · 0.5% restante" {
		t.Errorf("got %q", got)
	}
}

func TestLabelsInPortuguese(t *testing.T) {
	for in, want := range map[string]string{"Current session": "Sessão atual", "5h limit": "Limite de 5h", "3d limit": "Limite de 3 dias", "Spark": "Spark"} {
		if got := Label(PT, in); got != want {
			t.Errorf("Label(%q) = %q, want %q", in, got, want)
		}
	}
	if Label(EN, "Current session") != "Current session" {
		t.Error("English stays as the provider wrote it")
	}
}

func TestResetCopy(t *testing.T) {
	now := time.Date(2026, 9, 18, 10, 0, 0, 0, time.Local)
	cases := []struct {
		at   time.Time
		want string
	}{
		{now.Add(-time.Minute), "Reiniciando…"},
		{now.Add(2*time.Hour + 13*time.Minute), "Reinicia em 2h 13min"},
		{now.Add(59*time.Minute + 40*time.Second), "Reinicia em 1h"},
		{time.Date(2026, 9, 21, 16, 0, 0, 0, time.Local), "Reinicia seg 16:00"},
		{time.Date(2026, 10, 5, 13, 26, 0, 0, time.Local), "Reinicia 05/10 13:26"},
		{time.Time{}, ""},
	}
	for _, c := range cases {
		if got := Reset(PT, c.at, now); got != c.want {
			t.Errorf("Reset(%v) = %q, want %q", c.at, got, c.want)
		}
	}
}
