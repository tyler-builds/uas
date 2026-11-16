package appearance

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"
)

// Wallpapers. A picture of the user's own is copied into uas's wallpaper store, ~/.local/share/uas/wallpapers,
// as <sha256>.<ext>, and a preset names it by that hash ("sha256:<hex>"), so moving or deleting the original
// doesn't break the preset; the same picture is stored once. The system's wallpapers (under
// /usr/share/backgrounds and the like, GNOME's .xml slideshows too) are named by their path and never
// copied. A stored picture is removed once nothing uses it: no preset, no Previous look, no value saved for
// Revert, and not the desktop's wallpaper (PruneWallpapers).

// Wallpaper is a wallpaper: its pictures, and how they fit the screen.
type Wallpaper struct {
	// Light is the picture, and Dark the one with the dark style; "" for Dark is Light. In a preset part
	// each is a reference (WallpaperRef); on the desktop, a path.
	Light, Dark string
	// Fit is one of WallpaperFits; "" leaves it as it is when setting, and is FitZoom in a preset.
	Fit string
}

// DarkOrLight is the dark picture, or the light one when there is no other.
func (w Wallpaper) DarkOrLight() string {
	if w.Dark != "" {
		return w.Dark
	}
	return w.Light
}

// WallpaperExts are the kinds of picture the store takes, by extension.
var WallpaperExts = []string{".jpg", ".jpeg", ".png", ".webp", ".jxl", ".svg"}

// wallpaperPrefix starts a reference to a stored picture.
const wallpaperPrefix = "sha256:"

// maxWallpaper is the largest picture the store takes.
const maxWallpaper = 512 << 20

// WallpaperDir is the wallpaper store: ~/.local/share/uas/wallpapers.
func (in *Installer) WallpaperDir() string { return filepath.Join(in.Data, "uas", "wallpapers") }

// IsWallpaperFile reports whether a file name has one of WallpaperExts.
func IsWallpaperFile(name string) bool {
	return slices.Contains(WallpaperExts, strings.ToLower(filepath.Ext(name)))
}

// SystemPicture reports whether a picture is one of the system's own, which is named by its path and never
// copied: one under a backgrounds folder of $XDG_DATA_DIRS (/usr/share/backgrounds), outside the home folder.
func (in *Installer) SystemPicture(path string) bool {
	if !filepath.IsAbs(path) {
		return false
	}
	path = filepath.Clean(path)
	dirs := slices.Clone(in.SystemData)
	if !slices.Contains(dirs, "/usr/share") {
		dirs = append(dirs, "/usr/share")
	}
	for _, d := range dirs {
		if d == "" || !filepath.IsAbs(d) || (in.Home != "" && within(in.Home, d)) || within(in.Data, d) {
			continue
		}
		if within(filepath.Join(d, "backgrounds"), path) {
			return true
		}
	}
	return false
}

// within reports whether p is in the folder dir, at any depth.
func within(dir, p string) bool {
	rel, err := filepath.Rel(filepath.Clean(dir), filepath.Clean(p))
	return err == nil && rel != "." && filepath.IsLocal(rel)
}

// AddWallpaper copies a picture of the user's into the store, unless it is there already, and returns its
// reference: "sha256:<hex>".
func (in *Installer) AddWallpaper(path string) (string, error) {
	if !IsWallpaperFile(path) {
		return "", fmt.Errorf("%s isn't a picture uas can use as a wallpaper (%s)", filepath.Base(path),
			strings.Join(extNames(), ", "))
	}
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil {
		return "", err
	}
	switch {
	case !fi.Mode().IsRegular():
		return "", fmt.Errorf("%s isn't a file", path)
	case fi.Size() > maxWallpaper:
		return "", fmt.Errorf("%s is too large for a wallpaper", filepath.Base(path))
	}
	dir := in.WallpaperDir()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	tmp, err := os.CreateTemp(dir, ".tmp-*")
	if err != nil {
		return "", err
	}
	defer os.Remove(tmp.Name())
	h := sha256.New()
	_, err = io.Copy(io.MultiWriter(tmp, h), io.LimitReader(f, maxWallpaper+1))
	if cerr := tmp.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return "", err
	}
	sum := hex.EncodeToString(h.Sum(nil))
	ref := wallpaperPrefix + sum
	if have, ok := in.storedPicture(sum); ok {
		// Stored already: touched, so a cleanup running now keeps it for the preset about to use it.
		now := time.Now()
		os.Chtimes(have, now, now)
		return ref, nil
	}
	if err := os.Chmod(tmp.Name(), 0o644); err != nil {
		return "", err
	}
	if err := os.Rename(tmp.Name(), filepath.Join(dir, sum+strings.ToLower(filepath.Ext(path)))); err != nil {
		return "", err
	}
	return ref, nil
}

