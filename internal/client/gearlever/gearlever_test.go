package gearlever

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// conf is a gearlever.conf as Gear Lever 4.6 writes it, with an update_manager section whose app is gone.
const conf = `[DEFAULT]
fetch-updates-in-background = no

[app.3a90ef66b390c530c4a3238d02705e9f]
default_exec_arguments = %U
name = Helium
file_path = HOME/AppImages/helium.appimage

[app.3a90ef66b390c530c4a3238d02705e9f.update_manager]
url = https://example.org

[app.a7b6d64b0e810fce4c1c6531f1f87ddd]
default_exec_arguments = --no-sandbox %U
name = CurseForge
file_path = ~/AppImages/curseforge.appimage

[app.4a6ce0c65235dcf320e7064c3480ba47.update_manager]

[app.0129d0aeb44368d0d8bcb9ec8b665c0f]
default_exec_arguments = 
name = Bazzite Remote
file_path = HOME/AppImages/bazzite_remote.appimage
`

func TestEntriesAndForget(t *testing.T) {
	home := t.TempDir()
	path := ConfPaths(home)[0]
	folder := filepath.Join(home, "AppImages")
	for _, d := range []string{filepath.Dir(path), filepath.Join(folder, ".icons")} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	write := func(p, s string) {
		t.Helper()
		if err := os.WriteFile(p, []byte(s), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write(path, strings.ReplaceAll(conf, "HOME", home))
	// Only Bazzite Remote's AppImage is still there; every app has an icon.
	write(filepath.Join(folder, "bazzite_remote.appimage"), "x")
	for _, n := range []string{"helium", "curseforge", "bazzite_remote"} {
		write(filepath.Join(folder, ".icons", n), "png")
	}

	es := Entries(home)
	if len(es) != 3 {
		t.Fatalf("entries = %+v", es)
	}
	helium, curse, remote := es[0], es[1], es[2]
	if helium.Name != "Helium" || helium.Path != filepath.Join(folder, "helium.appimage") ||
		helium.Section != "app.3a90ef66b390c530c4a3238d02705e9f" || !helium.Gone() ||
		helium.Icon() != filepath.Join(folder, ".icons", "helium") {
		t.Errorf("helium = %+v", helium)
	}
	if curse.Path != filepath.Join(folder, "curseforge.appimage") || !curse.Gone() { // "~" is the home folder
		t.Errorf("curseforge = %+v", curse)
	}
	if remote.Name != "Bazzite Remote" || remote.Gone() {
		t.Errorf("bazzite remote = %+v", remote)
	}

	// Forgetting the two that are gone leaves the rest of the file, byte for byte.
	if err := Forget(helium, curse); err != nil {
		t.Fatal(err)
	}
	got, _ := os.ReadFile(path)
	want := strings.ReplaceAll(`[DEFAULT]
fetch-updates-in-background = no

[app.4a6ce0c65235dcf320e7064c3480ba47.update_manager]

[app.0129d0aeb44368d0d8bcb9ec8b665c0f]
default_exec_arguments = 
name = Bazzite Remote
file_path = HOME/AppImages/bazzite_remote.appimage
`, "HOME", home)
	if string(got) != want {
		t.Errorf("conf after Forget:\n%s\nwant:\n%s", got, want)
	}
	for n, there := range map[string]bool{"helium": false, "curseforge": false, "bazzite_remote": true} {
		if _, err := os.Stat(filepath.Join(folder, ".icons", n)); (err == nil) != there {
			t.Errorf("icon %s: there = %v, want %v", n, err == nil, there)
		}
	}
	if es := Entries(home); len(es) != 1 || es[0].Name != "Bazzite Remote" {
		t.Errorf("entries after Forget = %+v", es)
	}
	if _, err := os.Stat(path + ".uas-tmp"); err == nil {
		t.Error("the temporary file was left behind")
	}
	// No Gear Lever: no entries, and nothing to forget.
	if es := Entries(t.TempDir()); es != nil {
		t.Errorf("entries without Gear Lever = %+v", es)
	}
	if err := Forget(); err != nil {
		t.Error(err)
	}
}

func TestIconOf(t *testing.T) {
	for in, want := range map[string]string{
		"/home/me/AppImages/t3_code_beta.appimage": "/home/me/AppImages/.icons/t3_code_beta",
		"/home/me/AppImages/Thing.AppImage":        "/home/me/AppImages/.icons/Thing",
		"/home/me/AppImages/noext":                 "/home/me/AppImages/.icons/noext",
	} {
		if got := IconOf(in); got != want {
			t.Errorf("IconOf(%q) = %q, want %q", in, got, want)
		}
	}
}
