package appearance

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"

	looks "github.com/tyler-builds/uas/internal/appearance"
	"github.com/tyler-builds/uas/internal/client/state"
)

// GTK 4 files. GTK 4 apps, and libadwaita's above all, don't draw themselves with the GTK theme setting:
// they load the user's own style, $XDG_CONFIG_HOME/gtk-4.0/gtk.css, when they start. So when uas uses a GTK
// theme whose folder has a gtk-4.0 folder with a gtk.css, it copies that folder's files (gtk.css,
// gtk-dark.css, assets, and whatever else the styles load) into ~/.config/gtk-4.0 too.
//
// They are copies, not links into the theme's folder as theme install scripts make: a Flatpak app that may
// read ~/.config/gtk-4.0 (Bazzite lets every app) can't see ~/.local/share/themes, so a link would lead
// nowhere in it, and a copy stays whole when the theme is removed or updated.
//
// Only the names uas wrote there are uas's (state.GTK4Files.Written). A file or folder of the user's under a
// name the theme has is moved aside first, into a folder under $XDG_DATA_HOME/uas, and put back, as it was,
// when uas's go: on Revert, when a theme without GTK 4 files is used, and when the theme is removed.
// Everything else in ~/.config/gtk-4.0 (settings.ini, the user's other files) is never touched.

// GTK4Dir is where GTK 4 apps load the user's own style from: $XDG_CONFIG_HOME/gtk-4.0.
func (in *Installer) GTK4Dir() string { return filepath.Join(in.Config, "gtk-4.0") }

// GTK4 returns what uas put in ~/.config/gtk-4.0, or nil.
func (in *Installer) GTK4() (*state.GTK4Files, error) {
	st, err := in.State.Load()
	return st.GTK4, err
}

// builtinGTKThemes are the GTK themes built into GTK, which need no folder.
var builtinGTKThemes = []string{"Adwaita", "Adwaita-dark", "HighContrast", "HighContrastInverse", "Default"}

// HasGTK4Files reports whether the GTK theme of a name, as GTK finds it, has files for GTK 4 apps.
func (in *Installer) HasGTK4Files(theme string) bool {
	src, _ := in.gtk4Source(theme)
	return src != ""
}

// gtk4Source finds the GTK theme of a name as GTK does (~/.local/share/themes, ~/.themes, then the system's)
// and returns its gtk-4.0 folder, when that has a gtk.css, and the theme's folder.
func (in *Installer) gtk4Source(theme string) (src, dir string) {
	if !looks.ThemeName.MatchString(theme) {
		return "", ""
	}
	bases := []string{in.ThemesDir()}
	if in.Home != "" {
		bases = append(bases, filepath.Join(in.Home, ".themes"))
	}
	for _, d := range in.SystemData {
		bases = append(bases, filepath.Join(d, "themes"))
	}
	for _, b := range bases {
		d := filepath.Join(b, theme)
		g := filepath.Join(d, "gtk-4.0")
		if fi, err := os.Stat(filepath.Join(g, "gtk.css")); err == nil && fi.Mode().IsRegular() {
			return g, d
		}
	}
	return "", ""
}

// gtk4Limits cap what is copied from a theme's gtk-4.0 folder: its styles and pictures are a few MB.
const (
	gtk4MaxEntries = 20_000
	gtk4MaxBytes   = 256 << 20
)

// syncGTK4Locked makes ~/.config/gtk-4.0 hold the GTK 4 files of a GTK theme: uas's earlier files go, the
// user's files under the names the theme has are moved aside, and the theme's are copied in. With style
// false, or for a theme without GTK 4 files, it only takes uas's files away and puts the user's back: the
// folder is as it was before uas. st's record is kept to what is on disk, even when it fails part-way; the
// caller saves st.
func (in *Installer) syncGTK4Locked(st *state.State, theme string, style bool) error {
	dir := in.GTK4Dir()
	var names []string
	if style {
		if src, themeDir := in.gtk4Source(theme); src != "" {
			// Copied first, into a folder of its own next to where the files go, so a copy that fails
			// changes nothing.
			if err := os.MkdirAll(dir, 0o700); err != nil {
				return err
			}
			stage, err := os.MkdirTemp(dir, ".uas-gtk4-")
			if err != nil {
				return err
			}
			defer os.RemoveAll(stage)
			if names, err = copyGTK4(src, themeDir, stage); err != nil {
				return fmt.Errorf("copying %s's GTK 4 files: %w", theme, err)
			}
			return in.placeGTK4(st, theme, dir, stage, names)
		}
	}
	return in.placeGTK4(st, theme, dir, "", nil)
}