func extNames() []string {
	out := make([]string, len(WallpaperExts))
	for i, e := range WallpaperExts {
		out[i] = strings.TrimPrefix(e, ".")
	}
	return out
}

// storedPicture returns the stored file of a SHA-256, whatever its extension.
func (in *Installer) storedPicture(sum string) (string, bool) {
	if len(sum) != 64 || strings.Trim(sum, "0123456789abcdef") != "" {
		return "", false
	}
	for _, ext := range WallpaperExts {
		p := filepath.Join(in.WallpaperDir(), sum+ext)
		if fi, err := os.Stat(p); err == nil && fi.Mode().IsRegular() {
			return p, true
		}
	}
	return "", false
}

// WallpaperRef is how a preset names a picture: by its path for one of the system's (or a picture that
// isn't a file of a kind the store takes, such as a slideshow of the user's), else by its hash, copying it
// into the store first. A picture already in the store is named by its hash.
func (in *Installer) WallpaperRef(path string) (string, error) {
	if sum, ok := in.storeHash(path); ok {
		return wallpaperPrefix + sum, nil
	}
	if in.SystemPicture(path) || !IsWallpaperFile(path) {
		return filepath.Clean(path), nil
	}
	return in.AddWallpaper(path)
}

// storeHash returns the hash of a file in the store, by its name.
func (in *Installer) storeHash(path string) (string, bool) {
	if !filepath.IsAbs(path) || filepath.Dir(filepath.Clean(path)) != filepath.Clean(in.WallpaperDir()) {
		return "", false
	}
	name := filepath.Base(path)
	sum := strings.TrimSuffix(name, filepath.Ext(name))
	if _, ok := in.storedPicture(sum); !ok {
		return "", false
	}
	return sum, true
}

// WallpaperPath returns the file a reference names, and false when it isn't there.
func (in *Installer) WallpaperPath(ref string) (string, bool) {
	if sum, ok := strings.CutPrefix(ref, wallpaperPrefix); ok {
		return in.storedPicture(strings.ToLower(sum))
	}
	if !filepath.IsAbs(ref) {
		return ref, false
	}
	fi, err := os.Stat(ref)
	return ref, err == nil && fi.Mode().IsRegular()
}

// PictureName names a picture for people: its file's name, or for one in the store, "Your picture" and the
// start of its hash, as the store keeps no other name.
func PictureName(v string) string {
	switch {
	case v == "":
		return ""
	case strings.HasPrefix(v, wallpaperPrefix):
		return "Your picture " + shortHash(strings.TrimPrefix(v, wallpaperPrefix))
	case !strings.HasPrefix(v, "/"):
		return v
	}
	name := filepath.Base(v)
	if filepath.Base(filepath.Dir(v)) == "wallpapers" && filepath.Base(filepath.Dir(filepath.Dir(v))) == "uas" {
		if sum := strings.TrimSuffix(name, filepath.Ext(name)); len(sum) == 64 {
			return "Your picture " + shortHash(sum)
		}
	}
	return name
}

func shortHash(s string) string { return s[:min(len(s), 8)] }

// sameRef reports whether a picture on the desktop (a path) is the one a reference names: the same path, or
// for a stored picture, the stored file or another with its contents (the original of a look just saved).
func sameRef(path, ref string) bool {
	if path == "" || ref == "" {
		return path == ref
	}
	if sum, ok := strings.CutPrefix(ref, wallpaperPrefix); ok {
		name := filepath.Base(path)
		if filepath.Base(filepath.Dir(path)) == "wallpapers" &&
			strings.EqualFold(strings.TrimSuffix(name, filepath.Ext(name)), sum) {
			return true
		}
		h, ok := pictureHash(path)
		return ok && strings.EqualFold(h, sum)
	}
	return filepath.Clean(path) == filepath.Clean(ref)
}

// pictureHashes remembers the SHA-256 of the pictures hashed, by path, size and time, so a picture is read
// once however often it is compared.
var pictureHashes sync.Map

