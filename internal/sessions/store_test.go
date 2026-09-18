package sessions

import (
	"testing"
	"time"
)

type clock struct{ t time.Time }

func (c *clock) now() time.Time          { return c.t }
func (c *clock) advance(d time.Duration) { c.t = c.t.Add(d) }

func newTestStore() (*Store, *clock) {
	c := &clock{t: time.Unix(1_800_000_000, 0)}
	s := NewStore()
	s.now = c.now
	return s, c
}

func TestHookLifecycle(t *testing.T) {
	s, c := newTestStore()
	s.Apply(Event{Kind: EvSessionStart, SessionID: "abcd1234", CWD: "/home/u/proj", FromHook: true, PID: 42})
	if l := s.List(); len(l) != 1 || l[0].State != Idle || l[0].Title != "proj · abcd" || l[0].PID != 42 {
		t.Fatalf("after start: %+v", l)
	}
	s.Apply(Event{Kind: EvRunning, SessionID: "abcd1234", Prompt: "fix the bug", FromHook: true})
	s.Apply(Event{Kind: EvRunning, SessionID: "abcd1234", ToolName: "Bash", ToolCmd: "go test ./...", FromHook: true})
	c.advance(90 * time.Second)
	s.Apply(Event{Kind: EvAttention, SessionID: "abcd1234", Message: "Claude needs your permission to use Bash", FromHook: true})
	l := s.List()
	if l[0].State != Attention || l[0].Prompt != "fix the bug" || l[0].Last != "Bash: go test ./..." {
		t.Fatalf("attention: %+v", l[0])
	}
	s.Apply(Event{Kind: EvRunning, SessionID: "abcd1234", FromHook: true})
	c.advance(30 * time.Second)
	s.Apply(Event{Kind: EvDone, SessionID: "abcd1234", FromHook: true})
	// Answering the prompt starts a new stretch of work, so the clock counts from there
	if l := s.List(); l[0].State != Done || l[0].Attn != "" || l[0].Total != 30*time.Second {
		t.Fatalf("done: %+v", l[0])
	}
	if !s.Apply(Event{Kind: EvSessionEnd, SessionID: "abcd1234", FromHook: true}) || len(s.List()) != 0 {
		t.Fatal("session_end removes it")
	}
}

func TestWatcherYieldsToFreshHooks(t *testing.T) {
	s, c := newTestStore()
	s.Apply(Event{Kind: EvRunning, SessionID: "x", FromHook: true})
	if s.Apply(Event{Kind: EvDone, SessionID: "x"}) {
		t.Fatal("inference must not override a fresh hook")
	}
	c.advance(6 * time.Minute)
	if !s.Apply(Event{Kind: EvDone, SessionID: "x"}) || s.List()[0].State != Done {
		t.Fatal("once hooks go quiet, inference counts")
	}
}

func TestSweepEndsWhatNeverEnded(t *testing.T) {
	s, c := newTestStore()
	s.Apply(Event{Kind: EvRunning, SessionID: "run", FromHook: true})
	s.Apply(Event{Kind: EvAttention, SessionID: "attn", FromHook: true})
	c.advance(31 * time.Minute)
	if !s.Sweep() {
		t.Fatal("expected a change")
	}
	for _, se := range s.List() {
		if se.ID == "run" && se.State != Idle {
			t.Fatalf("stale running → idle, got %s", se.State)
		}
	}
	c.advance(25 * time.Hour)
	s.Sweep()
	if n := len(s.List()); n != 0 {
		t.Fatalf("everything old is gone, %d left", n)
	}
}

func TestAggregateAndOrderPutAttentionFirst(t *testing.T) {
	s, _ := newTestStore()
	s.Apply(Event{Kind: EvDone, SessionID: "d", FromHook: true})
	s.Apply(Event{Kind: EvRunning, SessionID: "r", FromHook: true})
	s.Apply(Event{Kind: EvAttention, SessionID: "a", FromHook: true})
	l := s.List()
	if l[0].ID != "a" || l[1].ID != "r" || l[2].ID != "d" || Aggregate(l) != Attention {
		t.Fatalf("order: %v %v %v agg=%s", l[0].ID, l[1].ID, l[2].ID, Aggregate(l))
	}
}

func TestAckTurnsDoneIntoIdle(t *testing.T) {
	s, _ := newTestStore()
	s.Apply(Event{Kind: EvDone, SessionID: "d", PID: 7, FromHook: true})
	if !s.AckDone(func(se Session) bool { return se.PID == 7 }) || s.List()[0].State != Idle {
		t.Fatal("ack")
	}
}
