package sessions

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/fsnotify/fsnotify"
)

// The watcher infers session state from Claude Code's transcripts in ~/.claude/projects/**/*.jsonl,
// for sessions whose hooks are not installed (or not firing):
//   - the file is being appended                           → running
//   - last entry is plain assistant text, quiet > 2.5 s    → done
//   - last entry is an assistant tool_use, quiet > 20 s    → waiting on you (a slow tool can be misread)
//   - last entry is the user's, quiet > 75 s               → done (an abandoned or stopped turn)
//
// Hook events win: a session with hook data from the last five minutes ignores all of this.
const (
	quietDone     = 2500 * time.Millisecond
	quietAttn     = 20 * time.Second
	quietUserDone = 75 * time.Second
	quietOther    = 5 * time.Minute
	rescanEvery   = 45 * time.Second
	freshWindow   = 10 * time.Minute
	ingestGap     = 800 * time.Millisecond // a streaming transcript fires dozens of writes a second
	tailBytes     = 256 * 1024             // one entry often exceeds 16 KB; a cut last line would never parse
)

type kind int

const (
	kindOther kind = iota
	kindUser
	kindAssistantText
	kindAssistantTool
)

type track struct {
	session     string
	cwd         string
	lastAppend  time.Time
	kind        kind
	interrupted bool
	prompt      string
	model       string
	sent        string // last state pushed, to avoid repeats
}

type Watcher struct {
	Root  string
	Apply func(Event) // the store's Apply, plus whatever the caller does on a change

	tracks     map[string]*track
	lastIngest map[string]time.Time
	dirty      map[string]bool
}

func NewWatcher(apply func(Event)) *Watcher {
	home, _ := os.UserHomeDir()
	return &Watcher{Root: filepath.Join(home, ".claude", "projects"), Apply: apply}
}

// IsTranscript accepts real session transcripts only: no sub-agents, no audit logs.
func IsTranscript(p string) bool {
	if !strings.HasSuffix(p, ".jsonl") || filepath.Base(p) == "audit.jsonl" {
		return false
	}
	return !strings.Contains(filepath.ToSlash(p), "/subagents/")
}

func (w *Watcher) Run(ctx context.Context) {
	w.tracks = map[string]*track{}
	w.lastIngest = map[string]time.Time{}
	w.dirty = map[string]bool{}
	fw, err := fsnotify.NewWatcher()
	if err != nil {
		return
	}
	defer fw.Close()
	// fsnotify is not recursive: the root and each project directory are watched, new ones as they appear
	watchTree := func() {
		_ = fw.Add(w.Root)
		entries, _ := os.ReadDir(w.Root)
		for _, e := range entries {
			if e.IsDir() {
				_ = fw.Add(filepath.Join(w.Root, e.Name()))
			}
		}
	}
	watchTree()
	w.rescan()
	tick := time.NewTicker(400 * time.Millisecond)
	defer tick.Stop()
	lastScan := time.Now()
	for {
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
			if !w.take(fw) {
				return
			}
			now := time.Now()
			for p := range w.dirty {
				if now.Sub(w.lastIngest[p]) >= ingestGap {
					delete(w.dirty, p)
					w.lastIngest[p] = now
					w.ingest(p)
				}
			}
			w.evaluate(now)
			// Self-healing: new project directories, and a safety net for events notify dropped
			if now.Sub(lastScan) > rescanEvery {
				lastScan = now
				watchTree()
				w.rescan()
				for p, t := range w.lastIngest {
					if now.Sub(t) > freshWindow {
						delete(w.lastIngest, p)
					}
				}
			}
		}
	}
}

// take drains the events queued since the last tick. Reading in batches is the point: while nobody
// reads, the kernel folds a transcript's repeated writes into one event, where reading each as it came
// cost about six wakeups per write. False once the watcher is closed.
func (w *Watcher) take(fw *fsnotify.Watcher) bool {
	for {
		select {
		case ev, ok := <-fw.Events:
			if !ok {
				return false
			}
			if ev.Op&fsnotify.Create != 0 {
				if st, err := os.Stat(ev.Name); err == nil && st.IsDir() {
					_ = fw.Add(ev.Name)
				}
			}
			if IsTranscript(ev.Name) && ev.Op&(fsnotify.Write|fsnotify.Create) != 0 {
				w.dirty[ev.Name] = true
			}
		case <-fw.Errors:
		default:
			return true
		}
	}
}

// rescan ingests every transcript written since it was last read, within the freshness window.
func (w *Watcher) rescan() {
	now := time.Now()
	_ = filepath.WalkDir(w.Root, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || !IsTranscript(p) {
			return nil
		}
		info, err := d.Info()
		if err != nil || now.Sub(info.ModTime()) > freshWindow {
			return nil
		}
		if t, ok := w.tracks[p]; ok && !info.ModTime().After(t.lastAppend) {
			return nil
		}
		w.ingest(p)
		if t, ok := w.tracks[p]; ok {
			t.lastAppend = info.ModTime() // judged from the write time, not from when we noticed it
		}
		return nil
	})
}

