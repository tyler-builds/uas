package appearance

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/tyler-builds/uas/internal/catalog"
	"github.com/tyler-builds/uas/internal/client/state"
)

// gtkPack is an archive of GTK themes as Pling's come: Orchis with GTK 3 and 4 styles, a GNOME Shell style
// and assets; Orchis-Dark with a gtk-dark.css too; Orchis-Three with GTK 3 alone.
func gtkPack() map[string]string {
	return map[string]string{
		"Orchis/index.theme":                    "[Desktop Entry]\nType=X-GNOME-Metatheme\nName=Orchis\n",
		"Orchis/gtk-3.0/gtk.css":                "/* orchis 3 */",
		"Orchis/gtk-4.0/gtk.css":                "/* orchis 4 */",
		"Orchis/gtk-4.0/assets/check.svg":       "<svg orchis/>",
		"Orchis/gnome-shell/gnome-shell.css":    "stage {}",
		"Orchis-Dark/gtk-3.0/gtk.css":           "/* orchis dark 3 */",
		"Orchis-Dark/gtk-4.0/gtk.css":           "/* orchis dark 4 */",
		"Orchis-Dark/gtk-4.0/gtk-dark.css":      "/* orchis dark 4, dark */",
		"Orchis-Dark/gtk-4.0/assets/check.svg":  "<svg orchis dark/>",
		"Orchis-Dark/gtk-4.0/assets/radio.svg":  "<svg radio/>",
		"Orchis-Three/gtk-3.0/gtk.css":          "/* three */",
		"Orchis-Three/gtk-3.0/assets/check.svg": "<svg three/>",
	}
}

// gtkWorld is a world with Orchis on the fake Pling, GNOME's gtk-theme set to Adwaita, a system GTK theme
// with GTK 3 alone (Yaru), one of the user's in ~/.themes, and the user's own style and settings in
// ~/.config/gtk-4.0, which uas didn't write.
func gtkWorld(t *testing.T) (*world, catalog.Listing) {
	t.Helper()
	w := newWorld(t)
	w.gs.values[KeyGTKTheme] = "Adwaita"
	a := w.offer("orchis.tar.gz", tarGz(t, gtkPack(), nil), "Orchis", "Orchis-Dark", "Orchis-Three")
	l := catalog.Listing{Source: catalog.SourcePling, Ref: "1357889", Kind: catalog.KindGTKTheme, Name: "Orchis",
		Version: "2024", Assets: []catalog.Asset{a}}
	writeFiles(t, map[string]string{
		filepath.Join(w.sysIcons, "themes", "Yaru", "gtk-3.0", "gtk.css"):      "/* yaru */",
		filepath.Join(w.sysIcons, "themes", "Yaru", "gtk-2.0", "gtkrc"):        "",
		filepath.Join(w.sysIcons, "themes", "Emacs", "gtk-2.0-key", "gtkrc"):   "", // not a GTK 3 or 4 theme
		filepath.Join(w.sysIcons, "themes", "Plain", "gtk-4.0", "gtk.css"):     "/* plain 4 */",
		filepath.Join(w.in.Home, ".themes", "Mine", "gtk-3.0", "gtk-dark.css"): "/* mine */",
		filepath.Join(w.in.GTK4Dir(), "gtk.css"):                               "/* the user's own */",
		filepath.Join(w.in.GTK4Dir(), "settings.ini"):                          "[Settings]\n",
		filepath.Join(w.in.GTK4Dir(), "colors.css"):                            "@define-color x red;",
	})
	return w, l
}

