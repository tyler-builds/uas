package gui

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/tyler-builds/jamb/access"
	"github.com/tyler-builds/jamb/ui/uitest"

	archives "github.com/tyler-builds/uas/internal/appearance"
	"github.com/tyler-builds/uas/internal/catalog"
	"github.com/tyler-builds/uas/internal/client/appearance"
	"github.com/tyler-builds/uas/internal/client/backend"
	"github.com/tyler-builds/uas/internal/client/session"
	"github.com/tyler-builds/uas/internal/client/settings"
	"github.com/tyler-builds/uas/internal/client/state"
	"github.com/tyler-builds/uas/internal/client/telemetry"
)

// presetTestApp is the app with a font and an icon theme in the catalog (neither installed), two icon themes
// on the system, a fake GNOME, and two presets.
func presetTestApp(t *testing.T) (*App, *fakeGNOME, *appearance.Presets) {
	dir := t.TempDir()
	font := catalog.Listing{Source: catalog.SourceGoogleFonts, Ref: "inter", Kind: catalog.KindFont, Name: "Inter",
		Summary: "A typeface for screens", Rank: 3}
	icons := catalog.Listing{Source: catalog.SourcePling, Ref: "1166289", Kind: catalog.KindIconTheme, Name: "Papirus",
		Summary: "Icons", Installs: 100, Assets: []catalog.Asset{{Kind: catalog.AssetTheme, Name: "papirus.tar.gz",
			SHA256: "00", Themes: []string{"Papirus", "Papirus-Dark"}}}}
	var apps []catalog.App
	for _, l := range []catalog.Listing{font, icons} {
		apps = append(apps, catalog.App{ID: catalog.AppearanceAppID(l.Source, l.Ref), Kind: l.Kind, Name: l.Name,
			Summary: l.Summary, Listings: []catalog.Listing{l}})
	}
	path := filepath.Join(dir, "catalog.db")
	if err := catalog.Write(context.Background(), path, apps, catalog.BuildInfo{BuiltAt: time.Now(),
		Appearance: []catalog.SourceStatus{{Source: catalog.SourcePling, Listings: 1},
			{Source: catalog.SourceGoogleFonts, Listings: 1}}}); err != nil {
		t.Fatal(err)
	}
	gs := newFakeGNOME()
	sys := filepath.Join(dir, "usr")
	for _, name := range []string{"Adwaita", "Breeze"} {
		os.MkdirAll(filepath.Join(sys, "icons", name, "48"), 0o755)
		os.WriteFile(filepath.Join(sys, "icons", name, "index.theme"),
			[]byte("[Icon Theme]\nName="+name+"\nDirectories=48\n"), 0o644)
	}
	in := &appearance.Installer{Data: filepath.Join(dir, "data"), Config: filepath.Join(dir, "config"),
		SystemData: []string{sys}, Cache: filepath.Join(dir, "cache"),
		State:  state.Store{Path: filepath.Join(dir, "data", "uas", "state.json")},
		Runner: gs, Desktop: &appearance.GNOME{Runner: gs, ExtensionDirs: []string{}}, Limits: archives.DefaultLimits}
	store := appearance.NewPresets(filepath.Join(dir, "config"), filepath.Join(dir, "data"))
	store.Save(appearance.Preset{Name: "Work", Parts: map[string]appearance.Part{
		appearance.PartInterface: {Value: "Cantarell 12"}, appearance.PartAccent: {Value: "teal"}}}, false)
	store.Save(appearance.Preset{Name: "Day", Parts: map[string]appearance.Part{
		appearance.PartAccent: {Value: "blue"}, appearance.PartIcons: {Value: "Adwaita"}}}, false)
	s := &session.Session{Settings: settings.Default(), Telemetry: telemetry.Nop{}, CatalogPath: path, Looks: in,
		Presets: store, Backends: map[catalog.SourceID]backend.Backend{}}
	a := New(s)
	a.icons.dir = filepath.Join(dir, "images")
	a.startup.Do(func() {})
	return a, gs, store
}