// pictureHash returns the SHA-256 of a picture of the user's, and false for anything that can't be stored.
func pictureHash(path string) (string, bool) {
	if !filepath.IsAbs(path) || !IsWallpaperFile(path) {
		return "", false
	}
	fi, err := os.Stat(path)
	if err != nil || !fi.Mode().IsRegular() || fi.Size() > maxWallpaper {
		return "", false
	}
	key := fmt.Sprintf("%s\x00%d\x00%d", path, fi.Size(), fi.ModTime().UnixNano())
	if v, ok := pictureHashes.Load(key); ok {
		return v.(string), true
	}
	f, err := os.Open(path)
	if err != nil {
		return "", false
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", false
	}
	sum := hex.EncodeToString(h.Sum(nil))
	pictureHashes.Store(key, sum)
	return sum, true
}

// WallpaperPart is a wallpaper as a preset part, from the desktop's: each picture named as WallpaperRef
// names it, the dark one left out when it is the light one.
func (in *Installer) WallpaperPart(w Wallpaper) (Part, error) {
	light, err := in.WallpaperRef(w.Light)
	if err != nil {
		return Part{}, err
	}
	p := Part{Light: light, Fit: w.Fit}
	if w.Dark != "" && filepath.Clean(w.Dark) != filepath.Clean(w.Light) {
		if p.Dark, err = in.WallpaperRef(w.Dark); err != nil {
			return Part{}, err
		}
	}
	if p.Fit == "" {
		p.Fit = FitZoom
	}
	return p, nil
}

// snapshotPart is the desktop's wallpaper as the look to undo to has it: by path, except a picture in the
// store, by its hash. Nothing is copied.
func (in *Installer) snapshotPart(w Wallpaper) Part {
	ref := func(p string) string {
		if sum, ok := in.storeHash(p); ok {
			return wallpaperPrefix + sum
		}
		return p
	}
	p := Part{Light: ref(w.Light), Fit: w.Fit}
	if w.Dark != "" && w.Dark != w.Light {
		p.Dark = ref(w.Dark)
	}
	return p
}

// PartWallpaper is a preset's wallpaper part as the desktop takes it: the pictures' paths, the dark one the
// light one when the part has none, and zoom when it has no fit. missing names a picture that isn't there.
func (in *Installer) PartWallpaper(p Part) (w Wallpaper, missing string) {
	light, ok := in.WallpaperPath(p.Light)
	if !ok {
		return Wallpaper{}, p.Light
	}
	dark := light
	if p.Dark != "" {
		if dark, ok = in.WallpaperPath(p.Dark); !ok {
			return Wallpaper{}, p.Dark
		}
	}
	fit := p.Fit
	if fit == "" {
		fit = FitZoom
	}
	return Wallpaper{Light: light, Dark: dark, Fit: fit}, ""
}

// CurrentWallpaper reads the desktop's wallpaper.
func (in *Installer) CurrentWallpaper(ctx context.Context) (Wallpaper, error) {
	if in.Desktop == nil || !in.Desktop.Supports(KeyWallpaper) {
		return Wallpaper{}, ErrUnsupported
	}
	var w Wallpaper
	var err error
	if w.Light, err = in.Desktop.Get(ctx, KeyWallpaper); err != nil {
		return w, err
	}
	if in.Desktop.Supports(KeyWallpaperDark) {
		if w.Dark, err = in.Desktop.Get(ctx, KeyWallpaperDark); err != nil {
			return w, err
		}
	}
	if in.Desktop.Supports(KeyWallpaperFit) {
		if w.Fit, err = in.Desktop.Get(ctx, KeyWallpaperFit); err != nil {
			return w, err
		}
	}
	return w, nil
}

// UseWallpaper sets the desktop's wallpaper to pictures of the user's or the system's: Light, Dark (or
// Light again when there is none) and Fit unless it is "". A picture of the user's is copied into the store
// first, and the desktop is pointed at the copy. Each setting goes through Use, so Revert goes back.
func (in *Installer) UseWallpaper(ctx context.Context, w Wallpaper) error {
	if in.Desktop == nil || !in.Desktop.Supports(KeyWallpaper) {
		return ErrUnsupported
	}
	if w.Fit != "" {
		if err := CheckValue(KeyWallpaperFit, w.Fit); err != nil {
			return err
		}
	}
	store := func(p string) (string, error) {
		if p == "" {
			return "", nil
		}
		abs, err := filepath.Abs(p)
		if err != nil {
			return "", err
		}
		ref, err := in.WallpaperRef(abs)
		if err != nil {
			return "", err
		}
		path, ok := in.WallpaperPath(ref)
		if !ok {
			return "", fmt.Errorf("%s can't be found", abs)
		}
		return path, nil
	}
	var err error
	if w.Light, err = store(w.Light); err != nil {
		return err
	}
	if w.Dark, err = store(w.Dark); err != nil {
		return err
	}
	if w.Light == "" {
		return errors.New("wallpaper: give a picture")
	}
	return in.setWallpaper(ctx, w)
}