func writeFiles(t *testing.T, files map[string]string) {
	t.Helper()
	for p, body := range files {
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

func readFile(t *testing.T, p string) string {
	t.Helper()
	b, err := os.ReadFile(p)
	if err != nil {
		return "<" + err.Error() + ">"
	}
	return string(b)
}

// gtk4Names are the names in ~/.config/gtk-4.0, but uas's stage folders, which must never be left behind.
func gtk4Names(t *testing.T, in *Installer) []string {
	t.Helper()
	entries, _ := os.ReadDir(in.GTK4Dir())
	var out []string
	for _, e := range entries {
		out = append(out, e.Name())
	}
	return out
}

// userUntouched checks that the user's own files in ~/.config/gtk-4.0 that uas never replaces are as they
// were.
func userUntouched(t *testing.T, in *Installer) {
	t.Helper()
	if got := readFile(t, filepath.Join(in.GTK4Dir(), "settings.ini")); got != "[Settings]\n" {
		t.Errorf("settings.ini = %q", got)
	}
	if got := readFile(t, filepath.Join(in.GTK4Dir(), "colors.css")); got != "@define-color x red;" {
		t.Errorf("colors.css = %q", got)
	}
}

// userRestored checks that ~/.config/gtk-4.0 is as it was before uas, and uas keeps nothing of the user's
// aside.
func userRestored(t *testing.T, in *Installer) {
	t.Helper()
	userUntouched(t, in)
	if got := readFile(t, filepath.Join(in.GTK4Dir(), "gtk.css")); got != "/* the user's own */" {
		t.Errorf("gtk.css = %q", got)
	}
	if got := gtk4Names(t, in); !slices.Equal(got, []string{"colors.css", "gtk.css", "settings.ini"}) {
		t.Errorf("~/.config/gtk-4.0 holds %v", got)
	}
	if g, _ := in.GTK4(); g != nil {
		t.Errorf("GTK 4 record left: %+v", g)
	}
	if left, _ := filepath.Glob(filepath.Join(in.Data, "uas", "gtk-4.0-yours-*")); len(left) != 0 {
		t.Errorf("backup folders left: %v", left)
	}
}

func TestGTKThemeInstallAndScan(t *testing.T) {
	w, l := gtkWorld(t)
	ctx := context.Background()
	rec, err := w.in.Install(ctx, l, "Orchis-Dark", nil)
	if err != nil {
		t.Fatal(err)
	}
	// Every theme of the file, whole (the GNOME Shell style too), in ~/.local/share/themes.
	if !slices.Equal(rec.Themes, []string{"Orchis", "Orchis-Dark", "Orchis-Three"}) || rec.Kind != "gtk-theme" ||
		len(rec.Paths) != 3 || filepath.Dir(rec.Paths[0]) != w.in.ThemesDir() {
		t.Fatalf("record = %+v", rec)
	}
	if got := readFile(t, filepath.Join(w.in.ThemesDir(), "Orchis", "gnome-shell", "gnome-shell.css")); got != "stage {}" {
		t.Errorf("gnome-shell.css = %q", got)
	}
	if _, err := os.Stat(filepath.Join(w.in.IconsDir(), "Orchis")); err == nil {
		t.Error("a GTK theme went into the icons folder")
	}
	// Installing changes nothing in ~/.config/gtk-4.0: only using does.
	if got := readFile(t, filepath.Join(w.in.GTK4Dir(), "gtk.css")); got != "/* the user's own */" {
		t.Errorf("gtk.css after install = %q", got)
	}

	// The system's GTK themes, the user's and uas's, with who installed them; not the icon themes.
	themes, err := w.in.GTKThemes()
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, th := range themes {
		s := th.Name
		if th.GTK4 {
			s += "+4"
		}
		switch {
		case th.By != "":
			s += "/" + th.By
		case th.User:
			s += "/user"
		}
		got = append(got, s)
	}
	want := []string{"Mine/user", "Orchis+4/pling:1357889", "Orchis-Dark+4/pling:1357889", "Orchis-Three/pling:1357889",
		"Plain+4", "Yaru"}
	if !slices.Equal(got, want) {
		t.Errorf("GTKThemes = %v\nwant %v", got, want)
	}
	icons, _ := w.in.Themes()
	for _, th := range icons {
		if strings.HasPrefix(th.Name, "Orchis") {
			t.Errorf("Themes lists %s", th.Name)
		}
	}

	// A system theme can be used whoever installed it: it has no GTK 4 files to copy.
	if err := w.in.Use(ctx, KeyGTKTheme, "Yaru"); err != nil {
		t.Fatal(err)
	}
	if w.gs.values[KeyGTKTheme] != "Yaru" {
		t.Errorf("gtk-theme = %q", w.gs.values[KeyGTKTheme])
	}
	userRestored(t, w.in)
}

func TestGTKThemeInstallRefuses(t *testing.T) {
	ctx := context.Background()
	for name, setup := range map[string]func(w *world){
		"a theme uas didn't install": func(w *world) {
			os.MkdirAll(filepath.Join(w.in.ThemesDir(), "Orchis-Dark"), 0o755)
		},
		"hides one in ~/.themes": func(w *world) {
			os.MkdirAll(filepath.Join(w.in.Home, ".themes", "Orchis"), 0o755)
		},
		"hides the system's": func(w *world) {
			os.MkdirAll(filepath.Join(w.sysIcons, "themes", "Orchis-Three"), 0o755)
		},
	} {
		t.Run(name, func(t *testing.T) {
			w, l := gtkWorld(t)
			setup(w)
			if _, err := w.in.Install(ctx, l, "", nil); err == nil {
				t.Fatal("installed")
			}
			if len(w.transp.hits) != 0 {
				t.Errorf("downloaded before refusing: %v", w.transp.hits)
			}
			if recs, _ := w.in.Records(); len(recs) != 0 {
				t.Errorf("recorded %+v", recs)
			}
		})
	}
	// Icon themes and GTK themes have folders of their own: an icon theme called Orchis, installed by uas or
	// not, doesn't stand in a GTK theme's way.
	w, l := gtkWorld(t)
	os.MkdirAll(filepath.Join(w.in.IconsDir(), "Orchis", "cursors"), 0o755)
	st, _ := w.in.State.Load()
	st.PutAppearance(state.AppearanceInstall{ID: "pling:1", Kind: "icon-theme", Name: "Orchis icons",
		Themes: []string{"Orchis-Dark"}})
	w.in.State.Save(st)
	if _, err := w.in.Install(ctx, l, "", nil); err != nil {
		t.Errorf("Install beside an icon theme of the same name: %v", err)
	}
}

func TestGNOMEGTKKey(t *testing.T) {
	ctx := context.Background()
	gs := &fakeGSettings{values: map[string]string{KeyGTKTheme: "Adwaita"}}
	g := &GNOME{Runner: gs}
	if !g.Supports(KeyGTKTheme) || (&Plasma{Runner: gs}).Supports(KeyGTKTheme) {
		t.Error("GNOME sets the GTK theme; Plasma installs only")
	}
	if v, err := g.Get(ctx, KeyGTKTheme); err != nil || v != "Adwaita" {
		t.Errorf("Get = %q, %v", v, err)
	}
	if err := g.Set(ctx, KeyGTKTheme, "Marwaita Alt OSX"); err != nil {
		t.Fatal(err)
	}
	if err := g.Set(ctx, KeyGTKTheme, `It's\here`); err != nil {
		t.Fatal(err)
	}
	if v, _ := g.Get(ctx, KeyGTKTheme); v != `It's\here` {
		t.Errorf("Get after a quoted set = %q", v)
	}
	if err := g.Reset(ctx, KeyGTKTheme); err != nil {
		t.Fatal(err)
	}
	want := []string{
		"gsettings get org.gnome.desktop.interface gtk-theme",
		"gsettings set org.gnome.desktop.interface gtk-theme 'Marwaita Alt OSX'",
		`gsettings set org.gnome.desktop.interface gtk-theme 'It\'s\\here'`,
		"gsettings get org.gnome.desktop.interface gtk-theme",
		"gsettings reset org.gnome.desktop.interface gtk-theme",
	}
	if !slices.Equal(gs.calls, want) {
		t.Errorf("calls =\n%s\nwant\n%s", strings.Join(gs.calls, "\n"), strings.Join(want, "\n"))
	}
	if KeyLabel(KeyGTKTheme) != "GTK theme" || KeyPart(KeyGTKTheme) != PartGTK || PartLabel(PartGTK) != "GTK theme" {
		t.Error("labels")
	}
	if k, ok := PartKey(PartGTK); !ok || k != KeyGTKTheme {
		t.Error("the gtk part isn't the gtk-theme setting")
	}
	if _, ok := PartKey(PartShell); ok {
		t.Error("Shell themes are set by a later uas")
	}
}

func TestGTKUseCopiesGTK4Files(t *testing.T) {
	w, l := gtkWorld(t)
	ctx := context.Background()
	if _, err := w.in.Install(ctx, l, "", nil); err != nil {
		t.Fatal(err)
	}
	dir := w.in.GTK4Dir()

	// First use: the user's gtk.css is moved aside and kept, the theme's files are copied in, the rest is
	// left alone.
	if err := w.in.Use(ctx, KeyGTKTheme, "Orchis-Dark"); err != nil {
		t.Fatal(err)
	}
	if w.gs.values[KeyGTKTheme] != "Orchis-Dark" {
		t.Errorf("gtk-theme = %q", w.gs.values[KeyGTKTheme])
	}
	for name, want := range map[string]string{"gtk.css": "/* orchis dark 4 */", "gtk-dark.css": "/* orchis dark 4, dark */",
		"assets/check.svg": "<svg orchis dark/>", "assets/radio.svg": "<svg radio/>"} {
		if got := readFile(t, filepath.Join(dir, name)); got != want {
			t.Errorf("%s = %q, want %q", name, got, want)
		}
	}
	if fi, err := os.Lstat(filepath.Join(dir, "gtk.css")); err != nil || !fi.Mode().IsRegular() {
		t.Errorf("gtk.css isn't a copy: %v %v", fi, err)
	}
	userUntouched(t, w.in)
	g, _ := w.in.GTK4()
	if g == nil || g.Theme != "Orchis-Dark" || !slices.Equal(g.Written, []string{"assets", "gtk-dark.css", "gtk.css"}) ||
		!slices.Equal(g.BackedUp, []string{"gtk.css"}) || readFile(t, filepath.Join(g.Backup, "gtk.css")) != "/* the user's own */" {
		t.Fatalf("GTK 4 record = %+v", g)
	}
	if !strings.HasPrefix(g.Backup, filepath.Join(w.in.Data, "uas")+string(filepath.Separator)) {
		t.Errorf("backup in %s", g.Backup)
	}
	if s, ok := savedKey(w.in, KeyGTKTheme); !ok || s.Previous != "Adwaita" || s.Value != "Orchis-Dark" {
		t.Errorf("saved = %+v", s)
	}

	// Another theme: uas's files are replaced, what the new theme lacks (gtk-dark.css, radio.svg) goes, and
	// the user's gtk.css stays aside.
	if err := w.in.Use(ctx, KeyGTKTheme, "Orchis"); err != nil {
		t.Fatal(err)
	}
	if got := readFile(t, filepath.Join(dir, "gtk.css")); got != "/* orchis 4 */" {
		t.Errorf("gtk.css = %q", got)
	}
	for _, gone := range []string{"gtk-dark.css", "assets/radio.svg"} {
		if _, err := os.Lstat(filepath.Join(dir, gone)); err == nil {
			t.Errorf("%s of the earlier theme is left", gone)
		}
	}
	if got := gtk4Names(t, w.in); !slices.Equal(got, []string{"assets", "colors.css", "gtk.css", "settings.ini"}) {
		t.Errorf("~/.config/gtk-4.0 holds %v", got)
	}
	userUntouched(t, w.in)

	// A theme without GTK 4 files: uas's go and the user's come back, while the setting stays changed.
	if err := w.in.Use(ctx, KeyGTKTheme, "Orchis-Three"); err != nil {
		t.Fatal(err)
	}
	userRestored(t, w.in)
	if s, ok := savedKey(w.in, KeyGTKTheme); !ok || s.Previous != "Adwaita" || s.Value != "Orchis-Three" {
		t.Errorf("saved = %+v", s)
	}

	// Revert: the setting and the folder are as they were before uas.
	if err := w.in.Use(ctx, KeyGTKTheme, "Orchis-Dark"); err != nil {
		t.Fatal(err)
	}
	if err := w.in.Revert(ctx, KeyGTKTheme); err != nil {
		t.Fatal(err)
	}
	if w.gs.values[KeyGTKTheme] != "Adwaita" {
		t.Errorf("after Revert, gtk-theme = %q", w.gs.values[KeyGTKTheme])
	}
	userRestored(t, w.in)
	if _, ok := savedKey(w.in, KeyGTKTheme); ok {
		t.Error("still saved after Revert")
	}

	// Revert all does the same.
	w.in.Use(ctx, KeyGTKTheme, "Orchis")
	w.in.Use(ctx, KeyIconTheme, "Adwaita")
	if err := w.in.RevertAll(ctx); err != nil {
		t.Fatal(err)
	}
	if w.gs.values[KeyGTKTheme] != "Adwaita" {
		t.Errorf("after Revert all, gtk-theme = %q", w.gs.values[KeyGTKTheme])
	}
	userRestored(t, w.in)

	// Removing the theme in use reverts first.
	w.in.Use(ctx, KeyGTKTheme, "Orchis-Dark")
	if err := w.in.Remove(ctx, "pling:1357889", nil); err != nil {
		t.Fatal(err)
	}
	if w.gs.values[KeyGTKTheme] != "Adwaita" {
		t.Errorf("after Remove, gtk-theme = %q", w.gs.values[KeyGTKTheme])
	}
	userRestored(t, w.in)
	if _, err := os.Stat(filepath.Join(w.in.ThemesDir(), "Orchis")); err == nil {
		t.Error("the theme is still installed")
	}
}

func TestGTK4FilesEdges(t *testing.T) {
	ctx := context.Background()

	t.Run("no folder of the user's", func(t *testing.T) {
		w, l := gtkWorld(t)
		os.RemoveAll(w.in.GTK4Dir())
		w.in.Install(ctx, l, "", nil)
		if err := w.in.Use(ctx, KeyGTKTheme, "Orchis"); err != nil {
			t.Fatal(err)
		}
		if g, _ := w.in.GTK4(); g == nil || g.Backup != "" || len(g.BackedUp) != 0 {
			t.Errorf("record = %+v", g)
		}
		if err := w.in.Revert(ctx, KeyGTKTheme); err != nil {
			t.Fatal(err)
		}
		if got := gtk4Names(t, w.in); len(got) != 0 {
			t.Errorf("left %v", got)
		}
	})

	t.Run("the user's style is a link", func(t *testing.T) {
		// As a theme's install script leaves it: links to a theme of the user's.
		w, l := gtkWorld(t)
		dir := w.in.GTK4Dir()
		os.Remove(filepath.Join(dir, "gtk.css"))
		os.Symlink("../../.themes/Mine/gtk-4.0/gtk.css", filepath.Join(dir, "gtk.css"))
		os.Symlink("/nowhere/assets", filepath.Join(dir, "assets"))
		w.in.Install(ctx, l, "", nil)
		if err := w.in.Use(ctx, KeyGTKTheme, "Orchis"); err != nil {
			t.Fatal(err)
		}
		if got := readFile(t, filepath.Join(dir, "assets", "check.svg")); got != "<svg orchis/>" {
			t.Errorf("check.svg = %q", got)
		}
		if err := w.in.Revert(ctx, KeyGTKTheme); err != nil {
			t.Fatal(err)
		}
		for name, want := range map[string]string{"gtk.css": "../../.themes/Mine/gtk-4.0/gtk.css", "assets": "/nowhere/assets"} {
			if got, err := os.Readlink(filepath.Join(dir, name)); err != nil || got != want {
				t.Errorf("%s links to %q, %v", name, got, err)
			}
		}
		userUntouched(t, w.in)
	})

	t.Run("set outside uas", func(t *testing.T) {
		// The setting already names the theme: using it copies its files all the same, and Revert takes them
		// away, leaving the setting as it was.
		w, l := gtkWorld(t)
		w.in.Install(ctx, l, "", nil)
		w.gs.values[KeyGTKTheme] = "Orchis"
		if err := w.in.Use(ctx, KeyGTKTheme, "Orchis"); err != nil {
			t.Fatal(err)
		}
		if got := readFile(t, filepath.Join(w.in.GTK4Dir(), "gtk.css")); got != "/* orchis 4 */" {
			t.Errorf("gtk.css = %q", got)
		}
		if s, ok := savedKey(w.in, KeyGTKTheme); !ok || s.Previous != "Orchis" {
			t.Errorf("saved = %+v %v", s, ok)
		}
		if err := w.in.Revert(ctx, KeyGTKTheme); err != nil {
			t.Fatal(err)
		}
		if w.gs.values[KeyGTKTheme] != "Orchis" {
			t.Errorf("gtk-theme = %q", w.gs.values[KeyGTKTheme])
		}
		userRestored(t, w.in)
	})

	t.Run("removed after the setting changed", func(t *testing.T) {
		// The GTK theme was changed outside uas since; removing the theme still takes its files away.
		w, l := gtkWorld(t)
		w.in.Install(ctx, l, "", nil)
		w.in.Use(ctx, KeyGTKTheme, "Orchis")
		w.gs.values[KeyGTKTheme] = "Yaru"
		if err := w.in.Remove(ctx, "pling:1357889", nil); err != nil {
			t.Fatal(err)
		}
		userRestored(t, w.in)
	})

	t.Run("a theme outside uas's folder", func(t *testing.T) {
		// The system's Plain has GTK 4 files: they are copied like any.
		w, _ := gtkWorld(t)
		if err := w.in.Use(ctx, KeyGTKTheme, "Plain"); err != nil {
			t.Fatal(err)
		}
		if got := readFile(t, filepath.Join(w.in.GTK4Dir(), "gtk.css")); got != "/* plain 4 */" {
			t.Errorf("gtk.css = %q", got)
		}
		w.in.Revert(ctx, KeyGTKTheme)
		userRestored(t, w.in)
	})

	t.Run("a file uas didn't write is never deleted", func(t *testing.T) {
		// A gtk-dark.css of the user's appears after uas copied Orchis (which has none). Switching to
		// Orchis-Dark, which has one, moves it aside; going back puts it back. A damaged record that names
		// paths elsewhere leads uas nowhere.
		w, l := gtkWorld(t)
		w.in.Install(ctx, l, "", nil)
		dir := w.in.GTK4Dir()
		w.in.Use(ctx, KeyGTKTheme, "Orchis")
		writeFiles(t, map[string]string{filepath.Join(dir, "gtk-dark.css"): "/* the user's dark */"})
		w.in.Use(ctx, KeyGTKTheme, "Orchis-Dark")
		if got := readFile(t, filepath.Join(dir, "gtk-dark.css")); got != "/* orchis dark 4, dark */" {
			t.Errorf("gtk-dark.css = %q", got)
		}
		w.in.Use(ctx, KeyGTKTheme, "Orchis")
		if got := readFile(t, filepath.Join(dir, "gtk-dark.css")); got != "/* the user's dark */" {
			t.Errorf("gtk-dark.css after switching back = %q", got)
		}
		st, _ := w.in.State.Load()
		st.GTK4.Written = append(st.GTK4.Written, "../settings.ini", "/etc", "")
		w.in.State.Save(st)
		if err := w.in.Revert(ctx, KeyGTKTheme); err != nil {
			t.Fatal(err)
		}
		if got := readFile(t, filepath.Join(dir, "gtk-dark.css")); got != "/* the user's dark */" {
			t.Errorf("gtk-dark.css after Revert = %q", got)
		}
		userUntouched(t, w.in)
		if got := readFile(t, filepath.Join(dir, "gtk.css")); got != "/* the user's own */" {
			t.Errorf("gtk.css = %q", got)
		}
	})
}

func TestGTKPreset(t *testing.T) {
	w, l := gtkWorld(t)
	ctx := context.Background()
	store := testPresets(t)
	lookup := func(id string) (catalog.Listing, bool) { return l, id == "pling:1357889" }

	// Capture: the GTK theme, with where uas installed it from.
	if _, err := w.in.Install(ctx, l, "", nil); err != nil {
		t.Fatal(err)
	}
	if err := w.in.Use(ctx, KeyGTKTheme, "Orchis-Dark"); err != nil {
		t.Fatal(err)
	}
	parts, err := w.in.Capture(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if p := parts[PartGTK]; p.Value != "Orchis-Dark" || p.From == nil || p.From.ID != "pling:1357889" ||
		p.From.Variant != "Orchis-Dark" || p.GTK4 != nil {
		t.Errorf("captured %+v", p)
	}
	w.in.RevertAll(ctx)
	w.in.Remove(ctx, "pling:1357889", nil)

	// Plan: install from the catalog first; on the system; already so; nowhere to get it from.
	plan := func(value string, from *Origin) Step {
		t.Helper()
		items, err := w.in.PlanPreset(ctx, Preset{Name: "x", Parts: map[string]Part{PartGTK: {Value: value, From: from}}}, lookup)
		if err != nil || len(items) != 1 {
			t.Fatalf("plan = %v %v", items, err)
		}
		return items[0].Step
	}
	for _, c := range []struct {
		value string
		from  *Origin
		want  Step
	}{
		{"Orchis-Dark", &Origin{ID: "pling:1357889", Variant: "Orchis-Dark"}, StepInstall},
		{"Yaru", nil, StepSet},
		{"Mine", nil, StepSet},
		{"HighContrast", nil, StepSet}, // built into GTK
		{"Adwaita", nil, StepSame},
		{"Nordic", nil, StepMissing},
		{"Nordic", &Origin{ID: "pling:404"}, StepMissing},
	} {
		if got := plan(c.value, c.from); got != c.want {
			t.Errorf("%s: %s, want %s", c.value, got, c.want)
		}
	}
	// The setting already names a theme with GTK 4 files uas hasn't copied: applying copies them.
	w.gs.values[KeyGTKTheme] = "Plain"
	if got := plan("Plain", nil); got != StepSet {
		t.Errorf("Plain set outside uas: %s", got)
	}
	w.gs.values[KeyGTKTheme] = "Adwaita"

	// Apply: installed first, set after the accent and before the cursor, files copied; then Undo puts
	// everything back, the user's gtk.css too.
	p := Preset{Name: "Night", Parts: map[string]Part{
		PartGTK:    {Value: "Orchis-Dark", From: &Origin{ID: "pling:1357889", Variant: "Orchis-Dark"}},
		PartAccent: {Value: "teal"},
		PartCursor: {Value: "Adwaita"},
	}}
	w.gs.values[KeyAccentColor] = "blue"
	w.gs.values[KeyCursorTheme] = "Bibata" // so the cursor is set
	items, err := w.in.PlanPreset(ctx, p, lookup)
	if err != nil {
		t.Fatal(err)
	}
	w.gs.calls = nil
	out, err := w.in.ApplyPreset(ctx, store, p, items, ApplyOptions{})
	if err != nil || Failed(out) != nil {
		t.Fatalf("apply: %v %v", err, Failed(out))
	}
	var sets []string
	for _, c := range w.gs.calls {
		if strings.HasPrefix(c, "gsettings set ") {
			sets = append(sets, strings.Fields(c)[3])
		}
	}
	if !slices.Equal(sets, []string{"accent-color", "gtk-theme", "cursor-theme"}) {
		t.Errorf("set in order %v", sets)
	}
	if got := readFile(t, filepath.Join(w.in.GTK4Dir(), "gtk.css")); got != "/* orchis dark 4 */" {
		t.Errorf("gtk.css = %q", got)
	}
	if !InUse(p, w.in.Current(ctx)) {
		t.Error("not in use after applying")
	}
	u, ok, _ := store.Undo()
	if !ok || u.Look.Parts[PartGTK].Value != "Adwaita" || u.Look.Parts[PartGTK].GTK4 == nil || *u.Look.Parts[PartGTK].GTK4 {
		t.Fatalf("undo look = %+v", u.Look.Parts)
	}
	if _, out, err := w.in.UndoPreset(ctx, store); err != nil || Failed(out) != nil {
		t.Fatalf("undo: %v %v", err, Failed(out))
	}
	if w.gs.values[KeyGTKTheme] != "Adwaita" || w.gs.values[KeyAccentColor] != "blue" {
		t.Errorf("after undo, gtk-theme %q accent %q", w.gs.values[KeyGTKTheme], w.gs.values[KeyAccentColor])
	}
	userRestored(t, w.in)
	if InUse(p, w.in.Current(ctx)) {
		t.Error("in use after undo")
	}

	// From one GTK theme to another and back: Undo copies the first theme's files back.
	w.in.Use(ctx, KeyGTKTheme, "Orchis")
	p2 := Preset{Name: "Plain", Parts: map[string]Part{PartGTK: {Value: "Plain"}}}
	items, _ = w.in.PlanPreset(ctx, p2, lookup)
	if out, err := w.in.ApplyPreset(ctx, store, p2, items, ApplyOptions{}); err != nil || Failed(out) != nil {
		t.Fatal(err, Failed(out))
	}
	if got := readFile(t, filepath.Join(w.in.GTK4Dir(), "gtk.css")); got != "/* plain 4 */" {
		t.Errorf("gtk.css = %q", got)
	}
	if _, out, err := w.in.UndoPreset(ctx, store); err != nil || Failed(out) != nil {
		t.Fatal(err, Failed(out))
	}
	if got := readFile(t, filepath.Join(w.in.GTK4Dir(), "gtk.css")); got != "/* orchis 4 */" || w.gs.values[KeyGTKTheme] != "Orchis" {
		t.Errorf("after undo, gtk.css %q, gtk-theme %q", got, w.gs.values[KeyGTKTheme])
	}
	w.in.Revert(ctx, KeyGTKTheme)
	userRestored(t, w.in)
}

func TestGTKPresetFiles(t *testing.T) {
	// A file from before GTK themes, and one with a gtk part as the format reserved it (a value, and a field
	// a later uas may add), read and written back as they were; the gtk part is now a GTK theme.
	s := testPresets(t)
	os.MkdirAll(s.Dir, 0o755)
	files := map[string]string{
		"old.json": `{"uas_preset":1,"name":"Old","created_at":"2026-11-01T20:00:00Z","updated_at":"2026-11-01T20:00:00Z",` +
			`"parts":{"icons":{"value":"Papirus"}}}`,
		"gtk.json": `{"uas_preset":1,"name":"Reserved","created_at":"2026-11-01T20:00:00Z","updated_at":"2026-11-01T20:00:00Z",` +
			`"parts":{"gtk":{"value":"Orchis-Dark","from":{"id":"pling:1357889","variant":"Orchis-Dark"},"later":{"x":1}},` +
			`"shell":{"value":"Orchis"}}}`,
		"undo.json": `{"uas_preset":1,"name":"Undo","created_at":"2026-11-01T20:00:00Z","updated_at":"2026-11-01T20:00:00Z",` +
			`"parts":{"gtk":{"value":"Adwaita","gtk4":false}}}`,
	}
	for name, body := range files {
		path := filepath.Join(s.Dir, name)
		os.WriteFile(path, []byte(body), 0o644)
		p, err := readPreset(path)
		if err != nil {
			t.Fatal(err)
		}
		if err := s.write(&p); err != nil {
			t.Fatal(err)
		}
		var a, z any
		b, _ := os.ReadFile(path)
		json.Unmarshal(b, &a)
		json.Unmarshal([]byte(body), &z)
		ab, _ := json.Marshal(a)
		zb, _ := json.Marshal(z)
		if string(ab) != string(zb) {
			t.Errorf("%s rewritten as\n%s", name, b)
		}
	}
	p, _ := s.Get("Reserved")
	if !slices.Equal(p.PartNames(), []string{PartGTK, PartShell}) || p.Parts[PartGTK].Value != "Orchis-Dark" {
		t.Errorf("Reserved = %+v", p.Parts)
	}
	w, l := gtkWorld(t)
	items, err := w.in.PlanPreset(context.Background(), p, func(id string) (catalog.Listing, bool) { return l, true })
	if err != nil || len(items) != 2 || items[0].Step != StepInstall || items[1].Step != StepUnknown {
		t.Errorf("plan = %+v %v", items, err)
	}
	if p, _ := s.Get("Old"); p.Sets(PartGTK) {
		t.Error("an old preset sets the GTK theme")
	}
}

func TestGTKUpdateRecopies(t *testing.T) {
	// An update of the theme whose GTK 4 files are in use copies them again; the user's file stays aside.
	w, l := gtkWorld(t)
	ctx := context.Background()
	w.in.Install(ctx, l, "", nil)
	w.in.Use(ctx, KeyGTKTheme, "Orchis")
	files := gtkPack()
	files["Orchis/gtk-4.0/gtk.css"] = "/* orchis 4, new */"
	a := w.offer("orchis.tar.gz", tarGz(t, files, nil), "Orchis", "Orchis-Dark", "Orchis-Three")
	l.Assets = []catalog.Asset{a}
	if _, err := w.in.Install(ctx, l, "Orchis", nil); err != nil {
		t.Fatal(err)
	}
	if got := readFile(t, filepath.Join(w.in.GTK4Dir(), "gtk.css")); got != "/* orchis 4, new */" {
		t.Errorf("gtk.css = %q", got)
	}
	w.in.Revert(ctx, KeyGTKTheme)
	userRestored(t, w.in)
}