// TestAddToPreset adds a font that isn't installed to a preset from its page, and to a new preset, and an
// icon theme of the system's from its row; nothing is applied or installed.
func TestAddToPreset(t *testing.T) {
	a, gs, store := presetTestApp(t)
	u := uitest.New(t, a.Root, uitest.Size(1280, 900))
	defer u.Close()
	id := catalog.AppearanceAppID(catalog.SourceGoogleFonts, "inter")

	a.openLook(id)
	u.WaitFor("the font's page", func() bool { return u.Has("look:"+id) && u.HasRole(access.RoleButton, "Add to preset") })
	clickButton(u, "Add to preset")
	u.WaitFor("the menu", func() bool {
		return u.Has("jamb.menu") && strings.Contains(u.Get("jamb.menu").Text(), "As the interface text, add to") &&
			u.HasRole(access.RoleMenuItemCheck, "Work")
	})
	if u.ByRole(access.RoleMenuItemCheck, "Work").Checked() {
		t.Error("Work is marked before it has Inter")
	}
	u.ByRole(access.RoleMenuItemCheck, "Work").Click()
	u.WaitFor("the confirmation", func() bool { return u.HasName("Added to Work") })
	p, _ := store.Get("Work")
	if part := p.Parts[appearance.PartInterface]; part.Value != "Inter 12" || part.From == nil || part.From.ID != id {
		t.Errorf("Work's interface = %+v", part)
	}
	if p.Parts[appearance.PartAccent].Value != "teal" || gs.get(appearance.KeyFont) != "Adwaita Sans 11" {
		t.Error("something else changed")
	}
	if recs, _ := a.s.Looks.Records(); len(recs) != 0 {
		t.Error("the font was installed")
	}
	// The menu marks the preset that has it now; Open opens the preset.
	clickButton(u, "Add to preset")
	u.WaitFor("the menu", func() bool { return u.HasRole(access.RoleMenuItemCheck, "Work") })
	if !u.ByRole(access.RoleMenuItemCheck, "Work").Checked() || u.ByRole(access.RoleMenuItemCheck, "Day").Checked() {
		t.Error("the marks are wrong")
	}
	// New preset… makes a preset with it.
	u.ByRole(access.RoleMenuItem, "New preset…").Click()
	u.WaitFor("the New preset dialog", func() bool { return u.HasRole(access.RoleDialog, "New preset") })
	u.Type("Fresh")
	u.Settle()
	u.ByRole(access.RoleDialog, "New preset").ByRole(access.RoleButton, "Create").Click()
	u.WaitFor("the confirmation", func() bool { return u.HasName("Added to Fresh") })
	if p, err := store.Get("Fresh"); err != nil || p.Parts[appearance.PartInterface].Value != "Inter" ||
		len(p.Parts) != 1 {
		t.Errorf("Fresh = %+v %v", p.Parts, err)
	}
	clickButton(u, "Open")
	u.WaitFor("Fresh's page", func() bool { return a.current().page == pageLookPreset && a.current().arg == "Fresh" })

	// A theme of the system's, from its row on the Installed tab.
	a.goTo(view{page: pageAppearance})
	a.lookTab.Set(lookInstalled)
	u.WaitFor("the row's menu", func() bool { return u.HasRole(access.RoleButton, "Add Breeze to a preset") })
	u.ByRole(access.RoleButton, "Add Breeze to a preset").Click()
	u.WaitFor("the menu", func() bool {
		return u.Has("jamb.menu") && strings.Contains(u.Get("jamb.menu").Text(), "As the icons, add to") &&
			u.HasRole(access.RoleMenuItemCheck, "Day")
	})
	u.ByRole(access.RoleMenuItemCheck, "Day").Click()
	u.WaitFor("the confirmation", func() bool { return u.HasName("Added to Day") })
	if p, _ := store.Get("Day"); p.Parts[appearance.PartIcons].Value != "Breeze" || p.Parts[appearance.PartIcons].From != nil {
		t.Errorf("Day's icons = %+v", p.Parts[appearance.PartIcons])
	}
	if gs.get(appearance.KeyIconTheme) != "Adwaita" {
		t.Error("the icons changed")
	}
}

// TestPresetChanged shows Changed on the preset last applied once the look changes, offers Update after Use,
// and updates it only when asked.
func TestPresetChanged(t *testing.T) {
	a, gs, store := presetTestApp(t)
	u := uitest.New(t, a.Root, uitest.Size(1280, 900))
	defer u.Close()
	day, _ := store.Get("Day")
	plan, err := a.s.PlanPreset(context.Background(), day)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := a.s.ApplyPreset(context.Background(), day, plan, false, false, nil); err != nil {
		t.Fatal(err)
	}
	a.goTo(view{page: pageAppearance})
	a.lookTab.Set(lookPresets)
	waitText(t, u, "preset-card:Day", "In use")

	// Use on a theme's row: the confirmation offers to update Day, which stays as it was.
	use := func(theme string) {
		a.lookTab.Set(lookInstalled)
		key := "theme-row:" + string(catalog.KindIconTheme) + ":" + theme
		u.WaitFor(theme+"'s row", func() bool { return u.Has(key) && u.Get(key).HasRole(access.RoleButton, "Use") })
		u.Get(key).ByRole(access.RoleButton, "Use").Click()
		u.WaitFor("the change", func() bool { return gs.get(appearance.KeyIconTheme) == theme })
	}
	use("Breeze")
	u.WaitFor("the offer", func() bool { return u.HasRole(access.RoleButton, "Update Day") })
	if p, _ := store.Get("Day"); p.Parts[appearance.PartIcons].Value != "Adwaita" {
		t.Fatal("Day was updated without asking")
	}
	// The card says Changed, with Update and Apply again.
	a.lookTab.Set(lookPresets)
	waitText(t, u, "preset-card:Day", "Changed")
	if !u.Get("preset-card:Day").HasRole(access.RoleButton, "Apply again") {
		t.Error("no way back")
	}
	u.Get("preset-card:Day").ByRole(access.RoleButton, "Update").Click()
	u.WaitFor("the update", func() bool {
		p, _ := store.Get("Day")
		return p.Parts[appearance.PartIcons].Value == "Breeze"
	})
	waitText(t, u, "preset-card:Day", "In use")
	if p, _ := store.Get("Day"); p.Parts[appearance.PartAccent].Value != "blue" {
		t.Error("Update changed a part that didn't change")
	}

	// The offer's own button updates it too (once the earlier confirmations are gone).
	u.Advance(30 * time.Second)
	u.Settle()
	use("Adwaita")
	u.WaitFor("the offer", func() bool { return u.HasRole(access.RoleButton, "Update Day") })
	u.ByRole(access.RoleButton, "Update Day").Click()
	u.WaitFor("the update", func() bool {
		p, _ := store.Get("Day")
		return p.Parts[appearance.PartIcons].Value == "Adwaita"
	})
	// Work isn't the preset applied last: it is never Changed.
	a.lookTab.Set(lookPresets)
	waitText(t, u, "preset-card:Day", "In use")
	if text := u.Get("preset-card:Work").Text(); strings.Contains(text, "Changed") || strings.Contains(text, "In use") {
		t.Errorf("Work's card = %q", text)
	}
}
