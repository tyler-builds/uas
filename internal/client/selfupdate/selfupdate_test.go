package selfupdate

import (
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/tyler-builds/uas/internal/manifest"
)

const apiPath = "/repos/tyler-builds/uas/releases/latest"

// fakeGitHub serves a latest release the way GitHub does: the API's JSON (trimmed, but with fields uas
// ignores), and assets at /<owner>/<repo>/releases/download/<tag>/<name> that redirect to where the file is.
type fakeGitHub struct {
	*httptest.Server
	mu       sync.Mutex
	tag      string
	assets   map[string][]byte // by name
	sizes    map[string]int64  // the size the API reports, when it isn't the asset's
	status   int               // when not 0, the API answers with it
	header   http.Header       // and these headers
	requests []*http.Request
}

func newFakeGitHub(t *testing.T) *fakeGitHub {
	g := &fakeGitHub{assets: map[string][]byte{}, sizes: map[string]int64{}}
	g.Server = httptest.NewServer(http.HandlerFunc(g.serve))
	t.Cleanup(g.Close)
	return g
}

func (g *fakeGitHub) serve(w http.ResponseWriter, r *http.Request) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.requests = append(g.requests, r)
	switch {
	case r.URL.Path == apiPath:
		for k, v := range g.header {
			w.Header()[k] = v
		}
		if g.status != 0 {
			w.WriteHeader(g.status)
			fmt.Fprint(w, `{"message":"API rate limit exceeded for 192.0.2.1."}`)
			return
		}
		var assets []string
		for name, b := range g.assets {
			size := int64(len(b))
			if s, ok := g.sizes[name]; ok {
				size = s
			}
			assets = append(assets, fmt.Sprintf(`{"id":1,"name":%q,"content_type":"application/octet-stream",`+
				`"state":"uploaded","size":%d,"digest":null,"browser_download_url":"%s/tyler-builds/uas/releases/download/%s/%s"}`,
				name, size, g.URL, g.tag, name))
		}
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		fmt.Fprintf(w, `{"url":"x","html_url":"x","id":7,"tag_name":%q,"target_commitish":"main","name":"uas",`+
			`"draft":false,"prerelease":false,"created_at":"2026-11-11T00:00:00Z","assets":[%s],"body":"notes"}`,
			g.tag, strings.Join(assets, ","))
	case strings.HasPrefix(r.URL.Path, "/tyler-builds/uas/releases/download/"+g.tag+"/"):
		http.Redirect(w, r, "/objects/"+filepath.Base(r.URL.Path), http.StatusFound)
	case strings.HasPrefix(r.URL.Path, "/objects/"):
		if b, ok := g.assets[filepath.Base(r.URL.Path)]; ok {
			w.Write(b)
			return
		}
		http.NotFound(w, r)
	default:
		http.NotFound(w, r)
	}
}