func (w *Watcher) ingest(path string) {
	info, ok := TailInfo(path)
	if !ok {
		return
	}
	var e struct {
		Type      string `json:"type"`
		SessionID string `json:"sessionId"` // transcripts are camelCase, unlike hook stdin
		CWD       string `json:"cwd"`
		Message   struct {
			Content json.RawMessage `json:"content"`
		} `json:"message"`
	}
	_ = json.Unmarshal(info.Entry, &e)
	session := e.SessionID
	if session == "" {
		session = strings.TrimSuffix(filepath.Base(path), ".jsonl")
	}
	k := kindOther
	switch e.Type {
	case "user":
		k = kindUser
	case "assistant":
		k = kindAssistantText
		if bytes.Contains(e.Message.Content, []byte(`"tool_use"`)) {
			k = kindAssistantTool
		}
	}
	t, ok := w.tracks[path]
	if !ok {
		t = &track{}
		w.tracks[path] = t
	}
	t.session = session
	if e.CWD != "" {
		t.cwd = e.CWD
	}
	t.lastAppend = time.Now()
	t.kind = k
	t.interrupted = k == kindUser && bytes.Contains(bytes.ToLower(e.Message.Content), []byte("interrupt"))
	if info.Prompt != "" {
		t.prompt = info.Prompt
	}
	if info.Model != "" {
		t.model = info.Model
	}
	t.sent = EvRunning
	w.push(EvRunning, t)
}

func (w *Watcher) evaluate(now time.Time) {
	for _, t := range w.tracks {
		quiet := now.Sub(t.lastAppend)
		next := ""
		switch {
		case t.kind == kindAssistantText && quiet > quietDone:
			next = EvDone
		case t.kind == kindAssistantTool && quiet > quietAttn:
			next = EvAttention
		case t.kind == kindUser && t.interrupted && quiet > quietDone:
			next = EvDone
		case t.kind == kindUser && quiet > quietUserDone:
			next = EvDone
		case t.kind == kindOther && quiet > quietOther:
			next = EvDone
		}
		if next != "" && t.sent != next {
			t.sent = next
			w.push(next, t)
		}
	}
}

func (w *Watcher) push(kind string, t *track) {
	w.Apply(Event{Kind: kind, SessionID: t.session, CWD: t.cwd, Prompt: t.prompt, Model: t.model})
}

// Tail is what the end of a transcript says: its latest conversation entry, the latest real user
// input, and the model the session actually uses.
type Tail struct {
	Entry  json.RawMessage
	Prompt string
	Model  string
}

// TailInfo reads the last 256 KB and walks back over at most 80 lines. A partial last line, a huge
// line cut by the window, and bookkeeping entries are all stepped over.
func TailInfo(path string) (Tail, bool) {
	f, err := os.Open(path)
	if err != nil {
		return Tail{}, false
	}
	defer f.Close()
	if st, err := f.Stat(); err == nil && st.Size() > tailBytes {
		_, _ = f.Seek(st.Size()-tailBytes, io.SeekStart)
	}
	raw, err := io.ReadAll(f)
	if err != nil {
		return Tail{}, false
	}
	var lines [][]byte
	sc := bufio.NewScanner(bytes.NewReader(raw))
	sc.Buffer(make([]byte, 0, 64*1024), tailBytes+1)
	for sc.Scan() {
		if len(bytes.TrimSpace(sc.Bytes())) > 0 {
			lines = append(lines, append([]byte(nil), sc.Bytes()...))
		}
	}
	var out Tail
	for i, seen := len(lines)-1, 0; i >= 0 && seen < 80; i, seen = i-1, seen+1 {
		var v struct {
			Type    string `json:"type"`
			Message struct {
				Model   string          `json:"model"`
				Content json.RawMessage `json:"content"`
			} `json:"message"`
		}
		if json.Unmarshal(lines[i], &v) != nil || (v.Type != "user" && v.Type != "assistant") {
			continue
		}
		if out.Entry == nil {
			out.Entry = lines[i]
		}
		if out.Model == "" && v.Type == "assistant" {
			out.Model = v.Message.Model
		}
		if out.Prompt == "" && v.Type == "user" {
			out.Prompt = userText(v.Message.Content)
		}
		if out.Entry != nil && out.Model != "" && out.Prompt != "" {
			break
		}
	}
	return out, out.Entry != nil
}

// userText is what the user typed: a plain string, or the text blocks of an array. Entries that
// carry a tool_result are the harness answering a tool, not the user.
func userText(content json.RawMessage) string {
	var s string
	if json.Unmarshal(content, &s) == nil {
		return truncate(strings.TrimSpace(s), 120)
	}
	var blocks []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	if json.Unmarshal(content, &blocks) != nil {
		return ""
	}
	for _, b := range blocks {
		if b.Type == "tool_result" {
			return ""
		}
	}
	for _, b := range blocks {
		if t := strings.TrimSpace(b.Text); b.Type == "text" && t != "" {
			return truncate(t, 120)
		}
	}
	return ""
}
