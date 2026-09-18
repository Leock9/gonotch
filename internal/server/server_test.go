package server

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/leock9/gonotch/internal/app"
	"github.com/leock9/gonotch/internal/config"
	"github.com/leock9/gonotch/internal/sessions"
	"github.com/leock9/gonotch/internal/sock"
)

func socketPath(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("/tmp", "gn") //nolint:usetesting // t.TempDir can exceed the 108-byte socket path limit
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	return filepath.Join(dir, "run", "gonotch.sock")
}

func isolate(t *testing.T) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, "config"))
	t.Setenv("XDG_STATE_HOME", filepath.Join(home, "state"))
}

func TestHookEventsReachTheStore(t *testing.T) {
	isolate(t)
	a := app.New(config.Default())
	body := `{"hook_event_name":"PreToolUse","session_id":"s1","cwd":"/home/u/proj","tool_name":"Bash","tool_input":{"command":"make"}}`
	rec := httptest.NewRecorder()
	Handler(a).ServeHTTP(rec, httptest.NewRequest("POST", "/event?pid=99", strings.NewReader(body)))
	if rec.Code != 200 {
		t.Fatalf("code %d", rec.Code)
	}
	l := a.State().Sessions
	if len(l) != 1 || l[0].State != sessions.Running || l[0].PID != 99 || l[0].Last != "Bash: make" {
		t.Fatalf("store: %+v", l)
	}
}

func TestTheSocketIsOnlyThisUsersAndOnlyOneInstanceHoldsIt(t *testing.T) {
	path := socketPath(t)
	ln, err := Listen(path)
	if err != nil {
		t.Fatal(err)
	}
	if st, _ := os.Stat(filepath.Dir(path)); st.Mode().Perm() != 0o700 {
		t.Errorf("socket dir mode %v, want 0700", st.Mode().Perm())
	}
	if st, _ := os.Stat(path); st.Mode().Perm() != 0o600 {
		t.Errorf("socket mode %v, want 0600", st.Mode().Perm())
	}
	if _, err := Listen(path); !errors.Is(err, ErrRunning) {
		t.Fatalf("a second instance got %v, want ErrRunning", err)
	}
	ln.Close()
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Error("closing must remove the socket file")
	}
	again, err := Listen(path)
	if err != nil {
		t.Fatalf("after the first closed: %v", err)
	}
	again.Close()
}

func TestASocketLeftByACrashIsReplaced(t *testing.T) {
	path := socketPath(t)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	stale, err := net.Listen("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	stale.(*net.UnixListener).SetUnlinkOnClose(false)
	stale.Close() // the file stays, as after a crash
	ln, err := Listen(path)
	if err != nil {
		t.Fatalf("stale socket not replaced: %v", err)
	}
	ln.Close()
}

func TestStateIsServedOverTheSocket(t *testing.T) {
	isolate(t)
	path := socketPath(t)
	ln, err := Listen(path)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go Serve(ctx, ln, app.New(config.Default()))
	resp, err := sock.Client(path, 2*time.Second).Get(sock.URL("/state"))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != 200 || !strings.Contains(string(raw), `"aggregate":"idle"`) {
		t.Fatalf("state: %d %s", resp.StatusCode, raw)
	}
}

func TestParseHookMapsEventNamesAndModelShapes(t *testing.T) {
	ev := ParseHook([]byte(`{"hook_event_name":"Notification","session_id":"x","message":"needs permission","model":{"id":"claude-opus-5"}}`))
	if ev.Kind != sessions.EvAttention || ev.Message != "needs permission" || ev.Model != "claude-opus-5" || !ev.FromHook {
		t.Fatalf("got %+v", ev)
	}
	if ParseHook([]byte(`not json`)).Kind != "" {
		t.Fatal("garbage maps to nothing")
	}
}
