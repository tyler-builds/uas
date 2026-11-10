package appimage

import (
	"bytes"
	"encoding/binary"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"
)

var png = []byte("\x89PNG\r\n\x1a\nrest-of-png")

const desktop = `[Desktop Entry]
Type=Application
Name=Example Notes
Name[de]=Beispiel-Notizen
Comment=Take notes
Exec=AppRun --no-sandbox %U
Icon=example-notes
Categories=Office;
MimeType=text/markdown;
[Desktop Action New]
Name=New note
Exec=rm -rf ~
`

func TestFromFS(t *testing.T) {
	info, err := fromFS(fstest.MapFS{
		"example-notes.desktop": {Data: []byte(desktop)},
		"example-notes.png":     {Data: png},
		"AppRun":                {Data: []byte("#!/bin/sh")},
		"usr/share/metainfo/example-notes.metainfo.xml": {Data: []byte("<component/>")},
	})
	if err != nil {
		t.Fatal(err)
	}
	if string(info.Metainfo) != "<component/>" {
		t.Errorf("metainfo = %q", info.Metainfo)
	}
	if info.Desktop["Name"] != "Example Notes" || info.Desktop["Exec"] != "AppRun --no-sandbox %U" {
		t.Errorf("desktop = %v", info.Desktop)
	}
	if info.IconExt != ".png" || !bytes.Equal(info.Icon, png) {
		t.Errorf("icon = %q %q", info.IconExt, info.Icon)
	}
}

// linkMap adds symlinks to a MapFS.
type linkMap struct {
	fstest.MapFS
	links map[string]string
}

func (l linkMap) ReadLink(name string) (string, error) {
	if t, ok := l.links[name]; ok {
		return t, nil
	}
	return "", errNotLink
}

