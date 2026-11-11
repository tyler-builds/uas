// Package selfinstall installs uas for the user who runs it, from the program alone: the program goes in
// ~/.local/bin as uas, with uas-gui beside it (a link to it, which opens the app), the launcher in the app
// menu and the icon. A release of uas is that one program, so running a downloaded copy installs it.
//
// Nothing here needs root, which suits immutable systems (Fedora Atomic, Bazzite, Bluefin, Aurora).
package selfinstall

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"

	packaging "github.com/tyler-builds/uas/packaging/linux"
)

const (
	// AppID names the launcher and the icon, and is the app's Wayland app ID.
	AppID = "io.github.tyler_builds.uas"
	// Program is the installed program's name; GUI is the link to it that opens the app.
	Program = "uas"
	GUI     = "uas-gui"

	desktopFile = AppID + ".desktop"
	iconFile    = AppID + ".svg"
)

// Layout is where uas is installed for one user.
type Layout struct {
	Bin   string // the programs: ~/.local/bin
	Apps  string // the launcher: $XDG_DATA_HOME/applications
	Icons string // the icon: $XDG_DATA_HOME/icons/hicolor/scalable/apps
}

// Default is where uas installs itself.
func Default() (Layout, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return Layout{}, err
	}
	data := os.Getenv("XDG_DATA_HOME")
	if !filepath.IsAbs(data) {
		data = filepath.Join(home, ".local", "share")
	}
	return Layout{
		Bin:   filepath.Join(home, ".local", "bin"),
		Apps:  filepath.Join(data, "applications"),
		Icons: filepath.Join(data, "icons", "hicolor", "scalable", "apps"),
	}, nil
}

// Installed is the layout of the installed uas: Default, with the programs' folder read from the launcher,
// since copies installed with the old install.sh may have put them elsewhere (with $PREFIX).
func Installed() (Layout, error) {
	l, err := Default()
	if err != nil {
		return l, err
	}
	if b, err := os.ReadFile(l.Desktop()); err == nil {
		if exe := execPath(b); filepath.IsAbs(exe) {
			if base := filepath.Base(exe); base == Program || base == GUI {
				l.Bin = filepath.Dir(exe)
			}
		}
	}
	return l, nil
}

// ProgramPath is the installed program; GUIPath is the link that opens the app.
func (l Layout) ProgramPath() string { return filepath.Join(l.Bin, Program) }
func (l Layout) GUIPath() string     { return filepath.Join(l.Bin, GUI) }

// Desktop is the launcher's path.
func (l Layout) Desktop() string { return filepath.Join(l.Apps, desktopFile) }

// Icon is the icon's path.
func (l Layout) Icon() string { return filepath.Join(l.Icons, iconFile) }

// Runs reports whether the program at exe is this layout's program, by its path or through uas-gui.
func (l Layout) Runs(exe string) bool {
	fi, err := os.Stat(exe)
	if err != nil {
		return false
	}
	for _, p := range []string{l.ProgramPath(), l.GUIPath()} {
		if pi, err := os.Stat(p); err == nil && os.SameFile(fi, pi) {
			return true
		}
	}
	return false
}

// Install copies the program at src into place and adds uas-gui, the launcher and the icon. Installing the
// program that is already in place only adds the rest.
func (l Layout) Install(src string) error {
	if err := os.MkdirAll(l.Bin, 0o755); err != nil {
		return err
	}
	if !samePath(src, l.ProgramPath()) {
		if err := copyProgram(src, l.ProgramPath()); err != nil {
			return err
		}
	}
	return l.Integrate()
}

// Integrate makes uas-gui a link to uas and writes the launcher and the icon this program carries, where
// they differ from what is there.
func (l Layout) Integrate() error {
	if err := l.LinkGUI(); err != nil {
		return err
	}
	changed, err := writeIfChanged(l.Desktop(), Launcher(l.GUIPath()), 0o644)
	if err != nil {
		return err
	}
	if _, err := writeIfChanged(l.Icon(), packaging.Icon, 0o644); err != nil {
		return err
	}
	if changed {
		// Desktops that cache launchers notice the new one; most watch the folder anyway.
		if p, err := exec.LookPath("update-desktop-database"); err == nil {
			exec.Command(p, "-q", l.Apps).Run()
		}
	}
	return nil
}

// Refresh brings an install up to date with the program running from it (exe), which an update has just
// replaced. Older copies of uas installed two programs, uas and uas-gui; a release now is one program that is
// both, and an update by such a copy left two copies of it, so uas-gui becomes the link to uas. The launcher
// and the icon are refreshed if the launcher is there: someone who removed it keeps it removed.
func (l Layout) Refresh(exe string) error {
	if !l.Runs(exe) {
		return nil
	}
	if fi, err := os.Lstat(l.GUIPath()); err == nil && fi.Mode().IsRegular() {
		// The old updater replaced uas only where it was installed: make sure it is this program.
		if !samePath(exe, l.ProgramPath()) {
			if same, _ := sameContent(exe, l.ProgramPath()); !same {
				if err := copyProgram(exe, l.ProgramPath()); err != nil {
					return err
				}
			}
		}
		if err := l.LinkGUI(); err != nil {
			return err
		}
	}
	if _, err := os.Stat(l.Desktop()); err != nil {
		return nil
	}
	return l.Integrate()
}

