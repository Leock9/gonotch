// gonotch shows how much of each coding assistant's usage limit is gone, in a notch on the screen
// edge, and whether Claude Code is working, done, or waiting on you.
//
//	gonotch                  run the notch
//	gonotch demo             run it with made-up readings and sessions (no accounts needed)
//	gonotch status [--json]  the running notch's readings, for a terminal or a status bar
//	gonotch settings         open the running notch's settings
//	gonotch doctor           what each provider finds on this machine
//	gonotch log              the end of the log, where errors are kept
//	gonotch install-hooks    wire Claude Code's hooks to gonotch-hook (and uninstall-hooks)
//	gonotch autostart on|off start at login
package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"time"

	"github.com/leock9/gonotch/internal/app"
	"github.com/leock9/gonotch/internal/config"
	"github.com/leock9/gonotch/internal/hooks"
	"github.com/leock9/gonotch/internal/logs"
	"github.com/leock9/gonotch/internal/providers/demo"
	"github.com/leock9/gonotch/internal/server"
	"github.com/leock9/gonotch/internal/sock"
	"github.com/leock9/gonotch/internal/text"
	"github.com/leock9/gonotch/internal/ui"
	"github.com/leock9/gonotch/internal/usage"
)

// version is set at build time: -ldflags "-X main.version=v1.2.3"
var version = "dev"

func main() {
	cmd := ""
	if len(os.Args) > 1 {
		cmd = os.Args[1]
	}
	var err error
	switch cmd {
	case "", "run":
		err = run(false)
	case "demo":
		err = run(true)
	case "status":
		err = status(len(os.Args) > 2 && os.Args[2] == "--json")
	case "settings":
		if !openSettings() {
			err = fmt.Errorf("gonotch is not running")
		}
	case "doctor":
		doctor()
	case "log":
		err = showLog()
	case "install-hooks":
		err = report(hooks.Install(ui.HookBinary()))
	case "uninstall-hooks":
		err = report(hooks.Uninstall())
	case "autostart":
		err = autostart(os.Args[2:])
	case "version", "--version", "-v":
		fmt.Println("gonotch", version)
	case "-h", "--help", "help":
		fmt.Println(strings.TrimSpace(usageText))
	default:
		err = fmt.Errorf("unknown command %q\n\n%s", cmd, strings.TrimSpace(usageText))
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "gonotch:", err)
		os.Exit(1)
	}
}

const usageText = `
usage: gonotch [command]

  (none)            run the notch
  demo              run it with made-up readings and sessions
  status [--json]   the running notch's readings
  settings          open the running notch's settings
  doctor            what each provider finds on this machine
  log               the end of the log, where errors are kept
  install-hooks     wire Claude Code's hooks to gonotch-hook
  uninstall-hooks   remove them again
  autostart on|off  start at login
  version           print the version
`

func run(demoMode bool) error {
	cfg := config.Load()
	ln, err := server.Listen(config.SocketPath())
	if errors.Is(err, server.ErrRunning) {
		if demoMode {
			return errors.New("gonotch is running — quit it first (right-click › Quit) to try the demo")
		}
		// Launched again while it runs: bring its settings forward, the way back to a hidden notch
		if openSettings() {
			return nil
		}
		return errors.New("gonotch is already running but does not answer")
	}
	// The log is set up past the check above: a second instance that only brings the settings forward
	// has nothing to say in it
	if lf, lerr := logs.Setup(); lerr == nil {
		defer lf.Close()
	} else {
		fmt.Fprintln(os.Stderr, "gonotch: no log file:", lerr)
	}
	if err != nil {
		slog.Error("cannot listen", "socket", config.SocketPath(), "err", err)
		return fmt.Errorf("cannot listen on %s: %w", config.SocketPath(), err)
	}
	slog.Info("started", "version", version, "pid", os.Getpid(), "demo", demoMode,
		"session", os.Getenv("XDG_SESSION_TYPE"), "wayland", os.Getenv("WAYLAND_DISPLAY") != "")
	defer slog.Info("quit")
	// Under Wayland the notch runs through XWayland: only an X11 window can place itself on the
	// screen edge and stay above the rest (a layer-shell backend would lift this)
	if os.Getenv("WAYLAND_DISPLAY") != "" && os.Getenv("DISPLAY") != "" {
		os.Setenv("GDK_BACKEND", "x11")
	}
	// The notch sleeps between GTK callbacks; with one P the Go scheduler stops waking idle threads on
	// every one of them (measured: 58% fewer wakeups while the arc turns). GOMAXPROCS still wins.
	if os.Getenv("GOMAXPROCS") == "" {
		runtime.GOMAXPROCS(1)
	}
	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()
	a := app.New(cfg)
	if demoMode {
		a = app.NewWith(cfg, demo.Providers())
	}
	a.Start(ctx)
	if demoMode {
		for _, ev := range demo.Sessions() {
			a.Apply(ev)
		}
	}
	go server.Serve(ctx, ln, a)
	go func() {
		<-ctx.Done()
		ui.Quit()
	}()
	ui.Run(a)
	a.Flush()
	return nil
}

