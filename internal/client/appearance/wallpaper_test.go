package appearance

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/tyler-builds/uas/internal/client/state"
)

type stateSaved = state.SavedSetting

// writePic writes a file, making its folder.
func writePic(t *testing.T, path, body string) string {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func sha(body string) string {
	s := sha256.Sum256([]byte(body))
	return hex.EncodeToString(s[:])
}

// wallWorld is a world with presets, whose wallpaper store PruneWallpapers may clean, and a few pictures: the
// user's (in their home) and the system's.
func wallWorld(t *testing.T) (*world, *Presets, string, string) {
	w := newWorld(t)
	store := testPresets(t)
	w.in.Presets = store
	home := w.in.Home
	sys := filepath.Join(w.sysIcons, "backgrounds")
	return w, store, home, sys
}

func TestWallpaperStore(t *testing.T) {
	w, _, home, sys := wallWorld(t)
	in := w.in
	beach := writePic(t, filepath.Join(home, "Pictures", "Beach day.JPG"), "beach")
	ref, err := in.AddWallpaper(beach)
	if err != nil {
		t.Fatal(err)
	}
	if ref != "sha256:"+sha("beach") {
		t.Errorf("ref = %q", ref)
	}
	stored := filepath.Join(in.Data, "uas", "wallpapers", sha("beach")+".jpg")
	if b, err := os.ReadFile(stored); err != nil || string(b) != "beach" {
		t.Fatalf("stored %q, %v", b, err)
	}
	// The same picture again, under another name and kind, is the same stored file.
	again := writePic(t, filepath.Join(home, "Downloads", "copy.jpeg"), "beach")
	if ref2, err := in.AddWallpaper(again); err != nil || ref2 != ref {
		t.Errorf("again: %q, %v", ref2, err)
	}
	if entries, _ := os.ReadDir(in.WallpaperDir()); len(entries) != 1 {
		t.Errorf("store has %d files", len(entries))
	}
	// Moving or deleting the original doesn't matter.
	os.Remove(beach)
	os.Remove(again)
	if p, ok := in.WallpaperPath(ref); !ok || p != stored {
		t.Errorf("WallpaperPath = %q, %v", p, ok)
	}
	// A picture already in the store is named by its hash; the system's by their path, never copied.
	if r, err := in.WallpaperRef(stored); err != nil || r != ref {
		t.Errorf("stored ref = %q, %v", r, err)
	}
	adwaita := writePic(t, filepath.Join(sys, "gnome", "adwaita-l.jxl"), "adwaita")
	slideshow := writePic(t, filepath.Join(sys, "f45", "f45.xml"), "<background/>")
	for _, p := range []string{adwaita, slideshow} {
		if r, err := in.WallpaperRef(p); err != nil || r != p {
			t.Errorf("system ref of %s = %q, %v", p, r, err)
		}
	}
	if entries, _ := os.ReadDir(in.WallpaperDir()); len(entries) != 1 {
		t.Errorf("the system's pictures were copied: %d files", len(entries))
	}
	// Every kind it takes, and nothing else.
	for _, ext := range []string{"jpg", "jpeg", "png", "webp", "jxl", "svg", "PNG"} {
		if _, err := in.AddWallpaper(writePic(t, filepath.Join(home, "p."+ext), ext)); err != nil {
			t.Errorf("%s: %v", ext, err)
		}
	}
	for _, name := range []string{"p.gif", "p.xml", "noext", "p.sh"} {
		if _, err := in.AddWallpaper(writePic(t, filepath.Join(home, name), "x")); err == nil {
			t.Errorf("stored %s", name)
		}
	}
	if _, err := in.AddWallpaper(filepath.Join(home, "gone.png")); err == nil {
		t.Error("stored a picture that isn't there")
	}
	if _, ok := in.WallpaperPath("sha256:" + sha("nope")); ok {
		t.Error("found a picture never stored")
	}
	if _, ok := in.WallpaperPath("sha256:../../etc/passwd"); ok {
		t.Error("a hash went outside the store")
	}

	// The store is in $XDG_DATA_HOME.
	data := t.TempDir()
	t.Setenv("XDG_DATA_HOME", data)
	t.Setenv("HOME", t.TempDir())
	real, err := New(w.gs, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got := real.WallpaperDir(); got != filepath.Join(data, "uas", "wallpapers") {
		t.Errorf("WallpaperDir = %s", got)
	}
}

func TestPictureURIs(t *testing.T) {
	for path, uri := range map[string]string{
		"/home/ana/Pictures/Beach day.jpg":         "file:///home/ana/Pictures/Beach%20day.jpg",
		"/home/ana/Bilder/Grüße ☃.png":             "file:///home/ana/Bilder/Gr%C3%BC%C3%9Fe%20%E2%98%83.png",
		"/home/ana/#1 at 100% & more?.webp":        "file:///home/ana/%231%20at%20100%25%20&%20more%3F.webp",
		"/usr/share/backgrounds/gnome/adwaita.jxl": "file:///usr/share/backgrounds/gnome/adwaita.jxl",
		"/tmp/it's [x].png":                        "file:///tmp/it's%20%5Bx%5D.png",
	} {
		if got := PathURI(path); got != uri {
			t.Errorf("PathURI(%q) = %q, want %q", path, got, uri)
		}
		if got := URIPath(uri); got != path {
			t.Errorf("URIPath(%q) = %q, want %q", uri, got, path)
		}
	}
	for _, v := range []string{"", "resource:///org/gnome/x.png", "https://example.com/a.png"} {
		if PathURI(v) != v || URIPath(v) != v {
			t.Errorf("%q changed", v)
		}
	}
	if got := URIPath("file://localhost/a%20b.png"); got != "/a b.png" {
		t.Errorf("localhost: %q", got)
	}
	if got := URIPath("file://elsewhere/a.png"); got != "file://elsewhere/a.png" {
		t.Errorf("another host: %q", got)
	}
}

func TestUseWallpaper(t *testing.T) {
	w, _, home, sys := wallWorld(t)
	ctx := context.Background()
	in := w.in
	mine := writePic(t, filepath.Join(home, ".local", "share", "backgrounds", "mine.png"), "mine")
	w.gs.values["bg picture-uri"] = PathURI(mine)
	w.gs.values["bg picture-uri-dark"] = PathURI(mine)
	w.gs.values["bg picture-options"] = "scaled"
	w.gs.values["lock picture-uri"] = PathURI(mine)
	if cur, err := in.CurrentWallpaper(ctx); err != nil || cur != (Wallpaper{mine, mine, "scaled"}) {
		t.Fatalf("CurrentWallpaper = %+v, %v", cur, err)
	}

	// One picture of the user's, in a folder with a space: copied into the store, and used for both styles
	// and the lock screen.
	pic := writePic(t, filepath.Join(home, "My Pictures", "Fjord #2.jpg"), "fjord")
	w.gs.calls = nil
	if err := in.UseWallpaper(ctx, Wallpaper{Light: pic, Fit: FitZoom}); err != nil {
		t.Fatal(err)
	}
	stored := filepath.Join(in.WallpaperDir(), sha("fjord")+".jpg")
	uri := PathURI(stored)
	for key, want := range map[string]string{"bg picture-uri": uri, "bg picture-uri-dark": uri,
		"lock picture-uri": uri, "bg picture-options": "zoom"} {
		if got := w.gs.values[key]; got != want {
			t.Errorf("%s = %q, want %q", key, got, want)
		}
	}
	var sets []string
	for _, c := range w.gs.calls {
		if strings.HasPrefix(c, "gsettings set ") {
			f := strings.Fields(c)
			sets = append(sets, f[2]+" "+f[3])
		}
	}
	want := []string{"org.gnome.desktop.background picture-uri", "org.gnome.desktop.screensaver picture-uri",
		"org.gnome.desktop.background picture-uri-dark", "org.gnome.desktop.background picture-options"}
	if !slices.Equal(sets, want) {
		t.Errorf("sets = %v", sets)
	}
	cur := in.Current(ctx)
	if cur[KeyWallpaper] != stored || cur[KeyWallpaperDark] != stored || cur[KeyWallpaperFit] != FitZoom {
		t.Errorf("Current = %v", cur)
	}
	// A light and a dark picture, the fit left as it is.
	light := writePic(t, filepath.Join(sys, "gnome", "blobs-l.svg"), "<svg/>")
	dark := writePic(t, filepath.Join(sys, "gnome", "blobs-d.svg"), "<svg dark/>")
	if err := in.UseWallpaper(ctx, Wallpaper{Light: light, Dark: dark}); err != nil {
		t.Fatal(err)
	}
	if w.gs.values["bg picture-uri"] != PathURI(light) || w.gs.values["bg picture-uri-dark"] != PathURI(dark) ||
		w.gs.values["lock picture-uri"] != PathURI(light) || w.gs.values["bg picture-options"] != "zoom" {
		t.Errorf("values = %v", w.gs.values)
	}
	if err := in.UseWallpaper(ctx, Wallpaper{Light: light, Fit: "sideways"}); err == nil {
		t.Error("used a fit GNOME doesn't have")
	}

	// Revert puts the whole wallpaper back as one: pictures, lock screen and fit.
	saved, _ := in.Saved()
	if len(saved) != 3 {
		t.Errorf("saved = %+v", saved)
	}
	if err := in.Revert(ctx, KeyWallpaper); err != nil {
		t.Fatal(err)
	}
	for key, want := range map[string]string{"bg picture-uri": PathURI(mine), "bg picture-uri-dark": PathURI(mine),
		"lock picture-uri": PathURI(mine), "bg picture-options": "scaled"} {
		if got := w.gs.values[key]; got != want {
			t.Errorf("after revert, %s = %q, want %q", key, got, want)
		}
	}
	if saved, _ := in.Saved(); len(saved) != 0 {
		t.Errorf("saved after revert = %+v", saved)
	}
	// Revert all, after the wallpaper and the accent changed: each put back once.
	w.gs.values[KeyAccentColor] = "blue"
	in.Use(ctx, KeyAccentColor, "teal")
	in.UseWallpaper(ctx, Wallpaper{Light: light, Dark: dark, Fit: FitCentered})
	if err := in.RevertAll(ctx); err != nil {
		t.Fatal(err)
	}
	if w.gs.values["bg picture-uri-dark"] != PathURI(mine) || w.gs.values["bg picture-options"] != "scaled" ||
		w.gs.values[KeyAccentColor] != "blue" {
		t.Errorf("after RevertAll: %v", w.gs.values)
	}
	// With nothing saved, Revert resets the wallpaper to GNOME's.
	if err := in.Revert(ctx, KeyWallpaperFit); err != nil {
		t.Fatal(err)
	}
	if w.gs.values["bg picture-uri"] != "default-picture-uri" || w.gs.values["lock picture-uri"] != "default-picture-uri" {
		t.Errorf("after reset: %v", w.gs.values)
	}

	// Plasma doesn't have it.
	w.in.Desktop = &Plasma{Runner: w.gs}
	if in.Desktop.Supports(KeyWallpaper) || in.UseWallpaper(ctx, Wallpaper{Light: light}) != ErrUnsupported {
		t.Error("Plasma sets the wallpaper")
	}
}

func TestPruneWallpapers(t *testing.T) {
	w, store, home, _ := wallWorld(t)
	ctx := context.Background()
	in := w.in
	add := func(name string) string {
		ref, err := in.AddWallpaper(writePic(t, filepath.Join(home, name), name))
		if err != nil {
			t.Fatal(err)
		}
		return ref
	}
	inPreset, inDark, inUndo, inSaved, inUse, unused := add("preset.png"), add("dark.png"), add("undo.png"),
		add("saved.png"), add("current.png"), add("unused.png")
	path := func(ref string) string { p, _ := in.WallpaperPath(ref); return p }
	if _, err := store.Save(Preset{Name: "Fjord", Parts: map[string]Part{
		PartWallpaper: {Light: inPreset, Dark: inDark, Fit: FitZoom}}}, false); err != nil {
		t.Fatal(err)
	}
	if err := store.saveUndo(Undo{Applied: "Fjord", Look: Preset{Name: "Previous look", Parts: map[string]Part{
		PartWallpaper: {Light: path(inUndo)}}}}); err != nil {
		t.Fatal(err)
	}
	st, _ := in.State.Load()
	st.Saved = append(st.Saved, savedSetting("gnome", KeyWallpaperDark, path(inSaved)))
	in.State.Save(st)
	w.gs.values["bg picture-uri"] = PathURI(path(inUse))

	// Just added: kept a while, as a preset may be about to use it.
	if removed := in.PruneWallpapers(ctx); len(removed) != 0 {
		t.Errorf("removed new pictures: %v", removed)
	}
	in.Now = func() time.Time { return time.Now().Add(time.Hour) }
	// Without the presets to look at, nothing goes.
	in.Presets = nil
	if removed := in.PruneWallpapers(ctx); len(removed) != 0 {
		t.Errorf("removed without presets: %v", removed)
	}
	in.Presets = store
	// Nor when the desktop can't be read.
	w.gs.values["bg picture-uri"] = ""
	in.Desktop = &GNOME{Runner: failingGet{w.gs}}
	if removed := in.PruneWallpapers(ctx); len(removed) != 0 {
		t.Errorf("removed with the desktop unreadable: %v", removed)
	}
	in.Desktop = &GNOME{Runner: w.gs}
	w.gs.values["bg picture-uri"] = PathURI(path(inUse))

	removed := in.PruneWallpapers(ctx)
	if !slices.Equal(removed, []string{strings.TrimPrefix(unused, "sha256:") + ".png"}) {
		t.Errorf("removed %v", removed)
	}
	for _, ref := range []string{inPreset, inDark, inUndo, inSaved, inUse} {
		if _, ok := in.WallpaperPath(ref); !ok {
			t.Errorf("%s is gone", ref)
		}
	}
	// Deleting the preset frees its pictures; the Changed hook cleans at once.
	store.Changed = func() { in.PruneWallpapers(ctx) }
	if err := store.Delete("Fjord"); err != nil {
		t.Fatal(err)
	}
	for ref, want := range map[string]bool{inPreset: false, inDark: false, inUndo: true, inUse: true} {
		if _, ok := in.WallpaperPath(ref); ok != want {
			t.Errorf("after delete, %s there: %v", ref, ok)
		}
	}
}

func savedSetting(desktop, key, prev string) (s stateSaved) {
	s.Desktop, s.Key, s.Previous, s.Value, s.At = desktop, key, prev, "x", time.Now()
	return s
}

// failingGet is gsettings that can't read the wallpaper.
type failingGet struct{ *fakeGSettings }

func (f failingGet) Run(ctx context.Context, name string, args []string, out func(string)) (string, error) {
	if len(args) == 3 && args[0] == "get" && args[1] == gnomeBackground {
		return "", os.ErrPermission
	}
	return f.fakeGSettings.Run(ctx, name, args, out)
}

func TestParseBackgrounds(t *testing.T) {
	got := ParseBackgrounds([]byte(`<?xml version="1.0"?>
<!DOCTYPE wallpapers SYSTEM "gnome-wp-list.dtd">
<wallpapers>
  <wallpaper deleted="false">
    <name>Default Background</name>
    <name xml:lang="de">Standardhintergrund</name>
    <filename>/usr/share/backgrounds/gnome/adwaita-l.jxl</filename>
    <filename-dark>/usr/share/backgrounds/gnome/adwaita-d.jxl</filename-dark>
    <options>zoom</options>
    <shade_type>solid</shade_type>
    <pcolor>#3071AE</pcolor>
  </wallpaper>
  <wallpaper>
    <_name>Same twice</_name>
    <filename>/usr/share/backgrounds/a.png</filename>
    <filename-dark>/usr/share/backgrounds/a.png</filename-dark>
    <options>sideways</options>
  </wallpaper>
  <wallpaper deleted="true"><name>Gone</name><filename>/usr/share/backgrounds/b.png</filename></wallpaper>
  <wallpaper><name>Relative</name><filename>c.png</filename></wallpaper>
  <wallpaper><filename>/usr/share/backgrounds/timed.xml</filename><options>spanned</options></wallpaper>
</wallpapers>`))
	want := []SystemWallpaper{
		{Name: "Default Background", Light: "/usr/share/backgrounds/gnome/adwaita-l.jxl",
			Dark: "/usr/share/backgrounds/gnome/adwaita-d.jxl", Fit: FitZoom},
		{Name: "Same twice", Light: "/usr/share/backgrounds/a.png"},
		{Name: "timed.xml", Light: "/usr/share/backgrounds/timed.xml", Fit: FitSpanned},
	}
	if !slices.Equal(got, want) {
		t.Errorf("ParseBackgrounds =\n%+v\nwant\n%+v", got, want)
	}
	// Malformed: what came before the fault, or nothing.
	got = ParseBackgrounds([]byte(`<wallpapers><wallpaper><name>One</name><filename>/x/one.png</filename>` +
		`</wallpaper><wallpaper><name>Two</name><filename>/x/two.png</fil`))
	if len(got) != 1 || got[0].Name != "One" {
		t.Errorf("truncated: %+v", got)
	}
	for _, bad := range []string{"", "not xml at all", "<wallpapers><wallpaper><name>", "\x00\x01<<>>"} {
		if got := ParseBackgrounds([]byte(bad)); len(got) != 0 {
			t.Errorf("%q gave %+v", bad, got)
		}
	}

	// The lists of each data folder: a picture that is gone is left out, and a dark one that is gone is
	// dropped; a file that can't be parsed is skipped.
	root := t.TempDir()
	a, b := filepath.Join(root, "a"), filepath.Join(root, "b")
	l := writePic(t, filepath.Join(a, "backgrounds", "l.png"), "l")
	d := writePic(t, filepath.Join(a, "backgrounds", "d.png"), "d")
	other := writePic(t, filepath.Join(b, "backgrounds", "o.jxl"), "o")
	entry := func(name, light, dark string) string {
		return "<wallpaper><name>" + name + "</name><filename>" + light + "</filename><filename-dark>" + dark +
			"</filename-dark></wallpaper>"
	}
	writePic(t, filepath.Join(a, "gnome-background-properties", "pair.xml"),
		"<wallpapers>"+entry("Pair", l, d)+entry("Gone", filepath.Join(a, "nope.png"), "")+"</wallpapers>")
	writePic(t, filepath.Join(a, "gnome-background-properties", "broken.xml"), "<wallpapers><wallp")
	writePic(t, filepath.Join(b, "gnome-background-properties", "other.xml"),
		"<wallpapers>"+entry("Another", other, filepath.Join(b, "gone-dark.jxl"))+entry("Pair again", l, "")+"</wallpapers>")
	in := &Installer{SystemData: []string{a, filepath.Join(root, "missing"), b}}
	list := in.SystemWallpapers()
	wantList := []SystemWallpaper{{Name: "Another", Light: other}, {Name: "Pair", Light: l, Dark: d}}
	if !slices.Equal(list, wantList) {
		t.Errorf("SystemWallpapers = %+v", list)
	}
	if WallpaperName(list, d) != "Pair (dark)" || WallpaperName(list, "/x/y.png") != "y.png" {
		t.Error("WallpaperName")
	}
}

func TestCaptureWallpaper(t *testing.T) {
	w, _, home, sys := wallWorld(t)
	ctx := context.Background()
	in := w.in
	// A picture of the user's: copied into the store, named by its hash; the same for dark is left out.
	mine := writePic(t, filepath.Join(home, ".local", "share", "backgrounds", "2026-11-14-mine.jpg"), "mine")
	w.gs.values["bg picture-uri"] = PathURI(mine)
	w.gs.values["bg picture-uri-dark"] = PathURI(mine)
	w.gs.values["bg picture-options"] = "spanned"
	parts, err := in.Capture(ctx)
	if err != nil {
		t.Fatal(err)
	}
	got := parts[PartWallpaper]
	if got.Light != "sha256:"+sha("mine") || got.Dark != "" || got.Fit != FitSpanned || got.Value != "" {
		t.Errorf("captured %+v", got)
	}
	if _, ok := in.WallpaperPath(got.Light); !ok {
		t.Error("not stored")
	}
	os.Remove(mine)
	if _, ok := in.WallpaperPath(got.Light); !ok {
		t.Error("the stored copy went with the original")
	}

	// The system's, with a dark picture: by path, nothing copied.
	light := writePic(t, filepath.Join(sys, "gnome", "fold-l.jxl"), "l")
	dark := writePic(t, filepath.Join(sys, "gnome", "fold-d.jxl"), "d")
	w.gs.values["bg picture-uri"] = PathURI(light)
	w.gs.values["bg picture-uri-dark"] = PathURI(dark)
	w.gs.values["bg picture-options"] = "zoom"
	before, _ := os.ReadDir(in.WallpaperDir())
	parts, _ = in.Capture(ctx)
	if got := parts[PartWallpaper]; got.Wallpaper() != (Wallpaper{light, dark, FitZoom}) {
		t.Errorf("captured %+v", got)
	}
	if after, _ := os.ReadDir(in.WallpaperDir()); len(after) != len(before) {
		t.Error("a system picture was copied")
	}
	// A slideshow of the system's, by path.
	timed := writePic(t, filepath.Join(sys, "f45", "f45.xml"), "<background/>")
	w.gs.values["bg picture-uri"] = PathURI(timed)
	w.gs.values["bg picture-uri-dark"] = PathURI(timed)
	parts, _ = in.Capture(ctx)
	if got := parts[PartWallpaper]; got.Light != timed || got.Dark != "" {
		t.Errorf("slideshow: %+v", got)
	}
	// No wallpaper, or one whose picture is gone: no part.
	for _, uri := range []string{"", PathURI(filepath.Join(home, "gone.png"))} {
		w.gs.values["bg picture-uri"] = uri
		if parts, _ := in.Capture(ctx); parts[PartWallpaper].Wallpaper() != (Wallpaper{}) {
			t.Errorf("%q: captured %+v", uri, parts[PartWallpaper])
		}
	}
}

func TestPlanApplyUndoWallpaper(t *testing.T) {
	w, store, home, sys := wallWorld(t)
	ctx := context.Background()
	in := w.in
	w.gs.values[KeyColorScheme] = SchemeDefault
	w.gs.values[KeyAccentColor] = "blue"
	mine := writePic(t, filepath.Join(home, ".local", "share", "backgrounds", "mine.png"), "mine")
	w.gs.values["bg picture-uri"] = PathURI(mine)
	w.gs.values["bg picture-uri-dark"] = PathURI(mine)
	w.gs.values["bg picture-options"] = "scaled"
	fjord, err := in.AddWallpaper(writePic(t, filepath.Join(home, "fjord.jpg"), "fjord"))
	if err != nil {
		t.Fatal(err)
	}
	night := writePic(t, filepath.Join(sys, "gnome", "night-d.jxl"), "night")
	p := Preset{Name: "Fjord", Parts: map[string]Part{
		PartWallpaper: {Light: fjord, Dark: night, Fit: FitCentered},
		PartAccent:    {Value: "teal"},
	}}

	// Pictures that are gone are skipped, the light or the dark one.
	for _, gone := range []Part{{Light: "sha256:" + sha("never")}, {Light: filepath.Join(sys, "gone.jxl")},
		{Light: fjord, Dark: filepath.Join(sys, "gone-d.jxl")}, {}} {
		plan, _ := in.PlanPreset(ctx, Preset{Name: "x", Parts: map[string]Part{PartWallpaper: gone}}, nil)
		if len(plan) != 1 || plan[0].Step != StepMissing || plan[0].Note == "" {
			t.Errorf("%+v: %+v", gone, plan)
		}
	}

	plan, err := in.PlanPreset(ctx, p, nil)
	if err != nil {
		t.Fatal(err)
	}
	stored, _ := in.WallpaperPath(fjord)
	if len(plan) != 2 || plan[1].Part != PartWallpaper || plan[1].Step != StepSet ||
		plan[1].Wallpaper != (Wallpaper{stored, night, FitCentered}) {
		t.Fatalf("plan = %+v", plan)
	}
	w.gs.calls = nil
	out, err := in.ApplyPreset(ctx, store, p, plan, ApplyOptions{})
	if err != nil || Failed(out) != nil {
		t.Fatal(err, Failed(out))
	}
	// The wallpaper is set last.
	var sets []string
	for _, c := range w.gs.calls {
		if strings.HasPrefix(c, "gsettings set ") {
			sets = append(sets, strings.Fields(c)[3])
		}
	}
	if !slices.Equal(sets, []string{"accent-color", "picture-uri", "picture-uri", "picture-uri-dark", "picture-options"}) {
		t.Errorf("set in order %v", sets)
	}
	if w.gs.values["bg picture-uri"] != PathURI(stored) || w.gs.values["bg picture-uri-dark"] != PathURI(night) ||
		w.gs.values["lock picture-uri"] != PathURI(stored) || w.gs.values["bg picture-options"] != "centered" {
		t.Errorf("values = %v", w.gs.values)
	}
	if !InUse(p, in.Current(ctx)) {
		t.Error("not in use after apply")
	}
	if plan, _ := in.PlanPreset(ctx, p, nil); plan[1].Step != StepSame {
		t.Errorf("again: %+v", plan[1])
	}
	// The look to undo to has the wallpaper as it was.
	u, _, _ := store.Undo()
	if got := u.Look.Parts[PartWallpaper]; got.Wallpaper() != (Wallpaper{Light: mine, Fit: FitScaled}) {
		t.Errorf("undo look = %+v", got)
	}
	// Another fit, or dark picture, is another look.
	cur := in.Current(ctx)
	cur[KeyWallpaperFit] = FitZoom
	if InUse(p, cur) {
		t.Error("in use with another fit")
	}

	// Undo puts it back, and the fjord stays stored for the preset.
	if _, out, err := in.UndoPreset(ctx, store); err != nil || Failed(out) != nil {
		t.Fatal(err, Failed(out))
	}
	if w.gs.values["bg picture-uri"] != PathURI(mine) || w.gs.values["bg picture-uri-dark"] != PathURI(mine) ||
		w.gs.values["bg picture-options"] != "scaled" || w.gs.values[KeyAccentColor] != "blue" {
		t.Errorf("after undo: %v", w.gs.values)
	}
	if saved, _ := in.Saved(); len(saved) != 0 {
		t.Errorf("saved after undo = %+v", saved)
	}

	// A preset without a wallpaper leaves it alone.
	plan, _ = in.PlanPreset(ctx, Preset{Name: "Teal", Parts: map[string]Part{PartAccent: {Value: "teal"}}}, nil)
	w.gs.calls = nil
	in.ApplyPreset(ctx, store, Preset{Name: "Teal"}, plan, ApplyOptions{})
	for _, c := range w.gs.calls {
		if strings.Contains(c, "background") && !strings.Contains(c, " get ") && !strings.Contains(c, "list-keys") {
			t.Errorf("changed the wallpaper: %s", c)
		}
	}

	// On Plasma, the wallpaper is skipped.
	in.Desktop = &Plasma{Runner: w.gs}
	if plan, _ := in.PlanPreset(ctx, p, nil); plan[1].Step != StepUnsupported {
		t.Errorf("on Plasma: %+v", plan[1])
	}
}

// TestOldPresetFiles reads a preset file from before wallpapers, and one with a wallpaper: each is written
// back as it was.
func TestOldPresetFiles(t *testing.T) {
	s := testPresets(t)
	os.MkdirAll(s.Dir, 0o755)
	old := `{
  "uas_preset": 1,
  "name": "Evening",
  "created_at": "2026-11-01T20:00:00Z",
  "updated_at": "2026-11-01T20:00:00Z",
  "parts": {
    "accent": {
      "value": "teal"
    },
    "icons": {
      "value": "Papirus-Dark",
      "from": {
        "id": "pling:1166289",
        "variant": "Papirus-Dark"
      }
    }
  }
}
`
	withWall := strings.Replace(old, `"accent": {`, `"wallpaper": {
      "light": "sha256:ab12",
      "dark": "/usr/share/backgrounds/gnome/adwaita-d.jxl",
      "fit": "zoom"
    },
    "accent": {`, 1)
	for name, body := range map[string]string{"evening.json": old, "wall.json": strings.Replace(withWall,
		"Evening", "Wall", 1)} {
		path := filepath.Join(s.Dir, name)
		writePic(t, path, body)
		p, err := readPreset(path)
		if err != nil {
			t.Fatal(err)
		}
		p.file = path
		if err := s.write(&p); err != nil {
			t.Fatal(err)
		}
		b, _ := os.ReadFile(path)
		var a, z map[string]any
		json.Unmarshal(b, &a)
		json.Unmarshal([]byte(body), &z)
		ab, _ := json.Marshal(a)
		zb, _ := json.Marshal(z)
		if string(ab) != string(zb) {
			t.Errorf("%s rewritten as\n%s", name, b)
		}
	}
	p, _ := s.Get("Evening")
	if p.Sets(PartWallpaper) || !slices.Equal(p.PartNames(), []string{PartAccent, PartIcons}) {
		t.Errorf("Evening's parts = %v", p.PartNames())
	}
	p, _ = s.Get("Wall")
	if w := p.Parts[PartWallpaper]; w.Light != "sha256:ab12" || w.Fit != FitZoom || !strings.HasSuffix(w.Dark, "d.jxl") {
		t.Errorf("wallpaper = %+v", w)
	}
}
