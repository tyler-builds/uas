package selfupdate

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/tyler-builds/uas/internal/manifest"
)

func archive(t *testing.T, files map[string]string) []byte {
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	for name, body := range files {
		tw.WriteHeader(&tar.Header{Name: name, Mode: 0o755, Size: int64(len(body)), Typeflag: tar.TypeReg})
		tw.Write([]byte(body))
	}
	tw.Close()
	gz.Close()
	return buf.Bytes()
}

// serve publishes a release of ver: the program, when prog isn't nil, and the archive, when arc isn't.
func serve(t *testing.T, key ed25519.PrivateKey, ver string, prog, arc []byte) *httptest.Server {
	file := func(path string, b []byte) manifest.ReleaseFile {
		sum := sha256.Sum256(b)
		return manifest.ReleaseFile{Arch: "x86_64", Path: path, SHA256: hex.EncodeToString(sum[:]), Size: int64(len(b))}
	}
	rel := manifest.Release{Version: ver, Released: time.Now()}
	objects := map[string][]byte{}
	if prog != nil {
		rel.Programs = append(rel.Programs, file(manifest.ProgramPath(ver, "x86_64"), prog))
		objects["/"+manifest.ProgramPath(ver, "x86_64")] = prog
	}
	if arc != nil {
		rel.Files = append(rel.Files, file(manifest.ReleasePath(ver, "x86_64"), arc))
		objects["/"+manifest.ReleasePath(ver, "x86_64")] = arc
	}
	signed, err := manifest.SignRelease(rel, key)
	if err != nil {
		t.Fatal(err)
	}
	objects["/"+manifest.ReleaseName] = signed
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if b, ok := objects[r.URL.Path]; ok {
			w.Write(b)
			return
		}
		http.NotFound(w, r)
	}))
}

// TestUpdate updates an install that has uas and uas-gui as two programs, as uas 0.5.0 and older installed
// them: uas becomes the release's program, and uas-gui a link to it.
func TestUpdate(t *testing.T) {
	pub, priv, _ := ed25519.GenerateKey(nil)
	arc := archive(t, map[string]string{"uas-0.3.0-linux-x86_64/uas": "archived", "uas-0.3.0-linux-x86_64/uas-gui": "archived"})
	srv := serve(t, priv, "0.3.0", []byte("new uas"), arc)
	defer srv.Close()
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "uas"), []byte("old cli"), 0o755)
	os.WriteFile(filepath.Join(dir, "uas-gui"), []byte("old gui"), 0o755)
	u := &Updater{BaseURL: srv.URL, TrustedKeys: []ed25519.PublicKey{pub}, Client: srv.Client(), Arch: "x86_64",
		Current: "0.2.9", Dir: dir}
	a, err := u.Check(context.Background())
	if err != nil || a == nil {
		t.Fatalf("Check = %v, %v", a, err)
	}
	if a.File.Archive() {
		t.Error("the program should be preferred to the archive")
	}
	if err := u.Install(context.Background(), a, nil); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(filepath.Join(dir, "uas")); string(b) != "new uas" {
		t.Errorf("uas = %q", b)
	}
	if l, err := os.Readlink(filepath.Join(dir, "uas-gui")); err != nil || l != "uas" {
		t.Errorf("uas-gui links to %q, %v; want uas", l, err)
	}
	if fi, _ := os.Stat(filepath.Join(dir, "uas")); fi.Mode().Perm() != 0o755 {
		t.Errorf("uas mode %v", fi.Mode())
	}

	// Up to date, a build from source, and an untrusted key.
	u.Current = "0.3.0"
	if a, err := u.Check(context.Background()); a != nil || err != nil {
		t.Errorf("same version: %v, %v", a, err)
	}
	u.Current = "dev"
	if _, err := u.Check(context.Background()); !errors.Is(err, ErrDevBuild) {
		t.Errorf("dev: %v", err)
	}
	other, _, _ := ed25519.GenerateKey(nil)
	u.Current, u.TrustedKeys = "0.1.0", []ed25519.PublicKey{other}
	if _, err := u.Check(context.Background()); err == nil {
		t.Error("a release signed by another key should be refused")
	}
}

// TestUpdateFromArchive updates from a release with only an archive; a CLI-only install gets no uas-gui.
func TestUpdateFromArchive(t *testing.T) {
	pub, priv, _ := ed25519.GenerateKey(nil)
	arc := archive(t, map[string]string{"uas-0.3.0-linux-x86_64/uas": "new uas",
		"uas-0.3.0-linux-x86_64/uas-gui": "new uas", "uas-0.3.0-linux-x86_64/LICENSE": "license"})
	srv := serve(t, priv, "0.3.0", nil, arc)
	defer srv.Close()
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "uas"), []byte("old cli"), 0o755)
	u := &Updater{BaseURL: srv.URL, TrustedKeys: []ed25519.PublicKey{pub}, Client: srv.Client(), Arch: "x86_64",
		Current: "0.2.9", Dir: dir}
	a, err := u.Check(context.Background())
	if err != nil || a == nil {
		t.Fatalf("Check = %v, %v", a, err)
	}
	if err := u.Install(context.Background(), a, nil); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(filepath.Join(dir, "uas")); string(b) != "new uas" {
		t.Errorf("uas = %q", b)
	}
	entries, _ := os.ReadDir(dir)
	if len(entries) != 1 {
		t.Errorf("only uas should be in the folder, found %v", entries)
	}
}

func TestTamperedProgram(t *testing.T) {
	pub, priv, _ := ed25519.GenerateKey(nil)
	srv := serve(t, priv, "0.3.0", []byte("program"), nil)
	defer srv.Close()
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "uas"), []byte("old"), 0o755)
	u := &Updater{BaseURL: srv.URL, TrustedKeys: []ed25519.PublicKey{pub}, Client: srv.Client(), Arch: "x86_64",
		Current: "0.2.0", Dir: dir}
	a, err := u.Check(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	a.File.SHA256 = hex.EncodeToString(make([]byte, 32)) // as if the program were swapped
	if err := u.Install(context.Background(), a, nil); err == nil {
		t.Error("a program that doesn't match its checksum should be refused")
	}
	if b, _ := os.ReadFile(filepath.Join(dir, "uas")); string(b) != "old" {
		t.Errorf("uas = %q after a refused update", b)
	}
}

func TestVersion(t *testing.T) {
	v := func(s string) manifest.Version {
		x, err := manifest.ParseVersion(s)
		if err != nil {
			t.Fatal(err)
		}
		return x
	}
	if !v("0.10.0").Newer(v("0.9.9")) || v("1.0.0").Newer(v("1.0.0")) || !v("v1.0.1").Newer(v("1.0.0")) {
		t.Error("version order is wrong")
	}
	for _, bad := range []string{"1.0", "1.0.0-rc1", "01.0.0", "dev"} {
		if _, err := manifest.ParseVersion(bad); err == nil {
			t.Errorf("%q should not parse", bad)
		}
	}
}
