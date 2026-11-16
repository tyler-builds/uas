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

func file(name, body string) member      { return member{name: name, body: body} }
func dir(name string) member             { return member{name: name + "/"} }
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
	if len(themes.Themes) != 1 {
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
		{"device", append(good, member{name: "Good/dev", typ: tar.TypeChar}), DefaultLimits, "device"},
		// Odd paths refuse the archive, whatever the file is, scripts included.
		{"traversal script", append(good, file("../install.sh", "#!/bin/sh")), DefaultLimits, "outside the archive"},
		{"absolute script", append(good, file("/etc/profile.d/x.sh", "x")), DefaultLimits, "absolute path"},
		{"backslash", append(good, file(`Good\..\..\evil`, "x")), DefaultLimits, "odd name"},
		// Compiled code refuses the archive rather than being left out.
		{"ELF program", append(good, file("Good/cursors/tool", "\x7fELF\x02\x01")), DefaultLimits, "a program"},
		{"ELF named like an icon", append(good, file("Good/48/a.png", "\x7fELF\x02\x01")), DefaultLimits, "a program"},
		{"ELF named like a script", append(good, file("Good/x.sh", "\x7fELF\x02\x01")), DefaultLimits, "a program"},
		{"library", append(good, file("Good/libtheme.so.1", "x")), DefaultLimits, "library"},
		{"shared object", append(good, file("Good/plugin.so", "x")), DefaultLimits, "library"},
		{"library link", append(good, symlink("Good/libtheme.so", "index.theme")), DefaultLimits, "library"},
		// What makes a theme is never left out: a theme isn't listed without it.
		{"index.theme script", append(cursorTheme("Good")[1:], file("Good/index.theme", "#!/bin/sh\n[Icon Theme]")),
			DefaultLimits, "a script"},
		{"cursor script", append(good, file("Good/cursors/watch", "#!/bin/sh")), DefaultLimits, "a script"},
		{"cursor named like a script", append(good, file("Good/cursors/wait.sh", "Xcur")), DefaultLimits,
			"program or script"},
		{"no theme left", []member{file("Good/index.theme", "[Icon Theme]\nName=Good\n"),
			symlink("Good/cursors/default", "../install.sh"), file("Good/install.sh", "#!/bin/sh"),
			file("Good/cursors.tar.gz", "x")}, DefaultLimits, "holds no theme"},
		{"left out twice", append(good, file("Good/x.sh", "a"), file("Good/x.sh", "b")), DefaultLimits, "twice"},
		{"too many files", append(good, file("Good/a", ""), file("Good/b", ""), file("Good/c", "")), small,
			"more than 8 files"},
		{"too large a file", append(good, file("Good/big", strings.Repeat("x", 120))), small, "larger than"},
		{"too many bytes", []member{file("Good/cursors/a", strings.Repeat("x", 90)),
			file("Good/cursors/b", strings.Repeat("x", 90)), file("Good/cursors/c", strings.Repeat("x", 90))}, small,
			"unpacks to more than"},
		{"duplicate entry", append(good, file("Good/cursors/default", "again")), DefaultLimits, "twice"},
		{"nothing to install", []member{file("README", "hi"), file("preview.png", "png")}, DefaultLimits,
			"holds no theme"},
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
	seven := filepath.Join(t.TempDir(), "x.7z")
	os.WriteFile(seven, []byte("7z\xbc\xaf\x27\x1c rest"), 0o644)
	if _, err := Inspect(seven, DefaultLimits); !IsRefused(err) || !strings.Contains(err.Error(), "7-Zip") {
		t.Errorf("7z: %v", err)
	}
}

