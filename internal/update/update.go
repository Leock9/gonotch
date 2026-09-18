// Package update finds out whether a newer gonotch has been released and installs it in place of the
// running binaries.
//
// Once a day the app asks GitHub's API for the latest release (GET /repos/leock9/gonotch/releases/latest,
// no credentials); the answer is kept in $XDG_STATE_HOME/gonotch/update.json, so restarts do not ask
// again and each new version is announced once. Only release builds (vX.Y.Z) check: a build from a
// source tree is newer or older than any release by its own measure.
package update

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/leock9/gonotch/internal/config"
)

const (
	Repo  = "leock9/gonotch"
	Asset = "gonotch-linux-amd64.tar.gz"
	// Every is how often the running app asks
	Every = 24 * time.Hour
)

// Binaries are what a release replaces, in this order: the hook first, which Claude Code starts on
// every tool call, so it is never the one left behind.
var Binaries = []string{"gonotch-hook", "gonotch"}

type Release struct {
	Version string `json:"version"` // the tag, "v0.3.0"
	URL     string `json:"url"`     // its page, with the release notes
}

// Source is where releases come from: GitHub, or a test server.
type Source struct {
	API      string // https://api.github.com
	Download string // https://github.com/<repo>/releases/download
	Client   *http.Client
}

var GitHub = Source{
	API:      "https://api.github.com",
	Download: "https://github.com/" + Repo + "/releases/download",
	// A timeout for the whole download of a ~10 MB tarball, not for a quick API call
	Client: &http.Client{Timeout: 2 * time.Minute},
}

const userAgent = "gonotch-update (Linux)"

