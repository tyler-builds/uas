package gui

import (
	"context"
	"path/filepath"
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

// TestPresets saves the current look, applies it after the desktop changed, undoes that, meets a name
// clash, and deletes the preset, against a fake GNOME.
func TestPresets(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "catalog.db")
	if err := catalog.Write(context.Background(), path, nil, catalog.BuildInfo{BuiltAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	gs := newFakeGNOME()
	in := &appearance.Installer{Data: filepath.Join(dir, "data"), Config: filepath.Join(dir, "config"), Cache: filepath.Join(dir, "cache"),
		State:  state.Store{Path: filepath.Join(dir, "data", "uas", "state.json")},
		Runner: gs, Desktop: &appearance.GNOME{Runner: gs}, Limits: archives.DefaultLimits}
	store := appearance.NewPresets(filepath.Join(dir, "config"), filepath.Join(dir, "data"))
	s := &session.Session{Settings: settings.Default(), Telemetry: telemetry.Nop{}, CatalogPath: path, Looks: in,
		Presets: store, Backends: map[catalog.SourceID]backend.Backend{}}
	a := New(s)
	a.icons.dir = filepath.Join(dir, "images")
	a.startup.Do(func() {})
	u := uitest.New(t, a.Root, uitest.Size(1280, 860))
	defer u.Close()

	// With no presets, the page opens on Fonts; Presets says how to start.
	a.goTo(view{page: pageAppearance})
	u.WaitFor("the Appearance page", func() bool { return u.HasName("Presets") })
	u.WaitFor("the Fonts tab", func() bool { return u.HasName("Fonts") && u.ByName("Fonts").Selected() })
	u.ByName("Presets").Click()
	u.WaitFor("the empty Presets tab", func() bool { return u.HasName("No presets yet") })

	save := func(name, accent string) {
		a.openSave()
		u.WaitFor("the Save dialog", func() bool { return u.HasRole(access.RoleDialog, "Save current look") })
		u.WaitFor("the captured look", func() bool {
			return u.ByRole(access.RoleDialog, "Save current look").HasRole(access.RoleCheckBox, "Accent color: "+accent)
		})
		u.Type(name)
		u.Settle()
	}
	save("Mine", "Blue")
	u.ByRole(access.RoleDialog, "Save current look").ByRole(access.RoleButton, "Save").Click()
	u.WaitFor("the saved preset", func() bool { return u.Has("preset-card:Mine") })
	p, err := store.Get("Mine")
	if err != nil {
		t.Fatal(err)
	}
	if p.Parts[appearance.PartAccent].Value != "blue" || p.Parts[appearance.PartMonospace].Value != "Adwaita Mono 11" ||
		p.Parts[appearance.PartGTK].Value != "Adwaita" || len(p.Parts) != 8 {
		t.Errorf("saved %+v", p.Parts)
	}
	waitText(t, u, "preset-card:Mine", "In use")

	// The desktop changes; Apply brings the preset's look back, and Undo goes back again.
	gs.mu.Lock()
	gs.values[appearance.KeyAccentColor] = "teal"
	gs.values[appearance.KeyColorScheme] = appearance.SchemeDark
	gs.mu.Unlock()
	card := u.Get("preset-card:Mine")
	if card.HasRole(access.RoleButton, "Apply again") {
		card.ByRole(access.RoleButton, "Apply again").Click()
	} else {
		card.ByRole(access.RoleButton, "Apply").Click()
	}
	u.WaitFor("the apply", func() bool { return gs.get(appearance.KeyAccentColor) == "blue" })
	if gs.get(appearance.KeyColorScheme) != appearance.SchemeDefault {
		t.Errorf("style = %q", gs.get(appearance.KeyColorScheme))
	}
	clickButton(u, "Undo")
	u.WaitFor("the undo", func() bool { return gs.get(appearance.KeyAccentColor) == "teal" })
	if gs.get(appearance.KeyColorScheme) != appearance.SchemeDark {
		t.Errorf("after undo, style = %q", gs.get(appearance.KeyColorScheme))
	}

	// A name of another preset, whatever its case, asks to replace it.
	save("MINE", "Teal")
	u.ByRole(access.RoleDialog, "Save current look").ByRole(access.RoleButton, "Save").Click()
	u.WaitFor("the clash", func() bool {
		return u.ByRole(access.RoleDialog, "Save current look").HasRole(access.RoleButton, "Replace")
	})
	u.ByRole(access.RoleDialog, "Save current look").ByRole(access.RoleButton, "Replace").Click()
	u.WaitFor("the replaced preset", func() bool { return u.Has("preset-card:MINE") })
	if list, _ := store.List(); len(list) != 1 || list[0].Parts[appearance.PartAccent].Value != "teal" {
		t.Errorf("after replace: %+v", list)
	}

	// The page of the preset changes a part.
	a.openPreset("MINE")
	u.WaitFor("the preset's page", func() bool { return u.Has("preset-page:MINE") })
	u.Get("preset-page:MINE").ByRole(access.RoleButton, "Pink").Click()
	u.WaitFor("the change", func() bool {
		p, _ := store.Get("mine")
		return p.Parts[appearance.PartAccent].Value == "pink"
	})

	// Delete asks first, and goes back from the preset's page.
	p, _ = store.Get("mine")
	a.presetDeleting.Set(&p)
	u.WaitFor("the delete dialog", func() bool { return u.HasRole(access.RoleAlertDialog, "Delete MINE?") })
	u.ByRole(access.RoleAlertDialog, "Delete MINE?").ByRole(access.RoleButton, "Delete").Click()
	u.WaitFor("the deletion", func() bool {
		list, _ := store.List()
		return len(list) == 0 && a.current().page == pageAppearance
	})
}
