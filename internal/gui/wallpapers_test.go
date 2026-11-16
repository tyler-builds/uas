package gui

import (
	"bytes"
	"context"
	"crypto/md5"
	"encoding/binary"
	"encoding/hex"
	"hash/crc32"
	"image"
	"image/color"
	"image/png"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/tyler-builds/jamb/access"
	"github.com/tyler-builds/jamb/ui"
	"github.com/tyler-builds/jamb/ui/uitest"

	archives "github.com/tyler-builds/uas/internal/appearance"
	"github.com/tyler-builds/uas/internal/catalog"
	"github.com/tyler-builds/uas/internal/client/appearance"
	"github.com/tyler-builds/uas/internal/client/backend"
	"github.com/tyler-builds/uas/internal/client/session"
	"github.com/tyler-builds/uas/internal/client/settings"
	"github.com/tyler-builds/uas/internal/client/state"
	"github.com/tyler-builds/uas/internal/client/telemetry"
)

// pngOf is a PNG picture of a size, in one color.
func pngOf(t *testing.T, w, h int, c color.Color) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			img.Set(x, y, c)
		}
	}
	var b bytes.Buffer
	if err := png.Encode(&b, img); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

func writeFile(t *testing.T, path string, b []byte) string {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, b, 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestThumbnail(t *testing.T) {
	t.Setenv("XDG_CACHE_HOME", t.TempDir()) // no desktop thumbnails
	dir := t.TempDir()
	// A large picture is scaled down, keeping its shape, and averaged.
	big := writeFile(t, filepath.Join(dir, "big.png"), pngOf(t, 3840, 2160, color.RGBA{200, 40, 40, 255}))
	pic, err := thumbnail(big)
	if err != nil || pic.img == nil {
		t.Fatalf("thumbnail = %+v, %v", pic, err)
	}
	if w, h := pic.img.Size(); w != thumbWidth || h != thumbWidth*2160/3840 {
		t.Errorf("size %dx%d", w, h)
	}
	small := shrink(image.NewRGBA(image.Rect(0, 0, 100, 50)), thumbWidth)
	if small.Bounds().Dx() != 100 {
		t.Error("a small picture was scaled up")
	}
	src := image.NewRGBA(image.Rect(0, 0, 4, 2))
	for x := 0; x < 4; x++ {
		for y := 0; y < 2; y++ {
			if x%2 == 0 {
				src.Set(x, y, color.RGBA{255, 255, 255, 255})
			} else {
				src.Set(x, y, color.RGBA{0, 0, 0, 255})
			}
		}
	}
	if got := shrink(src, 2).(*image.RGBA).RGBAAt(0, 0); got.R < 120 || got.R > 135 {
		t.Errorf("averaged to %v", got)
	}
	// An SVG is drawn as vectors.
	svg := writeFile(t, filepath.Join(dir, "blobs.svg"), []byte(`<svg xmlns="http://www.w3.org/2000/svg" width="16" height="9"><rect width="16" height="9" fill="#123"/></svg>`))
	if pic, err := thumbnail(svg); err != nil || pic.svg == nil {
		t.Errorf("svg: %+v, %v", pic, err)
	}
	// A slideshow shows its first picture.
	xml := writeFile(t, filepath.Join(dir, "timed.xml"), []byte("<background><starttime><year>2026</year></starttime>"+
		"<static><duration>60</duration><file>"+big+"</file></static></background>"))
	if pic, err := thumbnail(xml); err != nil || pic.img == nil {
		t.Errorf("slideshow: %+v, %v", pic, err)
	}
	// A picture that can't be decoded is an error, not a panic: a JPEG XL the decoder doesn't take, and
	// anything else.
	for name, body := range map[string][]byte{"bad.jxl": []byte("\xff\x0a\x00\x01junk"), "bad.png": []byte("nope"),
		"empty.webp": nil} {
		if _, err := thumbnail(writeFile(t, filepath.Join(dir, name), body)); err == nil {
			t.Errorf("%s decoded", name)
		}
	}
	if _, err := thumbnail(filepath.Join(dir, "gone.png")); err == nil {
		t.Error("a picture that isn't there")
	}

	// The desktop's own thumbnail is used when it is as new as the picture.
	cache := t.TempDir()
	t.Setenv("XDG_CACHE_HOME", cache)
	jxl := writeFile(t, filepath.Join(dir, "adwaita-l.jxl"), []byte("\xff\x0a not decodable"))
	fi, _ := os.Stat(jxl)
	thumb := pngWithText(pngOf(t, 256, 144, color.RGBA{0, 0, 200, 255}), "Thumb::MTime",
		strconv.FormatInt(fi.ModTime().Unix(), 10))
	sum := md5.Sum([]byte(appearance.PathURI(jxl)))
	writeFile(t, filepath.Join(cache, "thumbnails", "large", hex.EncodeToString(sum[:])+".png"), thumb)
	if pic, err := thumbnail(jxl); err != nil || pic.img == nil {
		t.Errorf("cached thumbnail: %+v, %v", pic, err)
	}
	// An old one isn't.
	os.Chtimes(jxl, time.Now().Add(time.Hour), time.Now().Add(time.Hour))
	if _, err := thumbnail(jxl); err == nil {
		t.Error("used a stale thumbnail")
	}
}

// TestPresetWallpaper saves a look with the user's wallpaper, then on the preset's page picks one of the
// system's wallpapers (a light and dark pair), a picture from a file, "Same for dark" and Leave as is,
// against a fake GNOME; and shows the wallpaper as one row of "Changed by uas".
func TestPresetWallpaper(t *testing.T) {
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	dir := t.TempDir()
	path := filepath.Join(dir, "catalog.db")
	if err := catalog.Write(context.Background(), path, nil, catalog.BuildInfo{BuiltAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	home, usr := filepath.Join(dir, "home"), filepath.Join(dir, "usr", "share")
	light := writeFile(t, filepath.Join(usr, "backgrounds", "pair-l.png"), pngOf(t, 64, 36, color.White))
	dark := writeFile(t, filepath.Join(usr, "backgrounds", "pair-d.png"), pngOf(t, 64, 36, color.Black))
	writeFile(t, filepath.Join(usr, "gnome-background-properties", "pair.xml"), []byte(`<wallpapers><wallpaper>
<name>Pair</name><filename>`+light+`</filename><filename-dark>`+dark+`</filename-dark><options>zoom</options>
</wallpaper></wallpapers>`))
	mine := writeFile(t, filepath.Join(home, ".local", "share", "backgrounds", "mine.png"),
		pngOf(t, 64, 36, color.RGBA{0, 128, 0, 255}))

	gs := newFakeGNOME()
	gs.values["bg picture-uri"] = appearance.PathURI(mine)
	gs.values["bg picture-uri-dark"] = appearance.PathURI(mine)
	gs.values["bg picture-options"] = "zoom"
	in := &appearance.Installer{Data: filepath.Join(dir, "data"), Home: home, SystemData: []string{usr},
		Cache: filepath.Join(dir, "cache"), State: state.Store{Path: filepath.Join(dir, "data", "uas", "state.json")},
		Runner: gs, Desktop: &appearance.GNOME{Runner: gs}, Limits: archives.DefaultLimits}
	store := appearance.NewPresets(filepath.Join(dir, "config"), filepath.Join(dir, "data"))
	in.Presets = store
	s := &session.Session{Settings: settings.Default(), Telemetry: telemetry.Nop{}, CatalogPath: path, Looks: in,
		Presets: store, Backends: map[catalog.SourceID]backend.Backend{}}
	a := New(s)
	a.icons.dir = filepath.Join(dir, "images")
	a.startup.Do(func() {})
	var uctx *ui.Context // the app's, to finish a file pick with, as the file picker can't be driven here
	u := uitest.New(t, func(ctx *ui.Context) ui.Element { uctx = ctx; return a.Root(ctx) }, uitest.Size(1280, 900))
	defer u.Close()

	// Save current look has the wallpaper, which is copied into uas's store.
	a.goTo(view{page: pageAppearance})
	a.openSave()
	u.WaitFor("the wallpaper in the Save dialog", func() bool {
		return u.HasRole(access.RoleDialog, "Save current look") &&
			u.ByRole(access.RoleDialog, "Save current look").HasRole(access.RoleCheckBox, "Wallpaper: mine.png")
	})
	u.Type("Mine")
	u.Settle()
	u.ByRole(access.RoleDialog, "Save current look").ByRole(access.RoleButton, "Save").Click()
	u.WaitFor("the saved preset", func() bool { return u.Has("preset-card:Mine") })
	p, _ := store.Get("Mine")
	w := p.Parts[appearance.PartWallpaper]
	if !strings.HasPrefix(w.Light, "sha256:") || w.Dark != "" || w.Fit != appearance.FitZoom {
		t.Fatalf("saved %+v", w)
	}
	if _, ok := in.WallpaperPath(w.Light); !ok {
		t.Error("the wallpaper wasn't stored")
	}
	waitText(t, u, "preset-card:Mine", "In use")

	// The preset's page: the light slot takes a system wallpaper, with its dark picture.
	a.openPreset("Mine")
	u.WaitFor("the wallpaper row", func() bool { return u.Has("wallpaper-row:Mine") })
	a.wallChooser.Set(&wallSlot{preset: "Mine"})
	u.WaitFor("the system wallpapers", func() bool {
		return u.HasRole(access.RoleDialog, "System wallpapers") &&
			u.ByRole(access.RoleDialog, "System wallpapers").HasRole(access.RoleButton, "Pair")
	})
	u.ByRole(access.RoleDialog, "System wallpapers").ByRole(access.RoleButton, "Pair").Click()
	u.WaitFor("the pair", func() bool {
		p, _ := store.Get("Mine")
		w := p.Parts[appearance.PartWallpaper]
		return w.Light == light && w.Dark == dark
	})
	u.WaitFor("the chooser to close", func() bool { return !u.HasRole(access.RoleDialog, "System wallpapers") })
	waitText(t, u, "wallpaper-row:Mine", "Pair (dark)")

	// "Same for dark" uses the light picture for both.
	u.WaitFor("Same for dark", func() bool {
		return u.Get("wallpaper-row:Mine").HasRole(access.RoleCheckBox, "Same for dark")
	})
	u.Get("wallpaper-row:Mine").ByRole(access.RoleCheckBox, "Same for dark").Click()
	u.WaitFor("no dark picture", func() bool {
		p, _ := store.Get("Mine")
		return p.Parts[appearance.PartWallpaper].Dark == "" && p.Parts[appearance.PartWallpaper].Light == light
	})

	// A picture from a file, for the dark slot: copied into the store.
	pick := writeFile(t, filepath.Join(home, "Pictures", "Night sky.png"), pngOf(t, 64, 36, color.RGBA{0, 0, 60, 255}))
	ref, err := a.wallpaperRef(pick)
	if err != nil || !strings.HasPrefix(ref, "sha256:") {
		t.Fatalf("wallpaperRef = %q, %v", ref, err)
	}
	if _, err := a.wallpaperRef(writeFile(t, filepath.Join(home, "notes.txt"), []byte("x"))); err == nil {
		t.Error("a text file was taken as a wallpaper")
	}
	// As pickWallpaperFile ends, once the file picker answered.
	a.putWallpaper(uctx, "Mine", true, ref, false, "", "")
	waitText(t, u, "wallpaper-row:Mine", "Your picture")
	if p, _ := store.Get("Mine"); p.Parts[appearance.PartWallpaper].Dark != ref {
		t.Errorf("dark = %+v", p.Parts[appearance.PartWallpaper])
	}

	// Leave as is takes the wallpaper out of the preset.
	u.Get("wallpaper-row:Mine").ByRole(access.RoleButton, "Leave as is").Click()
	u.WaitFor("no wallpaper", func() bool {
		p, _ := store.Get("Mine")
		return !p.Sets(appearance.PartWallpaper)
	})

	// The wallpaper uas set is one row of "Changed by uas", with one Revert.
	if err := in.UseWallpaper(context.Background(), appearance.Wallpaper{Light: light, Dark: dark,
		Fit: appearance.FitCentered}); err != nil {
		t.Fatal(err)
	}
	u.Advance(6 * time.Second)
	a.goTo(view{page: pageAppearance})
	a.lookTab.Set(lookInstalled)
	u.WaitFor("the wallpaper row", func() bool { return u.HasName("Revert") })
	if u.HasName("Revert all") {
		t.Error("the wallpaper's three settings are listed as more than one")
	}
	clickButton(u, "Revert")
	u.WaitFor("the revert", func() bool { return gs.get("bg picture-uri") == appearance.PathURI(mine) })
	if gs.get("bg picture-options") != "zoom" || gs.get("bg picture-uri-dark") != appearance.PathURI(mine) {
		t.Errorf("after revert: %v", gs.values)
	}
}

// pngWithText puts a tEXt field into a PNG file, after its header, as thumbnailers write Thumb::MTime.
func pngWithText(b []byte, key, value string) []byte {
	data := append([]byte(key+"\x00"), value...)
	chunk := binary.BigEndian.AppendUint32(nil, uint32(len(data)))
	chunk = append(chunk, "tEXt"...)
	chunk = append(chunk, data...)
	chunk = binary.BigEndian.AppendUint32(chunk, crc32.ChecksumIEEE(append([]byte("tEXt"), data...)))
	at := 8 + 8 + 13 + 4 // the signature and IHDR
	return append(append(append([]byte{}, b[:at]...), chunk...), b[at:]...)
}