// setWallpaper sets the wallpaper's settings to paths, through Use, and removes stored pictures nothing
// uses any more.
func (in *Installer) setWallpaper(ctx context.Context, w Wallpaper) error {
	if err := in.Use(ctx, KeyWallpaper, w.Light); err != nil {
		return err
	}
	var errs []error
	if in.Desktop.Supports(KeyWallpaperDark) {
		if err := in.Use(ctx, KeyWallpaperDark, w.DarkOrLight()); err != nil {
			errs = append(errs, err)
		}
	}
	if w.Fit != "" && in.Desktop.Supports(KeyWallpaperFit) {
		if err := in.Use(ctx, KeyWallpaperFit, w.Fit); err != nil {
			errs = append(errs, err)
		}
	}
	in.PruneWallpapers(ctx)
	return errors.Join(errs...)
}

// captureWallpaper is the desktop's wallpaper as a preset part, copying a picture of the user's into the
// store; ok is false when there is none.
func (in *Installer) captureWallpaper(ctx context.Context) (Part, bool, error) {
	w, err := in.CurrentWallpaper(ctx)
	if errors.Is(err, ErrUnsupported) {
		return Part{}, false, nil
	} else if err != nil {
		return Part{}, false, err
	}
	if w.Light == "" || !filepath.IsAbs(w.Light) {
		return Part{}, false, nil
	}
	if _, ok := in.WallpaperPath(w.Light); !ok {
		return Part{}, false, nil // a picture that is gone: nothing to save
	}
	if w.Dark != "" {
		if _, ok := in.WallpaperPath(w.Dark); !ok || !filepath.IsAbs(w.Dark) {
			w.Dark = ""
		}
	}
	p, err := in.WallpaperPart(w)
	return p, err == nil, err
}

// pruneGrace is how long a stored picture is kept after it was added, even when nothing uses it yet: it was
// just chosen for a preset that is about to be saved.
const pruneGrace = 10 * time.Minute

// PruneWallpapers removes the stored pictures that nothing uses: no preset, not the look to undo to, no
// value saved for Revert, and not the desktop's wallpaper. It removes nothing when it can't tell, when
// Presets is nil or anything of those can't be read. It returns the files it removed.
func (in *Installer) PruneWallpapers(ctx context.Context) []string {
	if in.Presets == nil {
		return nil
	}
	entries, err := os.ReadDir(in.WallpaperDir())
	if err != nil || len(entries) == 0 {
		return nil
	}
	used, ok := in.usedPictures(ctx)
	if !ok {
		return nil
	}
	var removed []string
	for _, e := range entries {
		name := e.Name()
		fi, err := e.Info()
		if err != nil || !fi.Mode().IsRegular() || in.now().Sub(fi.ModTime()) < pruneGrace {
			continue
		}
		sum := strings.TrimSuffix(name, filepath.Ext(name))
		switch {
		case strings.HasPrefix(name, ".tmp-"):
			if in.now().Sub(fi.ModTime()) < 24*time.Hour {
				continue
			}
		case used[sum] || !IsWallpaperFile(name):
			continue
		}
		if err := os.Remove(filepath.Join(in.WallpaperDir(), name)); err == nil {
			removed = append(removed, name)
		}
	}
	return removed
}

// usedPictures are the hashes of the stored pictures in use, and false when what uses them can't all be
// read.
func (in *Installer) usedPictures(ctx context.Context) (map[string]bool, bool) {
	used := map[string]bool{}
	add := func(v string) {
		if sum, ok := strings.CutPrefix(v, wallpaperPrefix); ok {
			used[strings.ToLower(sum)] = true
		} else if sum, ok := in.storeHash(v); ok {
			used[sum] = true
		}
	}
	addPart := func(p Preset) {
		w := p.Parts[PartWallpaper]
		add(w.Light)
		add(w.Dark)
	}
	list, err := in.Presets.List()
	if err != nil {
		return nil, false
	}
	for _, p := range list {
		addPart(p)
	}
	u, ok, err := in.Presets.Undo()
	if err != nil {
		return nil, false
	}
	if ok {
		addPart(u.Look)
	}
	st, err := in.State.Load()
	if err != nil {
		return nil, false
	}
	for _, s := range st.Saved {
		if IsWallpaperKey(s.Key) {
			add(s.Previous)
			add(s.Value)
		}
	}
	if in.Desktop != nil && in.Desktop.Supports(KeyWallpaper) {
		w, err := in.CurrentWallpaper(ctx)
		if err != nil {
			return nil, false
		}
		add(w.Light)
		add(w.Dark)
	}
	return used, true
}