// Remove removes the programs, the launcher and the icon. Apps installed with uas, and its settings and
// catalog, stay.
func (l Layout) Remove() error {
	var errs []error
	for _, p := range []string{l.ProgramPath(), l.GUIPath(), l.Desktop(), l.Icon()} {
		if err := os.Remove(p); err != nil && !errors.Is(err, os.ErrNotExist) {
			errs = append(errs, err)
		}
	}
	if p, err := exec.LookPath("update-desktop-database"); err == nil {
		exec.Command(p, "-q", l.Apps).Run()
	}
	return errors.Join(errs...)
}

// Launcher is the launcher's text, opening the app with the program at gui.
func Launcher(gui string) []byte {
	var out bytes.Buffer
	for _, line := range strings.SplitAfter(string(packaging.Desktop), "\n") {
		if strings.HasPrefix(line, "Exec=") {
			// The full path, so the launcher works whether or not the folder is on the PATH.
			line = "Exec=" + quoteExec(gui) + "\n"
		}
		out.WriteString(line)
	}
	return out.Bytes()
}

// quoteExec quotes a path for a launcher's Exec line, as the Desktop Entry spec asks.
func quoteExec(p string) string {
	if !strings.ContainsAny(p, " \t\n\"'\\><~|&;$*?#()`") {
		return p
	}
	r := strings.NewReplacer(`\`, `\\\\`, `"`, `\\"`, "`", "\\\\`", `$`, `\\$`)
	return `"` + r.Replace(p) + `"`
}

// execPath reads the program out of a launcher's Exec line.
func execPath(desktop []byte) string {
	for _, line := range strings.Split(string(desktop), "\n") {
		v, ok := strings.CutPrefix(line, "Exec=")
		if !ok {
			continue
		}
		if strings.HasPrefix(v, `"`) {
			// The file's escapes (\\ is \), then the quoting's (\x is x), up to the closing quote.
			v = strings.ReplaceAll(v[1:], `\\`, `\`)
			var b strings.Builder
			for i := 0; i < len(v); i++ {
				switch {
				case v[i] == '\\' && i+1 < len(v):
					i++
					b.WriteByte(v[i])
				case v[i] == '"':
					return b.String()
				default:
					b.WriteByte(v[i])
				}
			}
			return ""
		}
		exe, _, _ := strings.Cut(v, " ")
		return exe
	}
	return ""
}

// LinkGUI makes uas-gui a link to uas, in place of whatever is there.
func (l Layout) LinkGUI() error {
	if t, err := os.Readlink(l.GUIPath()); err == nil && t == Program {
		return nil
	}
	tmp := filepath.Join(l.Bin, "."+GUI+".link")
	os.Remove(tmp)
	if err := os.Symlink(Program, tmp); err != nil {
		return err
	}
	if err := os.Rename(tmp, l.GUIPath()); err != nil {
		os.Remove(tmp)
		return err
	}
	return nil
}

// copyProgram copies the program at src to dst, swapping it in whole so that a running copy of dst carries
// on and a failure leaves the old one.
func copyProgram(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	tmp, err := os.CreateTemp(filepath.Dir(dst), "."+filepath.Base(dst)+".install-*")
	if err != nil {
		return fmt.Errorf("can't write to %s: %w", filepath.Dir(dst), err)
	}
	defer os.Remove(tmp.Name())
	if _, err := io.Copy(tmp, in); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Chmod(0o755); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), dst)
}

// writeIfChanged writes b to path unless it holds b already, and reports whether it wrote.
func writeIfChanged(path string, b []byte, mode os.FileMode) (bool, error) {
	if old, err := os.ReadFile(path); err == nil && bytes.Equal(old, b) {
		return false, nil
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return false, err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, b, mode); err != nil {
		return false, err
	}
	if err := os.Rename(tmp, path); err != nil {
		os.Remove(tmp)
		return false, err
	}
	return true, nil
}

// samePath reports whether a and b are the same file.
func samePath(a, b string) bool {
	ai, err := os.Stat(a)
	if err != nil {
		return false
	}
	bi, err := os.Stat(b)
	return err == nil && os.SameFile(ai, bi)
}

// sameContent reports whether two files hold the same bytes.
func sameContent(a, b string) (bool, error) {
	ab, err := os.ReadFile(a)
	if err != nil {
		return false, err
	}
	bb, err := os.ReadFile(b)
	if err != nil {
		return false, err
	}
	return bytes.Equal(ab, bb), nil
}

// Detach starts command (a program and its arguments) on its own, so it outlives this process: as a
// transient systemd user unit when it can, else in a session of its own.
func Detach(command []string) error {
	if p, err := exec.LookPath("systemd-run"); err == nil {
		if _, err := os.Stat(filepath.Join(os.Getenv("XDG_RUNTIME_DIR"), "systemd", "private")); err == nil {
			return exec.Command(p, append([]string{"--user", "--collect", "--quiet", "--"}, command...)...).Run()
		}
	}
	cmd := exec.Command(command[0], command[1:]...)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := cmd.Start(); err != nil {
		return err
	}
	return cmd.Process.Release()
}
