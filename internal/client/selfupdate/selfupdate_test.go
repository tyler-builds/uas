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

func serve(t *testing.T, key ed25519.PrivateKey, ver string, arc []byte) *httptest.Server {
	sum := sha256.Sum256(arc)
	rel := manifest.Release{Version: ver, Released: time.Now(), Files: []manifest.ReleaseFile{{
		Arch: "x86_64", Path: manifest.ReleasePath(ver, "x86_64"), SHA256: hex.EncodeToString(sum[:]),
		Size: int64(len(arc))}}}
	signed, err := manifest.SignRelease(rel, key)
	if err != nil {
		t.Fatal(err)
	}
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/" + manifest.ReleaseName:
			w.Write(signed)
		case "/" + manifest.ReleasePath(ver, "x86_64"):
			w.Write(arc)
		default:
			http.NotFound(w, r)
		}
	}))
}

func TestUpdate(t *testing.T) {
	pub, priv, _ := ed25519.GenerateKey(nil)
	arc := archive(t, map[string]string{"uas-0.3.0-linux-x86_64/uas": "new cli",
		"uas-0.3.0-linux-x86_64/uas-gui": "new gui", "uas-0.3.0-linux-x86_64/install.sh": "#!/bin/sh"})
	srv := serve(t, priv, "0.3.0", arc)
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
	if err := u.Install(context.Background(), a, nil); err != nil {
		t.Fatal(err)
	}
	for name, want := range map[string]string{"uas": "new cli", "uas-gui": "new gui"} {
		if b, _ := os.ReadFile(filepath.Join(dir, name)); string(b) != want {
			t.Errorf("%s = %q", name, b)
		}
	}
	if _, err := os.Stat(filepath.Join(dir, "install.sh")); err == nil {
		t.Error("only the programs should be installed")
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

func TestTamperedArchive(t *testing.T) {
	pub, priv, _ := ed25519.GenerateKey(nil)
	good := archive(t, map[string]string{"uas-0.3.0-linux-x86_64/uas": "cli", "uas-0.3.0-linux-x86_64/uas-gui": "gui"})
	srv := serve(t, priv, "0.3.0", good)
	defer srv.Close()
	u := &Updater{BaseURL: srv.URL, TrustedKeys: []ed25519.PublicKey{pub}, Client: srv.Client(), Arch: "x86_64",
		Current: "0.2.0", Dir: t.TempDir()}
	a, err := u.Check(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	a.File.SHA256 = hex.EncodeToString(make([]byte, 32)) // as if the archive were swapped
	if err := u.Install(context.Background(), a, nil); err == nil {
		t.Error("an archive that doesn't match its checksum should be refused")
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
