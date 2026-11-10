package github

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/tyler-builds/uas/internal/catalog"
	"github.com/tyler-builds/uas/internal/client/appimage/appimagetest"
)

// TestStrays lists the AppImages uas doesn't manage: the ones other tools' launchers run and the ones in
// folders, each once, and only real AppImages.
func TestStrays(t *testing.T) {
	b := newBackend(t, files{}, catalog.Listing{})
	home := t.TempDir()
	folder, downloads := filepath.Join(home, "AppImages"), filepath.Join(home, "Downloads")
	for _, d := range []string{folder, downloads, filepath.Join(downloads, "deeper"), b.Dirs.Applications, b.Dirs.AppImages} {
		os.MkdirAll(d, 0o755)
	}
	img := appimagetest.Mark(appimagetest.Build(t, "App", "1.0", ""))
	write := func(p string, body []byte) string {
		t.Helper()
		if err := os.WriteFile(p, body, 0o755); err != nil {
			t.Fatal(err)
		}
		return p
	}
	launcher := func(name, target, extra string) string {
		return write(filepath.Join(b.Dirs.Applications, name), []byte("[Desktop Entry]\nType=Application\nName=From "+name+
			"\nTryExec="+target+"\nExec=env DESKTOPINTEGRATION=1 "+target+" %U\n"+extra))
	}
	// One Gear Lever set up, with two launchers; one AppImageLauncher set up, outside the folders.
	helium := write(filepath.Join(folder, "helium.appimage"), img)
	l1 := launcher("helium.desktop", helium, "")
	l2 := launcher("helium-again.desktop", helium, "")
	elsewhere := write(filepath.Join(home, "elsewhere.AppImage"), img)
	kit := launcher("appimagekit_0123abcd-Elsewhere.desktop", elsewhere, "X-AppImage-Identifier=0123abcd\n")
	// Files in a folder: a real one, one that is an AppImage only by name, one too deep to be looked at.
	loose := write(filepath.Join(downloads, "Loose-1.0.AppImage"), img)
	write(filepath.Join(downloads, "fake.AppImage"), []byte("not an AppImage"))
	write(filepath.Join(downloads, "deeper", "Deep.AppImage"), img)
	// What uas manages already, and its own launcher: neither is a stray.
	owned := write(filepath.Join(b.Dirs.AppImages, "file__owned.AppImage"), img)
	launcher("uas-file__owned.desktop", owned, "X-UAS-Source=file:owned\n")
	// A launcher whose AppImage is gone.
	launcher("gone.desktop", filepath.Join(folder, "gone.appimage"), "")

	got := b.Strays([]string{folder, downloads, filepath.Join(home, "missing"), b.Dirs.AppImages})
	want := []Stray{
		{Path: helium, Launchers: []string{l2, l1}, LauncherName: "From helium-again.desktop"},
		{Path: loose},
		{Path: elsewhere, Launchers: []string{kit}, LauncherName: "From appimagekit_0123abcd-Elsewhere.desktop",
			AppImageLauncher: true},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("Strays =\n%+v\nwant\n%+v", got, want)
	}
}
