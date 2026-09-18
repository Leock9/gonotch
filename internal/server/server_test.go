package server

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/leock9/gonotch/internal/app"
	"github.com/leock9/gonotch/internal/config"
	"github.com/leock9/gonotch/internal/sessions"
)

func TestHookEventsReachTheStore(t *testing.T) {
	a := app.New(config.Default())
	h := Handler(a, 48777)
	body := `{"hook_event_name":"PreToolUse","session_id":"s1","cwd":"/home/u/proj","tool_name":"Bash","tool_input":{"command":"make"}}`
	req := httptest.NewRequest("POST", "http://127.0.0.1:48777/event?pid=99", strings.NewReader(body))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != 200 {
		t.Fatalf("code %d", rec.Code)
	}
	l := a.Store.List()
	if len(l) != 1 || l[0].State != sessions.Running || l[0].PID != 99 || l[0].Last != "Bash: make" {
		t.Fatalf("store: %+v", l)
	}
}

func TestBrowsersAndForeignHostsAreRefused(t *testing.T) {
	h := Handler(app.New(config.Default()), 48777)
	cases := []struct {
		name   string
		host   string
		origin string
	}{
		{"cross-site page", "127.0.0.1:48777", "https://evil.example"},
		{"dns rebinding", "evil.example:48777", ""},
	}
	for _, c := range cases {
		req := httptest.NewRequest("GET", "http://"+c.host+"/state", nil)
		if c.origin != "" {
			req.Header.Set("Origin", c.origin)
		}
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code != http.StatusForbidden {
			t.Errorf("%s: code %d", c.name, rec.Code)
		}
	}
	req := httptest.NewRequest("GET", "http://localhost:48777/state", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), `"aggregate":"idle"`) {
		t.Fatalf("state: %d %s", rec.Code, rec.Body)
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
