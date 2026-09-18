// Package logs keeps gonotch's log file, where errors wait to be read later: a provider that could
// not read, a hook that could not reach the app, GTK's own warnings, a crash's stack trace.
//
// The file is $XDG_STATE_HOME/gonotch/gonotch.log (~/.local/state/gonotch/gonotch.log). Every run
// appends to it, and once it passes 1 MiB it moves to gonotch.log.1, so it never takes more than
// about 2 MiB and still holds the run before a crash. Records are slog's text format, one per line.
// Nothing written here may carry a credential.
package logs

import (
	"bytes"
	"context"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"runtime/debug"
	"strings"
	"sync"

	"github.com/leock9/gonotch/internal/config"
)

const maxSize = 1 << 20

func Path() string { return filepath.Join(config.StateDir(), "gonotch.log") }

func open(path string) (*os.File, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, err
	}
	return os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
}

// File appends to the log and rotates it once it passes max. gonotch-hook appends to the same file
// from processes of its own, so the size is asked of the file rather than counted.
type File struct {
	mu   sync.Mutex
	path string
	max  int64
	f    *os.File
}

func Open(path string) (*File, error) {
	f, err := open(path)
	if err != nil {
		return nil, err
	}
	return &File{path: path, max: maxSize, f: f}, nil
}

func (l *File) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if st, err := l.f.Stat(); err == nil && st.Size()+int64(len(p)) > l.max {
		// a rotation that fails keeps writing to the full file: better long than lost
		_ = l.rotate()
	}
	return l.f.Write(p)
}

func (l *File) rotate() error {
	if err := os.Rename(l.path, l.path+".1"); err != nil {
		return err
	}
	f, err := open(l.path)
	if err != nil {
		return err
	}
	l.f.Close()
	l.f = f
	return debug.SetCrashOutput(f, debug.CrashOptions{})
}

func (l *File) Close() error {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.f.Close()
}

// Setup makes the log slog's default, and with it the log package's and GLib's (gotk4 hands GTK's
// warnings to slog.Default), and the file a crash's trace goes to besides stderr.
func Setup() (*File, error) {
	l, err := Open(Path())
	if err != nil {
		return nil, err
	}
	if err := debug.SetCrashOutput(l.f, debug.CrashOptions{}); err != nil {
		l.Close()
		return nil, err
	}
	slog.SetDefault(slog.New(slog.NewTextHandler(l, nil)))
	return l, nil
}

// Append writes one record from a short-lived process, gonotch-hook, which leaves rotating to the app.
func Append(level slog.Level, msg string, args ...any) {
	f, err := open(Path())
	if err != nil {
		return
	}
	defer f.Close()
	slog.New(slog.NewTextHandler(f, nil)).Log(context.Background(), level, msg, args...)
}

// Tail returns the log's last n lines.
func Tail(path string, n int) ([]string, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	// n lines fit in the file's last 64 KiB for any record gonotch writes
	cut := false
	if st, err := f.Stat(); err == nil && st.Size() > 64<<10 {
		if _, err := f.Seek(-64<<10, io.SeekEnd); err != nil {
			return nil, err
		}
		cut = true
	}
	raw, err := io.ReadAll(f)
	if err != nil {
		return nil, err
	}
	raw = bytes.TrimRight(raw, "\n")
	if len(raw) == 0 {
		return nil, nil
	}
	lines := strings.Split(string(raw), "\n")
	if cut {
		lines = lines[1:] // the seek landed inside a line
	}
	return lines[max(0, len(lines)-n):], nil
}
