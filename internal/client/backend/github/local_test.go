package github

import (
	"context"
	"crypto/sha1"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/tyler-builds/uas/internal/catalog"
	"github.com/tyler-builds/uas/internal/client/backend"
)

// buildAppImage makes an AppImage-shaped file: an ELF64 header with a .upd_info section holding update,
// followed by a squashfs image with a desktop entry named name. It needs mksquashfs.
func buildAppImage(t *testing.T, name, version, update string) []byte {
	t.Helper()
	mksquashfs, err := exec.LookPath("mksquashfs")
	if err != nil {
		t.Skip("mksquashfs not installed")
	}
	dir := t.TempDir()
	root := filepath.Join(dir, "root")
	os.MkdirAll(root, 0o755)
	desktop := fmt.Sprintf("[Desktop Entry]\nType=Application\nName=%s\nExec=app %%F\nIcon=app\n"+
		"X-AppImage-Version=%s\n", name, version)
	os.WriteFile(filepath.Join(root, "org.example.App.desktop"), []byte(desktop), 0o644)
	os.WriteFile(filepath.Join(root, "app.svg"), []byte(`<svg xmlns="http://www.w3.org/2000/svg"/>`), 0o644)
	img := filepath.Join(dir, "img.sqfs")
	if out, err := exec.Command(mksquashfs, root, img, "-quiet", "-noappend", "-comp", "gzip").CombinedOutput(); err != nil {
		t.Fatalf("mksquashfs: %v\n%s", err, out)
	}
	sq, _ := os.ReadFile(img)

	le := binary.LittleEndian
	strtab := []byte("\x00.upd_info\x00.shstrtab\x00")
	upd := make([]byte, 512)
	copy(upd, update)
	updOff := uint64(64)
	strOff := updOff + uint64(len(upd))
	shoff := strOff + uint64(len(strtab))
	buf := make([]byte, shoff+3*64)
	copy(buf, "\x7fELF")
	buf[4], buf[5], buf[6] = 2, 1, 1
	le.PutUint16(buf[0x10:], 2)  // ET_EXEC
	le.PutUint16(buf[0x12:], 62) // x86-64
	le.PutUint32(buf[0x14:], 1)
	le.PutUint64(buf[0x28:], shoff)
	le.PutUint16(buf[0x34:], 64)
	le.PutUint16(buf[0x36:], 56)
	le.PutUint16(buf[0x3a:], 64)
	le.PutUint16(buf[0x3c:], 3)
	le.PutUint16(buf[0x3e:], 2)
	copy(buf[updOff:], upd)
	copy(buf[strOff:], strtab)
	section := func(i int, name, typ uint32, off, size uint64) {
		h := buf[shoff+uint64(i)*64:]
		le.PutUint32(h[0:], name)
		le.PutUint32(h[4:], typ)
		le.PutUint64(h[0x18:], off)
		le.PutUint64(h[0x20:], size)
		le.PutUint64(h[0x30:], 1)
	}
	section(1, 1, 1, updOff, uint64(len(upd)))     // .upd_info, PROGBITS
	section(2, 11, 3, strOff, uint64(len(strtab))) // .shstrtab, STRTAB
	return append(buf, sq...)
}

func sha1sum(b []byte) string {
	h := sha1.Sum(b)
	return hex.EncodeToString(h[:])
}

