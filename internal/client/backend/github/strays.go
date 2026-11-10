package github

import (
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/tyler-builds/uas/internal/client/appimage"
)

// Stray is an AppImage file on this system that uas doesn't manage: one another tool set up with a launcher,
// or one lying in a folder.
type Stray struct {
	Path string
	// Launchers are the launchers other tools wrote for it. uas replaces them when it takes the file over.
	Launchers []string
	// LauncherName is the name its launcher gives it, when it has one.
	LauncherName string
	// AppImageLauncher is true when one of its launchers is AppImageLauncher's.
	AppImageLauncher bool
}

// Strays lists the AppImage files uas doesn't manage: the ones that launchers of other tools run, wherever
// they are, and the ones in folders, which are looked at on their top level only. Each is a real AppImage
// (by its first bytes, not its name) and is listed once.
func (b *Backend) Strays(folders []string) []Stray {
	byPath := map[string]*Stray{}
	var order []string
	add := func(path string) *Stray {
		path = canonical(path)
		if s, ok := byPath[path]; ok {
			return s
		}
		if b.owns(path) || !isAppImage(path) {
			return nil
		}
		s := &Stray{Path: path}
		byPath[path] = s
		order = append(order, path)
		return s
	}
	for _, fl := range b.foreignLaunchers() {
		s := add(fl.target)
		if s == nil {
			continue
		}
		s.Launchers = append(s.Launchers, fl.file)
		if s.LauncherName == "" {
			s.LauncherName = fl.name
		}
		if strings.HasPrefix(filepath.Base(fl.file), "appimagekit_") {
			s.AppImageLauncher = true
		}
	}
	for _, dir := range folders {
		entries, err := os.ReadDir(dir)
		if err != nil {
			continue
		}
		for _, e := range entries {
			if e.Type().IsRegular() && appImageFile.MatchString(e.Name()) {
				add(filepath.Join(dir, e.Name()))
			}
		}
	}
	sort.Strings(order)
	out := make([]Stray, 0, len(order))
	for _, p := range order {
		out = append(out, *byPath[p])
	}
	return out
}

// canonical cleans a path and spells /var/home as /home, which Fedora Atomic links to it, so one file has one
// path whichever way a launcher names it.
func canonical(p string) string {
	p = filepath.Clean(p)
	if strings.HasPrefix(p, "/var/home/") {
		p = p[len("/var"):]
	}
	return p
}

// isAppImage reports whether path is a regular file that starts like an AppImage.
func isAppImage(path string) bool {
	st, err := os.Stat(path)
	if err != nil || !st.Mode().IsRegular() {
		return false
	}
	f, err := os.Open(path)
	if err != nil {
		return false
	}
	defer f.Close()
	return appimage.IsType2(f)
}

// ID is the ID uas would keep the AppImage under if it were added as a file.
func (f *File) ID() string { return fileID(f) }

// Name is the app's name: what its own launcher says, else what its file name spells.
func (f *File) Name() string {
	if n := strings.TrimSpace(f.Info.Desktop["Name"]); n != "" {
		return n
	}
	return appimage.NameFromFile(f.Path)
}

// Version is the app's version, when the AppImage says.
func (f *File) Version() string { return fileVersion(f.Info) }

// UpdatesFrom says where the AppImage would update from if added as a file ("github.com/owner/repo", a host
// name), or "" when it doesn't say or uas can't follow it.
func (f *File) UpdatesFrom() string {
	if f.UpdateErr != nil {
		return ""
	}
	return updateOrigin(f.Update.String())
}