// TestLeftOut checks that files that could be run are left out of an otherwise good archive, Kora's case: a
// helper script at the top of the theme. Inspect accepts it, finds the same themes Extract writes, and nothing
// that could run is written, not through a link either.
func TestLeftOut(t *testing.T) {
	top := "kora/"
	members := []member{
		file(top+"index.theme", iconIndex("Kora")),
		file(top+"48x48/apps/firefox.svg", "<svg/>"),
		file(top+"create-new-icon-theme.cache.sh", "#!/bin/bash\ngtk-update-icon-cache -f .\n"),
		file(top+"update", "#!/usr/bin/env python3\n"),                            // a script by its first bytes
		file(top+"Makefile", "all:\n"),                                            // a build script by name
		file(top+"kora.desktop", "[Desktop Entry]\n"),                             // a launcher
		file(top+"48x48/apps/main.qml", "Item {}"),                                // QML, deep in the theme
		file(top+"src.tar.gz", "x"),                                               // an archive inside
		file(top+"setup.exe", "MZ"),                                               // Windows can't run here, and it isn't ELF
		symlink(top+"48x48/apps/run.svg", "../../create-new-icon-theme.cache.sh"), // a link to a script
		symlink(top+"48x48/apps/hidden.svg", "../../update"),                      // and to one by content
		member{name: top + "48x48/apps/hard.svg", hardlink: top + "Makefile"},
		symlink(top+"apps", "48x48/apps"),                   // a folder link: what it leads to is written, scripts not
		symlink(top+"48x48/apps/install.sh", "firefox.svg"), // a link named like a script
		file("kora-light/index.theme", iconIndex("Kora Light", "kora")),
		file("kora-light/48x48/apps/firefox.svg", "<svg light/>"),
		symlink("kora-light/48x48/apps/tool", "../../../kora/update"),
	}
	zipped := slices.DeleteFunc(slices.Clone(members), func(m member) bool { return m.hardlink != "" })
	for _, p := range []string{writeTar(t, members, false), writeZip(t, zipped)} {
		c, err := Inspect(p, DefaultLimits)
		if err != nil {
			t.Fatal(p, err)
		}
		var left []string
		for name := range c.LeftOut {
			left = append(left, name)
		}
		slices.Sort(left)
		want := []string{"kora/48x48/apps/install.sh", "kora/48x48/apps/main.qml", "kora/Makefile",
			"kora/create-new-icon-theme.cache.sh", "kora/kora.desktop", "kora/setup.exe", "kora/src.tar.gz",
			"kora/update"}
		if !slices.Equal(left, want) {
			t.Errorf("%s: LeftOut = %v, want %v", p, left, want)
		}
		var pinned []string
		for _, th := range c.Of(catalog.KindIconTheme) {
			pinned = append(pinned, th.Name)
		}
		if !slices.Equal(pinned, []string{"kora", "kora-light"}) {
			t.Fatalf("%s: themes = %v", p, pinned)
		}

		dst := t.TempDir()
		got, err := Extract(p, catalog.KindIconTheme, dst, DefaultLimits)
		if err != nil {
			t.Fatal(p, err)
		}
		var names []string
		for _, th := range got.Themes {
			names = append(names, th.Name)
		}
		if !slices.Equal(names, pinned) || !slices.Equal(got.LeftOut, want) {
			t.Errorf("%s: Extract = %v, left out %v; Inspect found %v", p, names, got.LeftOut, pinned)
		}
		var written []string
		filepath.WalkDir(dst, func(path string, d os.DirEntry, err error) error {
			if err != nil || d.IsDir() {
				return nil
			}
			rel, _ := filepath.Rel(dst, path)
			written = append(written, rel)
			b, _ := os.ReadFile(path)
			if strings.HasPrefix(string(b), "#!") || strings.HasPrefix(string(b), "MZ") || string(b) == "all:\n" {
				t.Errorf("%s: %s holds something runnable: %q", p, rel, b)
			}
			return nil
		})
		slices.Sort(written)
		wantWritten := []string{"kora-light/48x48/apps/firefox.svg", "kora-light/index.theme",
			"kora/48x48/apps/firefox.svg", "kora/apps/firefox.svg", "kora/index.theme"}
		if !slices.Equal(written, wantWritten) {
			t.Errorf("%s: wrote %v, want %v", p, written, wantWritten)
		}
	}
}