func TestDirIconSymlink(t *testing.T) {
	info, err := fromFS(linkMap{
		MapFS: fstest.MapFS{
			"app.desktop":                         {Data: []byte("[Desktop Entry]\nName=App\nIcon=missing\n")},
			".DirIcon":                            {Data: []byte("placeholder")},
			"usr/share/icons/hicolor/256/app.svg": {Data: []byte(`<?xml version="1.0"?><svg xmlns="x"/>`)},
		},
		links: map[string]string{".DirIcon": "usr/share/icons/hicolor/256/app.svg"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if info.IconExt != ".svg" {
		t.Errorf("IconExt = %q", info.IconExt)
	}

	_, err = readFile(linkMap{MapFS: fstest.MapFS{"a": {}}, links: map[string]string{"a": "../../etc/passwd"}}, "a", 100)
	if err == nil {
		t.Error("followed a symlink out of the image")
	}
	_, err = readFile(linkMap{MapFS: fstest.MapFS{"a": {}}, links: map[string]string{"a": "/etc/passwd"}}, "a", 100)
	if err == nil {
		t.Error("followed an absolute symlink")
	}
}

func TestDesktopFile(t *testing.T) {
	got, err := DesktopFile(Entry{Name: "fallback", AppImage: "/home/u/.local/share/uas/appimages/example__notes.AppImage",
		Icon: "/home/u/.local/share/uas/icons/example__notes.png", Source: "github:example/notes"},
		parseDesktop(desktop))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"Name=Example Notes\n",
		// The app's own launch arguments are kept; its command is not.
		"Exec=\"/home/u/.local/share/uas/appimages/example__notes.AppImage\" --no-sandbox %U\n",
		"Icon=/home/u/.local/share/uas/icons/example__notes.png\n",
		"MimeType=text/markdown;\n",
		"X-UAS-Source=github:example/notes\n",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in\n%s", want, got)
		}
	}
	if strings.Contains(got, "rm -rf") || strings.Contains(got, "AppRun") || strings.Contains(got, "Desktop Action") {
		t.Errorf("copied an untrusted command:\n%s", got)
	}

	if _, err := DesktopFile(Entry{Name: "x", AppImage: "/tmp/$(evil).AppImage"}, nil); err == nil {
		t.Error("accepted a path with shell characters")
	}
}

// TestInspectRealSquashfs builds an AppImage-shaped file (a minimal ELF header followed by a squashfs image)
// when mksquashfs is available.
func TestInspectRealSquashfs(t *testing.T) {
	mksquashfs, err := exec.LookPath("mksquashfs")
	if err != nil {
		t.Skip("mksquashfs not installed")
	}
	dir := t.TempDir()
	root := filepath.Join(dir, "root")
	os.MkdirAll(root, 0o755)
	os.WriteFile(filepath.Join(root, "example-notes.desktop"), []byte(desktop), 0o644)
	os.WriteFile(filepath.Join(root, "example-notes.png"), png, 0o644)
	os.Symlink("example-notes.png", filepath.Join(root, ".DirIcon"))
	img := filepath.Join(dir, "img.sqfs")
	if out, err := exec.Command(mksquashfs, root, img, "-quiet", "-noappend", "-comp", "gzip").CombinedOutput(); err != nil {
		t.Fatalf("mksquashfs: %v\n%s", err, out)
	}
	sq, _ := os.ReadFile(img)

	// An ELF64 header whose section header table (0 entries of 64 bytes at offset 64) ends at byte 64.
	elf := make([]byte, 64)
	copy(elf, "\x7fELF")
	elf[4], elf[5] = 2, 1
	binary.LittleEndian.PutUint64(elf[0x28:], 64)
	binary.LittleEndian.PutUint16(elf[0x3a:], 64)
	binary.LittleEndian.PutUint16(elf[0x3c:], 0)
	app := filepath.Join(dir, "Example.AppImage")
	os.WriteFile(app, append(elf, sq...), 0o755)

	info, err := Inspect(app)
	if err != nil {
		t.Fatal(err)
	}
	if info.Desktop["Name"] != "Example Notes" || info.IconExt != ".png" {
		t.Errorf("info = %+v", info.Desktop)
	}

	if _, err := Inspect(img); err == nil {
		t.Error("accepted a file that is not an ELF")
	}
}

// TestSeveralDesktopFiles picks the app's own entry over a bundled library's.
func TestSeveralDesktopFiles(t *testing.T) {
	fsys := linkMap{MapFS: fstest.MapFS{
		"libgphoto2.desktop": {Data: []byte("[Desktop Entry]\nName=libgphoto2\nIcon=libgphoto2\n")},
		"performous.desktop": {Data: []byte("[Desktop Entry]\nName=Performous\nIcon=performous\n")},
		"performous.png":     {Data: png},
		".DirIcon":           {Data: png},
	}, links: map[string]string{".DirIcon": "performous.png"}}
	info, err := fromFS(fsys)
	if err != nil {
		t.Fatal(err)
	}
	if info.Desktop["Name"] != "Performous" {
		t.Errorf("name = %q", info.Desktop["Name"])
	}
	// Without a .DirIcon link, a library's entry still loses.
	info, err = fromFS(fstest.MapFS{
		"libgphoto2.desktop": {Data: []byte("[Desktop Entry]\nName=libgphoto2\n")},
		"wine.desktop":       {Data: []byte("[Desktop Entry]\nName=Wine\n")},
	})
	if err != nil || info.Desktop["Name"] != "Wine" {
		t.Errorf("name = %q, %v", info.Desktop["Name"], err)
	}
}

// TestMetainfoOfTheApp ignores a bundled library's metainfo.
func TestMetainfoOfTheApp(t *testing.T) {
	info, err := fromFS(fstest.MapFS{
		"Performous.desktop":                         {Data: []byte("[Desktop Entry]\nName=Performous\n")},
		"usr/share/metainfo/libgphoto2.metainfo.xml": {Data: []byte("<component><id>libgphoto2</id></component>")},
		"usr/share/metainfo/org.performous.Performous.metainfo.xml": {Data: []byte(
			"<component><launchable type=\"desktop-id\">Performous.desktop</launchable></component>")},
	})
	if err != nil || !bytes.Contains(info.Metainfo, []byte("Performous.desktop")) {
		t.Errorf("metainfo = %q, %v", info.Metainfo, err)
	}
	info, err = fromFS(fstest.MapFS{
		"wine.desktop": {Data: []byte("[Desktop Entry]\nName=Wine\n")},
		"usr/share/metainfo/libgphoto2.metainfo.xml": {Data: []byte("<component/>")},
	})
	if err != nil || info.Metainfo != nil {
		t.Errorf("a library's metainfo was used: %q, %v", info.Metainfo, err)
	}
}

func TestLaunchArgs(t *testing.T) {
	for exec, want := range map[string]string{
		// What apps ship: the command goes, its arguments stay.
		"AppRun --no-sandbox %U":               "--no-sandbox %U",
		"ghostty --gtk-single-instance=true":   "--gtk-single-instance=true",
		"helium %U":                            "%U",
		"AppRun":                               "",
		"":                                     "",
		"app --profile default --port=8080 %F": "--profile default --port=8080 %F",
		"env GDK_BACKEND=x11 FOO=1 app --flag %u": "--flag %u",
		"app %U %F":                                   "%U", // one placeholder
		"app --icon %i --name %c %k %U":               "--icon --name %U",
		"/usr/bin/app --config=/etc/app.conf,x:y+z@h": "--config=/etc/app.conf,x:y+z@h",
		// Anything that isn't plain: only the placeholder, as before uas kept arguments.
		`app --title "My App" %U`:   "%U",
		"app --run=$(curl evil) %F": "%F",
		"sh -c 'curl evil | sh'":    "",
		"app --a;rm -rf ~":          "",
		"app `id` %u":               "%u",
		"app --path=C:\\dir %U":     "%U",
		"app --x=1 && other --y %F": "%F",
		"app --redirect>/tmp/out":   "",
		"app --glob=* %U":           "%U",
		"rm -rf ~":                  "",
	} {
		if got := LaunchArgs(exec); got != want {
			t.Errorf("LaunchArgs(%q) = %q, want %q", exec, got, want)
		}
	}
	// With no Exec of its own to read, an entry's launcher takes the arguments uas recorded, made safe the
	// same way.
	for args, want := range map[string]string{"--gtk-single-instance=true": " --gtk-single-instance=true\n",
		"--x; rm -rf ~": "\n", "": "\n"} {
		got, err := DesktopFile(Entry{Name: "Ghostty", AppImage: "/a/ghostty.AppImage", Args: args}, map[string]string{})
		if err != nil || !strings.Contains(got, "Exec=\"/a/ghostty.AppImage\""+want) {
			t.Errorf("Args %q: %v\n%s", args, err, got)
		}
	}
	// An entry that can be read is asked instead.
	got, _ := DesktopFile(Entry{Name: "X", AppImage: "/a/x.AppImage", Args: "--stale"}, map[string]string{"Exec": "x --fresh %U"})
	if !strings.Contains(got, "Exec=\"/a/x.AppImage\" --fresh %U\n") {
		t.Errorf("own arguments not used:\n%s", got)
	}
}
