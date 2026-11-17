package appearance

import (
	"bufio"
	"io"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"

	"github.com/tyler-builds/uas/internal/catalog"
	"github.com/tyler-builds/uas/internal/client/state"
)

// Theme is an icon, cursor, GTK or GNOME Shell theme on this system, whoever installed it.
type Theme struct {
	Name    string // its folder name, which the desktop's settings use
	Title   string // what its index.theme calls it, if anything
	Dir     string
	Icons   bool
	Cursors bool
	// GTK is true for a GTK theme (from GTKThemes), and GTK4 when it has files for GTK 4 apps too.
	GTK  bool
	GTK4 bool
	// Shell is true for a GNOME Shell theme (from ShellThemes), and for a GTK theme whose folder has one.
	Shell bool
	// User is true for a theme in the user's own folders; the others came with the system.
	User bool
	// By is the catalog ID of the uas install it came from, or "".
	By string
}

// Themes lists the icon and cursor themes the desktop can find: the user's (~/.icons, then
// ~/.local/share/icons) and the system's, where a theme the desktop would find first hides one of the same
// name. They are sorted by name.
func (in *Installer) Themes() ([]Theme, error) {
	st, err := in.State.Load()
	if err != nil {
		return nil, err
	}
	by := map[string]string{}
	for _, rec := range st.Appearance {
		if catalog.Kind(rec.Kind) == catalog.KindGTKTheme {
			continue
		}
		for _, t := range rec.Themes {
			by[t] = rec.ID
		}
	}
	type root struct {
		dir  string
		user bool
	}
	var roots []root
	if in.Home != "" {
		roots = append(roots, root{filepath.Join(in.Home, ".icons"), true})
	}
	roots = append(roots, root{in.IconsDir(), true})
	for _, d := range in.SystemData {
		roots = append(roots, root{filepath.Join(d, "icons"), false})
	}
	seen := map[string]bool{}
	var out []Theme
	for _, r := range roots {
		entries, err := os.ReadDir(r.dir)
		if err != nil {
			continue
		}
		for _, e := range entries {
			name := e.Name()
			if seen[name] || strings.HasPrefix(name, ".") || name == "default" || name == "hicolor" || name == "locolor" {
				continue
			}
			dir := filepath.Join(r.dir, name)
			if fi, err := os.Stat(dir); err != nil || !fi.IsDir() {
				continue
			}
			t := Theme{Name: name, Dir: dir, User: r.user}
			if r.user && filepath.Dir(dir) == in.IconsDir() {
				t.By = by[name]
			}
			t.Title, t.Icons = readIndex(filepath.Join(dir, "index.theme"))
			if fi, err := os.Stat(filepath.Join(dir, "cursors")); err == nil && fi.IsDir() {
				t.Cursors = true
			}
			if !t.Icons && !t.Cursors {
				continue
			}
			seen[name] = true
			out = append(out, t)
		}
	}
	sort.Slice(out, func(i, j int) bool { return strings.ToLower(out[i].Name) < strings.ToLower(out[j].Name) })
	return out, nil
}

