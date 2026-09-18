package app

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/leock9/gonotch/internal/config"
	"github.com/leock9/gonotch/internal/providers"
	"github.com/leock9/gonotch/internal/sessions"
	"github.com/leock9/gonotch/internal/usage"
)

// isolate points every provider at an empty home, so none finds this machine's real tools.
func isolate(t *testing.T) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("PATH", home)
	t.Setenv("CODEX_HOME", filepath.Join(home, ".codex"))
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, "config"))
	t.Setenv("XDG_STATE_HOME", filepath.Join(home, "state"))
}

func ids(ps []ProviderState) []string {
	out := make([]string, 0, len(ps))
	for _, p := range ps {
		out = append(out, p.ID)
	}
	return out
}

func TestStateFollowsTheOrderAndLeavesOutHiddenAndAbsent(t *testing.T) {
	isolate(t)
	a := New(config.Config{Order: []string{"cursor", "claude", "codex"}})
	a.publish(usage.Snapshot{Provider: "claude", Status: usage.StatusOK})
	a.publish(usage.Snapshot{Provider: "codex", Status: usage.StatusAbsent})
	a.publish(usage.Snapshot{Provider: "cursor", Status: usage.StatusOK})
	if got := ids(a.State().Providers); !reflect.DeepEqual(got, []string{"cursor", "claude"}) {
		t.Fatalf("rings = %v, want cursor then claude (codex is not installed)", got)
	}
	a.UpdateConfig(func(c *config.Config) { c.SetHidden("cursor", true) })
	if got := ids(a.State().Providers); !reflect.DeepEqual(got, []string{"claude"}) {
		t.Fatalf("rings after hiding cursor = %v", got)
	}
	catalog := a.Catalog()
	if got := ids(catalog); !reflect.DeepEqual(got, []string{"cursor", "claude", "codex"}) {
		t.Fatalf("the settings list every provider: %v", got)
	}
	if !catalog[0].Hidden || catalog[2].Status != usage.StatusAbsent {
		t.Fatalf("catalog marks hidden and absent: %+v", catalog)
	}
}

func TestAProviderWithoutAReadingIsAbsentWhenItsToolIsMissing(t *testing.T) {
	isolate(t)
	for _, p := range New(config.Default()).Catalog() {
		if p.Status != usage.StatusAbsent {
			t.Errorf("%s: status %q in an empty home, want absent", p.ID, p.Status)
		}
	}
}

func TestSwitchingAProviderBackOnWakesItsPoller(t *testing.T) {
	isolate(t)
	a := New(config.Config{Hidden: []string{"codex"}})
	for _, p := range a.providers {
		a.runners[p.ID()] = providers.NewRunner(p, a.publish)
	}
	a.UpdateConfig(func(c *config.Config) { c.SetHidden("codex", false) })
	for id, r := range a.runners {
		want := 0
		if id == "codex" {
			want = 1
		}
		if got := len(r.Refresh); got != want {
			t.Errorf("%s: %d refresh requests, want %d", id, got, want)
		}
	}
}

func TestSubscribersGetOneSignalPerBurst(t *testing.T) {
	isolate(t)
	a := New(config.Default())
	ch := a.Subscribe()
	a.Apply(sessions.Event{Kind: sessions.EvRunning, SessionID: "a", FromHook: true})
	a.Apply(sessions.Event{Kind: sessions.EvRunning, SessionID: "b", FromHook: true})
	if len(ch) != 1 {
		t.Fatalf("two changes before the reader woke should coalesce into one signal, got %d", len(ch))
	}
	<-ch
	a.Apply(sessions.Event{Kind: sessions.EvRunning, SessionID: "a", FromHook: true})
	if len(ch) != 0 {
		t.Fatal("an event that changes nothing visible must not signal")
	}
}

func TestAcknowledgingAFinishedSessionNotifies(t *testing.T) {
	isolate(t)
	a := New(config.Default())
	a.Apply(sessions.Event{Kind: sessions.EvDone, SessionID: "d", FromHook: true})
	ch := a.Subscribe()
	a.AckSession("d")
	if got := a.State().Sessions[0].State; got != sessions.Idle || len(ch) != 1 {
		t.Fatalf("state %s, %d signals", got, len(ch))
	}
	a.AckSession("d")
	if len(ch) != 1 {
		t.Fatal("acknowledging again changes nothing and must not signal")
	}
}

func TestSettingsReachTheFileOnceTheySettle(t *testing.T) {
	isolate(t)
	a := New(config.Default())
	a.UpdateConfig(func(c *config.Config) { c.Position = 0.2 })
	a.UpdateConfig(func(c *config.Config) { c.Position = 0.8 })
	if _, err := os.Stat(config.Path()); !os.IsNotExist(err) {
		t.Fatal("a burst of changes must not write the file on every change")
	}
	a.Flush()
	if got := config.Load().Position; got != 0.8 {
		t.Fatalf("saved position %v, want the last one", got)
	}
	os.Remove(config.Path())
	a.Flush()
	if _, err := os.Stat(config.Path()); !os.IsNotExist(err) {
		t.Fatal("nothing unsaved: Flush must not write")
	}
}
