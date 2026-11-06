package gui

import (
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
	light, dark := Themes()
	fp := &fakeBackend{src: catalog.SourceFlathub, installed: []backend.InstalledApp{
		{Source: catalog.SourceFlathub, Ref: "app/org.mozilla.firefox/x86_64/stable", AppID: "org.mozilla.firefox", Name: "Firefox", Version: "159.0.1", Scope: "system"},
		{Source: catalog.SourceFlathub, Ref: "app/org.gimp.GIMP/x86_64/stable", AppID: "org.gimp.GIMP", Name: "GIMP", Version: "3.2.8", Scope: "user"},
	}, available: []backend.AvailableUpdate{{Ref: "app/org.gimp.GIMP/x86_64/stable", Scope: "user", Version: "3.2.9"}}}
	hb := &fakeBackend{src: catalog.SourceHomebrew, installed: []backend.InstalledApp{
		{Source: catalog.SourceHomebrew, Ref: "htop", AppID: "htop", Name: "htop", Version: "3.5.3", Scope: "user"}}}
	gh := &fakeBackend{src: catalog.SourceGitHub}
	shots := []struct {
		name     string
		setup    func(a *App)
		h        float32
		selected string
		w        float32 // 0: 1440
	}{
		{"home", func(a *App) {}, 1110, "", 0},
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
		{"restore", func(a *App) { a.goTo(view{page: pageInstalled}); a.restore.Set(shotRestorePlan()) }, 700, "", 0},
		{"updates", func(a *App) { a.goTo(view{page: pageUpdates}) }, 700, "", 0},
		{"settings", func(a *App) { a.goTo(view{page: pageSettings}) }, 1100, "", 0},
		// The system's own ujust commands, when this machine has them.
		{"tools", func(a *App) { a.goTo(view{page: pageTools}) }, 1100, "", 0},
	}
	for _, theme := range []struct {
		name string
		t    *ui.Theme
	}{{"dark", dark}, {"light", light}} {
		for _, sh := range shots {
			s := &session.Session{Settings: settings.Default(), Telemetry: telemetry.Nop{}, CatalogPath: path,
				Platform: platform.Info{PrettyName: "Bazzite", Arch: "x86_64"}, Tools: ujust.Detect(),
				Backends: map[catalog.SourceID]backend.Backend{catalog.SourceFlathub: fp,
					catalog.SourceHomebrew: hb, catalog.SourceGitHub: gh}}
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
			u := uitest.New(t, a.Root, uitest.Size(w, sh.h), uitest.Theme(theme.t))
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
		}}
}