// GTKThemes lists the GTK themes GTK can find: the user's (~/.local/share/themes, then ~/.themes, the order
// GTK looks in) and the system's, where a theme GTK would find first hides one of the same name. A folder is
// a GTK theme when its gtk-3.0 or gtk-4.0 folder has a gtk.css or gtk-dark.css. GTK's own themes (Adwaita,
// HighContrast), which it has built in, are listed only when a folder of theirs is. They are sorted by name.
func (in *Installer) GTKThemes() ([]Theme, error) {
	st, err := in.State.Load()
	if err != nil {
		return nil, err
	}
	by := map[string]string{}
	for _, rec := range st.Appearance {
		if catalog.Kind(rec.Kind) == catalog.KindGTKTheme {
			for _, t := range rec.Themes {
				by[t] = rec.ID
			}
		}
	}
	type root struct {
		dir  string
		user bool
	}
	roots := []root{{in.ThemesDir(), true}}
	if in.Home != "" {
		roots = append(roots, root{filepath.Join(in.Home, ".themes"), true})
	}
	for _, d := range in.SystemData {
		roots = append(roots, root{filepath.Join(d, "themes"), false})
	}
	has := func(dir, sub string) bool {
		for _, f := range []string{"gtk.css", "gtk-dark.css"} {
			if fi, err := os.Stat(filepath.Join(dir, sub, f)); err == nil && fi.Mode().IsRegular() {
				return true
			}
		}
		return false
	}
	seen := map[string]bool{}
	var out []Theme
	for _, r := range roots {
		entries, err := os.ReadDir(r.dir)
		if err != nil {
			continue
		}
		for _, e := range entries {
			name := e.Name()
			if seen[name] || strings.HasPrefix(name, ".") {
				continue
			}
			dir := filepath.Join(r.dir, name)
			if fi, err := os.Stat(dir); err != nil || !fi.IsDir() {
				continue
			}
			gtk3, gtk4 := has(dir, "gtk-3.0"), has(dir, "gtk-4.0")
			if !gtk3 && !gtk4 {
				continue
			}
			t := Theme{Name: name, Dir: dir, User: r.user, GTK: true, GTK4: gtk4, Shell: hasShellCSS(dir)}
			if r.user && filepath.Dir(dir) == in.ThemesDir() {
				t.By = by[name]
			}
			seen[name] = true
			out = append(out, t)
		}
	}
	sort.Slice(out, func(i, j int) bool { return strings.ToLower(out[i].Name) < strings.ToLower(out[j].Name) })
	return out, nil
}

// readIndex reads the name an index.theme gives its icon theme, and whether it lists icon folders.
func readIndex(p string) (title string, icons bool) {
	f, err := os.Open(p)
	if err != nil {
		return "", false
	}
	defer f.Close()
	sc := bufio.NewScanner(io.LimitReader(f, 256<<10))
	sc.Buffer(make([]byte, 64<<10), 1<<20)
	in := false
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if strings.HasPrefix(line, "[") {
			in = line == "[Icon Theme]"
			continue
		}
		key, value, ok := strings.Cut(line, "=")
		if !in || !ok {
			continue
		}
		switch strings.TrimSpace(key) {
		case "Name":
			title = strings.TrimSpace(value)
		case "Directories":
			icons = icons || strings.TrimSpace(value) != ""
		}
	}
	return title, icons
}

// Update is a font or theme uas installed whose catalog listing has a newer file.
type Update struct {
	Install state.AppearanceInstall
	Listing catalog.Listing
}

// Version is the update's version for people, or "" when the catalog has none or it hasn't changed (the
// author replaced the file without a new version number).
func (u Update) Version() string {
	if u.Listing.Version == u.Install.Version {
		return ""
	}
	return u.Listing.Version
}

// Variant is the theme to reinstall, which picks the file: the first theme installed.
func (u Update) Variant() string {
	if len(u.Install.Themes) > 0 {
		return u.Install.Themes[0]
	}
	return ""
}

// Updates compares uas's installs with their catalog listings. lookup returns an install's listing, or false
// when the catalog no longer has it. Installs of a file the catalog no longer lists, or whose themes are no
// longer in one file, aren't offered an update.
func Updates(recs []state.AppearanceInstall, lookup func(id string) (catalog.Listing, bool)) []Update {
	var out []Update
	for _, rec := range recs {
		l, ok := lookup(rec.ID)
		if !ok {
			continue
		}
		var pinned string
		switch catalog.Kind(rec.Kind) {
		case catalog.KindFont:
			pinned = Pinned(l, "")
		default:
			a, ok := assetOf(l, rec)
			if !ok {
				continue
			}
			pinned = a.SHA256
		}
		if pinned != "" && !strings.EqualFold(pinned, rec.SHA256) {
			out = append(out, Update{Install: rec, Listing: l})
		}
	}
	return out
}

// assetOf is the catalog's file for an install: the file of the same name, else the one that has its
// themes.
func assetOf(l catalog.Listing, rec state.AppearanceInstall) (catalog.Asset, bool) {
	for _, a := range l.Assets {
		if a.Name == rec.Asset {
			return a, true
		}
	}
	for _, a := range l.Assets {
		if len(rec.Themes) > 0 && sameSet(a.Themes, rec.Themes) {
			return a, true
		}
	}
	return catalog.Asset{}, false
}

