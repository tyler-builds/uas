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
)

// TestShots renders full pages to PNGs for design review. It needs UAS_SHOTS (an output directory) and a real
// catalog in UAS_CATALOG, and loads icons from the network.
func TestShots(t *testing.T) {
	out, path := os.Getenv("UAS_SHOTS"), os.Getenv("UAS_CATALOG")
	if out == "" || path == "" {
		t.Skip("set UAS_SHOTS and UAS_CATALOG")
	}
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
	}{
		{"discover", func(a *App) {}, 1500, ""},
		{"detail-github", func(a *App) {}, 1500, "github:adileo/squirreldisk"},
		{"detail-darktable", func(a *App) {}, 1700, "org.darktable.Darktable"},
		{"installed", func(a *App) { a.page.Set(pageInstalled) }, 820, ""},
		{"settings", func(a *App) { a.page.Set(pageSettings) }, 1100, ""},
	}
	for _, theme := range []struct {
		name string
		t    *ui.Theme
	}{{"dark", dark}, {"light", light}} {
		for _, sh := range shots {
			s := &session.Session{Settings: settings.Default(), Telemetry: telemetry.Nop{}, CatalogPath: path,
				Platform: platform.Info{PrettyName: "Bazzite", Arch: "x86_64"},
				Backends: map[catalog.SourceID]backend.Backend{catalog.SourceFlathub: fp,
					catalog.SourceHomebrew: hb, catalog.SourceGitHub: gh}}
			a := New(s)
			a.icons.dir = filepath.Join(os.TempDir(), "uas-shot-images")
			a.startup.Do(func() {})
			sh.setup(a)
			if sh.selected != "" {
				a.selected.Set(sh.selected)
			}
			u := uitest.New(t, a.Root, uitest.Size(1180, sh.h), uitest.Theme(theme.t))
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
