package github

import (
	"os"
	"path/filepath"
	"strings"

	"github.com/tyler-builds/uas/internal/client/backend"
)

// What follows an AppImage when uas takes it over from where the user or another tool kept it, besides its
// launcher and its place in the dock (see takeOver): the settings and folders that name its old launcher or
// its old path. The app's own data is in the home folder's usual places and doesn't move.

// replaceDefaultApps rewrites the user's default-app settings (mimeapps.list) so that entries naming the
// launchers in old name id instead. A browser that was the default for web links stays the default: without
// this the setting names a launcher that is gone, and the desktop picks another app that opens the type.
// It returns how many entries it changed.
func (b *Backend) replaceDefaultApps(old map[string]bool, id string) int {
	if len(old) == 0 || id == "" {
		return 0
	}
	changed := 0
	for _, file := range b.mimeappsFiles() {
		raw, err := readLimited(file, 4<<20)
		if err != nil {
			continue
		}
		lines := strings.Split(string(raw), "\n")
		n := 0
		for i, line := range lines {
			key, val, ok := strings.Cut(line, "=")
			if !ok || strings.HasPrefix(strings.TrimSpace(line), "[") || strings.HasPrefix(strings.TrimSpace(line), "#") {
				continue
			}
			var out []string
			hit := false
			for _, app := range strings.Split(val, ";") {
				a := strings.TrimSpace(app)
				if a == "" {
					continue
				}
				if old[a] {
					a, hit = id, true
				}
				if !containsString(out, a) {
					out = append(out, a)
				}
			}
			if !hit {
				continue
			}
			val = strings.Join(out, ";")
			if strings.HasSuffix(strings.TrimSpace(line), ";") {
				val += ";"
			}
			lines[i] = key + "=" + val
			n++
		}
		if n > 0 && writeBeside(file, []byte(strings.Join(lines, "\n"))) == nil {
			changed += n
		}
	}
	return changed
}

// mimeappsFiles are the user's default-app settings: the one in the config folder, and the older one in the
// applications folder.
func (b *Backend) mimeappsFiles() []string {
	var files []string
	if b.Dirs.Config != "" {
		files = append(files, filepath.Join(b.Dirs.Config, "mimeapps.list"))
	}
	return append(files, filepath.Join(b.Dirs.Applications, "mimeapps.list"))
}

// DefaultAppsNaming returns the launcher IDs among ids that the user's default-app settings name.
func (b *Backend) DefaultAppsNaming(ids ...string) []string {
	var out []string
	for _, file := range b.mimeappsFiles() {
		raw, err := readLimited(file, 4<<20)
		if err != nil {
			continue
		}
		for _, line := range strings.Split(string(raw), "\n") {
			_, val, ok := strings.Cut(line, "=")
			if !ok {
				continue
			}
			for _, app := range strings.Split(val, ";") {
				if a := strings.TrimSpace(app); containsString(ids, a) && !containsString(out, a) {
					out = append(out, a)
				}
			}
		}
	}
	return out
}

// RepointDefaultApps makes the default-app settings that name the launcher oldID name newLauncher (a launcher
// file of uas's) instead, when oldID's launcher is gone: what is left when an AppImage moved to uas before uas
// carried these settings over. It returns how many entries it changed.
func (b *Backend) RepointDefaultApps(oldID, newLauncher string) int {
	if oldID == "" || strings.ContainsAny(oldID, "/;=\n") || !b.owns(newLauncher) {
		return 0
	}
	if _, err := os.Stat(filepath.Join(b.Dirs.Applications, oldID)); err == nil {
		return 0 // still a launcher: the setting is right
	}
	return b.replaceDefaultApps(map[string]bool{oldID: true}, filepath.Base(newLauncher))
}

// repointAutostart makes the user's autostart entries that start the AppImage at its old path start it at its
// new one, so an app set to start at login still does.
func (b *Backend) repointAutostart(oldPath, newPath string) int {
	if b.Dirs.Config == "" {
		return 0
	}
	// A path under the home folder is written /home/... or /var/home/... (the same folder on Fedora Atomic).
	olds := []string{oldPath}
	if c := canonical(oldPath); c != oldPath {
		olds = append(olds, c)
	} else if strings.HasPrefix(oldPath, "/home/") {
		olds = append(olds, "/var"+oldPath)
	}
	entries, _ := filepath.Glob(filepath.Join(b.Dirs.Config, "autostart", "*.desktop"))
	changed := 0
	for _, file := range entries {
		raw, err := readLimited(file, 256<<10)
		if err != nil {
			continue
		}
		lines := strings.Split(string(raw), "\n")
		hit := false
		for i, line := range lines {
			if !strings.HasPrefix(line, "Exec=") && !strings.HasPrefix(line, "TryExec=") {
				continue
			}
			for _, o := range olds {
				if strings.Contains(line, o) {
					lines[i], hit = strings.ReplaceAll(lines[i], o, newPath), true
				}
			}
		}
		if hit && writeBeside(file, []byte(strings.Join(lines, "\n"))) == nil {
			changed++
		}
	}
	return changed
}

// movePortable moves an AppImage's portable folders with it. An AppImage run with a folder named after its
// file plus ".home" or ".config" beside it keeps its data there instead of in the home folder, so leaving
// them behind would start the app empty.
func movePortable(oldPath, newPath string) (moved, stayed []string) {
	for _, suffix := range []string{".home", ".config"} {
		from, to := oldPath+suffix, newPath+suffix
		if st, err := os.Stat(from); err != nil || !st.IsDir() {
			continue
		}
		if _, err := os.Lstat(to); err == nil {
			stayed = append(stayed, from) // uas's copy has one already; don't mix them
			continue
		}
		if err := os.Rename(from, to); err != nil {
			stayed = append(stayed, from) // another disk, say: the data stays where it was, not lost
			continue
		}
		moved = append(moved, to)
	}
	return moved, stayed
}

// carryOver moves what follows the AppImage file itself, and says what it did.
func (b *Backend) carryOver(oldPath, newPath string, progress func(backend.Progress)) {
	moved, stayed := movePortable(oldPath, newPath)
	for _, m := range moved {
		report(progress, "Moved its portable data folder to "+m)
	}
	for _, s := range stayed {
		report(progress, "Its portable data folder stays in "+s+"; move it next to "+newPath+" to keep using it")
	}
	if n := b.repointAutostart(oldPath, newPath); n > 0 {
		report(progress, "Pointed its start-at-login entry at the new file")
	}
}

// writeBeside replaces a file with data by writing a temporary file beside it and renaming that over it,
// keeping its permissions.
func writeBeside(file string, data []byte) error {
	st, err := os.Stat(file)
	if err != nil {
		return err
	}
	tmp := file + ".uas-tmp"
	if err := os.WriteFile(tmp, data, st.Mode().Perm()); err != nil {
		return err
	}
	if err := os.Rename(tmp, file); err != nil {
		os.Remove(tmp)
		return err
	}
	return nil
}

func containsString(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}