// release publishes uas ver, tagged tag, with prog as its x86_64 program, signed with key.
func (g *fakeGitHub) release(t *testing.T, key ed25519.PrivateKey, tag, ver string, prog []byte) {
	sum := sha256.Sum256(prog)
	rel := manifest.Release{Version: ver, Released: time.Now(), Programs: []manifest.ReleaseFile{{Arch: "x86_64",
		Name: manifest.AssetName("x86_64"), SHA256: hex.EncodeToString(sum[:]), Size: int64(len(prog))}}}
	signed, err := manifest.SignRelease(rel, key)
	if err != nil {
		t.Fatal(err)
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	g.tag = tag
	g.assets = map[string][]byte{manifest.AssetName("x86_64"): prog, manifest.ReleaseAsset: signed,
		manifest.ChecksumsAsset: []byte("checksums")}
	g.sizes = map[string]int64{}
}

func (g *fakeGitHub) updater(pub ed25519.PublicKey, current, dir string) *Updater {
	return &Updater{LatestURL: g.URL + apiPath, TrustedKeys: []ed25519.PublicKey{pub}, Client: g.Client(),
		Arch: "x86_64", Current: current, Dir: dir}
}

// TestUpdate updates an install that has uas and uas-gui as two programs, as uas 0.5.0 and older installed
// them: uas becomes the release's program, and uas-gui a link to it.
func TestUpdate(t *testing.T) {
	pub, priv, _ := ed25519.GenerateKey(nil)
	g := newFakeGitHub(t)
	g.release(t, priv, "v0.6.0", "0.6.0", []byte("new uas"))
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "uas"), []byte("old cli"), 0o755)
	os.WriteFile(filepath.Join(dir, "uas-gui"), []byte("old gui"), 0o755)
	u := g.updater(pub, "0.5.0", dir)
	a, err := u.Check(context.Background())
	if err != nil || a == nil {
		t.Fatalf("Check = %v, %v", a, err)
	}
	if a.Release.Version != "0.6.0" || a.File.Name != "uas-linux-x86_64" ||
		a.URL != g.URL+"/tyler-builds/uas/releases/download/v0.6.0/uas-linux-x86_64" {
		t.Errorf("Check = %+v", a)
	}
	for _, r := range g.requests {
		if r.Header.Get("Authorization") != "" || !strings.HasPrefix(r.Header.Get("User-Agent"), "uas/") {
			t.Errorf("%s: Authorization %q, User-Agent %q", r.URL, r.Header.Get("Authorization"), r.Header.Get("User-Agent"))
		}
	}
	if accept := g.requests[0].Header.Get("Accept"); accept != "application/vnd.github+json" {
		t.Errorf("API request Accept = %q", accept)
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
	if entries, _ := os.ReadDir(dir); len(entries) != 2 {
		t.Errorf("leftovers in the folder: %v", entries)
	}

	// Up to date or newer: one request, to the API, and nothing downloaded.
	for _, cur := range []string{"0.6.0", "0.7.0"} {
		g.requests = nil
		u.Current = cur
		if a, err := u.Check(context.Background()); a != nil || err != nil {
			t.Errorf("%s: %v, %v", cur, a, err)
		}
		if len(g.requests) != 1 {
			t.Errorf("%s: %d requests, want 1", cur, len(g.requests))
		}
	}
	u.Current = "dev"
	if _, err := u.Check(context.Background()); !errors.Is(err, ErrDevBuild) {
		t.Errorf("dev: %v", err)
	}
}

// TestCLIOnly updates an install without uas-gui, which doesn't get one.
func TestCLIOnly(t *testing.T) {
	pub, priv, _ := ed25519.GenerateKey(nil)
	g := newFakeGitHub(t)
	g.release(t, priv, "v0.6.0", "0.6.0", []byte("new uas"))
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "uas"), []byte("old"), 0o755)
	u := g.updater(pub, "0.5.0", dir)
	a, err := u.Check(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	var steps []string
	if err := u.Install(context.Background(), a, func(s string) { steps = append(steps, s) }); err != nil {
		t.Fatal(err)
	}
	if entries, _ := os.ReadDir(dir); len(entries) != 1 {
		t.Errorf("only uas should be in the folder, found %v", entries)
	}
	if strings.Join(steps, "|") != "Downloading uas 0.6.0|Checksum verified|Updated to uas 0.6.0" {
		t.Errorf("progress: %q", steps)
	}
}

// TestRefused covers releases that must not be installed, and that leave the old program in place.
func TestRefused(t *testing.T) {
	pub, priv, _ := ed25519.GenerateKey(nil)
	_, otherKey, _ := ed25519.GenerateKey(nil)
	for _, tc := range []struct {
		name  string
		setup func(g *fakeGitHub)
		want  string
	}{
		{"signed by another key", func(g *fakeGitHub) { g.release(t, otherKey, "v0.6.0", "0.6.0", []byte("x")) },
			"unknown key"},
		{"tag newer than its signed version", func(g *fakeGitHub) { g.release(t, priv, "v0.7.0", "0.6.0", []byte("x")) },
			"signed as uas 0.6.0"},
		{"no signed description", func(g *fakeGitHub) {
			g.release(t, priv, "v0.6.0", "0.6.0", []byte("x"))
			delete(g.assets, manifest.ReleaseAsset)
		}, "has no uas-release.json"},
		{"no program", func(g *fakeGitHub) {
			g.release(t, priv, "v0.6.0", "0.6.0", []byte("x"))
			delete(g.assets, "uas-linux-x86_64")
		}, "missing"},
		{"program of another size", func(g *fakeGitHub) {
			g.release(t, priv, "v0.6.0", "0.6.0", []byte("x"))
			g.sizes["uas-linux-x86_64"] = 99
		}, "isn't the signed one"},
		{"tag that isn't a version", func(g *fakeGitHub) { g.release(t, priv, "nightly", "0.6.0", []byte("x")) },
			"not a version"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			g := newFakeGitHub(t)
			tc.setup(g)
			_, err := g.updater(pub, "0.5.0", t.TempDir()).Check(context.Background())
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Errorf("Check = %v, want an error with %q", err, tc.want)
			}
		})
	}
}

