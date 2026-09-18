// Package sessions tracks Claude Code sessions: running, waiting on you, done, idle. Hook events
// are the primary source; the transcript watcher infers the same states where hooks are missing.
package sessions

import (
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode/utf8"
)

type State string

const (
	Running   State = "running"
	Attention State = "attention" // waiting on you: a permission prompt or a question
	Done      State = "done"
	Idle      State = "idle"
)

// Event kinds, as the hook binary reports them.
const (
	EvSessionStart = "session_start"
	EvRunning      = "running"
	EvAttention    = "attention"
	EvDone         = "done"
	EvSessionEnd   = "session_end"
)

const (
	runningStale   = 30 * time.Minute // running with no event for this long was an abnormal exit
	doneStale      = 24 * time.Hour
	attentionStale = 24 * time.Hour // a session that crashed while waiting never sends another event
	idleDrop       = 10 * time.Minute
	hookFresh      = 5 * time.Minute // watcher inference yields to hook data this recent
	maxSessions    = 200             // bounds what a misbehaving local sender could make the store hold
)

type Session struct {
	ID      string        `json:"id"`
	Title   string        `json:"title"`
	State   State         `json:"state"`
	Started time.Time     `json:"started"`         // start of the current activity
	Total   time.Duration `json:"total,omitempty"` // elapsed time, frozen at done
	Last    string        `json:"last,omitempty"`  // last tool action
	Attn    string        `json:"attn,omitempty"`  // what it is waiting on you for
	Prompt  string        `json:"prompt,omitempty"`
	Model   string        `json:"model,omitempty"`
	CWD     string        `json:"cwd,omitempty"`
	// PID of the process that ran the hook (the Claude Code CLI), for jumping back to its terminal
	PID int `json:"-"`

	lastEvent time.Time
	lastHook  time.Time
}

type Event struct {
	Kind      string
	SessionID string
	PID       int
	CWD       string
	Prompt    string
	Message   string
	ToolName  string
	ToolCmd   string
	Model     string
	// FromHook: a real hook event, as opposed to the watcher's inference
	FromHook bool
}

type Store struct {
	mu  sync.Mutex
	m   map[string]*Session
	now func() time.Time
}

func NewStore() *Store { return &Store{m: map[string]*Session{}, now: time.Now} }

func truncate(s string, n int) string {
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	r := []rune(s)
	return string(r[:n]) + "…"
}

func titleOf(cwd, id string) string {
	base := filepath.Base(strings.TrimRight(cwd, "/"))
	if base == "." || base == "/" || base == "" {
		base = "claude"
	}
	short := id
	if len(short) > 4 {
		short = short[:4]
	}
	return base + " · " + short
}

// Apply folds an event into the store and reports whether anything visible changed, so a burst of
// watcher appends does not turn into a burst of redraws.
func (s *Store) Apply(ev Event) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.now()
	if ev.SessionID == "" {
		ev.SessionID = "unknown"
	}
	if ev.Kind == EvSessionEnd {
		_, ok := s.m[ev.SessionID]
		delete(s.m, ev.SessionID)
		return ok
	}
	cwd := truncate(ev.CWD, 1024)
	se, ok := s.m[ev.SessionID]
	if !ok {
		if len(s.m) >= maxSessions {
			s.evictOldest()
		}
		se = &Session{ID: ev.SessionID, Title: titleOf(cwd, ev.SessionID), State: Idle, Started: now, CWD: cwd, lastEvent: now}
		s.m[ev.SessionID] = se
	}
	if !ev.FromHook && !se.lastHook.IsZero() && now.Sub(se.lastHook) < hookFresh {
		return false
	}
	if ev.FromHook {
		se.lastHook = now
	}
	before := *se
	se.lastEvent = now
	if ev.PID != 0 {
		se.PID = ev.PID
	}
	if ev.Model != "" {
		se.Model = ev.Model
	}
	if cwd != "" && se.CWD == "" {
		se.CWD = cwd
		se.Title = titleOf(cwd, se.ID)
	}
	switch ev.Kind {
	case EvSessionStart:
		if se.State != Running {
			se.State = Idle
		}
	case EvRunning:
		if se.State != Running {
			se.Started = now
		}
		se.State = Running
		se.Attn = ""
		if ev.Prompt != "" {
			se.Prompt = truncate(ev.Prompt, 120)
		}
		if ev.ToolName != "" {
			se.Last = ev.ToolName
			if ev.ToolCmd != "" {
				se.Last += ": " + truncate(ev.ToolCmd, 60)
			}
		}
	case EvAttention:
		se.State = Attention
		if ev.Message != "" {
			se.Attn = truncate(ev.Message, 200)
		}
	case EvDone:
		if se.State != Done {
			se.Total = now.Sub(se.Started)
		}
		se.State = Done
		se.Attn = ""
	}
	return se.State != before.State || se.Last != before.Last || se.Attn != before.Attn ||
		se.Prompt != before.Prompt || se.Model != before.Model || !ok
}

func (s *Store) evictOldest() {
	var oldest string
	var at time.Time
	for id, se := range s.m {
		if oldest == "" || se.lastEvent.Before(at) {
			oldest, at = id, se.lastEvent
		}
	}
	delete(s.m, oldest)
}

func (s *Store) Dismiss(id string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, ok := s.m[id]
	delete(s.m, id)
	return ok
}

// AckDone turns done sessions the predicate matches into idle: looking at a finished session
// acknowledges it, and the sweep removes it later.
func (s *Store) AckDone(seen func(Session) bool) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	changed := false
	for _, se := range s.m {
		if se.State == Done && seen(*se) {
			se.State = Idle
			se.lastEvent = s.now()
			changed = true
		}
	}
	return changed
}

// Sweep ends what never ended on its own; reports whether anything changed.
func (s *Store) Sweep() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.now()
	changed := false
	for id, se := range s.m {
		age := now.Sub(se.lastEvent)
		switch {
		case se.State == Running && age > runningStale:
			se.State = Idle
			changed = true
		case se.State == Idle && age > idleDrop,
			se.State == Done && age > doneStale,
			se.State == Attention && age > attentionStale:
			delete(s.m, id)
			changed = true
		}
	}
	return changed
}

func rank(st State) int {
	switch st {
	case Attention:
		return 0
	case Running:
		return 1
	case Done:
		return 2
	}
	return 3
}

// List returns the sessions, the ones costing attention first.
func (s *Store) List() []Session {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]Session, 0, len(s.m))
	for _, se := range s.m {
		out = append(out, *se)
	}
	sort.Slice(out, func(i, j int) bool {
		if rank(out[i].State) != rank(out[j].State) {
			return rank(out[i].State) < rank(out[j].State)
		}
		return out[i].Started.After(out[j].Started)
	})
	return out
}

// Aggregate is the state the ring shows: the most demanding of all sessions.
func Aggregate(list []Session) State {
	agg := Idle
	for _, se := range list {
		if rank(se.State) < rank(agg) {
			agg = se.State
		}
	}
	return agg
}

func (s *Store) PIDOf(id string) (int, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	se, ok := s.m[id]
	if !ok {
		return 0, false
	}
	return se.PID, true
}
