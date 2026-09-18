package config

import (
	"os"
	"reflect"
	"testing"
)

var ids = []string{"claude", "codex", "cursor"}

func TestOrderedKeepsUnknownProvidersAfterTheKnown(t *testing.T) {
	c := Config{Order: []string{"cursor", "gone", "claude"}}
	if got := c.Ordered(ids); !reflect.DeepEqual(got, []string{"cursor", "claude", "codex"}) {
		t.Fatalf("got %v", got)
	}
	if got := (Config{}).Ordered(ids); !reflect.DeepEqual(got, ids) {
		t.Fatalf("no order keeps the default: %v", got)
	}
}

func TestMoveStopsAtTheEnds(t *testing.T) {
	var c Config
	c.Move(ids, "cursor", -1)
	if !reflect.DeepEqual(c.Order, []string{"claude", "cursor", "codex"}) {
		t.Fatalf("up: %v", c.Order)
	}
	c.Move(ids, "claude", -1)
	if !reflect.DeepEqual(c.Order, []string{"claude", "cursor", "codex"}) {
		t.Fatalf("the first cannot go up: %v", c.Order)
	}
}

func TestSetHiddenIsIdempotentAndCloneIsDeep(t *testing.T) {
	var c Config
	c.SetHidden("codex", true)
	c.SetHidden("codex", true)
	if !c.IsHidden("codex") || len(c.Hidden) != 1 {
		t.Fatalf("hidden: %v", c.Hidden)
	}
	d := c.Clone()
	d.SetHidden("codex", false)
	if !c.IsHidden("codex") || d.IsHidden("codex") {
		t.Fatal("a clone must not share its slices")
	}
}

func TestLoadRepairsWhatItCannotUse(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	if err := os.MkdirAll(Dir(), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(Path(), []byte(`{"edge":"top","position":7,"port":48777,"hidden":["codex"]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	c := Load()
	if c.Edge != "right" || c.Position != 0.5 || !c.IsHidden("codex") {
		t.Fatalf("got %+v", c)
	}
	if err := os.WriteFile(Path(), []byte(`not json`), 0o600); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(Load(), Default()) {
		t.Fatal("an unreadable file falls back to the defaults")
	}
}

func TestTheSocketLivesInTheRuntimeDir(t *testing.T) {
	t.Setenv("XDG_RUNTIME_DIR", "/run/user/1000")
	if got := SocketPath(); got != "/run/user/1000/gonotch/gonotch.sock" {
		t.Fatalf("got %s", got)
	}
	t.Setenv("XDG_RUNTIME_DIR", "")
	t.Setenv("XDG_STATE_HOME", "/home/u/.local/state")
	if got := SocketPath(); got != "/home/u/.local/state/gonotch/gonotch.sock" {
		t.Fatalf("without a runtime dir: %s", got)
	}
}
