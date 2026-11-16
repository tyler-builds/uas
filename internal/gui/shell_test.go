package gui

import (
	"context"
	"os"
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
	"github.com/tyler-builds/uas/internal/client/shellext"
	"github.com/tyler-builds/uas/internal/client/state"
	"github.com/tyler-builds/uas/internal/client/telemetry"
)

// shellTestApp is the app with Shell themes on a temporary system, a fake GNOME and a fake GNOME Shell whose
// User Themes came with the system and is off. Orchis is a GTK theme uas installed, with a Shell theme.
func shellTestApp(t *testing.T) (*App, *fakeGNOME, *fakeShell, string) {
	dir := t.TempDir()
	l := catalog.Listing{Source: catalog.SourcePling, Ref: "1357889", Kind: catalog.KindGTKTheme, Name: "Orchis",
		Summary: "A GTK theme", Installs: 1000, Assets: []catalog.Asset{{Kind: catalog.AssetTheme, Name: "orchis.tar.gz",
			SHA256: "00", Themes: []string{"Orchis-Dark"}}}}
	id := catalog.AppearanceAppID(l.Source, l.Ref)
	path := filepath.Join(dir, "catalog.db")
	if err := catalog.Write(context.Background(), path, []catalog.App{{ID: id, Kind: l.Kind, Name: l.Name,
		Summary: l.Summary, Listings: []catalog.Listing{l}}}, catalog.BuildInfo{BuiltAt: time.Now(),
		Appearance: []catalog.SourceStatus{{Source: catalog.SourcePling, Listings: 1}}}); err != nil {
		t.Fatal(err)
	}
	gs := newFakeGNOME()
	shell := &fakeShell{version: "51.1", answer: "successful", extensions: map[string]map[string]any{
		appearance.UserThemesUUID: {"uuid": appearance.UserThemesUUID, "name": "User Themes", "type": 1.0,
			"state": 2.0, "enabled": false, "canChange": true}}}
	in := &appearance.Installer{Data: filepath.Join(dir, "data"), Home: filepath.Join(dir, "home"),
		SystemData: []string{filepath.Join(dir, "usr")}, Config: filepath.Join(dir, "config"),
		State:  state.Store{Path: filepath.Join(dir, "data", "uas", "state.json")},
		Runner: gs, Desktop: &appearance.GNOME{Runner: gs, ExtensionDirs: []string{}}, Limits: archives.DefaultLimits,
		Shell: &shellext.Manager{Bus: shell}}
	for _, d := range []string{filepath.Join(in.Home, ".themes", "Nordic"), filepath.Join(dir, "usr", "themes", "Yaru"),
		filepath.Join(in.ThemesDir(), "Orchis-Dark")} {
		os.MkdirAll(filepath.Join(d, "gnome-shell"), 0o755)
		os.WriteFile(filepath.Join(d, "gnome-shell", "gnome-shell.css"), []byte("stage {}"), 0o644)
	}
	os.MkdirAll(filepath.Join(in.ThemesDir(), "Orchis-Dark", "gtk-3.0"), 0o755)
	os.WriteFile(filepath.Join(in.ThemesDir(), "Orchis-Dark", "gtk-3.0", "gtk.css"), []byte("*{}"), 0o644)
	st, _ := in.State.Load()
	st.PutAppearance(state.AppearanceInstall{ID: id, Source: "pling", Ref: "1357889", Kind: string(l.Kind),
		Name: "Orchis", Themes: []string{"Orchis-Dark"}, Paths: []string{filepath.Join(in.ThemesDir(), "Orchis-Dark")}})
	in.State.Save(st)
	s := &session.Session{Settings: settings.Default(), Telemetry: telemetry.Nop{}, CatalogPath: path, Looks: in,
		Presets:    appearance.NewPresets(filepath.Join(dir, "config"), filepath.Join(dir, "data")),
		Extensions: in.Shell, Backends: map[catalog.SourceID]backend.Backend{}}
	a := New(s)
	a.icons.dir = filepath.Join(dir, "images")
	a.startup.Do(func() {})
	return a, gs, shell, id
}

// TestShellThemes lists the Shell themes on the system, says User Themes is off, asks before turning it on
// for a theme, and uses one from a GTK theme's page.
func TestShellThemes(t *testing.T) {
	a, gs, shell, id := shellTestApp(t)
	u := uitest.New(t, a.Root, uitest.Size(1280, 900))
	defer u.Close()

	a.goTo(view{page: pageAppearance})
	u.WaitFor("the Shell themes tab", func() bool { return u.HasName("Shell themes") })
	u.ByName("Shell themes").Click()
	u.WaitFor("the list", func() bool {
		return u.Has("shell-row:") && u.Has("shell-row:Nordic") && u.Has("shell-row:Yaru") && u.Has("shell-row:Orchis-Dark")
	})
	waitText(t, u, "shell-row:", "In use") // the Shell's own look, with User Themes off
	waitText(t, u, "shell-row:Orchis-Dark", "Comes with Orchis")
	u.WaitFor("the note that User Themes is off", func() bool { return u.HasName("User Themes is off") })
	if shell.called("EnableExtension " + appearance.UserThemesUUID) {
		t.Fatal("User Themes was turned on by itself")
	}

	// Use asks first; Cancel changes nothing.
	u.Get("shell-row:Nordic").ByRole(access.RoleButton, "Use").Click()
	u.WaitFor("the question", func() bool { return u.HasRole(access.RoleAlertDialog, "Turn on User Themes?") })
	u.ByRole(access.RoleAlertDialog, "Turn on User Themes?").ByRole(access.RoleButton, "Cancel").Click()
	u.WaitFor("the dialog to close", func() bool { return !u.HasRole(access.RoleAlertDialog, "Turn on User Themes?") })
	if shell.called("EnableExtension") || gs.get("shell name") != "" {
		t.Fatal("Cancel changed something")
	}
	u.Advance(time.Second)
	u.Settle()
	// Yes turns it on, then uses the theme.
	u.Get("shell-row:Nordic").ByRole(access.RoleButton, "Use").Click()

	u.WaitFor("the question", func() bool { return u.HasRole(access.RoleAlertDialog, "Turn on User Themes?") })
	u.ByRole(access.RoleAlertDialog, "Turn on User Themes?").ByRole(access.RoleButton, "Turn on User Themes and use").Click()
	u.WaitFor("the theme in use", func() bool { return gs.get("shell name") == "Nordic" })
	if !shell.enabled(appearance.UserThemesUUID) {
		t.Error("User Themes isn't on")
	}
	a.goTo(view{page: pageAppearance})
	a.lookTab.Set(lookShell)
	waitText(t, u, "shell-row:Nordic", "In use")

	// A GTK theme uas installed with a Shell theme says so on its page, and uses it for the Shell.
	a.openLook(id)
	u.WaitFor("the theme's page", func() bool { return u.Has("look:" + id) })
	waitText(t, u, "look:"+id, "Orchis-Dark has a GNOME Shell theme")
	waitText(t, u, "look:"+id, appearance.ShellNote)
	clickButton(u, "Use for the Shell")
	u.WaitFor("the Shell theme", func() bool { return gs.get("shell name") == "Orchis-Dark" })
}
