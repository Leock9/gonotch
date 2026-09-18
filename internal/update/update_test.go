package update

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

func TestVersions(t *testing.T) {
	for _, c := range []struct {
		a, b  string
		newer bool
	}{
		{"v0.3.0", "v0.2.0", true},
		{"v0.10.0", "v0.9.9", true},
		{"v1.0.0", "v0.99.99", true},
		{"v0.2.0", "v0.2.0", false},
		{"v0.2.0", "v0.3.0", false},
		{"v0.3.0", "v0.2.0-3-gabc1234-dirty", false}, // a dev build never looks for updates
		{"v0.3.0", "dev", false},
		{"v0.3", "v0.2.0", false},
		{"v0.03.0", "v0.2.0", false},
	} {
		if got := Newer(c.a, c.b); got != c.newer {
			t.Errorf("Newer(%q, %q) = %v", c.a, c.b, got)
		}
	}
	if !IsRelease("v0.2.0") || IsRelease("5d47da3-dirty") || IsRelease("0.2.0") {
		t.Error("IsRelease")
	}
}

// fakeGitHub serves a latest release and its assets, as GitHub's API and release downloads do.
func fakeGitHub(t *testing.T, tag string, tarball, sums []byte) Source {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("GET /repos/"+Repo+"/releases/latest", func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprintf(w, `{"tag_name": %q, "html_url": "https://github.com/%s/releases/tag/%s", "draft": false}`, tag, Repo, tag)
	})
	mux.HandleFunc("GET /dl/"+tag+"/"+Asset, func(w http.ResponseWriter, _ *http.Request) { w.Write(tarball) })
	mux.HandleFunc("GET /dl/"+tag+"/SHA256SUMS", func(w http.ResponseWriter, _ *http.Request) { w.Write(sums) })
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return Source{API: srv.URL, Download: srv.URL + "/dl", Client: srv.Client()}
}

func release(t *testing.T, files map[string]string) (tarball, sums []byte) {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	for name, body := range files {
		if err := tw.WriteHeader(&tar.Header{Name: "gonotch-linux-amd64/" + name, Mode: 0o755, Size: int64(len(body)), Typeflag: tar.TypeReg}); err != nil {
			t.Fatal(err)
		}
		tw.Write([]byte(body))
	}
	tw.Close()
	gz.Close()
	sum := sha256.Sum256(buf.Bytes())
	return buf.Bytes(), []byte(hex.EncodeToString(sum[:]) + "  " + Asset + "\nabc  gonotch_0.3.0_amd64.deb\n")
}

func TestCheckAnnouncesEachVersionOnce(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	src := fakeGitHub(t, "v0.3.0", nil, nil)
	ctx := context.Background()
	r, newer, announce, err := src.Check(ctx, "v0.2.0", true)
	if err != nil || !newer || !announce || r.Version != "v0.3.0" || r.URL == "" {
		t.Fatalf("first check: %+v newer=%v announce=%v err=%v", r, newer, announce, err)
	}
	if _, newer, announce, _ := src.Check(ctx, "v0.2.0", false); !newer || announce {
		t.Fatalf("from the record: newer=%v announce=%v, want newer and already announced", newer, announce)
	}
	if _, newer, _, _ := src.Check(ctx, "v0.3.0", false); newer {
		t.Fatal("the latest version is not newer than itself")
	}
	if p, ok := Pending("v0.2.0"); !ok || p.Version != "v0.3.0" {
		t.Fatalf("Pending = %+v, %v", p, ok)
	}
}

func TestInstallReplacesBothBinaries(t *testing.T) {
	tarball, sums := release(t, map[string]string{"gonotch": "new app", "gonotch-hook": "new hook", "README.md": "readme"})
	src := fakeGitHub(t, "v0.3.0", tarball, sums)
	dir := t.TempDir()
	for _, b := range Binaries {
		os.WriteFile(filepath.Join(dir, b), []byte("old"), 0o755)
	}
	if err := src.Install(context.Background(), "v0.3.0", dir); err != nil {
		t.Fatal(err)
	}
	for name, want := range map[string]string{"gonotch": "new app", "gonotch-hook": "new hook"} {
		raw, _ := os.ReadFile(filepath.Join(dir, name))
		st, _ := os.Stat(filepath.Join(dir, name))
		if string(raw) != want || st.Mode().Perm() != 0o755 {
			t.Errorf("%s = %q, mode %v", name, raw, st.Mode())
		}
	}
	if left, _ := filepath.Glob(filepath.Join(dir, ".*")); len(left) > 0 {
		t.Errorf("temporary files left behind: %v", left)
	}
	if _, err := os.Stat(filepath.Join(dir, "README.md")); err == nil {
		t.Error("only the binaries are installed")
	}
}

func TestInstallRefusesATamperedTarball(t *testing.T) {
	tarball, sums := release(t, map[string]string{"gonotch": "new app", "gonotch-hook": "new hook"})
	tarball = append(tarball, 0)
	src := fakeGitHub(t, "v0.3.0", tarball, sums)
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "gonotch"), []byte("old"), 0o755)
	if err := src.Install(context.Background(), "v0.3.0", dir); err == nil {
		t.Fatal("a tarball that does not match SHA256SUMS must not be installed")
	}
	if raw, _ := os.ReadFile(filepath.Join(dir, "gonotch")); string(raw) != "old" {
		t.Fatalf("the old binary was touched: %q", raw)
	}
}

func TestInstallNeedsAWritableDirectory(t *testing.T) {
	dir := t.TempDir()
	os.Chmod(dir, 0o555)
	defer os.Chmod(dir, 0o755)
	if Writable(dir) {
		t.Skip("running as root: every directory is writable")
	}
	src := fakeGitHub(t, "v0.3.0", nil, nil)
	if err := src.Install(context.Background(), "v0.3.0", dir); !errors.Is(err, ErrNotWritable) {
		t.Fatalf("err = %v, want ErrNotWritable", err)
	}
}