// SystemWallpaper is one of the system's wallpapers, as GNOME's settings list them.
type SystemWallpaper struct {
	Name string
	// Light is the picture, or a slideshow's .xml; Dark the picture with the dark style, or "".
	Light, Dark string
	// Fit is how it is meant to fill the screen, one of WallpaperFits, or "".
	Fit string
}

// SystemWallpapers lists the system's wallpapers from gnome-background-properties/*.xml in each of $XDG_DATA_DIRS:
// the light and dark pictures they pair, with their names. A file that can't be read or parsed is skipped,
// as is a wallpaper whose picture is gone or that is marked deleted; a picture listed twice is listed once.
func (in *Installer) SystemWallpapers() []SystemWallpaper {
	dirs := slices.Clone(in.SystemData)
	var out []SystemWallpaper
	seen := map[string]bool{}
	for _, d := range dirs {
		files, _ := filepath.Glob(filepath.Join(d, "gnome-background-properties", "*.xml"))
		slices.Sort(files)
		for _, f := range files {
			b, err := readLimited(f, 1<<20)
			if err != nil {
				continue
			}
			for _, w := range ParseBackgrounds(b) {
				if seen[w.Light] {
					continue
				}
				if fi, err := os.Stat(w.Light); err != nil || !fi.Mode().IsRegular() {
					continue
				}
				if w.Dark != "" {
					if fi, err := os.Stat(w.Dark); err != nil || !fi.Mode().IsRegular() {
						w.Dark = ""
					}
				}
				seen[w.Light] = true
				out = append(out, w)
			}
		}
	}
	slices.SortStableFunc(out, func(a, b SystemWallpaper) int {
		return strings.Compare(strings.ToLower(a.Name), strings.ToLower(b.Name))
	})
	return out
}

func readLimited(path string, n int64) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	return io.ReadAll(io.LimitReader(f, n))
}

// ParseBackgrounds reads a gnome-background-properties file: its wallpapers, with absolute paths, the dark
// picture "" when it is the light one, and the fit only when it is one of WallpaperFits. Malformed XML
// gives what was read before the fault, which may be nothing.
func ParseBackgrounds(b []byte) []SystemWallpaper {
	type entry struct {
		Deleted string `xml:"deleted,attr"`
		Names   []struct {
			Lang string `xml:"lang,attr"`
			Text string `xml:",chardata"`
		} `xml:"name"`
		Untrans  string `xml:"_name"`
		Filename string `xml:"filename"`
		Dark     string `xml:"filename-dark"`
		Options  string `xml:"options"`
	}
	dec := xml.NewDecoder(strings.NewReader(string(b)))
	dec.Strict = false
	var out []SystemWallpaper
	for {
		tok, err := dec.Token()
		if err != nil {
			return out
		}
		se, ok := tok.(xml.StartElement)
		if !ok || se.Name.Local != "wallpaper" {
			continue
		}
		var e entry
		if err := dec.DecodeElement(&e, &se); err != nil {
			return out
		}
		light, dark := strings.TrimSpace(e.Filename), strings.TrimSpace(e.Dark)
		if e.Deleted == "true" || !filepath.IsAbs(light) {
			continue
		}
		light = filepath.Clean(light)
		if dark != "" && (!filepath.IsAbs(dark) || filepath.Clean(dark) == light) {
			dark = ""
		}
		if dark != "" {
			dark = filepath.Clean(dark)
		}
		name := ""
		for _, n := range e.Names {
			if t := strings.TrimSpace(n.Text); t != "" && (name == "" || n.Lang == "") {
				name = t
				if n.Lang == "" {
					break
				}
			}
		}
		if name == "" {
			name = strings.TrimSpace(e.Untrans)
		}
		if name == "" {
			name = filepath.Base(light)
		}
		fit := strings.TrimSpace(e.Options)
		if !slices.Contains(WallpaperFits, fit) {
			fit = ""
		}
		out = append(out, SystemWallpaper{Name: name, Light: light, Dark: dark, Fit: fit})
	}
}

// WallpaperName names a picture for people: a system wallpaper's name ("Default Background", with "(dark)"
// for its dark picture), else PictureName.
func WallpaperName(system []SystemWallpaper, v string) string {
	for _, w := range system {
		switch v {
		case w.Light:
			return w.Name
		case w.Dark:
			return w.Name + " (dark)"
		}
	}
	return PictureName(v)
}
