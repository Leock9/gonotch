// Package usage holds the shape every provider reports in: a list of limit windows plus what is
// known about how current they are. Nothing here talks to the network.
package usage

import (
	"encoding/json"
	"os"
	"path/filepath"
	"time"
)

// Status says how far a snapshot can be trusted. A provider never invents a number: when a read
// fails it keeps the last windows and marks them stale.
type Status string

const (
	StatusLoading   Status = ""          // nothing read yet
	StatusOK        Status = "ok"        // fresh reading
	StatusStale     Status = "stale"     // an older reading, kept because the latest attempt failed
	StatusNeedsAuth Status = "needsAuth" // no usable credential; the note says what to do
	StatusError     Status = "error"     // failed and there is nothing older to show
	StatusNone      Status = "none"      // signed in, but the plan meters nothing
	StatusAbsent    Status = "absent"    // the tool is not installed here: no ring at all
)

// StaleAfter is how old a reading may get before it is drawn dimmed, whatever its status says.
const StaleAfter = 5 * time.Minute

// Window is one limit: a session, a week, a billing cycle.
type Window struct {
	ID       string    `json:"id"`
	Label    string    `json:"label"`
	Used     float64   `json:"used"` // 0–1
	ResetsAt time.Time `json:"resets_at,omitzero"`
	// Group is the heading a window sits under on the card (Codex's Spark, Code review); empty = main list
	Group string `json:"group,omitempty"`
}

// Snapshot is a provider's latest reading.
type Snapshot struct {
	Provider     string    `json:"provider"`
	Status       Status    `json:"status"`
	Windows      []Window  `json:"windows"`
	FetchedAt    time.Time `json:"fetched_at,omitzero"`
	Note         string    `json:"note,omitempty"`
	Plan         string    `json:"plan,omitempty"`
	BackoffUntil time.Time `json:"backoff_until,omitzero"`
	// Headline is the id of the window the ring shows; Weekly the thinner second ring, if any.
	// Declared per provider, so a window missing from one reply is a dash, never a stand-in.
	Headline string `json:"headline,omitempty"`
	Weekly   string `json:"weekly,omitempty"`
}

// Window returns the window with that id.
func (s Snapshot) Window(id string) (Window, bool) {
	for _, w := range s.Windows {
		if w.ID == id {
			return w, true
		}
	}
	return Window{}, false
}

// HeadlineWindow is the window the ring draws.
func (s Snapshot) HeadlineWindow() (Window, bool) {
	if s.Headline == "" {
		return Window{}, false
	}
	return s.Window(s.Headline)
}

// WeeklyWindow is the second ring's window, when it is a different one from the headline.
func (s Snapshot) WeeklyWindow() (Window, bool) {
	if s.Weekly == "" || s.Weekly == s.Headline {
		return Window{}, false
	}
	return s.Window(s.Weekly)
}

// IsStale reports whether the reading should be drawn dimmed.
func (s Snapshot) IsStale(now time.Time) bool {
	if s.Status == StatusStale {
		return true
	}
	return !s.FetchedAt.IsZero() && now.Sub(s.FetchedAt) > StaleAfter
}

// Failed turns a failed read into the right snapshot: the last windows marked stale if there are
// any, an error otherwise.
func (s Snapshot) Failed(note string) Snapshot {
	if len(s.Windows) > 0 {
		s.Status = StatusStale
	} else {
		s.Status = StatusError
	}
	s.Note = note
	return s
}

// Band is the colour band of a used fraction: under half, under 80 %, the rest.
type Band int

const (
	BandAmple Band = iota
	BandWatch
	BandCritical
)

func BandOf(used float64) Band {
	switch {
	case used >= 0.8:
		return BandCritical
	case used >= 0.5:
		return BandWatch
	default:
		return BandAmple
	}
}

// Load reads a persisted snapshot. An old reading after a restart is labelled stale: stale beats blank.
func Load(path string) Snapshot {
	var s Snapshot
	raw, err := os.ReadFile(path)
	if err != nil || json.Unmarshal(raw, &s) != nil {
		return Snapshot{}
	}
	if len(s.Windows) > 0 {
		s.Status = StatusStale
	}
	return s
}

// Save persists a snapshot, through a temporary file so a crash never leaves half of one.
func Save(path string, s Snapshot) error {
	raw, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, raw, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}
