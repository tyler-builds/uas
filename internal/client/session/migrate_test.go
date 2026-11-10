package session

import (
	"context"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/tyler-builds/uas/internal/catalog"
	"github.com/tyler-builds/uas/internal/client/appimage/appimagetest"
	"github.com/tyler-builds/uas/internal/client/backend"
	"github.com/tyler-builds/uas/internal/client/backend/github"
	"github.com/tyler-builds/uas/internal/client/gearlever"
	"github.com/tyler-builds/uas/internal/client/settings"
	"github.com/tyler-builds/uas/internal/client/state"
	"github.com/tyler-builds/uas/internal/client/telemetry"
)

// TestMigrateFromGearLever scans a home folder set up as Gear Lever leaves it, and hands its AppImage over to
// uas.
func TestMigrateFromGearLever(t *testing.T) {
	ctx := context.Background()
	home := t.TempDir()
	data := filepath.Join(home, ".local", "share")
	apps := filepath.Join(data, "applications")
	folder := filepath.Join(home, "AppImages")
	conf := gearlever.ConfPaths(home)[0]
	config := filepath.Join(home, ".config")
	for _, d := range []string{apps, filepath.Join(folder, ".icons"), filepath.Join(home, "Downloads"), filepath.Dir(conf),
		filepath.Join(config, "autostart")} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	write := func(p string, b []byte) {
		t.Helper()
		if err := os.WriteFile(p, b, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	// Helium, as Gear Lever integrates an AppImage: the file in its folder, an icon, a launcher, a record.
	helium := filepath.Join(folder, "helium.appimage")
	write(helium, appimagetest.Mark(appimagetest.Build(t, "Helium", "1.0", "")))
	write(filepath.Join(folder, ".icons", "helium"), []byte("\x89PNG\r\n\x1a\n"))
	write(filepath.Join(folder, ".icons", "ghostty"), []byte("\x89PNG\r\n\x1a\n"))
	launcher := filepath.Join(apps, "helium.desktop")
	write(launcher, []byte("[Desktop Entry]\nType=Application\nName=Helium\nIcon="+filepath.Join(folder, ".icons", "helium")+
		"\nTryExec="+helium+"\nExec=env DESKTOPINTEGRATION=1 "+helium+" %U\nX-AppImage-Version=1.0\n"))
	// What the user set up around it: it opens web links, starts at login, and keeps its data beside its file.
	mimeapps := filepath.Join(config, "mimeapps.list")
	write(mimeapps, []byte("[Default Applications]\nx-scheme-handler/https=helium.desktop\n"+
		"text/html=helium.desktop;other.desktop;\nx-scheme-handler/mailto=mail.desktop\n\n[Added Associations]\n"+
		"image/png=viewer.desktop;helium.desktop;\n"))
	autostart := filepath.Join(config, "autostart", "helium.desktop")
	write(autostart, []byte("[Desktop Entry]\nType=Application\nName=Helium\nExec="+helium+" --hidden\n"))
	if err := os.MkdirAll(helium+".home", 0o755); err != nil {
		t.Fatal(err)
	}
	write(filepath.Join(helium+".home", "profile"), []byte("the user's data"))
	// Ghostty is on record too, but its file is gone: a leftover.
	write(conf, []byte("[DEFAULT]\nfetch-updates-in-background = no\n\n[app.aaa111]\nname = Helium\nfile_path = "+helium+
		"\n\n[app.aaa111.update_manager]\n\n[app.bbb222]\nname = Ghostty\nfile_path = "+filepath.Join(folder, "ghostty.appimage")+"\n"))
	// A download that nothing set up, a file that only looks like an AppImage by name, and one that isn't marked.
	other := filepath.Join(home, "Downloads", "Other-2.0-x86_64.AppImage")
	write(other, appimagetest.Mark(appimagetest.Build(t, "Other", "2.0", "gh-releases-zsync|example|other|latest|Other-*.AppImage.zsync")))
	write(filepath.Join(home, "Downloads", "notes.AppImage"), []byte("just text"))
	write(filepath.Join(home, "Downloads", "unmarked.AppImage"), appimagetest.Build(t, "Unmarked", "1.0", ""))

	catalogPath := filepath.Join(home, "catalog.db")
	if err := catalog.Write(ctx, catalogPath, nil, catalog.BuildInfo{BuiltAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	gh := &github.Backend{State: state.Store{Path: filepath.Join(data, "uas", "state.json")},
		Dirs: github.Dirs{AppImages: filepath.Join(data, "uas", "appimages"), Icons: filepath.Join(data, "uas", "icons"),
			Applications: apps, Config: config},
		Client: http.DefaultClient, Forge: catalog.SourceGitHub}
	s := &Session{Settings: settings.Default(), Telemetry: telemetry.Nop{}, CatalogPath: catalogPath, Home: home,
		Backends: map[catalog.SourceID]backend.Backend{catalog.SourceGitHub: gh, catalog.SourceAppImage: &github.Local{GH: gh}}}

	scan, err := s.ScanMigrations(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(scan.Apps) != 2 || len(scan.Leftovers) != 1 || scan.Leftovers[0].Name != "Ghostty" {
		t.Fatalf("scan = %+v", scan)
	}
	o, h := scan.Apps[0], scan.Apps[1] // by path: ~/AppImages sorts after ~/Downloads? No: sorted by path
	if h.Name != "Helium" {
		o, h = h, o
	}
	if h.Path != helium || h.Version != "1.0" || h.From != "Gear Lever" || h.Folder != "~/AppImages" || !h.Suggested ||
		h.Blocked != "" || h.Updates != "" || len(h.Launchers) != 1 || h.Launchers[0] != launcher {
		t.Errorf("helium = %+v", h)
	}
	// A download is listed but not suggested; it says where it would update from.
	if o.Name != "Other" || o.From != "" || o.Folder != "~/Downloads" || o.Suggested || o.Blocked != "" ||
		o.Updates != "github.com/example/other" || o.Catalog {
		t.Errorf("other = %+v", o)
	}

	var msgs []string
	app, err := s.Migrate(ctx, h, func(p backend.Progress) { msgs = append(msgs, p.Message) })
	if err != nil {
		t.Fatalf("Migrate: %v (%q)", err, msgs)
	}
	if app.Source != catalog.SourceAppImage || app.Name != "Helium" {
		t.Errorf("migrated app = %+v", app)
	}
	// The file left Gear Lever's folder for uas's, with uas's launcher in place of Gear Lever's.
	for path, there := range map[string]bool{
		helium:   false,
		launcher: false,
		filepath.Join(folder, ".icons", "helium"):  false,
		filepath.Join(folder, ".icons", "ghostty"): true, // the leftover's, until leftovers are cleared
		other: true,
	} {
		if _, err := os.Stat(path); (err == nil) != there {
			t.Errorf("%s: there = %v, want %v", path, err == nil, there)
		}
	}
	installed, err := s.Installed(ctx)
	if err != nil || len(installed) != 1 || installed[0].Name != "Helium" || installed[0].Version != "1.0" {
		t.Fatalf("installed = %+v, %v", installed, err)
	}
	if _, err := os.Stat(installed[0].Desktop); err != nil || !strings.HasPrefix(filepath.Base(installed[0].Desktop), "uas-") {
		t.Errorf("uas's launcher: %q, %v", installed[0].Desktop, err)
	}
	// What the user set up followed it: still the default for web links, still starting at login, and its
	// data beside the file in its new place.
	id := filepath.Base(installed[0].Desktop)
	got, _ := os.ReadFile(mimeapps)
	if want := "[Default Applications]\nx-scheme-handler/https=" + id + "\ntext/html=" + id + ";other.desktop;\n" +
		"x-scheme-handler/mailto=mail.desktop\n\n[Added Associations]\nimage/png=viewer.desktop;" + id + ";\n"; string(got) != want {
		t.Errorf("default apps after migrating:\n%s\nwant:\n%s", got, want)
	}
	moved := filepath.Join(gh.Dirs.AppImages, "file__org.example.App.AppImage")
	if got, _ := os.ReadFile(autostart); !strings.Contains(string(got), "Exec="+moved+" --hidden\n") {
		t.Errorf("autostart entry after migrating:\n%s", got)
	}
	if got, err := os.ReadFile(filepath.Join(moved+".home", "profile")); err != nil || string(got) != "the user's data" {
		t.Errorf("portable data after migrating: %q, %v", got, err)
	}
	if _, err := os.Stat(helium + ".home"); err == nil {
		t.Error("the portable folder was left behind")
	}
	// Gear Lever's record of Helium is gone; the leftover's stays until cleared.
	left := gearlever.Entries(home)
	if len(left) != 1 || left[0].Name != "Ghostty" {
		t.Errorf("Gear Lever's records after migrating = %+v", left)
	}

	// A second scan no longer lists Helium. Another copy of it would replace the one uas has, so it is blocked.
	write(filepath.Join(home, "Downloads", "Helium-0.9.AppImage"), appimagetest.Mark(appimagetest.Build(t, "Helium", "0.9", "")))
	scan, err = s.ScanMigrations(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(scan.Apps) != 2 {
		t.Fatalf("second scan = %+v", scan.Apps)
	}
	for _, m := range scan.Apps {
		switch m.Name {
		case "Helium":
			if m.Blocked != "uas already manages Helium 1.0" || m.Suggested {
				t.Errorf("the second Helium = %+v", m)
			}
			if _, err := s.Migrate(ctx, m, nil); err == nil {
				t.Error("migrated a copy of an app uas already manages")
			}
		case "Other":
		default:
			t.Errorf("unexpected %+v", m)
		}
	}

	// A leftover of an app uas manages now, as after a move that didn't carry default-app settings over: its
	// record names an AppImage that is gone, and a setting still names Gear Lever's launcher for it.
	raw, _ := os.ReadFile(conf)
	write(conf, append(raw, []byte("\n[app.ccc333]\nname = Helium\nfile_path = "+filepath.Join(folder, "helium_old.appimage")+"\n")...))
	raw, _ = os.ReadFile(mimeapps)
	write(mimeapps, append(raw, []byte("x-scheme-handler/http=helium_old.desktop\nx-scheme-handler/ghostty=ghostty.desktop\n")...))
	if scan, err = s.ScanMigrations(ctx); err != nil || len(scan.Leftovers) != 2 {
		t.Fatalf("leftovers = %+v, %v", scan.Leftovers, err)
	}
	if err := s.ClearLeftovers(ctx, scan.Leftovers); err != nil {
		t.Fatal(err)
	}
	// Helium's stale setting points at uas's launcher; Ghostty's stays, since uas has no Ghostty to point it at.
	if got, _ := os.ReadFile(mimeapps); !strings.Contains(string(got), "x-scheme-handler/http="+id+"\n") ||
		!strings.Contains(string(got), "x-scheme-handler/ghostty=ghostty.desktop\n") {
		t.Errorf("default apps after clearing leftovers:\n%s", got)
	}
	if left := gearlever.Entries(home); len(left) != 0 {
		t.Errorf("records after clearing = %+v", left)
	}
	if _, err := os.Stat(filepath.Join(folder, ".icons", "ghostty")); err == nil {
		t.Error("the leftover's icon is still there")
	}
}
