// Package server is the app's local HTTP endpoint, on a Unix socket in a directory only this user
// can enter: the hook binary posts Claude Code's hook events to it, and `gonotch status` (or a status
// bar) reads the current state from it. No other local account and no browser page can reach it.
//
//	POST /event            body = the hook's stdin JSON; ?pid= the process that ran the hook
//	GET  /state            the same State the notch draws, as JSON
//	POST /refresh          ?provider=claude|codex|cursor|copilot, or every provider without it
//	POST /settings         opens the settings window
package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"syscall"
	"time"

	"github.com/leock9/gonotch/internal/app"
	"github.com/leock9/gonotch/internal/sessions"
)

const maxBody = 256 * 1024

// ErrRunning: another instance holds the socket.
var ErrRunning = errors.New("gonotch is already running")

func Handler(a *app.App) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /event", func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(io.LimitReader(r.Body, maxBody))
		ev := ParseHook(body)
		if e := r.URL.Query().Get("e"); e != "" {
			ev.Kind = e
		}
		ev.PID, _ = strconv.Atoi(r.URL.Query().Get("pid"))
		if ev.Kind != "" {
			a.Apply(ev)
		}
		fmt.Fprintln(w, "ok")
	})
	mux.HandleFunc("GET /state", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(a.State())
	})
	mux.HandleFunc("POST /refresh", func(w http.ResponseWriter, r *http.Request) {
		a.Refresh(r.URL.Query().Get("provider"))
		fmt.Fprintln(w, "ok")
	})
	mux.HandleFunc("POST /settings", func(w http.ResponseWriter, r *http.Request) {
		a.RequestSettings()
		fmt.Fprintln(w, "ok")
	})
	return mux
}

// listener is the socket plus the lock that makes this instance its only owner.
type listener struct {
	net.Listener
	lock *os.File
}

func (l *listener) Close() error {
	err := l.Listener.Close() // also unlinks the socket file
	l.lock.Close()
	return err
}

// Listen claims the socket at path. An exclusive lock beside it decides who owns it — two hooks
// starting the app at the same moment must not both get past a check-then-bind — and a socket left
// by a crash is replaced by whoever takes the lock.
func Listen(path string) (net.Listener, error) {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	// MkdirAll leaves an existing directory's mode alone; this one must admit nobody else
	if err := os.Chmod(dir, 0o700); err != nil {
		return nil, err
	}
	lock, err := os.OpenFile(path+".lock", os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}
	if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		lock.Close()
		if errors.Is(err, syscall.EWOULDBLOCK) {
			return nil, ErrRunning
		}
		return nil, err
	}
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		lock.Close()
		return nil, err
	}
	ln, err := net.Listen("unix", path)
	if err != nil {
		lock.Close()
		return nil, err
	}
	if err := os.Chmod(path, 0o600); err != nil {
		ln.Close()
		lock.Close()
		return nil, err
	}
	return &listener{Listener: ln, lock: lock}, nil
}

func Serve(ctx context.Context, ln net.Listener, a *app.App) {
	srv := &http.Server{Handler: Handler(a), ReadHeaderTimeout: 5 * time.Second}
	go func() {
		<-ctx.Done()
		_ = srv.Close()
	}()
	_ = srv.Serve(ln)
}

// hookKinds maps Claude Code's hook_event_name to a session event.
var hookKinds = map[string]string{
	"SessionStart":     sessions.EvSessionStart,
	"UserPromptSubmit": sessions.EvRunning,
	"PreToolUse":       sessions.EvRunning,
	"PostToolUse":      sessions.EvRunning,
	"Notification":     sessions.EvAttention,
	"Stop":             sessions.EvDone,
	"SessionEnd":       sessions.EvSessionEnd,
}

// ParseHook reads a hook's stdin leniently: no missing field is an error.
func ParseHook(body []byte) sessions.Event {
	var h struct {
		Event     string `json:"hook_event_name"`
		SessionID string `json:"session_id"`
		CWD       string `json:"cwd"`
		Prompt    string `json:"prompt"`
		Message   string `json:"message"`
		ToolName  string `json:"tool_name"`
		Model     any    `json:"model"`
		ToolInput struct {
			Command string `json:"command"`
		} `json:"tool_input"`
	}
	_ = json.Unmarshal(body, &h)
	model := ""
	switch m := h.Model.(type) {
	case string:
		model = m
	case map[string]any:
		model, _ = m["id"].(string)
	}
	return sessions.Event{
		Kind:      hookKinds[h.Event],
		SessionID: h.SessionID,
		CWD:       h.CWD,
		Prompt:    h.Prompt,
		Message:   h.Message,
		ToolName:  h.ToolName,
		ToolCmd:   h.ToolInput.Command,
		Model:     model,
		FromHook:  true,
	}
}
