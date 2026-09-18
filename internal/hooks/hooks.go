// Package hooks wires gonotch-hook into Claude Code's ~/.claude/settings.json. The file is edited
// in place: only the hooks this package owns change, everything else keeps its order and format,
// and a backup is written first.
package hooks

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

// Marker identifies our entries: any hook command containing it is ours.
const Marker = "gonotch-hook"

// Events are the Claude Code hooks gonotch listens to.
var Events = []string{"SessionStart", "UserPromptSubmit", "PreToolUse", "PostToolUse", "Notification", "Stop", "SessionEnd"}

// matcherEvents take a tool matcher.
var matcherEvents = map[string]bool{"PreToolUse": true, "PostToolUse": true}

func SettingsPath() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".claude", "settings.json")
}

func isOurs(entry gjson.Result) bool {
	ours := false
	entry.Get("hooks").ForEach(func(_, h gjson.Result) bool {
		if strings.Contains(h.Get("command").String(), Marker) {
			ours = true
			return false
		}
		return true
	})
	return ours
}

// withoutOurs returns an event's hook array minus our entries, as raw JSON.
func withoutOurs(doc []byte, event string) []string {
	var keep []string
	gjson.GetBytes(doc, "hooks."+event).ForEach(func(_, e gjson.Result) bool {
		if !isOurs(e) {
			keep = append(keep, e.Raw)
		}
		return true
	})
	return keep
}

func setArray(doc []byte, event string, entries []string) ([]byte, error) {
	return sjson.SetRawBytesOptions(doc, "hooks."+event, []byte("["+strings.Join(entries, ",")+"]"), &sjson.Options{ReplaceInPlace: false})
}

// Install adds one entry per event that runs hookBin, replacing any older entry of ours.
func Install(hookBin string) (string, error) {
	if _, err := os.Stat(hookBin); err != nil {
		return "", fmt.Errorf("hook binary not found: %s", hookBin)
	}
	path := SettingsPath()
	doc, err := read(path)
	if err != nil {
		return "", err
	}
	for _, ev := range Events {
		entry, err := entryJSON(hookBin, matcherEvents[ev])
		if err != nil {
			return "", err
		}
		if doc, err = setArray(doc, ev, append(withoutOurs(doc, ev), entry)); err != nil {
			return "", err
		}
	}
	if err := write(path, doc); err != nil {
		return "", err
	}
	return fmt.Sprintf("wrote %s (%d events)", path, len(Events)), nil
}

type command struct {
	Type    string `json:"type"`
	Command string `json:"command"`
	Timeout int    `json:"timeout"`
}

type entry struct {
	Matcher string    `json:"matcher,omitempty"`
	Hooks   []command `json:"hooks"`
}

// entryJSON is one settings entry running hookBin, single-quoted for the shell Claude Code uses.
func entryJSON(hookBin string, matcher bool) (string, error) {
	e := entry{Hooks: []command{{Type: "command", Command: shellQuote(hookBin), Timeout: 5}}}
	if matcher {
		e.Matcher = "*"
	}
	raw, err := json.Marshal(e)
	return string(raw), err
}

func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// Uninstall removes every entry of ours and leaves the rest alone.
func Uninstall() (string, error) {
	path := SettingsPath()
	doc, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return "settings.json does not exist, nothing to remove", nil
	} else if err != nil {
		return "", err
	}
	removed := 0
	for ev := range gjson.GetBytes(doc, "hooks").Map() {
		keep := withoutOurs(doc, ev)
		n := len(gjson.GetBytes(doc, "hooks."+ev).Array())
		if len(keep) == n {
			continue
		}
		removed += n - len(keep)
		if doc, err = setArray(doc, ev, keep); err != nil {
			return "", err
		}
	}
	if removed == 0 {
		return "no gonotch hooks found", nil
	}
	if err := write(path, doc); err != nil {
		return "", err
	}
	return fmt.Sprintf("removed %d gonotch hook(s)", removed), nil
}

func IsInstalled() bool {
	doc, err := os.ReadFile(SettingsPath())
	return err == nil && strings.Contains(string(doc), Marker)
}

func read(path string) ([]byte, error) {
	doc, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return []byte("{}"), nil
	}
	if err != nil {
		return nil, err
	}
	if !gjson.ValidBytes(doc) {
		return nil, fmt.Errorf("%s is not valid JSON; not touching it", path)
	}
	return doc, nil
}

func write(path string, doc []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	mode := os.FileMode(0o600)
	if old, err := os.ReadFile(path); err == nil {
		if st, err := os.Stat(path); err == nil {
			mode = st.Mode().Perm()
		}
		backup := fmt.Sprintf("%s.gonotch-bak-%d", path, time.Now().Unix())
		if err := os.WriteFile(backup, old, 0o600); err != nil {
			return err
		}
	}
	tmp := path + ".gonotch-tmp"
	if err := os.WriteFile(tmp, doc, mode); err != nil {
		return err
	}
	// WriteFile's mode passes through the umask; the original's has to come back exactly
	if err := os.Chmod(tmp, mode); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}
