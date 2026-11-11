package gui

import (
	"context"
	"image/png"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/tyler-builds/jamb/ui"
	"github.com/tyler-builds/jamb/ui/uitest"

	"github.com/tyler-builds/uas/internal/catalog"
	"github.com/tyler-builds/uas/internal/client/backend"
	"github.com/tyler-builds/uas/internal/client/platform"
	"github.com/tyler-builds/uas/internal/client/session"
	"github.com/tyler-builds/uas/internal/client/settings"
	"github.com/tyler-builds/uas/internal/client/shellext"
	"github.com/tyler-builds/uas/internal/client/sysapps"
	"github.com/tyler-builds/uas/internal/client/telemetry"
	"github.com/tyler-builds/uas/internal/client/ujust"
)

// TestShots renders full pages to PNGs for design review. It needs UAS_SHOTS (an output directory) and a real
// catalog in UAS_CATALOG, and loads icons from the network.
func TestShots(t *testing.T) {
	out, path := os.Getenv("UAS_SHOTS"), os.Getenv("UAS_CATALOG")
	if out == "" || path == "" {
		t.Skip("set UAS_SHOTS and UAS_CATALOG")
	}
	if err := os.MkdirAll(out, 0o755); err != nil {
		t.Fatal(err)
	}
	useBundledFont(t)
	spotlightDay = func() int { return 0 } // the most popular app, so the shots don't change by the day
	// The update GNOME Shell has downloaded for Dash to Dock.
	updatesDir := t.TempDir()
	writeUpdate(t, updatesDir, "dash-to-dock@micxgx.gmail.com", "111")
	light, dark := Themes()
	fp := &fakeBackend{src: catalog.SourceFlathub, installed: []backend.InstalledApp{
		{Source: catalog.SourceFlathub, Ref: "app/org.mozilla.firefox/x86_64/stable", AppID: "org.mozilla.firefox", Name: "Firefox", Version: "159.0.1", Scope: "system"},
		{Source: catalog.SourceFlathub, Ref: "app/org.gimp.GIMP/x86_64/stable", AppID: "org.gimp.GIMP", Name: "GIMP", Version: "3.2.4", Scope: "user"},
	}, available: []backend.AvailableUpdate{{Ref: "app/org.gimp.GIMP/x86_64/stable", Scope: "user", Version: "3.2.8"},
		// A rebuild of the installed version.
		{Ref: "app/org.mozilla.firefox/x86_64/stable", Scope: "system", Version: "159.0.1"}}}
	hb := &fakeBackend{src: catalog.SourceHomebrew, installed: []backend.InstalledApp{
		{Source: catalog.SourceHomebrew, Ref: "htop", AppID: "htop", Name: "htop", Version: "3.5.3", Scope: "user"}}}
	gh := &fakeBackend{src: catalog.SourceGitHub}
	// An AppImage added from a file, which the catalog has from Flathub.
	files := &fakeBackend{src: catalog.SourceAppImage, installed: []backend.InstalledApp{
		{Source: catalog.SourceAppImage, Ref: "curseforge", AppID: "curseforge", Name: "CurseForge",
			Version: "1.325.0", Scope: "user", Origin: "curseforge.overwolf.com"}}}
	shots := []struct {
		name     string
		setup    func(a *App)
		h        float32
		selected string
		w        float32 // 0: 1440
	}{
		{"home", func(a *App) {}, 1038, "", 0},
		{"detail-github", func(a *App) {}, 1400, "github:adileo/squirreldisk", 0},
		{"detail-darktable", func(a *App) {}, 940, "org.darktable.Darktable", 0},
		{"detail-vscode", func(a *App) {}, 1100, "com.visualstudio.code", 0},
		// An installed Flatpak app, with its permissions.
		{"detail-installed", func(a *App) {}, 1600, "org.gimp.GIMP", 0},
		// Narrow enough that the side panel goes under the main column.
		{"detail-narrow", func(a *App) {}, 1900, "org.darktable.Darktable", 1100},
		{"search", func(a *App) { a.query.Set("editor"); a.search.Set("editor"); a.goTo(view{page: pageSearch}) }, 1100, "", 0},
		{"sources", func(a *App) { a.goTo(view{page: pageSources}) }, 1300, "", 0},
		// At the default window width, a row with several sources, a verified mark and a long developer name.
		{"search-narrow", func(a *App) { a.query.Set("discord"); a.search.Set("discord"); a.goTo(view{page: pageSearch}) },
			800, "", 1180},
		{"installed", func(a *App) { a.goTo(view{page: pageInstalled}) }, 640, "", 0},
		{"restore", func(a *App) { a.goTo(view{page: pageInstalled}); a.restore.Set(shotRestorePlan()) }, 760, "", 0},
		{"updates", func(a *App) { a.goTo(view{page: pageUpdates}) }, 760, "", 0},
		{"settings", func(a *App) { a.goTo(view{page: pageSettings}) }, 1100, "", 0},
		// Migrate, after its scan.
		{"migrate", func(a *App) { a.goTo(view{page: pageSettings}); a.migrate.Set(newMigratePlan(shotMigrations(), nil)) },
			900, "", 0},
		// The system's own ujust commands, when this machine has them.
		{"tools", func(a *App) { a.goTo(view{page: pageTools}) }, 1100, "", 0},
		// An app that came with the system image.
		{"detail-system", func(a *App) {}, 900, "com.valvesoftware.Steam", 0},
		// An app installed as an AppImage the user added, which none of its sources describes.
		{"detail-added", func(a *App) {}, 900, "com.overwolf.curseforge", 0},
		// The notes of an update, opened from the app page.
		{"whats-new", func(a *App) { a.whatsNew.Set(shotWhatsNew(a, "org.gimp.GIMP")) }, 900, "org.gimp.GIMP", 0},
		// GNOME Shell extensions: the ones on the system, the catalog's, a search, and one's page.
		{"extensions", func(a *App) { a.goTo(view{page: pageExtensions}) }, 700, "", 0},
		{"extensions-browse", func(a *App) { a.goTo(view{page: pageExtensions}); a.extTab.Set(extBrowse) }, 900, "", 0},
		{"extensions-search", func(a *App) {
			a.goTo(view{page: pageExtensions})
			a.extTab.Set(extBrowse)
			a.listFilter.Set("clipboard")
		}, 900, "", 0},
		{"detail-extension", func(a *App) {
			a.goTo(view{page: pageExtensions})
			a.openExtension(catalog.ExtensionAppID("blur-my-shell@aunetx"))
		}, 1000, "", 0},
		// An app search that extensions match too.
		{"search-extensions", func(a *App) { a.query.Set("blur"); a.search.Set("blur"); a.goTo(view{page: pageSearch}) },
			700, "", 0},
		{"detail-extension-new", func(a *App) {
			a.goTo(view{page: pageExtensions})
			a.openExtension(catalog.ExtensionAppID("Vitals@CoreCoding.com"))
		}, 1000, "", 0},
	}
	for _, theme := range []struct {
		name string
		t    *ui.Theme
	}{{"dark", dark}, {"light", light}} {
		for _, sh := range shots {
			s := &session.Session{Settings: settings.Default(), Telemetry: telemetry.Nop{}, CatalogPath: path,
				Platform: platform.Info{PrettyName: "Bazzite", Arch: "x86_64", Atomic: true}, Tools: ujust.Detect(),
				SysApps: map[string]sysapps.App{"com.valvesoftware.Steam": {ID: "com.valvesoftware.Steam", Name: "Steam",
					Version: "1.0.0.87", Desktop: "/usr/share/applications/steam.desktop"}},
				Extensions: &shellext.Manager{Bus: shotShell(), UpdatesDir: updatesDir},
				Backends: map[catalog.SourceID]backend.Backend{catalog.SourceFlathub: fp,
					catalog.SourceHomebrew: hb, catalog.SourceGitHub: gh, catalog.SourceAppImage: files}}
			a := New(s)
			a.icons.dir = filepath.Join(os.TempDir(), "uas-shot-images")
			a.startup.Do(func() {})
			sh.setup(a)
			if sh.selected != "" {
				a.openApp(sh.selected)
			}
			w := sh.w
			if w == 0 {
				w = 1440
			}
			// The window's buttons as GNOME draws them, which is where uas runs.
			u := uitest.New(t, a.Root, uitest.Size(w, sh.h), uitest.Theme(theme.t),
				uitest.Settings(ui.SystemSettings{RoundCaptionButtons: true}))
			for i := 0; i < 40; i++ {
				u.Advance(100 * time.Millisecond)
				time.Sleep(60 * time.Millisecond)
			}
			u.Settle()
			f, err := os.Create(filepath.Join(out, theme.name+"-"+sh.name+".png"))
			if err != nil {
				t.Fatal(err)
			}
			png.Encode(f, u.Screenshot())
			f.Close()
			u.Close()
		}
	}
}

