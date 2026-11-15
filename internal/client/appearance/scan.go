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

// Theme is an icon or cursor theme on this system, whoever installed it.
type Theme struct {
	Name    string // its folder name, which the desktop's settings use
	Title   string // what its index.theme calls it, if anything
	Dir     string
	Icons   bool
	Cursors bool
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