// Latest asks for the newest release; GitHub leaves drafts and pre-releases out of it.
func (s Source) Latest(ctx context.Context) (Release, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, s.API+"/repos/"+Repo+"/releases/latest", nil)
	if err != nil {
		return Release{}, err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	req.Header.Set("User-Agent", userAgent)
	resp, err := s.Client.Do(req)
	if err != nil {
		return Release{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return Release{}, fmt.Errorf("latest release: HTTP %d", resp.StatusCode)
	}
	var r struct {
		Tag string `json:"tag_name"`
		URL string `json:"html_url"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&r); err != nil {
		return Release{}, fmt.Errorf("latest release: %w", err)
	}
	if _, ok := parse(r.Tag); !ok {
		return Release{}, fmt.Errorf("latest release: unexpected tag %q", r.Tag)
	}
	return Release{Version: r.Tag, URL: r.URL}, nil
}

// parse reads "vX.Y.Z"; anything else (a dev build, "v0.2.0-3-gabc1234-dirty") is not a release.
func parse(v string) ([3]int, bool) {
	var n [3]int
	parts := strings.Split(strings.TrimPrefix(v, "v"), ".")
	if !strings.HasPrefix(v, "v") || len(parts) != 3 {
		return n, false
	}
	for i, p := range parts {
		x, err := strconv.Atoi(p)
		if err != nil || x < 0 || strconv.Itoa(x) != p {
			return n, false
		}
		n[i] = x
	}
	return n, true
}

// IsRelease reports whether v names a release, the only kind of build that looks for updates.
func IsRelease(v string) bool {
	_, ok := parse(v)
	return ok
}

// Newer reports whether release a comes after release b.
func Newer(a, b string) bool {
	x, okA := parse(a)
	y, okB := parse(b)
	if !okA || !okB {
		return false
	}
	for i := range x {
		if x[i] != y[i] {
			return x[i] > y[i]
		}
	}
	return false
}

// State is what the last check found.
type State struct {
	CheckedAt time.Time `json:"checked_at"`
	Latest    Release   `json:"latest"`
	// Announced is the last version the desktop was told about: each one is announced once
	Announced string `json:"announced,omitempty"`
}

func statePath() string { return filepath.Join(config.StateDir(), "update.json") }

func LoadState() State {
	var s State
	if raw, err := os.ReadFile(statePath()); err == nil {
		_ = json.Unmarshal(raw, &s)
	}
	return s
}

func SaveState(s State) error {
	raw, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(config.StateDir(), 0o700); err != nil {
		return err
	}
	return os.WriteFile(statePath(), raw, 0o600)
}

// Pending is the newer release the last check found for a build of version current, if any: what
// `gonotch version` mentions without asking the network.
func Pending(current string) (Release, bool) {
	s := LoadState()
	return s.Latest, Newer(s.Latest.Version, current)
}

// Check compares current with the latest release, asked of GitHub when ask is set and otherwise as
// last recorded, and records what it found. It returns the newer release, if there is one, and
// whether this is the first time that version is announced.
func (s Source) Check(ctx context.Context, current string, ask bool) (r Release, newer, announce bool, err error) {
	st := LoadState()
	if ask {
		if st.Latest, err = s.Latest(ctx); err != nil {
			return Release{}, false, false, err
		}
		st.CheckedAt = time.Now()
	}
	if newer = Newer(st.Latest.Version, current); newer {
		announce = st.Announced != st.Latest.Version
		st.Announced = st.Latest.Version
	}
	return st.Latest, newer, announce, SaveState(st)
}

// Watch looks for releases newer than current while ctx lasts: a minute after start when the last
// check is a day old, then once a day. Hourly ticks compare wall-clock time, which, unlike a 24-hour
// timer, counts the hours a laptop spends asleep. found gets each newer release, with announce set
// the first time that version is seen.
func (s Source) Watch(ctx context.Context, current string, enabled func() bool, found func(r Release, announce bool)) {
	if !IsRelease(current) {
		return
	}
	check := func(ask bool) {
		r, newer, announce, err := s.Check(ctx, current, ask)
		if err != nil {
			slog.Warn("update check", "err", err)
			return
		}
		if newer {
			found(r, announce)
		}
	}
	due := func() bool { return time.Now().Round(0).Sub(LoadState().CheckedAt) >= Every }
	// What an earlier check found needs no network to be shown again
	if enabled() && !due() {
		check(false)
	}
	first := time.NewTimer(time.Minute)
	defer first.Stop()
	tick := time.NewTicker(time.Hour)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-first.C:
		case <-tick.C:
		}
		if enabled() && due() {
			check(true)
		}
	}
}

// ErrNotWritable: the binaries live where this user cannot write, as a .deb puts them in /usr/bin.
var ErrNotWritable = errors.New("the install directory is not writable")

// Writable reports whether dir takes a new binary from this user.
func Writable(dir string) bool {
	f, err := os.CreateTemp(dir, ".gonotch-write-test-*")
	if err != nil {
		return false
	}
	f.Close()
	os.Remove(f.Name())
	return true
}

// Install downloads a release's tarball and SHA256SUMS, checks one against the other, and replaces
// the binaries in dir. Each goes through a temporary file in dir and a rename, so a failure halfway
// never leaves a broken binary behind.
func (s Source) Install(ctx context.Context, version, dir string) error {
	if !Writable(dir) {
		return ErrNotWritable
	}
	base := s.Download + "/" + version + "/"
	sums, err := s.get(ctx, base+"SHA256SUMS", 64<<10)
	if err != nil {
		return err
	}
	tarball, err := s.get(ctx, base+Asset, 200<<20)
	if err != nil {
		return err
	}
	if err := verify(tarball, sums, Asset); err != nil {
		return err
	}
	files, err := extract(tarball)
	if err != nil {
		return err
	}
	for _, name := range Binaries {
		if err := replace(filepath.Join(dir, name), files[name]); err != nil {
			return err
		}
	}
	return nil
}

func (s Source) get(ctx context.Context, url string, limit int64) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", userAgent)
	resp, err := s.Client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("%s: HTTP %d", url, resp.StatusCode)
	}
	return io.ReadAll(io.LimitReader(resp.Body, limit))
}

func verify(data, sums []byte, name string) error {
	for _, line := range strings.Split(string(sums), "\n") {
		fields := strings.Fields(line)
		if len(fields) == 2 && strings.TrimPrefix(fields[1], "*") == name {
			got := sha256.Sum256(data)
			if hex.EncodeToString(got[:]) != strings.ToLower(fields[0]) {
				return fmt.Errorf("%s does not match its SHA256SUMS entry", name)
			}
			return nil
		}
	}
	return fmt.Errorf("SHA256SUMS lists no %s", name)
}

// extract takes the binaries out of the tarball (gonotch-linux-amd64/<name>).
func extract(tarball []byte) (map[string][]byte, error) {
	gz, err := gzip.NewReader(bytes.NewReader(tarball))
	if err != nil {
		return nil, err
	}
	tr := tar.NewReader(gz)
	files := map[string][]byte{}
	for {
		h, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, err
		}
		name := filepath.Base(h.Name)
		if h.Typeflag != tar.TypeReg || !slices.Contains(Binaries, name) {
			continue
		}
		if files[name], err = io.ReadAll(io.LimitReader(tr, 100<<20)); err != nil {
			return nil, err
		}
	}
	for _, name := range Binaries {
		if len(files[name]) == 0 {
			return nil, fmt.Errorf("the release has no %s", name)
		}
	}
	return files, nil
}

func replace(path string, data []byte) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".new-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name()) // a no-op once renamed
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Chmod(0o755); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}

// Notify shows a desktop notification through notify-send, which Ubuntu's desktop installs.
func Notify(title, body string) error {
	return exec.Command("notify-send", "--app-name=gonotch", "--icon=software-update-available", title, body).Run()
}