// useBundledFont makes fontconfig answer "sans-serif" with the bundled Inter, so the shots show the font the
// app registers (uitest can't register fonts itself). It must run before the first text is laid out.
func useBundledFont(t *testing.T) {
	dir, err := filepath.Abs("fonts")
	if err != nil {
		t.Fatal(err)
	}
	conf := filepath.Join(t.TempDir(), "fonts.conf")
	xml := `<?xml version="1.0"?><!DOCTYPE fontconfig SYSTEM "fonts.dtd"><fontconfig>
<include ignore_missing="yes">/etc/fonts/fonts.conf</include><dir>` + dir + `</dir>
<alias binding="strong"><family>sans-serif</family><prefer><family>Inter</family></prefer></alias>
</fontconfig>`
	if err := os.WriteFile(conf, []byte(xml), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("FONTCONFIG_FILE", conf)
}

// shotShell is a GNOME 51 with a few well-known extensions: two the user added and three that came with the
// system, one of each state.
func shotShell() *fakeShell {
	ext := func(uuid, name, desc string, system, on bool, more map[string]any) map[string]any {
		kind, state := 2.0, 6.0
		if system {
			kind = 1.0
		}
		if on {
			state = 1.0
		}
		e := map[string]any{"uuid": uuid, "name": name, "description": desc, "type": kind, "state": state,
			"enabled": on, "hasPrefs": true, "canChange": true}
		for k, v := range more {
			e[k] = v
		}
		return e
	}
	return &fakeShell{version: "51.1", answer: "successful", extensions: map[string]map[string]any{
		"dash-to-dock@micxgx.gmail.com": ext("dash-to-dock@micxgx.gmail.com", "Dash to Dock",
			"A dock for the Gnome Shell.", false, true, map[string]any{"version": 110.0, "hasUpdate": true}),
		"clipboard-indicator@tudmotu.com": ext("clipboard-indicator@tudmotu.com", "Clipboard Indicator",
			"The most popular clipboard manager for GNOME.", false, true,
			map[string]any{"version": 72.0, "state": 3.0, "error": "SyntaxError: unexpected token"}),
		"blur-my-shell@aunetx": ext("blur-my-shell@aunetx", "Blur my Shell",
			"Adds a blur look to different parts of the GNOME Shell.", true, true, map[string]any{"version": 76.0}),
		"caffeine@patapon.info": ext("caffeine@patapon.info", "Caffeine",
			"Disable the screensaver and auto suspend.", true, false, map[string]any{"version": 61.0}),
		"gsconnect@andyholmes.github.io": ext("gsconnect@andyholmes.github.io", "GSConnect",
			"KDE Connect for GNOME: share files, notifications and more with your phone.", true, true,
			map[string]any{"version": 73.0}),
	}}
}

// shotRestorePlan is a restore with one app of each kind.
func shotRestorePlan() *restorePlan {
	app := func(src catalog.SourceID, name string) session.ExportedApp {
		return session.ExportedApp{Source: src, Name: name}
	}
	return &restorePlan{ex: session.Export{System: "Bazzite", FlatpakOverrides: map[string]string{"org.gimp.GIMP": ""}},
		items: []session.RestoreItem{
			{App: app(catalog.SourceFlathub, "GIMP"), Action: session.RestoreInstalled},
			{App: app(catalog.SourceFlathub, "Krita"), Action: session.RestoreInstall,
				Listing: catalog.Listing{Source: catalog.SourceFlathub}},
			{App: app(catalog.SourceHomebrew, "htop"), Action: session.RestoreInstall,
				Listing: catalog.Listing{Source: catalog.SourceHomebrew}},
			{App: app(catalog.SourceFedora, "Okular"), Action: session.RestoreInstall,
				Listing: catalog.Listing{Source: catalog.SourceFlathub}, Note: "from Flathub instead of Fedora"},
			{App: app(catalog.SourceAppImage, "CurseForge"), Action: session.RestoreManual,
				Note: "added from a file; add the AppImage again"},
		},
		exts: []session.ExtensionRestore{
			{Ext: session.ExportedExtension{Name: "Vitals", Enabled: true}, Action: session.RestoreInstall},
			{Ext: session.ExportedExtension{Name: "Caffeine", Enabled: true, System: true}, Action: session.RestoreEnable},
			{Ext: session.ExportedExtension{Name: "Dash to Dock", Enabled: true}, Action: session.RestoreInstalled},
		}}
}

// shotWhatsNew is the "What's new" dialog of the fake GIMP update (3.2.4 to 3.2.8), as on the Updates page.
func shotWhatsNew(a *App, id string) *whatsNew {
	db, err := a.catalog(context.Background())
	if err != nil {
		return nil
	}
	app, err := db.Resolve(context.Background(), id)
	if err != nil {
		return nil
	}
	w := updateNotes(app, backend.InstalledApp{Source: catalog.SourceFlathub, Ref: "app/" + id + "/x86_64/stable",
		AppID: id, Version: "3.2.4"}, "3.2.8")
	if w != nil {
		w.update = func() {}
	}
	return w
}
