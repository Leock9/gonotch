package hooks

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/tidwall/gjson"
)

const existing = `{
  "model": "opus",
  "permissions": {"allow": ["Bash(go test:*)"]},
  "hooks": {
    "Stop": [{"hooks": [{"type": "command", "command": "notify-send done"}]}]
  }
}`

func setup(t *testing.T) (settings, bin string) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	settings = filepath.Join(home, ".claude", "settings.json")
	if err := os.MkdirAll(filepath.Dir(settings), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(settings, []byte(existing), 0o644); err != nil {
		t.Fatal(err)
	}
	bin = filepath.Join(home, "it's here", "gonotch-hook")
	_ = os.MkdirAll(filepath.Dir(bin), 0o700)
	if err := os.WriteFile(bin, nil, 0o755); err != nil {
		t.Fatal(err)
	}
	return settings, bin
}

func TestInstallKeepsEverythingElseAndIsIdempotent(t *testing.T) {
	settings, bin := setup(t)
	_ = os.Chmod(settings, 0o644)
	for range 2 {
		if _, err := Install(bin); err != nil {
			t.Fatal(err)
		}
	}
	doc, _ := os.ReadFile(settings)
	if !strings.HasPrefix(string(doc), "{\n  \"model\": \"opus\",") {
		t.Errorf("the user's own formatting and order must survive:\n%s", doc)
	}
	if gjson.GetBytes(doc, "permissions.allow.0").String() != "Bash(go test:*)" {
		t.Error("permissions lost")
	}
	stop := gjson.GetBytes(doc, "hooks.Stop").Array()
	if len(stop) != 2 || stop[0].Get("hooks.0.command").String() != "notify-send done" {
		t.Fatalf("Stop hooks: %s", gjson.GetBytes(doc, "hooks.Stop").Raw)
	}
	cmd := stop[1].Get("hooks.0.command").String()
	if cmd != `'`+strings.ReplaceAll(bin, "'", `'\''`)+`'` {
		t.Errorf("command = %s", cmd)
	}
	if gjson.GetBytes(doc, "hooks.PreToolUse.0.matcher").String() != "*" {
		t.Error("tool hooks need a matcher")
	}
	if st, _ := os.Stat(settings); st.Mode().Perm() != 0o644 {
		t.Errorf("mode changed to %v", st.Mode().Perm())
	}
	if !IsInstalled() {
		t.Error("IsInstalled")
	}
}

func TestUninstallRemovesOnlyOurs(t *testing.T) {
	settings, bin := setup(t)
	if _, err := Install(bin); err != nil {
		t.Fatal(err)
	}
	msg, err := Uninstall()
	if err != nil || !strings.Contains(msg, "removed 7") {
		t.Fatalf("%q %v", msg, err)
	}
	doc, _ := os.ReadFile(settings)
	if strings.Contains(string(doc), Marker) {
		t.Fatalf("still there:\n%s", doc)
	}
	if n := len(gjson.GetBytes(doc, "hooks.Stop").Array()); n != 1 {
		t.Fatalf("the user's Stop hook must stay, got %d", n)
	}
}

func TestInvalidSettingsAreNotTouched(t *testing.T) {
	settings, bin := setup(t)
	_ = os.WriteFile(settings, []byte("{ broken"), 0o644)
	if _, err := Install(bin); err == nil {
		t.Fatal("expected an error")
	}
	if doc, _ := os.ReadFile(settings); string(doc) != "{ broken" {
		t.Fatal("file was modified")
	}
}