func writeDownload(t *testing.T, body []byte) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "Downloads", "App-x86_64.AppImage")
	os.MkdirAll(filepath.Dir(p), 0o755)
	if err := os.WriteFile(p, body, 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

// release returns the GitHub API's JSON for a release of example/app holding body and its .zsync file.
func release(tag string, body []byte, digest string) []byte {
	base := "https://github.com/example/app/releases/download/" + tag + "/"
	type asset struct {
		Name   string `json:"name"`
		URL    string `json:"browser_download_url"`
		Size   int64  `json:"size"`
		Digest string `json:"digest,omitempty"`
	}
	b, _ := json.Marshal(map[string]any{"tag_name": tag, "assets": []asset{
		{Name: "App-x86_64.AppImage", URL: base + "App-x86_64.AppImage", Size: int64(len(body)), Digest: digest},
		{Name: "App-x86_64.AppImage.zsync", URL: base + "App-x86_64.AppImage.zsync", Size: 100},
	}})
	return b
}

func zsyncHeader(url string, body []byte) []byte {
	return []byte(fmt.Sprintf("zsync: 0.6.2\nFilename: App-x86_64.AppImage\nBlocksize: 2048\nLength: %d\n"+
		"Hash-Lengths: 2,2,5\nURL: %s\nSHA-1: %s\n\n\x00\x01binary block sums", len(body), url, sha1sum(body)))
}

func TestAddAppImageFromGitHubReleases(t *testing.T) {
	const update = "gh-releases-zsync|example|app|latest|App-*x86_64.AppImage.zsync"
	v1 := buildAppImage(t, "Example App", "1.0", update)
	v2 := buildAppImage(t, "Example App 2", "", update)
	srv := files{}
	b := newBackend(t, srv, catalog.Listing{})
	l := &Local{GH: b}
	ctx := context.Background()

	src := writeDownload(t, v1)
	f, err := ReadFile(src)
	if err != nil {
		t.Fatal(err)
	}
	if f.UpdateErr != nil || f.Update.GitHubRepo() != "example/app" || f.Info.AppID() != "org.example.App" {
		t.Fatalf("file = %+v, %v", f.Update, f.UpdateErr)
	}
	app, err := l.Add(ctx, f, nil)
	if err != nil {
		t.Fatal(err)
	}
	if app.Ref != "org.example.App" || app.Name != "Example App" || app.Version != "1.0" ||
		app.Origin != "github.com/example/app" {
		t.Errorf("added %+v", app)
	}
	if _, err := os.Stat(src); !os.IsNotExist(err) {
		t.Errorf("the downloaded file is still there: %v", err)
	}
	installed := filepath.Join(b.Dirs.AppImages, "file__org.example.App.AppImage")
	if st, err := os.Stat(installed); err != nil || st.Mode().Perm() != 0o755 {
		t.Fatalf("AppImage: %v, %v", st, err)
	}
	desktop, _ := os.ReadFile(filepath.Join(b.Dirs.Applications, "uas-file__org.example.App.desktop"))
	if !strings.Contains(string(desktop), `Exec="`+installed+`" %F`) {
		t.Errorf("desktop file:\n%s", desktop)
	}

	// The latest release has v2, with a published digest.
	srv["https://api.github.com/repos/example/app/releases/latest"] = release("v2.0", v2, "sha256:"+sum(v2))
	srv["https://github.com/example/app/releases/download/v2.0/App-x86_64.AppImage.zsync"] =
		zsyncHeader("App-x86_64.AppImage", v2)
	srv["https://github.com/example/app/releases/download/v2.0/App-x86_64.AppImage"] = v2
	ups, err := l.Updates(ctx)
	if err != nil || len(ups) != 1 || ups[0].Ref != "org.example.App" || ups[0].Version != "2.0" {
		t.Fatalf("updates = %+v, %v", ups, err)
	}
	if err := l.Update(ctx, app, nil); err != nil {
		t.Fatal(err)
	}
	if got, _ := os.ReadFile(installed); string(got) != string(v2) {
		t.Error("the AppImage was not replaced")
	}
	apps, _ := l.Installed(ctx)
	if len(apps) != 1 || apps[0].Name != "Example App 2" || apps[0].Version != "2.0" {
		t.Errorf("installed = %+v", apps)
	}
	l.latest = nil
	if ups, _ := l.Updates(ctx); len(ups) != 0 {
		t.Errorf("updates after updating = %+v", ups)
	}

	if err := l.Remove(ctx, apps[0], nil); err != nil {
		t.Fatal(err)
	}
	for _, d := range []string{b.Dirs.AppImages, b.Dirs.Icons, b.Dirs.Applications} {
		if entries, _ := os.ReadDir(d); len(entries) != 0 {
			t.Errorf("%s still has %v", d, entries)
		}
	}
}

func TestUpdateRejectsWrongChecksum(t *testing.T) {
	const update = "zsync|https://example.com/app/App.AppImage.zsync"
	v1 := buildAppImage(t, "Example App", "1.0", update)
	v2 := buildAppImage(t, "Example App", "2.0", update)
	srv := files{}
	b := newBackend(t, srv, catalog.Listing{})
	l := &Local{GH: b}
	ctx := context.Background()
	f, err := ReadFile(writeDownload(t, v1))
	if err != nil {
		t.Fatal(err)
	}
	app, err := l.Add(ctx, f, nil)
	if err != nil {
		t.Fatal(err)
	}
	if app.Origin != "example.com" {
		t.Errorf("origin = %q", app.Origin)
	}

	// The .zsync file names v2's checksum, but the server sends something else.
	srv["https://example.com/app/App.AppImage.zsync"] = zsyncHeader("files/App.AppImage", v2)
	srv["https://example.com/app/files/App.AppImage"] = append([]byte{}, v1...)
	ups, _ := l.Updates(ctx)
	if len(ups) != 1 {
		t.Fatalf("updates = %+v", ups)
	}
	err = l.Update(ctx, app, nil)
	if err == nil || !strings.Contains(err.Error(), "checksum") {
		t.Fatalf("Update = %v, want a checksum error", err)
	}
	if got, _ := os.ReadFile(filepath.Join(b.Dirs.AppImages, "file__org.example.App.AppImage")); string(got) != string(v1) {
		t.Error("the installed AppImage changed")
	}

	srv["https://example.com/app/files/App.AppImage"] = v2
	if err := l.Update(ctx, app, nil); err != nil {
		t.Fatal(err)
	}
	apps, _ := l.Installed(ctx)
	if len(apps) != 1 || apps[0].Version != "2.0" {
		t.Errorf("installed = %+v", apps)
	}
}

func TestAddWithoutUpdateInformation(t *testing.T) {
	v1 := buildAppImage(t, "Example App", "1.0", "")
	b := newBackend(t, files{}, catalog.Listing{})
	l := &Local{GH: b}
	ctx := context.Background()
	f, err := ReadFile(writeDownload(t, v1))
	if err != nil {
		t.Fatal(err)
	}
	if f.UpdateErr == nil {
		t.Fatal("no error for an AppImage without update information")
	}
	app, err := l.Add(ctx, f, nil)
	if err != nil {
		t.Fatal(err)
	}
	if app.Origin != "" {
		t.Errorf("origin = %q", app.Origin)
	}
	if ups, _ := l.Updates(ctx); len(ups) != 0 {
		t.Errorf("updates = %+v", ups)
	}
	if err := l.Update(ctx, app, nil); err == nil {
		t.Error("Update of an AppImage with nowhere to update from succeeded")
	}
}

func TestReadFileRejectsOtherFiles(t *testing.T) {
	p := filepath.Join(t.TempDir(), "notes.txt")
	os.WriteFile(p, []byte("hello"), 0o644)
	if _, err := ReadFile(p); err == nil {
		t.Error("accepted a text file")
	}
}

// TestAdopt records an AppImage of a catalog app as its GitHub install, with the catalog's version when the
// file is the release's.
func TestAdopt(t *testing.T) {
	v1 := buildAppImage(t, "Example App", "", "gh-releases-zsync|example|notes|latest|*.zsync")
	l1 := listing("1.0", v1)
	b := newBackend(t, files{}, l1)
	ctx := context.Background()
	src := writeDownload(t, v1)
	f, err := ReadFile(src)
	if err != nil {
		t.Fatal(err)
	}
	if err := b.Adopt(ctx, f, l1, nil); err != nil {
		t.Fatal(err)
	}
	apps, _ := b.Installed(ctx)
	if len(apps) != 1 || apps[0].Ref != "example/notes" || apps[0].Version != "1.0" || apps[0].Name != "Example App" {
		t.Errorf("installed = %+v", apps)
	}
	if ups, _ := b.Updates(ctx); len(ups) != 0 {
		t.Errorf("updates = %+v", ups)
	}
	if _, err := os.Stat(src); !os.IsNotExist(err) {
		t.Error("the downloaded file is still there")
	}
	if err := b.Remove(ctx, backend.InstalledApp{Source: catalog.SourceGitHub, Ref: "example/notes"}, nil); err != nil {
		t.Fatal(err)
	}
}
