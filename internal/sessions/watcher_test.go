package sessions

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestTailFindsEntryPromptAndModel(t *testing.T) {
	path := filepath.Join(t.TempDir(), "s.jsonl")
	lines := []string{
		`{"type":"user","sessionId":"s1","cwd":"/p","message":{"content":"refactor the parser"}}`,
		`{"type":"assistant","sessionId":"s1","message":{"model":"claude-opus-5","content":[{"type":"tool_use","name":"Read"}]}}`,
		`{"type":"user","sessionId":"s1","message":{"content":[{"type":"tool_result","content":"..."}]}}`,
		`{"type":"assistant","sessionId":"s1","message":{"model":"claude-opus-5","content":[{"type":"text","text":"Done."}]}}`,
		`{"type":"summary","summary":"bookkeeping"}`,
		`{"type":"assistant","trunc`,
	}
	if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")), 0o600); err != nil {
		t.Fatal(err)
	}
	tail, ok := TailInfo(path)
	if !ok || tail.Prompt != "refactor the parser" || tail.Model != "claude-opus-5" || !strings.Contains(string(tail.Entry), "Done.") {
		t.Fatalf("got %+v ok=%v", tail, ok)
	}
}

func TestInferenceFromTheLastEntry(t *testing.T) {
	var got []string
	w := &Watcher{Apply: func(ev Event) { got = append(got, ev.Kind) }, tracks: map[string]*track{}}
	base := time.Now()
	w.tracks["a"] = &track{session: "a", kind: kindAssistantText, lastAppend: base, sent: EvRunning}
	w.tracks["b"] = &track{session: "b", kind: kindAssistantTool, lastAppend: base, sent: EvRunning}
	w.evaluate(base.Add(3 * time.Second))
	if len(got) != 1 || got[0] != EvDone {
		t.Fatalf("after 3 s: %v", got)
	}
	w.evaluate(base.Add(21 * time.Second))
	w.evaluate(base.Add(22 * time.Second))
	if len(got) != 2 || got[1] != EvAttention {
		t.Fatalf("after 21 s, pushed once: %v", got)
	}
}

func TestOnlyRealTranscripts(t *testing.T) {
	for p, want := range map[string]bool{
		"/h/.claude/projects/x/abc.jsonl":             true,
		"/h/.claude/projects/x/abc/subagents/s.jsonl": false,
		"/h/.claude/projects/x/audit.jsonl":           false,
		"/h/.claude/projects/x/notes.txt":             false,
	} {
		if IsTranscript(p) != want {
			t.Errorf("IsTranscript(%q) != %v", p, want)
		}
	}
}
