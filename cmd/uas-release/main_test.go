package main

import (
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/tyler-builds/uas/internal/manifest"
)

func TestRelease(t *testing.T) {
	seed := make([]byte, ed25519.SeedSize)
	seed[0] = 7
	t.Setenv("UAS_SIGNING_KEY", base64.StdEncoding.EncodeToString(seed))
	pub := ed25519.NewKeyFromSeed(seed).Public().(ed25519.PublicKey)

	dist, out := t.TempDir(), t.TempDir()
	progs := map[string]string{"x86_64": "x86 program", "aarch64": "arm program"}
	var files []string
	for arch, body := range progs {
		p := filepath.Join(dist, "uas-0.6.0-linux-"+arch)
		os.WriteFile(p, []byte(body), 0o755)
		files = append(files, p)
	}
	if err := run("v0.6.0", out, files, time.Unix(1794410000, 0)); err != nil {
		t.Fatal(err)
	}

	signed, _ := os.ReadFile(filepath.Join(out, manifest.ReleaseAsset))
	rel, err := manifest.VerifyRelease(signed, []ed25519.PublicKey{pub})
	if err != nil || rel.Version != "0.6.0" || len(rel.Programs) != 2 {
		t.Fatalf("release = %+v, %v", rel, err)
	}
	sums, _ := os.ReadFile(filepath.Join(out, manifest.ChecksumsAsset))
	for arch, body := range progs {
		name := "uas-linux-" + arch
		if b, _ := os.ReadFile(filepath.Join(out, name)); string(b) != body {
			t.Errorf("%s = %q", name, b)
		}
		if fi, _ := os.Stat(filepath.Join(out, name)); fi == nil || fi.Mode().Perm() != 0o755 {
			t.Errorf("%s isn't executable", name)
		}
		sum := sha256.Sum256([]byte(body))
		f, _ := rel.File(arch)
		if f.SHA256 != hex.EncodeToString(sum[:]) || f.Size != int64(len(body)) || f.Name != name {
			t.Errorf("signed %+v for %s", f, arch)
		}
		if !strings.Contains(string(sums), f.SHA256+"  "+name+"\n") {
			t.Errorf("SHA256SUMS has no line for %s:\n%s", name, sums)
		}
	}

	for _, bad := range [][]string{
		{filepath.Join(dist, "uas-0.6.0-linux-x86_64"), filepath.Join(dist, "uas-0.6.0-linux-x86_64")},
		{filepath.Join(dist, "uas-0.5.0-linux-x86_64")},
		{filepath.Join(dist, "uas-0.6.0-linux-x86_64.tar.gz")},
		nil,
	} {
		if err := run("0.6.0", t.TempDir(), bad, time.Now()); err == nil {
			t.Errorf("run(%v) should fail", bad)
		}
	}
}
