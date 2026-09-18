// gonotch shows how much of each coding assistant's usage limit is gone, in a notch on the screen
// edge, and whether Claude Code is working, done, or waiting on you.
//
//	gonotch                  run the notch
//	gonotch status [--json]  the running notch's readings, for a terminal or a status bar
//	gonotch doctor           what each provider finds on this machine
//	gonotch install-hooks    wire Claude Code's hooks to gonotch-hook (and uninstall-hooks)
//	gonotch autostart on|off start at login
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/leock9/gonotch/internal/app"
	"github.com/leock9/gonotch/internal/config"
	"github.com/leock9/gonotch/internal/hooks"
	"github.com/leock9/gonotch/internal/server"
	"github.com/leock9/gonotch/internal/text"
	"github.com/leock9/gonotch/internal/ui"
	"github.com/leock9/gonotch/internal/usage"
)

func main() {
	cmd := ""
	if len(os.Args) > 1 {
		cmd = os.Args[1]
	}
	var err error
	switch cmd {
	case "", "run":
		err = run()
	case "status":
		err = status(len(os.Args) > 2 && os.Args[2] == "--json")
	case "doctor":
		doctor()
	case "install-hooks":
		err = report(hooks.Install(ui.HookBinary()))
	case "uninstall-hooks":
		err = report(hooks.Uninstall())
	case "autostart":
		err = autostart(os.Args[2:])
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
  status [--json]   the running notch's readings
  doctor            what each provider finds on this machine
  install-hooks     wire Claude Code's hooks to gonotch-hook
  uninstall-hooks   remove them again
  autostart on|off  start at login
`

func run() error {
	cfg := config.Load()
	ln, err := server.Listen(cfg.Port)
	if err != nil {
		return fmt.Errorf("port %d is taken — is gonotch already running? (%v)", cfg.Port, err)
	}
	if err := os.MkdirAll(config.StateDir(), 0o700); err == nil {
		if f, err := os.OpenFile(filepath.Join(config.StateDir(), "gonotch.log"), os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600); err == nil {
			log.SetOutput(f)
		}
	}
	// Under Wayland the notch runs through XWayland: only an X11 window can place itself on the
	// screen edge and stay above the rest (a layer-shell backend would lift this)
	if os.Getenv("WAYLAND_DISPLAY") != "" && os.Getenv("DISPLAY") != "" {
		os.Setenv("GDK_BACKEND", "x11")
	}
	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()
	a := app.New(cfg)
	a.Start(ctx)
	go server.Serve(ctx, ln, a, cfg.Port)
	go func() {
		<-ctx.Done()
		ui.Quit()
	}()
	ui.Run(a)
	return nil
}

func status(asJSON bool) error {
	cfg := config.Load()
	client := &http.Client{Timeout: 2 * time.Second}
	resp, err := client.Get(fmt.Sprintf("http://127.0.0.1:%d/state", cfg.Port))
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
	for _, p := range st.Providers {
		line := fmt.Sprintf("%-7s", p.Name)
		h, ok := p.HeadlineWindow()
		switch {
		case ok:
			line += fmt.Sprintf(" %5s  %s", text.Pct(h.Used), text.Reset(lang, h.ResetsAt, now))
			if w, ok := p.WeeklyWindow(); ok {
				line += fmt.Sprintf(" · %s %s", w.Label, text.Pct(w.Used))
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
	fmt.Printf("gonotch doctor\nconfig: %s (port %d)\nstate:  %s\n\n", config.Path(), cfg.Port, config.StateDir())
	a := app.New(cfg)
	for _, p := range a.Providers() {
		fmt.Printf("%s: present=%v\n  %s\n", p.Name(), p.Present(), p.Probe())
	}
	fmt.Printf("\nClaude Code hooks: installed=%v (%s)\nhook binary: %s\n", hooks.IsInstalled(), hooks.SettingsPath(), ui.HookBinary())
	fmt.Printf("session: XDG_SESSION_TYPE=%s WAYLAND_DISPLAY=%s DISPLAY=%s\n", os.Getenv("XDG_SESSION_TYPE"), os.Getenv("WAYLAND_DISPLAY"), os.Getenv("DISPLAY"))
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
