package github

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/tyler-builds/uas/internal/catalog"
	"github.com/tyler-builds/uas/internal/client/backend"
	"github.com/tyler-builds/uas/internal/client/state"
)

// files serves fixed bodies by URL, standing in for github.com.
type files map[string][]byte

func (f files) RoundTrip(r *http.Request) (*http.Response, error) {
	body, ok := f[r.URL.String()]
	if !ok {
		return &http.Response{StatusCode: 404, Status: "404 Not Found", Body: io.NopCloser(strings.NewReader("")),
			Request: r}, nil
	}
	return &http.Response{StatusCode: 200, Status: "200 OK", Body: io.NopCloser(bytes.NewReader(body)),
		ContentLength: int64(len(body)), Request: r}, nil
}

func sum(b []byte) string {
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}

func listing(version string, body []byte) catalog.Listing {
	url := "https://github.com/example/notes/releases/download/v" + version + "/Notes.AppImage"
	return catalog.Listing{Source: catalog.SourceGitHub, Ref: "example/notes", Name: "notes", Summary: "Take notes",
		Version: version, Assets: []catalog.Asset{{Kind: catalog.AssetAppImage, Name: "Notes.AppImage", URL: url,
			Size: int64(len(body)), SHA256: sum(body)}}}
}

func newBackend(t *testing.T, srv files, latest catalog.Listing) *Backend {
	dir := t.TempDir()
	return &Backend{
		State: state.Store{Path: filepath.Join(dir, "state.json")},
		Dirs: Dirs{AppImages: filepath.Join(dir, "appimages"), Icons: filepath.Join(dir, "icons"),
			Applications: filepath.Join(dir, "applications")},
		Client: &http.Client{Transport: srv},
		Latest: func(context.Context, string) (catalog.Listing, error) { return latest, nil },
	}
}

func TestInstallUpdateRemove(t *testing.T) {
	v1, v2 := []byte("not really an appimage v1"), []byte("not really an appimage v2")
	l1, l2 := listing("1.0", v1), listing("2.0", v2)
	srv := files{l1.Assets[0].URL: v1, l2.Assets[0].URL: v2}
	b := newBackend(t, srv, l1)
	ctx := context.Background()

	if !b.Supports(l1) {
		t.Fatal("Supports(l1) = false")
	}
	var msgs []string
	if err := b.Install(ctx, l1, func(p backend.Progress) { msgs = append(msgs, p.Message) }); err != nil {
		t.Fatal(err)
	}
	app := filepath.Join(b.Dirs.AppImages, "example__notes.AppImage")
	if st, err := os.Stat(app); err != nil || st.Mode().Perm() != 0o755 {
		t.Fatalf("AppImage: %v, %v", st, err)
	}
	desktop, err := os.ReadFile(filepath.Join(b.Dirs.Applications, "uas-example__notes.desktop"))
	if err != nil {
		t.Fatal(err)
	}
	// The file is not a real AppImage, so the launcher falls back to catalog details.
	if !strings.Contains(string(desktop), "Name=notes\n") || !strings.Contains(string(desktop), `Exec="`+app+`"`) {
		t.Errorf("desktop file:\n%s", desktop)
	}
	if !strings.Contains(strings.Join(msgs, "\n"), "Checksum verified") {
		t.Errorf("progress = %q", msgs)
	}

	apps, _ := b.Installed(ctx)
	if len(apps) != 1 || apps[0].Ref != "example/notes" || apps[0].Version != "1.0" || !apps[0].Matches(l1) {
		t.Fatalf("installed = %+v", apps)
	}

	// Same release: nothing to do. New release: reinstalled.
	if err := b.Update(ctx, apps[0], nil); err != nil {
		t.Fatal(err)
	}
	b.Latest = func(context.Context, string) (catalog.Listing, error) { return l2, nil }
	if err := b.Update(ctx, apps[0], nil); err != nil {
		t.Fatal(err)
	}
	if got, _ := os.ReadFile(app); !bytes.Equal(got, v2) {
		t.Error("update did not replace the AppImage")
	}
	apps, _ = b.Installed(ctx)
	if apps[0].Version != "2.0" {
		t.Errorf("version after update = %q", apps[0].Version)
	}

	if err := b.Remove(ctx, apps[0], nil); err != nil {
		t.Fatal(err)
	}
	for _, d := range []string{b.Dirs.AppImages, b.Dirs.Applications} {
		if entries, _ := os.ReadDir(d); len(entries) != 0 {
			t.Errorf("%s not empty after remove: %v", d, entries)
		}
	}
	if apps, _ := b.Installed(ctx); len(apps) != 0 {
		t.Errorf("still installed: %+v", apps)
	}
}

func TestInstallRejectsBadChecksum(t *testing.T) {
	l := listing("1.0", []byte("expected"))
	b := newBackend(t, files{l.Assets[0].URL: []byte("tampered")}, l)
	err := b.Install(context.Background(), l, nil)
	if err == nil || !strings.Contains(err.Error(), "bytes") && !strings.Contains(err.Error(), "checksum") {
		t.Fatalf("err = %v", err)
	}
	if entries, _ := os.ReadDir(b.Dirs.AppImages); len(entries) != 0 {
		t.Errorf("left files behind: %v", entries)
	}
}

func TestSupports(t *testing.T) {
	b := newBackend(t, files{}, catalog.Listing{})
	good := listing("1.0", []byte("x"))
	noSum := listing("1.0", []byte("x"))
	noSum.Assets[0].SHA256 = ""
	elsewhere := listing("1.0", []byte("x"))
	elsewhere.Assets[0].URL = "https://evil.example/Notes.AppImage"
	otherRepo := listing("1.0", []byte("x"))
	otherRepo.Ref = "someone/else"
	rpmOnly := listing("1.0", []byte("x"))
	rpmOnly.Assets[0].Kind = catalog.AssetRPM
	if !b.Supports(good) || b.Supports(noSum) || b.Supports(elsewhere) || b.Supports(otherRepo) || b.Supports(rpmOnly) {
		t.Error("Supports gave a wrong answer")
	}
}

func TestOwns(t *testing.T) {
	b := newBackend(t, files{}, catalog.Listing{})
	if !b.owns(filepath.Join(b.Dirs.AppImages, "x.AppImage")) {
		t.Error("should own a file in the AppImage dir")
	}
	for _, p := range []string{"/etc/passwd", filepath.Join(b.Dirs.AppImages, "..", "state.json"),
		filepath.Join(b.Dirs.Applications, "sub", "x.desktop")} {
		if b.owns(p) {
			t.Errorf("owns(%q) = true", p)
		}
	}
}
