package usage

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestAReadingLoadedAfterARestartIsStale(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state", "claude.json")
	saved := Snapshot{Provider: "claude", Status: StatusOK, Windows: []Window{{ID: "session", Used: 0.4}}, Headline: "session"}
	if err := Save(path, saved); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path + ".tmp"); !os.IsNotExist(err) {
		t.Error("the temporary file must be renamed away")
	}
	got := Load(path)
	if got.Status != StatusStale || len(got.Windows) != 1 || got.Windows[0].Used != 0.4 {
		t.Fatalf("got %+v", got)
	}
	if Load(filepath.Join(t.TempDir(), "missing.json")).Status != StatusLoading {
		t.Error("no file is no reading, not an error")
	}
}

func TestAFailedReadKeepsTheLastNumbers(t *testing.T) {
	with := Snapshot{Status: StatusOK, Windows: []Window{{ID: "session"}}}
	if got := with.Failed("HTTP 500"); got.Status != StatusStale || got.Note != "HTTP 500" || len(got.Windows) != 1 {
		t.Fatalf("with a reading: %+v", got)
	}
	if got := (Snapshot{}).Failed("HTTP 500"); got.Status != StatusError {
		t.Fatalf("without one: %+v", got)
	}
}

func TestStalenessByStatusOrAge(t *testing.T) {
	now := time.Now()
	cases := []struct {
		s    Snapshot
		want bool
	}{
		{Snapshot{Status: StatusOK, FetchedAt: now.Add(-time.Minute)}, false},
		{Snapshot{Status: StatusOK, FetchedAt: now.Add(-6 * time.Minute)}, true},
		{Snapshot{Status: StatusStale, FetchedAt: now}, true},
		{Snapshot{Status: StatusLoading}, false},
	}
	for i, c := range cases {
		if got := c.s.IsStale(now); got != c.want {
			t.Errorf("case %d: IsStale = %v, want %v", i, got, c.want)
		}
	}
}

func TestTheRingsShowTheDeclaredWindowsOnly(t *testing.T) {
	s := Snapshot{Headline: "session", Weekly: "weekly", Windows: []Window{{ID: "weekly", Used: 0.9}}}
	if _, ok := s.HeadlineWindow(); ok {
		t.Error("a missing headline window is a dash, never a stand-in")
	}
	if w, ok := s.WeeklyWindow(); !ok || w.Used != 0.9 {
		t.Errorf("weekly = %+v %v", w, ok)
	}
	same := Snapshot{Headline: "primary", Weekly: "primary", Windows: []Window{{ID: "primary"}}}
	if _, ok := same.WeeklyWindow(); ok {
		t.Error("no second ring when it would repeat the first")
	}
}

func TestBandsSwitchAtHalfAndEightyPercent(t *testing.T) {
	for used, want := range map[float64]Band{0: BandAmple, 0.499: BandAmple, 0.5: BandWatch, 0.799: BandWatch, 0.8: BandCritical, 1: BandCritical} {
		if got := BandOf(used); got != want {
			t.Errorf("BandOf(%v) = %v, want %v", used, got, want)
		}
	}
}
