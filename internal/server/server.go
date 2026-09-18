// Package server is the local HTTP endpoint on 127.0.0.1: the hook binary posts Claude Code's hook
// events to it, and `gonotch status` (or a status bar) reads the current state from it.
//
//	POST /event            body = the hook's stdin JSON; ?pid= the process that ran the hook
//	GET  /state            the same State the notch draws, as JSON
//	POST /refresh          ?provider=claude|codex|cursor, or every provider without it
//
// Only loopback callers that are not a browser page get through: the Host must name the loopback
// port (DNS rebinding), and any request carrying an Origin is refused (a page's cross-site POST).
package server

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"strconv"
	"time"

	"github.com/leock9/gonotch/internal/app"
	"github.com/leock9/gonotch/internal/sessions"
)

const maxBody = 256 * 1024

func Handler(a *app.App, port int) http.Handler {
	allowedHosts := map[string]bool{
		fmt.Sprintf("127.0.0.1:%d", port): true,
		fmt.Sprintf("localhost:%d", port): true,
	}
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
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !allowedHosts[r.Host] || r.Header.Get("Origin") != "" {
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}
		mux.ServeHTTP(w, r)
	})
}

// Listen binds the loopback port; an error usually means another instance already runs.
func Listen(port int) (net.Listener, error) {
	return net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", port))
}

func Serve(ctx context.Context, ln net.Listener, a *app.App, port int) {
	srv := &http.Server{Handler: Handler(a, port), ReadHeaderTimeout: 5 * time.Second}
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