// InstalledAs returns the install that put a theme of a name in place, if uas did.
func InstalledAs(recs []state.AppearanceInstall, theme string) (state.AppearanceInstall, bool) {
	for _, r := range recs {
		if slices.Contains(r.Themes, theme) {
			return r, true
		}
	}
	return state.AppearanceInstall{}, false
}

// SampleIcons are icons nearly every icon theme draws, for showing what one looks like: a folder, a
// terminal and the settings, each with names to try in turn.
var SampleIcons = [][]string{
	{"folder", "inode-directory"},
	{"utilities-terminal", "org.gnome.Terminal", "terminal"},
	{"org.gnome.Settings", "preferences-system", "preferences-desktop"},
}

// IconFiles returns the files an icon theme draws icons with, one for each list of names that it (or a
// theme it inherits, but not hicolor) has: a scalable SVG when there is one, else the largest picture.
// themes are as Themes returns them.
func IconFiles(themes []Theme, theme string, names [][]string) []string {
	dirs := map[string]string{}
	for _, t := range themes {
		dirs[t.Name] = t.Dir
	}
	var chain []string // the theme, then the ones it inherits
	var walk func(name string, depth int)
	walk = func(name string, depth int) {
		d, ok := dirs[name]
		if !ok || depth > 3 || name == "hicolor" || slices.Contains(chain, d) {
			return
		}
		chain = append(chain, d)
		_, inherits := readIconIndex(filepath.Join(d, "index.theme"))
		for _, i := range inherits {
			walk(i, depth+1)
		}
	}
	walk(theme, 0)
	var out []string
	for _, alts := range names {
	found:
		for _, d := range chain {
			subdirs, _ := readIconIndex(filepath.Join(d, "index.theme"))
			for _, n := range alts {
				if f := bestIcon(d, subdirs, n); f != "" {
					out = append(out, f)
					break found
				}
			}
		}
	}
	return out
}

// bestIcon finds an icon in a theme's folders: a scalable SVG first, else the one of the largest size, and a
// one-color symbolic icon only when there is nothing else.
func bestIcon(dir string, subdirs []string, name string) string {
	best, score := "", -1<<30
	for _, sd := range subdirs {
		for _, ext := range []string{".svg", ".png"} {
			p := filepath.Join(dir, sd, name+ext)
			if fi, err := os.Stat(p); err != nil || !fi.Mode().IsRegular() || fi.Size() > 1<<20 {
				continue
			}
			// Full-color pictures before one-color symbolic ones; then scalable SVGs; then the largest.
			s := min(iconDirSize(sd), 512)
			switch {
			case strings.Contains(sd, "symbolic"):
				s -= 10000
			case ext == ".svg" && strings.Contains(sd, "scalable"):
				s += 2000
			case ext == ".svg":
				s += 1000
			}
			if s > score {
				best, score = p, s
			}
		}
	}
	return best
}

// iconDirSize reads the size from an icon folder's name ("48x48/apps", "apps/48", "48@2x"), 0 when it has
// none.
func iconDirSize(sd string) int {
	most := 0
	for _, part := range strings.FieldsFunc(sd, func(r rune) bool { return r == '/' || r == 'x' || r == '@' }) {
		n := 0
		for _, r := range part {
			if r < '0' || r > '9' {
				n = -1
				break
			}
			n = n*10 + int(r-'0')
		}
		most = max(most, n)
	}
	return most
}

// readIconIndex reads the icon folders and the inherited themes an index.theme lists.
func readIconIndex(p string) (dirs, inherits []string) {
	f, err := os.Open(p)
	if err != nil {
		return nil, nil
	}
	defer f.Close()
	sc := bufio.NewScanner(io.LimitReader(f, 256<<10))
	sc.Buffer(make([]byte, 64<<10), 1<<20)
	in := false
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if strings.HasPrefix(line, "[") {
			in = line == "[Icon Theme]"
			continue
		}
		key, value, ok := strings.Cut(line, "=")
		if !in || !ok {
			continue
		}
		list := func() []string {
			var out []string
			for _, v := range strings.Split(value, ",") {
				if v = strings.TrimSpace(v); v != "" && filepath.IsLocal(v) {
					out = append(out, v)
				}
			}
			return out
		}
		switch strings.TrimSpace(key) {
		case "Directories", "ScaledDirectories":
			dirs = append(dirs, list()...)
		case "Inherits":
			inherits = list()
		}
	}
	return dirs, inherits
}
