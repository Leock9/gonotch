// Package config holds gonotch's settings and where its files live (XDG base directories).
package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
)

type Config struct {
	// Edge the notch sits on: "right" or "left"
	Edge string `json:"edge"`
	// Position of the notch's centre along the edge, 0–1
	Position float64 `json:"position"`
	// Monitor connector name ("DP-1"); empty = primary
	Monitor string `json:"monitor,omitempty"`
	// Providers whose rings are hidden; a hidden provider is not polled either
	Hidden []string `json:"hidden,omitempty"`
	// Ring order by provider id; providers missing from it follow in their default order
	Order []string `json:"order,omitempty"`
	// AutoHide tucks the notch into a thin strip on the edge until the pointer reaches it
	AutoHide bool `json:"auto_hide,omitempty"`
}

func Default() Config {
	return Config{Edge: "right", Position: 0.5}
}

// Dir is $XDG_CONFIG_HOME/gonotch (~/.config/gonotch).
func Dir() string {
	base := os.Getenv("XDG_CONFIG_HOME")
	if !filepath.IsAbs(base) {
		home, _ := os.UserHomeDir()
		base = filepath.Join(home, ".config")
	}
	return filepath.Join(base, "gonotch")
}

// StateDir is $XDG_STATE_HOME/gonotch (~/.local/state/gonotch): persisted readings and logs.
func StateDir() string {
	base := os.Getenv("XDG_STATE_HOME")
	if !filepath.IsAbs(base) {
		home, _ := os.UserHomeDir()
		base = filepath.Join(home, ".local", "state")
	}
	return filepath.Join(base, "gonotch")
}

func Path() string { return filepath.Join(Dir(), "config.json") }

// SocketPath is where the running app listens: $XDG_RUNTIME_DIR/gonotch/gonotch.sock, in a directory
// only this user can enter, so no other local account can reach the app.
func SocketPath() string {
	if run := os.Getenv("XDG_RUNTIME_DIR"); filepath.IsAbs(run) {
		return filepath.Join(run, "gonotch", "gonotch.sock")
	}
	return filepath.Join(StateDir(), "gonotch.sock")
}

// Load returns the saved config, or the defaults for anything missing or unreadable.
func Load() Config {
	c := Default()
	if raw, err := os.ReadFile(Path()); err == nil {
		_ = json.Unmarshal(raw, &c)
	}
	if c.Edge != "left" {
		c.Edge = "right"
	}
	if c.Position < 0 || c.Position > 1 {
		c.Position = 0.5
	}
	return c
}

func Save(c Config) error {
	raw, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(Dir(), 0o700); err != nil {
		return err
	}
	return os.WriteFile(Path(), raw, 0o600)
}

// Clone copies the slices too, so a copy handed out can be changed without touching the original.
func (c Config) Clone() Config {
	c.Hidden = append([]string(nil), c.Hidden...)
	c.Order = append([]string(nil), c.Order...)
	return c
}

// Ordered sorts provider ids by Order, unknown ones keeping their place after the known.
func (c Config) Ordered(ids []string) []string {
	out := make([]string, 0, len(ids))
	for _, id := range c.Order {
		for _, x := range ids {
			if x == id && !slices.Contains(out, id) {
				out = append(out, id)
			}
		}
	}
	for _, id := range ids {
		if !slices.Contains(out, id) {
			out = append(out, id)
		}
	}
	return out
}

// SetHidden shows or hides a provider's ring.
func (c *Config) SetHidden(provider string, hidden bool) {
	var keep []string
	for _, h := range c.Hidden {
		if h != provider {
			keep = append(keep, h)
		}
	}
	if hidden {
		keep = append(keep, provider)
	}
	c.Hidden = keep
}

// Move shifts a provider one place up (-1) or down (+1) in the given full order.
func (c *Config) Move(ids []string, provider string, delta int) {
	order := c.Ordered(ids)
	for i, id := range order {
		j := i + delta
		if id == provider && j >= 0 && j < len(order) {
			order[i], order[j] = order[j], order[i]
			break
		}
	}
	c.Order = order
}

func (c Config) IsHidden(provider string) bool { return slices.Contains(c.Hidden, provider) }