// placeGTK4 puts the files staged under names into dir, after taking away uas's earlier files and moving
// the user's aside, and puts back the user's files whose names it no longer needs.
func (in *Installer) placeGTK4(st *state.State, theme, dir, stage string, names []string) error {
	rec := state.GTK4Files{Theme: theme, Dir: dir, At: in.now().UTC()}
	if st.GTK4 != nil {
		rec.Backup = st.GTK4.Backup
	}
	var errs []error
	// uas's own files go.
	if old := st.GTK4; old != nil {
		for _, n := range old.Written {
			if !plainName(n) {
				continue
			}
			if err := os.RemoveAll(filepath.Join(old.Dir, n)); err != nil {
				errs = append(errs, err)
				rec.Written = append(rec.Written, n) // still there, still uas's
			}
		}
		if len(rec.Written) > 0 {
			rec.Dir = old.Dir
		}
		// The user's files come back, unless the new files take their names.
		for _, n := range old.BackedUp {
			if !plainName(n) {
				continue
			}
			if old.Dir == dir && slices.Contains(names, n) {
				rec.BackedUp = append(rec.BackedUp, n)
				continue
			}
			to := filepath.Join(old.Dir, n)
			if _, err := os.Lstat(to); err == nil {
				errs = append(errs, fmt.Errorf("your %s is kept in %s, as %s has a file of that name now", n,
					old.Backup, old.Dir))
				rec.BackedUp = append(rec.BackedUp, n)
				continue
			}
			if err := move(filepath.Join(old.Backup, n), to); err != nil {
				errs = append(errs, err)
				rec.BackedUp = append(rec.BackedUp, n)
			}
		}
	}
	if len(errs) == 0 {
		for _, n := range names {
			to := filepath.Join(dir, n)
			if _, err := os.Lstat(to); err == nil {
				// The user's own: moved aside, to be put back when uas's file goes.
				if rec.Backup == "" {
					b, err := in.newGTK4Backup()
					if err != nil {
						errs = append(errs, err)
						break
					}
					rec.Backup = b
				}
				aside := filepath.Join(rec.Backup, n)
				if _, err := os.Lstat(aside); err == nil {
					errs = append(errs, fmt.Errorf("uas keeps an earlier %s of yours in %s already, so it won't "+
						"move %s aside", n, rec.Backup, to))
					break
				}
				if err := move(to, aside); err != nil {
					errs = append(errs, err)
					break
				}
				rec.BackedUp = append(rec.BackedUp, n)
			}
			if err := os.Rename(filepath.Join(stage, n), to); err != nil {
				errs = append(errs, err)
				break
			}
			rec.Written = append(rec.Written, n)
		}
	}
	if len(rec.BackedUp) == 0 && rec.Backup != "" {
		if err := os.Remove(rec.Backup); err == nil || errors.Is(err, os.ErrNotExist) {
			rec.Backup = ""
		}
	}
	if len(rec.Written) == 0 && len(rec.BackedUp) == 0 {
		st.GTK4 = nil
	} else {
		st.GTK4 = &rec
	}
	return errors.Join(errs...)
}

// newGTK4Backup makes a new folder for the user's files that uas moves aside.
func (in *Installer) newGTK4Backup() (string, error) {
	base := filepath.Join(in.Data, "uas")
	if err := os.MkdirAll(base, 0o755); err != nil {
		return "", err
	}
	return os.MkdirTemp(base, "gtk-4.0-yours-")
}

// plainName reports whether a recorded name is one entry of a folder, so a damaged state file can't lead uas
// anywhere else.
func plainName(n string) bool {
	return n != "" && filepath.IsLocal(n) && !strings.ContainsRune(n, filepath.Separator)
}

// copyGTK4 copies the entries of a theme's gtk-4.0 folder into dst, and returns their names. Links are
// followed while they stay within the theme's folder (a variant's assets can be a link to another's) and
// left out otherwise; files are written without execute permission, and anything but files and folders is
// left out.
func copyGTK4(src, themeDir, dst string) ([]string, error) {
	root, err := filepath.EvalSymlinks(themeDir)
	if err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(src)
	if err != nil {
		return nil, err
	}
	c := &treeCopy{root: root}
	var names []string
	for _, e := range entries {
		n := e.Name()
		if strings.HasPrefix(n, ".") {
			continue
		}
		ok, err := c.copy(filepath.Join(src, n), filepath.Join(dst, n), 0)
		if err != nil {
			return nil, err
		}
		if ok {
			names = append(names, n)
		}
	}
	return names, nil
}

type treeCopy struct {
	root    string
	entries int
	bytes   int64
}

// copy copies src to dst, reporting whether anything was written.
func (c *treeCopy) copy(src, dst string, depth int) (bool, error) {
	if depth > 32 {
		return false, nil
	}
	real, err := filepath.EvalSymlinks(src)
	if err != nil {
		return false, nil // a link to nothing
	}
	if rel, err := filepath.Rel(c.root, real); err != nil || !filepath.IsLocal(rel) {
		return false, nil // a link out of the theme
	}
	fi, err := os.Stat(real)
	if err != nil {
		return false, err
	}
	if c.entries++; c.entries > gtk4MaxEntries {
		return false, fmt.Errorf("its gtk-4.0 folder holds more than %d files", gtk4MaxEntries)
	}
	switch {
	case fi.IsDir():
		if err := os.Mkdir(dst, 0o755); err != nil {
			return false, err
		}
		entries, err := os.ReadDir(real)
		if err != nil {
			return false, err
		}
		for _, e := range entries {
			if _, err := c.copy(filepath.Join(src, e.Name()), filepath.Join(dst, e.Name()), depth+1); err != nil {
				return false, err
			}
		}
		return true, nil
	case fi.Mode().IsRegular():
		if c.bytes += fi.Size(); c.bytes > gtk4MaxBytes {
			return false, fmt.Errorf("its gtk-4.0 folder holds more than %s", humanSize(gtk4MaxBytes))
		}
		return true, copyFile(real, dst)
	}
	return false, nil
}

func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if err != nil {
		return err
	}
	_, err = io.Copy(out, in)
	if cerr := out.Close(); err == nil {
		err = cerr
	}
	return err
}
