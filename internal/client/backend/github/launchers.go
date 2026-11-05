package github

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/tyler-builds/uas/internal/client/appimage"
	"github.com/tyler-builds/uas/internal/client/backend"
)

// foreignLauncher is a launcher another tool, such as Gear Lever, wrote for an AppImage.
type foreignLauncher struct {
	file    string // its path
	name    string
	wmclass string
	icon    []byte
	iconExt string
	target  string // the AppImage it runs
}

// foreignLaunchers lists the launchers in Dirs.Applications that run an AppImage and weren't written by uas.
func (b *Backend) foreignLaunchers() []foreignLauncher {
	entries, err := os.ReadDir(b.Dirs.Applications)
	if err != nil {
		return nil
	}
	var out []foreignLauncher
	for _, e := range entries {
		n := e.Name()
		if !strings.HasSuffix(n, ".desktop") || strings.HasPrefix(n, "uas-") || !e.Type().IsRegular() {
			continue
		}
		path := filepath.Join(b.Dirs.Applications, n)
		raw, err := readLimited(path, 256<<10)
		if err != nil {
			continue
		}
		d := appimage.ParseDesktop(string(raw))
		if d["X-UAS-Source"] != "" {
			continue
		}
		target := appImageTarget(d)
		if target == "" {
			continue
		}
		fl := foreignLauncher{file: path, name: d["Name"], wmclass: d["StartupWMClass"], target: target}
		if icon := d["Icon"]; filepath.IsAbs(icon) {
			if b, err := readLimited(icon, 4<<20); err == nil {
				if ext := appimage.IconExt(b); ext != "" {
					fl.icon, fl.iconExt = b, ext
				}
			}
		}
		out = append(out, fl)
	}
	return out
}

var appImageFile = regexp.MustCompile(`(?i)\.appimage$`)

// appImageTarget returns the AppImage a launcher runs: its TryExec, or the program of its Exec after any
// "env NAME=value" prefix, if that is an absolute path to an .AppImage file.
func appImageTarget(d map[string]string) string {
	if t := d["TryExec"]; filepath.IsAbs(t) && appImageFile.MatchString(t) {
		return t
	}
	for _, tok := range strings.Fields(d["Exec"]) {
		if tok == "env" || strings.Contains(tok, "=") {
			continue
		}
		tok = strings.Trim(tok, `"'`)
		if filepath.IsAbs(tok) && appImageFile.MatchString(tok) {
			return tok
		}
		break
	}
	return ""
}

// samePath compares paths, taking /home and /var/home (a symlink on Fedora Atomic) as the same.
func samePath(a, b string) bool {
	norm := func(p string) string {
		p = filepath.Clean(p)
		if strings.HasPrefix(p, "/var/home/") {
			p = p[len("/var"):]
		}
		return p
	}
	return norm(a) == norm(b)
}

// launchersOf returns the foreign launchers that run the AppImage at path.
func (b *Backend) launchersOf(path string) []foreignLauncher {
	var out []foreignLauncher
	for _, fl := range b.foreignLaunchers() {
		if samePath(fl.target, path) {
			out = append(out, fl)
		}
	}
	return out
}

// brokenLaunchersNamed returns the foreign launchers named name whose AppImage is gone, as when uas added
// the AppImage from another tool's folder before it took over launchers.
func (b *Backend) brokenLaunchersNamed(name string) []foreignLauncher {
	var out []foreignLauncher
	for _, fl := range b.foreignLaunchers() {
		if !strings.EqualFold(strings.TrimSpace(fl.name), strings.TrimSpace(name)) {
			continue
		}
		if _, err := os.Stat(fl.target); os.IsNotExist(err) {
			out = append(out, fl)
		}
	}
	return out
}

// adoptLauncherDetails fills in what uas couldn't read from the AppImage from the launchers it takes over:
// the window class and, when the image gave none, the icon. It returns the window class to keep.
func adoptLauncherDetails(info *appimage.Info, fls []foreignLauncher) string {
	wmclass := ""
	for _, fl := range fls {
		if wmclass == "" && fl.wmclass != "" {
			wmclass = fl.wmclass
		}
		if len(info.Icon) == 0 && len(fl.icon) > 0 {
			info.Icon, info.IconExt = fl.icon, fl.iconExt
		}
	}
	return wmclass
}

// takeOver removes launchers uas replaced and points the GNOME dock's favorites at uas's launcher instead.
func (b *Backend) takeOver(ctx context.Context, fls []foreignLauncher, desktopFile string,
	progress func(backend.Progress)) {
	if len(fls) == 0 {
		return
	}
	ids := map[string]bool{}
	for _, fl := range fls {
		if err := os.Remove(fl.file); err == nil {
			ids[filepath.Base(fl.file)] = true
			report(progress, "Replaced the launcher "+filepath.Base(fl.file)+" that another tool made")
		}
	}
	b.replaceFavorites(ctx, ids, filepath.Base(desktopFile))
	b.refreshLaunchers(ctx)
}

var favoriteID = regexp.MustCompile(`'([^']*)'`)

// replaceFavorites swaps launcher IDs in GNOME's dock favorites, keeping their places.
func (b *Backend) replaceFavorites(ctx context.Context, old map[string]bool, id string) {
	if b.Runner == nil || len(old) == 0 {
		return
	}
	out, err := b.Runner.Run(ctx, "gsettings", []string{"get", "org.gnome.shell", "favorite-apps"}, nil)
	if err != nil {
		return // not GNOME, or no gsettings
	}
	var favs []string
	replaced, seen := false, false
	for _, m := range favoriteID.FindAllStringSubmatch(out, -1) {
		f := m[1]
		if old[f] {
			replaced = true
			f = id
		}
		if f == id {
			if seen {
				continue
			}
			seen = true
		}
		favs = append(favs, "'"+f+"'")
	}
	if !replaced {
		return
	}
	_, _ = b.Runner.Run(ctx, "gsettings", []string{"set", "org.gnome.shell", "favorite-apps",
		"[" + strings.Join(favs, ", ") + "]"}, nil)
}

func readLimited(path string, limit int64) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	return io.ReadAll(io.LimitReader(f, limit))
}