func TestTamperedProgram(t *testing.T) {
	pub, priv, _ := ed25519.GenerateKey(nil)
	g := newFakeGitHub(t)
	g.release(t, priv, "v0.6.0", "0.6.0", []byte("program"))
	g.assets["uas-linux-x86_64"] = []byte("swapped") // same size, other content
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "uas"), []byte("old"), 0o755)
	u := g.updater(pub, "0.5.0", dir)
	a, err := u.Check(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if err := u.Install(context.Background(), a, nil); err == nil || !strings.Contains(err.Error(), "signed checksum") {
		t.Errorf("Install = %v; a program that doesn't match its checksum should be refused", err)
	}
	if b, _ := os.ReadFile(filepath.Join(dir, "uas")); string(b) != "old" {
		t.Errorf("uas = %q after a refused update", b)
	}
	if entries, _ := os.ReadDir(dir); len(entries) != 1 {
		t.Errorf("leftovers in the folder: %v", entries)
	}
}

func TestAPIErrors(t *testing.T) {
	pub, _, _ := ed25519.GenerateKey(nil)
	now := time.Unix(1794410000, 0)
	for _, tc := range []struct {
		name    string
		status  int
		header  map[string]string
		want    string
		limited bool
	}{
		{"primary rate limit", http.StatusForbidden, map[string]string{"X-RateLimit-Remaining": "0",
			"X-RateLimit-Reset": "1794410700"}, "GitHub is limiting update checks from this network; try again in 12 minutes", true},
		{"primary rate limit as 429", http.StatusTooManyRequests, map[string]string{"X-RateLimit-Remaining": "0",
			"X-RateLimit-Reset": "1794410010"}, "try again in a minute", true},
		{"secondary rate limit", http.StatusForbidden, map[string]string{"Retry-After": "120"}, "try again in 2 minutes", true},
		{"429 without headers", http.StatusTooManyRequests, nil, "try again later", true},
		{"forbidden", http.StatusForbidden, nil, "GitHub answered 403 Forbidden", false},
		{"no release", http.StatusNotFound, nil, "no uas release is published yet", false},
		{"server error", http.StatusBadGateway, nil, "GitHub answered 502 Bad Gateway", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			g := newFakeGitHub(t)
			g.status, g.header = tc.status, http.Header{}
			for k, v := range tc.header {
				g.header.Set(k, v)
			}
			u := g.updater(pub, "0.5.0", t.TempDir())
			u.Now = func() time.Time { return now }
			_, err := u.Check(context.Background())
			var rl *RateLimitError
			if err == nil || !strings.Contains(err.Error(), tc.want) || errors.As(err, &rl) != tc.limited {
				t.Errorf("Check = %v, want %q (rate limit %v)", err, tc.want, tc.limited)
			}
		})
	}
}

func TestOffline(t *testing.T) {
	pub, _, _ := ed25519.GenerateKey(nil)
	g := newFakeGitHub(t)
	u := g.updater(pub, "0.5.0", t.TempDir())
	g.Close()
	if _, err := u.Check(context.Background()); err == nil || !strings.HasPrefix(err.Error(), "can't reach GitHub") {
		t.Errorf("Check = %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := u.Check(ctx); !errors.Is(err, context.Canceled) {
		t.Errorf("cancelled Check = %v", err)
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
