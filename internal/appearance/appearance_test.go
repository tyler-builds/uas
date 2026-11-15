package appearance

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/ulikunitz/xz"

	"github.com/tyler-builds/uas/internal/catalog"
)

// member is one entry of a test archive: a file with body, a folder (name ending in "/"), or a link.
type member struct {
	name, body string
	symlink    string
	hardlink   string
	mode       int64
	typ        byte // a tar type to force, such as tar.TypeChar
}

func file(name, body string) member     { return member{name: name, body: body} }
func dir(name string) member            { return member{name: name + "/"} }
func symlink(name, target string) member { return member{name: name, symlink: target} }

// iconIndex is an icon theme's index.theme.
func iconIndex(name string, inherits ...string) string {
	s := "[Icon Theme]\nName=" + name + "\nComment=A theme\nDirectories=48x48/apps\n"
	if len(inherits) > 0 {
		s += "Inherits=" + strings.Join(inherits, ",") + "\n"
	}
	return s + "\n[48x48/apps]\nSize=48\n"
}

// writeTar writes a .tar.gz (or .tar.xz when xzip) of members and returns its path.
func writeTar(t *testing.T, members []member, xzip bool) string {
	t.Helper()
	var raw bytes.Buffer
	tw := tar.NewWriter(&raw)
	for _, m := range members {
		h := &tar.Header{Name: m.name, Mode: 0o644, Size: int64(len(m.body)), Typeflag: tar.TypeReg}
		if m.mode != 0 {
			h.Mode = m.mode
		}
		switch {
		case m.typ != 0:
			h.Typeflag, h.Size = m.typ, 0
		case strings.HasSuffix(m.name, "/"):
			h.Typeflag, h.Mode, h.Size = tar.TypeDir, 0o755, 0
		case m.symlink != "":
			h.Typeflag, h.Linkname, h.Size = tar.TypeSymlink, m.symlink, 0
		case m.hardlink != "":
			h.Typeflag, h.Linkname, h.Size = tar.TypeLink, m.hardlink, 0
		}
		if err := tw.WriteHeader(h); err != nil {
			t.Fatal(err)
		}
		if h.Typeflag == tar.TypeReg {
			if _, err := tw.Write([]byte(m.body)); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	name := "theme.tar.gz"
	if xzip {
		name = "theme.tar.xz"
		w, err := xz.NewWriter(&out)
		if err != nil {
			t.Fatal(err)
		}
		w.Write(raw.Bytes())
		w.Close()
	} else {
		w := gzip.NewWriter(&out)
		w.Write(raw.Bytes())
		w.Close()
	}
	p := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(p, out.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

func writeZip(t *testing.T, members []member) string {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for _, m := range members {
		h := &zip.FileHeader{Name: m.name, Method: zip.Deflate}
		switch {
		case m.symlink != "":
			h.SetMode(os.ModeSymlink | 0o777)
		case strings.HasSuffix(m.name, "/"):
			h.SetMode(os.ModeDir | 0o755)
		default:
			h.SetMode(0o644)
		}
		w, err := zw.CreateHeader(h)
		if err != nil {
			t.Fatal(err)
		}
		body := m.body
		if m.symlink != "" {
			body = m.symlink
		}
		w.Write([]byte(body))
	}
	zw.Close()
	p := filepath.Join(t.TempDir(), "theme.zip")
	if err := os.WriteFile(p, buf.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

// cursorTheme is a cursor theme in a folder, with the links cursor themes are full of.
func cursorTheme(dir string) []member {
	return []member{
		file(dir+"/index.theme", "[Icon Theme]\nName="+dir+"\nInherits=Adwaita\n"),
		file(dir+"/cursors/default", "Xcur cursor"),
		file(dir+"/cursors/pointer", "Xcur pointer"),
		symlink(dir+"/cursors/left_ptr", "default"),
		symlink(dir+"/cursors/hand2", "pointer"),
		symlink(dir+"/cursors/hand1", "hand2"), // a link to a link
	}
}

func TestInspectSingleCursorTheme(t *testing.T) {
	p := writeTar(t, cursorTheme("WhiteSur-cursors"), true)
	c, err := Inspect(p, DefaultLimits)
	if err != nil {
		t.Fatal(err)
	}
	if len(c.Themes) != 1 || c.Themes[0].Name != "WhiteSur-cursors" || !c.Themes[0].Cursors || c.Themes[0].Icons {
		t.Fatalf("Themes = %+v", c.Themes)
	}
	if got := c.Of(catalog.KindIconTheme); len(got) != 0 {
		t.Errorf("icon themes = %+v", got)
	}

	dst := t.TempDir()
	themes, err := Extract(p, catalog.KindCursorTheme, dst, DefaultLimits)
	if err != nil {
		t.Fatal(err)
	}
	if len(themes) != 1 {
		t.Fatalf("Extract = %+v", themes)
	}
	// Links are written as files with the contents they lead to, never as symbolic links.
	for name, want := range map[string]string{"default": "Xcur cursor", "left_ptr": "Xcur cursor",
		"hand1": "Xcur pointer", "hand2": "Xcur pointer"} {
		p := filepath.Join(dst, "WhiteSur-cursors", "cursors", name)
		fi, err := os.Lstat(p)
		if err != nil {
			t.Fatal(err)
		}
		if fi.Mode()&os.ModeSymlink != 0 || !fi.Mode().IsRegular() || fi.Mode().Perm()&0o111 != 0 {
			t.Errorf("%s: mode %v", name, fi.Mode())
		}
		if b, _ := os.ReadFile(p); string(b) != want {
			t.Errorf("%s = %q, want %q", name, b, want)
		}
	}
	// Only the theme is left: no raw files.
	entries, _ := os.ReadDir(dst)
	if len(entries) != 1 || entries[0].Name() != "WhiteSur-cursors" {
		t.Errorf("dst holds %v", entries)
	}
}

func TestInspectMultiVariantIconArchive(t *testing.T) {
	top := "papirus-icon-theme-20250501/"
	members := []member{
		file(top+"README.md", "Papirus"),
		dir(top + "Papirus"),
		file(top+"Papirus/index.theme", iconIndex("Papirus", "breeze", "hicolor")),
		file(top+"Papirus/48x48/apps/firefox.svg", "<svg/>"),
		symlink(top+"Papirus/48x48/apps/browser.svg", "firefox.svg"),
		// A folder link, as Papirus has for its @2x sizes.
		symlink(top+"Papirus/48x48@2x", "48x48"),
		file(top+"Papirus/icon-theme.cache", "stale"),
		file(top+"Papirus-Dark/index.theme", iconIndex("Papirus Dark", "Papirus")),
		// A link into the other variant.
		symlink(top+"Papirus-Dark/48x48/apps/firefox.svg", "../../../Papirus/48x48/apps/firefox.svg"),
		file(top+"Papirus-Light/index.theme", iconIndex("Papirus Light", "Papirus")),
		file(top+"Papirus-Light/48x48/apps/x.svg", "<svg light/>"),
		// A GTK theme's index.theme is not an icon theme.
		file(top+"extras/gtk/index.theme", "[X-GNOME-Metatheme]\nName=Gtk\n"),
	}
	for _, p := range []string{writeTar(t, members, false), writeZip(t, members)} {
		c, err := Inspect(p, DefaultLimits)
		if err != nil {
			t.Fatal(p, err)
		}
		var names []string
		for _, th := range c.Of(catalog.KindIconTheme) {
			names = append(names, th.Name)
		}
		if !slices.Equal(names, []string{"Papirus", "Papirus-Dark", "Papirus-Light"}) {
			t.Fatalf("%s: icon themes = %v", p, names)
		}
		if th := c.Themes[1]; th.Title != "Papirus Dark" || !slices.Equal(th.Inherits, []string{"Papirus"}) {
			t.Errorf("Papirus-Dark = %+v", th)
		}
		dst := t.TempDir()
		if _, err := Extract(p, catalog.KindIconTheme, dst, DefaultLimits); err != nil {
			t.Fatal(p, err)
		}
		for name, want := range map[string]string{
			"Papirus/48x48/apps/browser.svg":      "<svg/>",
			"Papirus/48x48@2x/apps/firefox.svg":   "<svg/>",
			"Papirus/48x48@2x/apps/browser.svg":   "<svg/>",
			"Papirus-Dark/48x48/apps/firefox.svg": "<svg/>",
			"Papirus-Light/48x48/apps/x.svg":      "<svg light/>",
		} {
			b, err := os.ReadFile(filepath.Join(dst, name))
			if err != nil || string(b) != want {
				t.Errorf("%s: %s = %q, %v", p, name, b, err)
			}
		}
		for _, absent := range []string{"Papirus/icon-theme.cache", "README.md", "papirus-icon-theme-20250501", "extras"} {
			if _, err := os.Lstat(filepath.Join(dst, absent)); err == nil {
				t.Errorf("%s: %s was written", p, absent)
			}
		}
	}
}

func TestInspectRefuses(t *testing.T) {
	good := cursorTheme("Good")
	small := Limits{MaxEntries: 8, MaxBytes: 200, MaxFileBytes: 100}
	for _, tc := range []struct {
		name    string
		members []member
		lim     Limits
		want    string // part of the reason
	}{
		{"path traversal", append(good, file("../evil", "x")), DefaultLimits, "outside the archive"},
		{"traversal inside", append(good, file("Good/../../evil", "x")), DefaultLimits, "outside the archive"},
		{"absolute path", append(good, file("/etc/evil", "x")), DefaultLimits, "absolute path"},
		{"symlink out of the archive", append(good, symlink("Good/cursors/x", "../../../../etc/passwd")), DefaultLimits,
			"outside the archive"},
		{"absolute symlink", append(good, symlink("Good/cursors/x", "/etc/passwd")), DefaultLimits, "outside the archive"},
		{"dangling symlink", append(good, symlink("Good/cursors/x", "nothing")), DefaultLimits, "isn't in the archive"},
		{"hard link out", append(good, member{name: "Good/cursors/x", hardlink: "../etc/passwd"}), DefaultLimits,
			"outside the archive"},
		{"link loop", append(good, symlink("Good/cursors/a", "b"), symlink("Good/cursors/b", "a")), DefaultLimits,
			"circle"},
		{"folder link to its own folder", append(good, symlink("Good/cursors/up", "..")), DefaultLimits,
			"folder it is in"},
		{"device", append(good, member{name: "Good/dev", typ: tar.TypeChar}), DefaultLimits, "device"},
		{"ELF program", append(good, file("Good/cursors/tool", "\x7fELF\x02\x01")), DefaultLimits, "a program"},
		{"script by shebang", append(good, file("Good/setup", "#!/bin/sh\nrm -rf ~")), DefaultLimits, "a script"},
		{"install.sh", append(good, file("install.sh", "echo hi")), DefaultLimits, "program or script"},
		{"library", append(good, file("Good/libtheme.so.1", "x")), DefaultLimits, "library"},
		{"shared object", append(good, file("Good/plugin.so", "x")), DefaultLimits, "program or script"},
		{"desktop file", append(good, file("Good/app.desktop", "[Desktop Entry]")), DefaultLimits, "program or script"},
		{"QML", append(good, file("Good/main.qml", "Item {}")), DefaultLimits, "program or script"},
		{"JavaScript", append(good, file("Good/x.js", "alert(1)")), DefaultLimits, "program or script"},
		{"nested archive", append(good, file("Good-blue.tar.gz", "x")), DefaultLimits, "another archive"},
		{"too many files", append(good, file("Good/a", ""), file("Good/b", ""), file("Good/c", "")), small,
			"more than 8 files"},
		{"too large a file", append(good, file("Good/big", strings.Repeat("x", 120))), small, "larger than"},
		{"too many bytes", []member{file("Good/cursors/a", strings.Repeat("x", 90)),
			file("Good/cursors/b", strings.Repeat("x", 90)), file("Good/cursors/c", strings.Repeat("x", 90))}, small,
			"unpacks to more than"},
		{"duplicate entry", append(good, file("Good/cursors/default", "again")), DefaultLimits, "twice"},
		{"nothing to install", []member{file("README", "hi"), file("preview.png", "png")}, DefaultLimits,
			"no icon or cursor theme"},
		{"theme at the top", cursorTheme(".")[1:], DefaultLimits, "top of the archive"},
		{"theme in a theme", append(good, file("Good/Inner/index.theme", iconIndex("Inner"))), DefaultLimits,
			"another theme inside"},
		{"two of a name", append(cursorTheme("a/Good"), cursorTheme("b/Good")...), DefaultLimits, "two of its themes"},
		{"reserved name", cursorTheme("default"), DefaultLimits, "take over"},
		{"bad name", cursorTheme("-rf"), DefaultLimits, "won't use"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := Inspect(writeTar(t, tc.members, false), tc.lim)
			if !IsRefused(err) || !strings.Contains(err.Error(), tc.want) {
				t.Errorf("Inspect = %v, want a refusal mentioning %q", err, tc.want)
			}
		})
	}
	// A zip's symlink pointing out, and formats uas doesn't open.
	if _, err := Inspect(writeZip(t, append(good, symlink("Good/cursors/x", "../../../etc/passwd"))),
		DefaultLimits); !IsRefused(err) {
		t.Errorf("zip with an escaping link: %v", err)
	}
	seven := filepath.Join(t.TempDir(), "x.7z")
	os.WriteFile(seven, []byte("7z\xbc\xaf\x27\x1c rest"), 0o644)
	if _, err := Inspect(seven, DefaultLimits); !IsRefused(err) || !strings.Contains(err.Error(), "7-Zip") {
		t.Errorf("7z: %v", err)
	}
}

func TestExtractWantsKindAndEmptyDir(t *testing.T) {
	p := writeTar(t, cursorTheme("Cur"), false)
	if _, err := Extract(p, catalog.KindIconTheme, t.TempDir(), DefaultLimits); !IsRefused(err) ||
		!strings.Contains(err.Error(), "no icon theme") {
		t.Errorf("icon theme from a cursor archive: %v", err)
	}
	dst := t.TempDir()
	os.WriteFile(filepath.Join(dst, "x"), nil, 0o644)
	if _, err := Extract(p, catalog.KindCursorTheme, dst, DefaultLimits); err == nil {
		t.Error("extracted into a folder that wasn't empty")
	}
}

func interFont(t *testing.T) []byte {
	t.Helper()
	b, err := os.ReadFile("../gui/fonts/Inter-Regular.ttf")
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestFontFamilies(t *testing.T) {
	fams, err := FontFamilies(interFont(t))
	if err != nil || !slices.Equal(fams, []string{"Inter"}) {
		t.Errorf("FontFamilies = %v, %v", fams, err)
	}
	for _, bad := range [][]byte{[]byte("wOF2 web font"), []byte("\x7fELF"), []byte("OTTO but not really")} {
		if _, err := FontFamilies(bad); !IsRefused(err) {
			t.Errorf("FontFamilies(%q) = %v", bad[:4], err)
		}
	}
}

func TestExtractFonts(t *testing.T) {
	inter := string(interFont(t))
	p := writeTar(t, []member{file("README.md", "Nerd Fonts"), file("LICENSE", "OFL"),
		file("InterNerdFont-Regular.ttf", inter), file("InterNerdFontMono-Regular.ttf", inter)}, true)
	dst := t.TempDir()
	got, err := ExtractFonts(p, dst, DefaultLimits)
	if err != nil || len(got) != 2 || got[0].Families[0] != "Inter" {
		t.Fatalf("ExtractFonts = %+v, %v", got, err)
	}
	entries, _ := os.ReadDir(dst)
	if len(entries) != 2 {
		t.Errorf("dst holds %v", entries)
	}
	for _, tc := range []struct {
		name    string
		members []member
	}{
		{"script", []member{file("a.ttf", inter), file("install.sh", "#!/bin/sh")}},
		{"not a font", []member{file("fake.ttf", "MZ not a font")}},
		{"link", []member{file("a.ttf", inter), symlink("b.ttf", "a.ttf")}},
		{"traversal", []member{file("../a.ttf", inter)}},
		{"nothing", []member{file("README", "x")}},
	} {
		if _, err := ExtractFonts(writeTar(t, tc.members, false), t.TempDir(), DefaultLimits); !IsRefused(err) {
			t.Errorf("%s: %v", tc.name, err)
		}
	}
}
