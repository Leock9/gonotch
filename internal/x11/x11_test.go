package x11

import "testing"

func TestParentFromStatSurvivesOddNames(t *testing.T) {
	for stat, want := range map[string]int{
		"4242 (claude) S 4100 4242 4100 0":  4100,
		"77 (Web Content (x)) R 12 77 12 0": 12,
		"9 (a) b) S 3 9 9 0":                3,
	} {
		if got, ok := parentFromStat(stat); !ok || got != want {
			t.Errorf("parentFromStat(%q) = %d, %v", stat, got, ok)
		}
	}
	if _, ok := parentFromStat("garbage"); ok {
		t.Error("garbage parsed")
	}
}

func TestChainStopsAtInit(t *testing.T) {
	parents := map[int]int{50: 40, 40: 30, 30: 1}
	got := Chain(50, parents)
	if len(got) != 3 || got[0] != 50 || got[2] != 30 {
		t.Fatalf("got %v", got)
	}
	loop := map[int]int{5: 6, 6: 5}
	if n := len(Chain(5, loop)); n > 13 {
		t.Fatalf("a cycle must end, got %d", n)
	}
}