// openSettings asks a running gonotch to show its settings window.
func openSettings() bool {
	client := sock.Client(config.SocketPath(), 2*time.Second)
	resp, err := client.Post(sock.URL("/settings"), "text/plain", nil)
	if err != nil {
		return false
	}
	resp.Body.Close()
	return resp.StatusCode == http.StatusOK
}

func status(asJSON bool) error {
	client := sock.Client(config.SocketPath(), 2*time.Second)
	resp, err := client.Get(sock.URL("/state"))
	if err != nil {
		return fmt.Errorf("gonotch is not running")
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return err
	}
	if asJSON {
		_, err = os.Stdout.Write(raw)
		return err
	}
	var st app.State
	if err := json.Unmarshal(raw, &st); err != nil {
		return err
	}
	lang, now := text.Detect(), time.Now()
	nameWidth := 0
	for _, p := range st.Providers {
		nameWidth = max(nameWidth, len(p.Name))
	}
	for _, p := range st.Providers {
		line := fmt.Sprintf("%-*s", nameWidth, p.Name)
		h, ok := p.HeadlineWindow()
		switch {
		case ok:
			line += fmt.Sprintf(" %5s  %s", text.Pct(h.Used), text.Reset(lang, h.ResetsAt, now))
			if w, ok := p.WeeklyWindow(); ok {
				line += fmt.Sprintf(" · %s %s", text.Label(lang, w.Label), text.Pct(w.Used))
			}
		case p.Status == usage.StatusLoading:
			line += "     …"
		default:
			line += "     —  " + p.Note
		}
		if p.IsStale(now) {
			line += " (stale)"
		}
		fmt.Println(line)
	}
	for _, s := range st.Sessions {
		fmt.Printf("  %-9s %s\n", s.State, s.Title)
	}
	return nil
}

func doctor() {
	cfg := config.Load()
	fmt.Printf("gonotch doctor\nconfig: %s\nsocket: %s\nstate:  %s\nlog:    %s\n\n", config.Path(), config.SocketPath(), config.StateDir(), logs.Path())
	a := app.New(cfg)
	for _, p := range a.Providers() {
		fmt.Printf("%s: present=%v\n  %s\n", p.Name(), p.Present(), p.Probe())
	}
	fmt.Printf("\nClaude Code hooks: installed=%v (%s)\nhook binary: %s\n", hooks.IsInstalled(), hooks.SettingsPath(), ui.HookBinary())
	fmt.Printf("session: XDG_SESSION_TYPE=%s WAYLAND_DISPLAY=%s DISPLAY=%s\n", os.Getenv("XDG_SESSION_TYPE"), os.Getenv("WAYLAND_DISPLAY"), os.Getenv("DISPLAY"))
}

// showLog prints the end of the log: what to attach to an issue.
func showLog() error {
	lines, err := logs.Tail(logs.Path(), 50)
	if errors.Is(err, os.ErrNotExist) {
		fmt.Println("nothing logged yet:", logs.Path())
		return nil
	}
	if err != nil {
		return err
	}
	for _, l := range lines {
		fmt.Println(l)
	}
	fmt.Fprintf(os.Stderr, "\n(the last %d lines of %s; the run before a rotation is in %[2]s.1)\n", len(lines), logs.Path())
	return nil
}

func report(msg string, err error) error {
	if err != nil {
		return err
	}
	fmt.Println(msg)
	return nil
}

func autostart(args []string) error {
	path := filepath.Join(filepath.Dir(config.Dir()), "autostart", "gonotch.desktop")
	switch {
	case len(args) == 1 && args[0] == "on":
		exe, err := os.Executable()
		if err != nil {
			return err
		}
		entry := fmt.Sprintf("[Desktop Entry]\nType=Application\nName=gonotch\nComment=Coding assistant usage notch\nExec=%q\nTerminal=false\nNoDisplay=true\nX-GNOME-Autostart-enabled=true\n", exe)
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			return err
		}
		if err := os.WriteFile(path, []byte(entry), 0o644); err != nil {
			return err
		}
		fmt.Println("start at login enabled:", path)
	case len(args) == 1 && args[0] == "off":
		if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
			return err
		}
		fmt.Println("start at login disabled")
	default:
		return fmt.Errorf("usage: gonotch autostart on|off")
	}
	return nil
}
