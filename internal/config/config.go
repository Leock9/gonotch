// Package config holds gonotch's settings and where its files live (XDG base directories).
package config

import (
	"encoding/json"
	"os"
	"path/filepath"
)

// DefaultPort is where the hook binary finds the running app. Not Codenotch's 48666, so both can run.
const DefaultPort = 48777

type Config struct {
	Port int `json:"port"`
	// Edge the notch sits on: "right" or "left"
	Edge string `json:"edge"`
	// Position of the notch's centre along the edge, 0–1
	Position float64 `json:"position"`
	// Monitor connector name ("DP-1"); empty = primary
	Monitor string `json:"monitor,omitempty"`
	// Providers whose rings are hidden
	Hidden []string `json:"hidden,omitempty"`
}

func Default() Config {
	return Config{Port: DefaultPort, Edge: "right", Position: 0.5}
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

// Load returns the saved config, or the defaults for anything missing or unreadable.
func Load() Config {
	c := Default()
	if raw, err := os.ReadFile(Path()); err == nil {
		_ = json.Unmarshal(raw, &c)
	}
	if c.Port <= 0 || c.Port > 65535 {
		c.Port = DefaultPort
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

func (c Config) IsHidden(provider string) bool {
	for _, h := range c.Hidden {
		if h == provider {
			return true
		}
	}
	return false
}
