// gonotch-hook is what Claude Code's hooks run: it forwards the hook's stdin to the running gonotch
// and starts gonotch if it is not running. It never blocks Claude Code: about two seconds at most,
// and every failure exits 0 silently.
package main

import (
	"bytes"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"time"

	"github.com/leock9/gonotch/internal/config"
	"github.com/leock9/gonotch/internal/sock"
)

const maxStdin = 256 * 1024

var client = sock.Client(config.SocketPath(), time.Second)

func main() {
	body, _ := io.ReadAll(io.LimitReader(os.Stdin, maxStdin))
	url := sock.URL(fmt.Sprintf("/event?pid=%d", os.Getppid()))
	if send(url, body) {
		return
	}
	// A session ending is no reason to start the notch
	if bytes.Contains(body, []byte(`"SessionEnd"`)) {
		return
	}
	if !spawnApp() {
		return
	}
	for range 20 {
		time.Sleep(100 * time.Millisecond)
		if send(url, body) {
			return
		}
	}
}

func send(url string, body []byte) bool {
	resp, err := client.Post(url, "application/json", bytes.NewReader(body))
	if err != nil {
		return false
	}
	resp.Body.Close()
	return resp.StatusCode == http.StatusOK
}

// spawnApp starts gonotch from next to this binary (or PATH), in a session of its own so a Ctrl+C
// in Claude Code's terminal does not take the notch down with it.
func spawnApp() bool {
	app := ""
	if self, err := os.Executable(); err == nil {
		if c := filepath.Join(filepath.Dir(self), "gonotch"); fileExists(c) {
			app = c
		}
	}
	if app == "" {
		var err error
		if app, err = exec.LookPath("gonotch"); err != nil {
			return false
		}
	}
	cmd := exec.Command(app)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if cmd.Start() != nil {
		return false
	}
	_ = cmd.Process.Release()
	return true
}

func fileExists(p string) bool {
	st, err := os.Stat(p)
	return err == nil && !st.IsDir()
}