// TestBadLinksSkipped checks that links leading nowhere in the archive are left out, and that nothing is
// written for them or through them, while the rest of the theme installs.
func TestBadLinksSkipped(t *testing.T) {
	members := append(cursorTheme("Good"),
		symlink("Good/cursors/out", "../../../../etc/passwd"),
		symlink("Good/cursors/abs", "/etc/passwd"),
		symlink("Good/cursors/dangling", "nothing"),
		member{name: "Good/cursors/hardout", hardlink: "../etc/passwd"},
		symlink("Good/cursors/a", "b"), symlink("Good/cursors/b", "a"), // a loop
		symlink("Good/cursors/up", ".."),   // a folder it is in
		symlink("Good/cursors/via", "out"), // a link to a skipped link
		symlink("Good/icons", "/usr/share/icons"),
	)
	// Zip has no hard links.
	zipped := slices.DeleteFunc(slices.Clone(members), func(m member) bool { return m.hardlink != "" })
	for _, p := range []string{writeTar(t, members, false), writeZip(t, zipped)} {
		c, err := Inspect(p, DefaultLimits)
		if err != nil {
			t.Fatal(p, err)
		}
		if want := map[bool]int{true: 8, false: 9}[strings.HasSuffix(p, ".zip")]; len(c.Skipped) != want {
			t.Errorf("%s: Skipped = %v", p, c.Skipped)
		}
		dst := t.TempDir()
		if _, err := Extract(p, catalog.KindCursorTheme, dst, DefaultLimits); err != nil {
			t.Fatal(p, err)
		}
		var written []string
		filepath.WalkDir(dst, func(path string, d os.DirEntry, err error) error {
			if err == nil && !d.IsDir() {
				rel, _ := filepath.Rel(dst, path)
				written = append(written, rel)
				if fi, _ := os.Lstat(path); fi.Mode()&os.ModeSymlink != 0 {
					t.Errorf("%s: %s is a symbolic link", p, rel)
				}
			}
			return nil
		})
		slices.Sort(written)
		want := []string{"Good/cursors/default", "Good/cursors/hand1", "Good/cursors/hand2", "Good/cursors/left_ptr",
			"Good/cursors/pointer", "Good/index.theme"}
		if !slices.Equal(written, want) {
			t.Errorf("%s: wrote %v, want %v", p, written, want)
		}
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

// gtkTheme is a GTK theme in a folder as Pling's come: GTK 3 and 4 styles, with GTK 2, GNOME Shell and xfwm4
// folders and a metatheme index.theme beside them.
func gtkTheme(dir string) []member {
	return []member{
		file(dir+"/index.theme", "[Desktop Entry]\nType=X-GNOME-Metatheme\nName="+dir+"\n\n[X-GNOME-Metatheme]\nGtkTheme="+dir+"\n"),
		file(dir+"/gtk-2.0/gtkrc", "style \"x\" {}"),
		file(dir+"/gtk-3.0/gtk.css", "@import url(\"gtk-main.css\");"),
		file(dir+"/gtk-3.0/gtk-main.css", "window {}"),
		file(dir+"/gtk-3.0/assets/check.png", "PNG"),
		file(dir+"/gtk-4.0/gtk.css", "window {}"),
		file(dir+"/gnome-shell/gnome-shell.css", "stage {}"),
		file(dir+"/xfwm4/themerc", "x=1"),
		file(dir+"/assets/sed.sh", "#!/bin/sh\nsed -i s/a/b/ ../gtk-3.0/gtk.css\n"),
	}
}

func TestInspectGTKThemes(t *testing.T) {
	// Variants side by side, one's gtk-4.0 a link to another's and one with GTK 4 alone; in a zip, as many are.
	members := append(gtkTheme("Orchis"), file("Orchis-Dark/gtk-3.0/gtk-dark.css", "window {}"),
		symlink("Orchis-Dark/gtk-4.0", "../Orchis/gtk-4.0"), file("Orchis-Four/gtk-4.0/gtk.css", "window {}"),
		file("README.md", "hi"))
	p := writeZip(t, members)
	c, err := Inspect(p, DefaultLimits)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, th := range c.Of(catalog.KindGTKTheme) {
		names = append(names, th.Name)
	}
	if !slices.Equal(names, []string{"Orchis", "Orchis-Dark", "Orchis-Four"}) || len(c.Of(catalog.KindIconTheme)) != 0 {
		t.Fatalf("GTK themes = %v (%+v)", names, c.Themes)
	}
	// Extracting writes each theme's whole folder (GNOME Shell, GTK 2 and xfwm4 too), with links made files,
	// and leaves the script out.
	dst := t.TempDir()
	got, err := Extract(p, catalog.KindGTKTheme, dst, DefaultLimits)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Themes) != 3 || !slices.Equal(got.LeftOut, []string{"Orchis/assets/sed.sh"}) {
		t.Errorf("extracted %+v", got)
	}
	for _, f := range []string{"Orchis/gnome-shell/gnome-shell.css", "Orchis/gtk-2.0/gtkrc", "Orchis/xfwm4/themerc",
		"Orchis/gtk-3.0/assets/check.png", "Orchis-Dark/gtk-4.0/gtk.css", "Orchis-Four/gtk-4.0/gtk.css"} {
		fi, err := os.Lstat(filepath.Join(dst, f))
		if err != nil || !fi.Mode().IsRegular() || fi.Mode()&0o111 != 0 {
			t.Errorf("%s: %v %v", f, fi, err)
		}
	}
	if _, err := os.Lstat(filepath.Join(dst, "Orchis/assets/sed.sh")); err == nil {
		t.Error("the script was written")
	}
	if _, err := os.Lstat(filepath.Join(dst, "README.md")); err == nil {
		t.Error("a file outside the themes was written")
	}
	// An icon theme's archive holds no GTK theme, and the other way round.
	if _, err := Extract(p, catalog.KindIconTheme, t.TempDir(), DefaultLimits); !IsRefused(err) ||
		!strings.Contains(err.Error(), "no icon theme") {
		t.Errorf("icon theme from a GTK archive: %v", err)
	}
	if _, err := Extract(writeTar(t, cursorTheme("Cur"), false), catalog.KindGTKTheme, t.TempDir(), DefaultLimits); !IsRefused(err) ||
		!strings.Contains(err.Error(), "no GTK theme") {
		t.Errorf("GTK theme from a cursor archive: %v", err)
	}
}

func TestInspectGTKWithOthers(t *testing.T) {
	// A GTK theme and an icon theme side by side: each is found, and extracted for its own kind.
	p := writeTar(t, append(gtkTheme("Nordic"), file("Nordic-icons/index.theme", iconIndex("Nordic")),
		file("Nordic-icons/48x48/apps/a.svg", "<svg/>")), false)
	c, err := Inspect(p, DefaultLimits)
	if err != nil {
		t.Fatal(err)
	}
	if g, i := c.Of(catalog.KindGTKTheme), c.Of(catalog.KindIconTheme); len(g) != 1 || g[0].Name != "Nordic" ||
		len(i) != 1 || i[0].Name != "Nordic-icons" {
		t.Errorf("themes = %+v", c.Themes)
	}
	dst := t.TempDir()
	if got, err := Extract(p, catalog.KindGTKTheme, dst, DefaultLimits); err != nil || len(got.Themes) != 1 {
		t.Errorf("extract GTK: %+v %v", got, err)
	}
	if _, err := os.Stat(filepath.Join(dst, "Nordic-icons")); err == nil {
		t.Error("the icon theme was written with the GTK theme")
	}

	// An icon theme inside a GTK theme's folder isn't a theme inside another: they install to different
	// places. Installing the GTK theme writes it along with the rest of the folder.
	p = writeTar(t, append(gtkTheme("Mac"), file("Mac/icons/MacIcons/index.theme", iconIndex("MacIcons")),
		file("Mac/icons/MacIcons/48x48/apps/a.svg", "<svg/>")), false)
	if c, err := Inspect(p, DefaultLimits); err != nil || len(c.Themes) != 2 {
		t.Errorf("icon theme in a GTK theme: %v %+v", err, c)
	}

	// A folder that is both an icon theme and a GTK theme is one theme of both kinds.
	p = writeTar(t, []member{file("Both/index.theme", iconIndex("Both")), file("Both/48x48/apps/a.svg", "<svg/>"),
		file("Both/gtk-3.0/gtk.css", "window {}")}, false)
	if c, err := Inspect(p, DefaultLimits); err != nil || len(c.Themes) != 1 || !c.Themes[0].Icons || !c.Themes[0].GTK {
		t.Errorf("icon and GTK theme in one folder: %v %+v", err, c)
	}

	// An icon theme archive with a GTK layout uas can't tell apart (a gtk-3.0 at the top) keeps its icon
	// theme, as before GTK themes, and lists no GTK theme.
	p = writeTar(t, []member{file("Pap/index.theme", iconIndex("Pap")), file("Pap/48x48/apps/a.svg", "<svg/>"),
		file("gtk-3.0/gtk.css", "window {}")}, false)
	if c, err := Inspect(p, DefaultLimits); err != nil || len(c.Themes) != 1 || c.Themes[0].GTK {
		t.Errorf("icon theme with a stray gtk-3.0: %v %+v", err, c)
	}
}

func TestInspectGTKRefuses(t *testing.T) {
	for name, members := range map[string][]member{
		"at the top":           {file("gtk-3.0/gtk.css", "window {}"), file("index.theme", "[Desktop Entry]\n")},
		"built in":             gtkTheme("Adwaita"),
		"built in, other case": {file("highcontrast/gtk-3.0/gtk.css", "window {}")},
		// A theme's source code: styles to build, no gtk.css.
		"no theme": {file("Orchis-main/src/gtk-3.0/_common.scss", "window {}"),
			file("Orchis-main/src/gtk-3.0/gtk.scss", "@import 'common';"), file("Orchis-main/install.sh", "#!/bin/sh\n")},
		"a Shell theme alone": {file("Shell/gnome-shell/gnome-shell.css", "stage {}")},
		"nested":              append(gtkTheme("Outer"), file("Outer/extra/Inner/gtk-3.0/gtk.css", "window {}")),
		"gtk.css a script":    {file("Bad/gtk-3.0/gtk.css", "#!/bin/sh\n")},
		"two of a name":       {file("a/T/gtk-3.0/gtk.css", "x"), file("b/T/gtk-3.0/gtk.css", "x")},
		"gtk.css a link out":  {symlink("Out/gtk-3.0/gtk.css", "/usr/share/themes/Adwaita/gtk-3.0/gtk.css")},
	} {
		if c, err := Inspect(writeTar(t, members, false), DefaultLimits); !IsRefused(err) {
			t.Errorf("%s: %v %+v", name, err, c)
		}
	}
}
