package logs

import (
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestTheLogIsKeptAcrossRunsAndRotated(t *testing.T) {
	path := filepath.Join(t.TempDir(), "gonotch.log")
	for _, line := range []string{"first run\n", "second run\n"} {
		l, err := Open(path)
		if err != nil {
			t.Fatal(err)
		}
		l.Write([]byte(line))
		l.Close()
	}
	if raw, _ := os.ReadFile(path); string(raw) != "first run\nsecond run\n" {
		t.Fatalf("a new run must append, not start over: %q", raw)
	}
	l, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	l.max = 30
	l.Write([]byte("third run, past the limit\n"))
	old, _ := os.ReadFile(path + ".1")
	cur, _ := os.ReadFile(path)
	if string(old) != "first run\nsecond run\n" || string(cur) != "third run, past the limit\n" {
		t.Fatalf("after rotating: .1 = %q, current = %q", old, cur)
	}
}

func TestAppendAndTail(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	Append(slog.LevelError, "could not start gonotch", "err", "exec: not found")
	Append(slog.LevelWarn, "second")
	lines, err := Tail(Path(), 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(lines) != 1 || !strings.Contains(lines[0], `level=WARN msg=second`) {
		t.Fatalf("Tail(1) = %q", lines)
	}
	if lines, _ := Tail(Path(), 10); len(lines) != 2 || !strings.Contains(lines[0], `level=ERROR msg="could not start gonotch" err="exec: not found"`) {
		t.Fatalf("Tail(10) = %q", lines)
	}
}

func TestTailDropsTheLineASeekCutInto(t *testing.T) {
	path := filepath.Join(t.TempDir(), "gonotch.log")
	var b strings.Builder
	for b.Len() < 70<<10 {
		b.WriteString("level=INFO msg=filler\n")
	}
	b.WriteString("level=ERROR msg=last\n")
	if err := os.WriteFile(path, []byte(b.String()), 0o600); err != nil {
		t.Fatal(err)
	}
	lines, err := Tail(path, 5000)
	if err != nil {
		t.Fatal(err)
	}
	if lines[0] != "level=INFO msg=filler" || lines[len(lines)-1] != "level=ERROR msg=last" {
		t.Fatalf("first %q, last %q", lines[0], lines[len(lines)-1])
	}
}
