package github

import (
	"bytes"
	"context"
	"crypto/sha1"
	"crypto/sha512"
	"encoding/base64"
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
func buildAppImage(t *testing.T, name, version, update string, extra ...string) []byte {
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
	for i := 0; i+1 < len(extra); i += 2 { // extra files: path, contents, ...
		os.MkdirAll(filepath.Join(root, filepath.Dir(extra[i])), 0o755)
		os.WriteFile(filepath.Join(root, extra[i]), []byte(extra[i+1]), 0o644)
	}
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

func sha512b64(b []byte) string {
	h := sha512.Sum512(b)
	return base64.StdEncoding.EncodeToString(h[:])
}

func electronFeed(version string, body []byte) []byte {
	return []byte(fmt.Sprintf("version: %s\nfiles:\n  - url: App-%s.AppImage\n    sha512: %s\n    size: %d\n"+
		"    blockMapSize: 1234\n  - url: app_%s_amd64.deb\n    sha512: %s\n    size: 10\n"+
		"path: App-%s.AppImage\nsha512: %s\nreleaseDate: '2026-10-20T12:48:49.418Z'\n",
		version, version, sha512b64(body), len(body), version, sha512b64([]byte("deb")), version, sha512b64(body)))
}

// TestElectronUpdates follows the feed an electron-builder app names in resources/app-update.yml.
func TestElectronUpdates(t *testing.T) {
	cfg := "provider: generic\nurl: https://updates.example.com/app/linux\nupdaterCacheDirName: app-updater\n"
	v1 := buildAppImage(t, "Example App", "1.0.0", "", "resources/app-update.yml", cfg)
	v2 := buildAppImage(t, "Example App", "1.1.0", "", "resources/app-update.yml", cfg)
	srv := files{}
	b := newBackend(t, srv, catalog.Listing{})
	l := &Local{GH: b}
	ctx := context.Background()
	f, err := ReadFile(writeDownload(t, v1))
	if err != nil {
		t.Fatal(err)
	}
	if f.UpdateErr != nil || f.Update.String() != "electron-updater|https://updates.example.com/app/linux/latest-linux.yml" {
		t.Fatalf("update = %q, %v", f.Update.String(), f.UpdateErr)
	}
	app, err := l.Add(ctx, f, nil)
	if err != nil {
		t.Fatal(err)
	}
	if app.Origin != "updates.example.com" {
		t.Errorf("origin = %q", app.Origin)
	}

	srv["https://updates.example.com/app/linux/latest-linux.yml"] = electronFeed("1.0.0", v1)
	if ups, _ := l.Updates(ctx); len(ups) != 0 {
		t.Errorf("updates while current = %+v", ups)
	}
	l.latest = nil
	srv["https://updates.example.com/app/linux/latest-linux.yml"] = electronFeed("1.1.0", v2)
	srv["https://updates.example.com/app/linux/App-1.1.0.AppImage"] = v2
	ups, _ := l.Updates(ctx)
	if len(ups) != 1 || ups[0].Version != "1.1.0" {
		t.Fatalf("updates = %+v", ups)
	}
	if err := l.Update(ctx, app, nil); err != nil {
		t.Fatal(err)
	}
	apps, _ := l.Installed(ctx)
	if len(apps) != 1 || apps[0].Version != "1.1.0" {
		t.Errorf("installed = %+v", apps)
	}
}

// TestKnownFeed gives an AppImage that names no update source a known feed, also when it was added before
// uas knew the feed and kept no SHA-512.
func TestKnownFeed(t *testing.T) {
	v1 := buildAppImage(t, "Example App", "1.0.0", "")
	b := newBackend(t, files{}, catalog.Listing{})
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
	st, _ := b.State.Load()
	st.AppImages[0].SHA512 = ""
	b.State.Save(st)

	knownFeeds["org.example.App"] = "electron-updater|https://example.com/feed/latest-linux.yml"
	defer delete(knownFeeds, "org.example.App")
	b.Client.Transport = files{"https://example.com/feed/latest-linux.yml": electronFeed("1.0.0", v1)}
	if apps, _ := l.Installed(ctx); apps[0].Origin != "example.com" {
		t.Errorf("origin = %q", apps[0].Origin)
	}
	if ups, _ := l.Updates(ctx); len(ups) != 0 {
		t.Errorf("updates = %+v", ups)
	}
	if st, _ := b.State.Load(); st.AppImages[0].SHA512 == "" {
		t.Error("the SHA-512 was not filled in")
	}
	if err := l.Update(ctx, app, nil); err != nil {
		t.Errorf("Update when current: %v", err)
	}
}

// TestAddDwarFS adds an AppImage whose files are in a DwarFS image, which uas can't read: it gets its name
// from the file name and still follows its update information.
func TestAddDwarFS(t *testing.T) {
	const update = "gh-releases-zsync|example|app|latest|App-*x86_64.AppImage.zsync"
	img := buildAppImage(t, "Example App", "1.0", update)
	off := bytes.Index(img, []byte("hsqs"))
	img = append(img[:off:off], []byte("DWARFS\x02\x05not really dwarfs")...)
	b := newBackend(t, files{}, catalog.Listing{})
	l := &Local{GH: b}
	src := filepath.Join(t.TempDir(), "Ghostty-1.4.0-x86_64.AppImage")
	os.WriteFile(src, img, 0o755)
	if _, err := ReadFile(src); err == nil {
		t.Fatal("accepted a DwarFS file without the AppImage magic")
	}
	copy(img[8:], "AI\x02")
	os.WriteFile(src, img, 0o755)
	f, err := ReadFile(src)
	if err != nil {
		t.Fatal(err)
	}
	if f.Unreadable == nil || f.Update.GitHubRepo() != "example/app" {
		t.Fatalf("file = %+v", f)
	}
	app, err := l.Add(context.Background(), f, nil)
	if err != nil {
		t.Fatal(err)
	}
	if app.Ref != "ghostty" || app.Name != "Ghostty" || app.Origin != "github.com/example/app" {
		t.Errorf("added %+v", app)
	}
}

// TestLauncherWMClass names the window class after the app's own launcher, and rewrites launchers from an
// older uas.
func TestLauncherWMClass(t *testing.T) {
	img := buildAppImage(t, "Example App", "1.0", "")
	b := newBackend(t, files{}, catalog.Listing{})
	l := &Local{GH: b}
	ctx := context.Background()
	f, err := ReadFile(writeDownload(t, img))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := l.Add(ctx, f, nil); err != nil {
		t.Fatal(err)
	}
	desktop := filepath.Join(b.Dirs.Applications, "uas-file__org.example.App.desktop")
	got, _ := os.ReadFile(desktop)
	if !strings.Contains(string(got), "StartupWMClass=org.example.App\n") ||
		!strings.Contains(string(got), "X-UAS-Launcher=2\n") {
		t.Fatalf("launcher:\n%s", got)
	}

	old := "[Desktop Entry]\nType=Application\nName=Example App\nComment=Old comment\nExec=x\n"
	os.WriteFile(desktop, []byte(old), 0o644)
	if err := b.RepairLaunchers(ctx); err != nil {
		t.Fatal(err)
	}
	got, _ = os.ReadFile(desktop)
	if !strings.Contains(string(got), "StartupWMClass=org.example.App\n") ||
		!strings.Contains(string(got), "Comment=Old comment\n") {
		t.Errorf("repaired launcher:\n%s", got)
	}
	before, _ := os.Stat(desktop)
	b.RepairLaunchers(ctx) // nothing to do now
	if after, _ := os.Stat(desktop); !after.ModTime().Equal(before.ModTime()) {
		t.Error("rewrote a current launcher")
	}
}

// gnome answers gsettings like GNOME, with a dock holding favs.
type gnome struct {
	favs string
	ran  []string
}

func (g *gnome) Run(_ context.Context, name string, args []string, _ func(string)) (string, error) {
	g.ran = append(g.ran, name+" "+strings.Join(args, " "))
	if name == "gsettings" && args[0] == "get" {
		return g.favs, nil
	}
	if name == "gsettings" && args[0] == "set" {
		g.favs = args[3]
	}
	return "", nil
}

// TestTakeOverLaunchers replaces a launcher another tool wrote for the added AppImage, keeping its window
// class and icon, and swaps it in the dock.
func TestTakeOverLaunchers(t *testing.T) {
	img := buildAppImage(t, "Example App", "1.0", "")
	off := bytes.Index(img, []byte("hsqs")) // a DwarFS image: no icon or launcher uas can read
	img = append(img[:off:off], []byte("DWARFS\x02\x05")...)
	copy(img[8:], "AI\x02")
	b := newBackend(t, files{}, catalog.Listing{})
	g := &gnome{favs: "['other.desktop', 'example.desktop', 'org.gnome.Nautilus.desktop']"}
	b.Runner = g
	l := &Local{GH: b}
	ctx := context.Background()

	src := filepath.Join(t.TempDir(), "AppImages", "example.appimage")
	os.MkdirAll(filepath.Dir(src), 0o755)
	os.WriteFile(src, img, 0o755)
	png := []byte("\x89PNG\r\n\x1a\nnot really")
	iconPath := filepath.Join(filepath.Dir(src), ".icons", "example")
	os.MkdirAll(filepath.Dir(iconPath), 0o755)
	os.WriteFile(iconPath, png, 0o644)
	os.MkdirAll(b.Dirs.Applications, 0o755)
	theirs := filepath.Join(b.Dirs.Applications, "example.desktop")
	os.WriteFile(theirs, []byte("[Desktop Entry]\nType=Application\nName=Example\nIcon="+iconPath+
		"\nTryExec="+src+"\nExec=env DESKTOPINTEGRATION=1 "+src+" %U\nStartupWMClass=org.example.Real\n"), 0o644)
	unrelated := filepath.Join(b.Dirs.Applications, "other.desktop")
	os.WriteFile(unrelated, []byte("[Desktop Entry]\nType=Application\nName=Other\nExec=/usr/bin/other\n"), 0o644)

	f, err := ReadFile(src)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := l.Add(ctx, f, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(theirs); !os.IsNotExist(err) {
		t.Error("the other tool's launcher is still there")
	}
	if _, err := os.Stat(unrelated); err != nil {
		t.Error("removed an unrelated launcher")
	}
	ours, _ := os.ReadFile(filepath.Join(b.Dirs.Applications, "uas-file__example.desktop"))
	icon := filepath.Join(b.Dirs.Icons, "file__example.png")
	if !strings.Contains(string(ours), "StartupWMClass=org.example.Real\n") ||
		!strings.Contains(string(ours), "Icon="+icon+"\n") {
		t.Errorf("launcher:\n%s", ours)
	}
	if got, _ := os.ReadFile(icon); string(got) != string(png) {
		t.Error("the other tool's icon was not kept")
	}
	if g.favs != "['other.desktop', 'uas-file__example.desktop', 'org.gnome.Nautilus.desktop']" {
		t.Errorf("favorites = %s", g.favs)
	}
}

// TestRepairTakesOverBrokenLaunchers takes over a launcher left pointing at an AppImage uas moved before it
// took over launchers.
func TestRepairTakesOverBrokenLaunchers(t *testing.T) {
	img := buildAppImage(t, "Example App", "1.0", "")
	b := newBackend(t, files{}, catalog.Listing{})
	g := &gnome{favs: "['example.desktop']"}
	b.Runner = g
	l := &Local{GH: b}
	ctx := context.Background()
	f, err := ReadFile(writeDownload(t, img))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := l.Add(ctx, f, nil); err != nil {
		t.Fatal(err)
	}
	theirs := filepath.Join(b.Dirs.Applications, "example.desktop")
	os.WriteFile(theirs, []byte("[Desktop Entry]\nType=Application\nName=Example App\n"+
		"Exec=/home/u/AppImages/gone.appimage %U\nStartupWMClass=org.example.Real\n"), 0o644)
	if err := b.RepairLaunchers(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(theirs); !os.IsNotExist(err) {
		t.Error("the broken launcher is still there")
	}
	ours, _ := os.ReadFile(filepath.Join(b.Dirs.Applications, "uas-file__org.example.App.desktop"))
	if !strings.Contains(string(ours), "StartupWMClass=org.example.Real\n") {
		t.Errorf("launcher:\n%s", ours)
	}
	if g.favs != "['uas-file__org.example.App.desktop']" {
		t.Errorf("favorites = %s", g.favs)
	}
	st, _ := b.State.Load()
	if st.AppImages[0].WMClass != "org.example.Real" {
		t.Errorf("state = %+v", st.AppImages[0])
	}
}
