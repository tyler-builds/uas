package selfinstall

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	packaging "github.com/tyler-builds/uas/packaging/linux"
)

func testLayout(t *testing.T) Layout {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_DATA_HOME", "")
	l, err := Default()
	if err != nil {
		t.Fatal(err)
	}
	return l
}

func readFile(t *testing.T, p string) string {
	t.Helper()
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestInstallAndRemove(t *testing.T) {
	l := testLayout(t)
	src := filepath.Join(t.TempDir(), "uas-0.6.0-linux-x86_64")
	os.WriteFile(src, []byte("program"), 0o644) // as downloaded, before chmod
	if err := l.Install(src); err != nil {
		t.Fatal(err)
	}
	if got := readFile(t, l.ProgramPath()); got != "program" {
		t.Errorf("uas = %q", got)
	}
	if fi, _ := os.Stat(l.ProgramPath()); fi.Mode().Perm() != 0o755 {
		t.Errorf("uas mode %v", fi.Mode())
	}
	if target, err := os.Readlink(l.GUIPath()); err != nil || target != "uas" {
		t.Errorf("uas-gui -> %q, %v", target, err)
	}
	desktop := readFile(t, l.Desktop())
	if !strings.Contains(desktop, "\nExec="+l.GUIPath()+"\n") || !strings.Contains(desktop, "Icon="+AppID) {
		t.Errorf("launcher:\n%s", desktop)
	}
	if !bytes.Equal([]byte(readFile(t, l.Icon())), packaging.Icon) {
		t.Error("icon differs")
	}
	if !l.Runs(l.ProgramPath()) || !l.Runs(l.GUIPath()) || l.Runs(src) {
		t.Error("Runs is wrong")
	}
	// Installed finds the install from its launcher.
	if got, err := Installed(); err != nil || got != l {
		t.Errorf("Installed = %+v, %v; want %+v", got, err, l)
	}
	// Installing again, from the installed program, keeps it.
	if err := l.Install(l.ProgramPath()); err != nil || readFile(t, l.ProgramPath()) != "program" {
		t.Errorf("reinstall: %v", err)
	}

	if err := l.Remove(); err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{l.ProgramPath(), l.GUIPath(), l.Desktop(), l.Icon()} {
		if _, err := os.Lstat(p); err == nil {
			t.Errorf("%s is still there", p)
		}
	}
	if err := l.Remove(); err != nil {
		t.Errorf("removing again: %v", err)
	}
}

// TestRefreshOldInstall is an install by install.sh after uas 0.5.0 or older updated itself to a release that
// is one program: uas and uas-gui are two copies of it, and the launcher is the old one.
func TestRefreshOldInstall(t *testing.T) {
	l := testLayout(t)
	os.MkdirAll(l.Bin, 0o755)
	os.MkdirAll(l.Apps, 0o755)
	os.WriteFile(l.ProgramPath(), []byte("new"), 0o755)
	os.WriteFile(l.GUIPath(), []byte("new"), 0o755)
	os.WriteFile(l.Desktop(), []byte("[Desktop Entry]\nName=uas\nExec="+l.GUIPath()+"\n"), 0o644)

	// Run as uas-gui, as the old app's Restart button or the launcher does.
	if err := l.Refresh(l.GUIPath()); err != nil {
		t.Fatal(err)
	}
	if target, err := os.Readlink(l.GUIPath()); err != nil || target != "uas" {
		t.Errorf("uas-gui -> %q, %v", target, err)
	}
	if got := readFile(t, l.ProgramPath()); got != "new" {
		t.Errorf("uas = %q", got)
	}
	if got := readFile(t, l.Desktop()); got != string(Launcher(l.GUIPath())) {
		t.Errorf("launcher not refreshed:\n%s", got)
	}
	if _, err := os.Stat(l.Icon()); err != nil {
		t.Errorf("icon: %v", err)
	}
}

// TestRefreshOnlyUASGUI: an old update replaced uas-gui alone (there was no uas), so uas becomes a copy of it.
func TestRefreshOnlyUASGUI(t *testing.T) {
	l := testLayout(t)
	os.MkdirAll(l.Bin, 0o755)
	os.WriteFile(l.GUIPath(), []byte("new"), 0o755)
	if err := l.Refresh(l.GUIPath()); err != nil {
		t.Fatal(err)
	}
	if got := readFile(t, l.GUIPath()); got != "new" { // through the link
		t.Errorf("uas-gui = %q", got)
	}
	if fi, err := os.Lstat(l.ProgramPath()); err != nil || !fi.Mode().IsRegular() {
		t.Errorf("uas: %v, %v", fi, err)
	}
	// No launcher was there, so none is added.
	if _, err := os.Stat(l.Desktop()); err == nil {
		t.Error("Refresh added a launcher")
	}
}

func TestRefreshElsewhere(t *testing.T) {
	l := testLayout(t)
	other := filepath.Join(t.TempDir(), "uas")
	os.WriteFile(other, []byte("x"), 0o755)
	if err := l.Refresh(other); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(l.Bin); err == nil {
		t.Error("Refresh of a program that isn't installed changed the install")
	}
}

func TestInstalledFromPrefix(t *testing.T) {
	l := testLayout(t)
	os.MkdirAll(l.Apps, 0o755)
	os.WriteFile(l.Desktop(), Launcher("/opt/my tools/bin/uas-gui"), 0o644)
	got, err := Installed()
	if err != nil || got.Bin != "/opt/my tools/bin" {
		t.Errorf("Installed = %+v, %v", got, err)
	}
}

func TestQuoteExec(t *testing.T) {
	for _, p := range []string{"/home/a/.local/bin/uas-gui", "/home/a b/bin/uas-gui", `/home/a"b$c\d/uas-gui`} {
		if got := execPath(Launcher(p)); got != p {
			t.Errorf("%q: read back %q from %s", p, got, Launcher(p))
		}
	}
}
