package session

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/tyler-builds/uas/internal/client/appimage"
	"github.com/tyler-builds/uas/internal/client/backend"
	"github.com/tyler-builds/uas/internal/client/state"
)

// Launcher returns the launcher (desktop file) of an installed app, or "" if it has none uas can find, as
// command-line tools don't. ids are other names the app's launcher may have, such as its AppStream ID.
// An app waiting for a restart has none yet.
func (s *Session) Launcher(in backend.InstalledApp, ids ...string) string {
	if in.Pending {
		return ""
	}
	if in.Desktop != "" {
		if usable(in.Desktop) {
			return in.Desktop
		}
		return ""
	}
	names := append([]string{in.AppID}, ids...)
	for _, dir := range applicationDirs() {
		for _, id := range names {
			if id == "" || strings.ContainsAny(id, "/\\") {
				continue
			}
			p := filepath.Join(dir, strings.TrimSuffix(id, ".desktop")+".desktop")
			if usable(p) {
				return p
			}
		}
	}
	return ""
}

// usable reports whether a desktop file exists and is meant to be shown and run.
func usable(path string) bool {
	b, err := os.ReadFile(path)
	if err != nil {
		return false
	}
	d := appimage.ParseDesktop(string(b))
	return d["Exec"] != "" && d["NoDisplay"] != "true" && d["Hidden"] != "true" &&
		(d["Type"] == "" || d["Type"] == "Application")
}

// applicationDirs are where launchers live, user's first: $XDG_DATA_HOME, Flatpak's exports, $XDG_DATA_DIRS.
func applicationDirs() []string {
	var dirs []string
	if data, err := state.DataDir(); err == nil {
		dirs = append(dirs, filepath.Join(data, "applications"),
			filepath.Join(data, "flatpak", "exports", "share", "applications"))
	}
	dirs = append(dirs, "/var/lib/flatpak/exports/share/applications")
	xdg := os.Getenv("XDG_DATA_DIRS")
	if xdg == "" {
		xdg = "/usr/local/share:/usr/share"
	}
	for _, d := range filepath.SplitList(xdg) {
		if filepath.IsAbs(d) {
			dirs = append(dirs, filepath.Join(d, "applications"))
		}
	}
	return dirs
}

// Launch starts an app from its launcher, as the desktop would, and returns once it has started.
func (s *Session) Launch(desktopFile string) error {
	var cmd *exec.Cmd
	if gio, err := exec.LookPath("gio"); err == nil {
		cmd = exec.Command(gio, "launch", desktopFile)
	} else if gtk, err := exec.LookPath("gtk-launch"); err == nil {
		cmd = exec.Command(gtk, strings.TrimSuffix(filepath.Base(desktopFile), ".desktop"))
	} else {
		return errors.New("neither gio nor gtk-launch is installed to start apps")
	}
	if err := cmd.Start(); err != nil {
		return err
	}
	go cmd.Wait() // gio returns once the app has started
	return nil
}
