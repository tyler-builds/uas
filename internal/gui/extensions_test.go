package gui

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/godbus/dbus/v5"
	"github.com/tyler-builds/jamb/access"
	"github.com/tyler-builds/jamb/ui/uitest"

	"github.com/tyler-builds/uas/internal/catalog"
	"github.com/tyler-builds/uas/internal/client/backend"
	"github.com/tyler-builds/uas/internal/client/session"
	"github.com/tyler-builds/uas/internal/client/settings"
	"github.com/tyler-builds/uas/internal/client/shellext"
	"github.com/tyler-builds/uas/internal/client/telemetry"
)

// fakeShell is GNOME Shell's extension service, in memory.
type fakeShell struct {
	mu         sync.Mutex
	version    string
	extensions map[string]map[string]any
	answer     string // what the Shell's install dialog answers: "successful" or "cancelled"
	calls      []string
}

func (f *fakeShell) Call(_ context.Context, method string, args []any, out ...any) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	uuid := ""
	if len(args) > 0 {
		uuid, _ = args[0].(string)
	}
	f.calls = append(f.calls, method+" "+uuid)
	switch method {
	case "ListExtensions":
		list := map[string]map[string]dbus.Variant{}
		for id, e := range f.extensions {
			list[id] = map[string]dbus.Variant{}
			for k, v := range e {
				list[id][k] = dbus.MakeVariant(v)
			}
		}
		*out[0].(*map[string]map[string]dbus.Variant) = list
	case "InstallRemoteExtension":
		if f.answer == "successful" {
			f.extensions[uuid] = map[string]any{"uuid": uuid, "name": uuid, "type": 2.0, "state": 1.0,
				"enabled": true, "canChange": true, "version": 7.0}
		}
		*out[0].(*string) = f.answer
	case "EnableExtension", "DisableExtension":
		e, ok := f.extensions[uuid]
		if ok {
			e["enabled"] = method == "EnableExtension"
		}
		*out[0].(*bool) = ok
	case "UninstallExtension":
		e, ok := f.extensions[uuid]
		ok = ok && e["type"] == 2.0
		if ok {
			delete(f.extensions, uuid)
		}
		*out[0].(*bool) = ok
	case "OpenExtensionPrefs", "CheckForUpdates":
	default:
		return errors.New("unknown method " + method)
	}
	return nil
}

func (f *fakeShell) Get(_ context.Context, property string) (any, error) {
	switch property {
	case "ShellVersion":
		return f.version, nil
	case "UserExtensionsEnabled":
		return true, nil
	}
	return nil, errors.New("unknown property " + property)
}

func (f *fakeShell) Set(context.Context, string, any) error { return nil }

func (f *fakeShell) called(call string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Contains(f.calls, call)
}

// count is how many times a call was made.
func (f *fakeShell) count(call string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	n := 0
	for _, c := range f.calls {
		if c == call {
			n++
		}
	}
	return n
}

// set changes a field of an extension, as the Shell does when something happens to it.
func (f *fakeShell) set(uuid, key string, value any) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.extensions[uuid][key] = value
}

// writeUpdate puts a downloaded update for an extension in dir, as the Shell unpacks one.
func writeUpdate(t testing.TB, dir, uuid, version string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(dir, uuid), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, uuid, "metadata.json"), []byte(`{"version-name": "`+version+`"}`),
		0o644); err != nil {
		t.Fatal(err)
	}
}

func (f *fakeShell) has(uuid string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	_, ok := f.extensions[uuid]
	return ok
}

func (f *fakeShell) enabled(uuid string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	on, _ := f.extensions[uuid]["enabled"].(bool)
	return on
}

// extTestApp is an app on GNOME 51 with two installed extensions (the user's Dock and the system's Caffeine)
// and a catalog of three: Dock, Clock (for GNOME 51) and Old (for GNOME 45 only).
func extTestApp(t *testing.T) (*App, *fakeShell) {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "catalog.db")
	ext := func(uuid, name, summary string, downloads int, shells ...string) catalog.App {
		l := catalog.Listing{Source: catalog.SourceGNOMEExtensions, Ref: uuid, Kind: catalog.KindShellExtension,
			Name: name, Summary: summary, Description: summary + ".\n\nSecond paragraph.", Developer: "someone",
			Installs: downloads, Shells: shells, StoreURL: "https://extensions.gnome.org/extension/1/x/"}
		return catalog.App{ID: catalog.ExtensionAppID(uuid), Kind: catalog.KindShellExtension, Name: name,
			Summary: summary, Description: l.Description, Developer: "someone", Popularity: downloads,
			Listings: []catalog.Listing{l}}
	}
	apps := []catalog.App{
		{ID: "org.example.Notes", Kind: catalog.KindDesktopApp, Name: "Notes", Summary: "Take notes",
			Listings: []catalog.Listing{{Source: catalog.SourceFlathub, Ref: "app/org.example.Notes/x86_64/stable",
				AppStreamID: "org.example.Notes", Name: "Notes", Version: "1.0"}}},
		ext("dock@example.com", "Dock", "A dock for the desktop", 9000, "50", "51"),
		ext("clock@example.com", "Clock", "Move the clock", 400, "51"),
		ext("old@example.com", "Old", "For an old GNOME", 80000, "45"),
	}
	info := catalog.BuildInfo{BuiltAt: time.Now(), Apps: 1,
		Extensions: &catalog.SourceStatus{Source: catalog.SourceGNOMEExtensions, Listings: 3}}
	if err := catalog.Write(context.Background(), path, apps, info); err != nil {
		t.Fatal(err)
	}
	shell := &fakeShell{version: "51.1", answer: "successful", extensions: map[string]map[string]any{
		"dock@example.com": {"uuid": "dock@example.com", "name": "Dock", "description": "A dock", "type": 2.0,
			"state": 1.0, "enabled": true, "hasPrefs": true, "canChange": true, "version": 110.0},
		"caffeine@example.com": {"uuid": "caffeine@example.com", "name": "Caffeine", "description": "Stay awake",
			"type": 1.0, "state": 6.0, "enabled": false, "canChange": true, "version-name": "61.1"},
	}}
	s := &session.Session{
		Settings:    settings.Default(),
		Backends:    map[catalog.SourceID]backend.Backend{catalog.SourceFlathub: &fakeBackend{src: catalog.SourceFlathub}},
		Telemetry:   telemetry.Nop{},
		CatalogPath: path,
		Extensions:  &shellext.Manager{Bus: shell, UpdatesDir: filepath.Join(dir, "extension-updates")},
	}
	a := New(s)
	a.icons.dir = filepath.Join(dir, "images")
	a.startup.Do(func() {})
	a.goTo(view{page: pageExtensions})
	return a, shell
}

func TestExtensionsInstalled(t *testing.T) {
	a, shell := extTestApp(t)
	u := uitest.New(t, a.Root, uitest.Size(1100, 760))
	defer u.Close()

	// Both installed extensions are listed, whoever installed them: the user's and the system's.
	u.WaitFor("the installed extensions", func() bool { return u.Has("ext:dock@example.com") && u.Has("ext:caffeine@example.com") })
	waitText(t, u, "extensions", "Added by you")
	waitText(t, u, "ext:dock@example.com", "Version 110")
	waitText(t, u, "ext:caffeine@example.com", "Version 61.1")
	// The system's can't be removed.
	if !u.HasRole(access.RoleButton, "Remove Dock") || u.HasRole(access.RoleButton, "Remove Caffeine") {
		t.Error("Remove should be offered for the user's extension only")
	}

	// The switch turns one on in GNOME Shell.
	u.ByRole(access.RoleSwitch, "Turn on Caffeine").Click()
	u.WaitFor("Caffeine to turn on", func() bool { return shell.enabled("caffeine@example.com") })
	u.ByRole(access.RoleSwitch, "Turn on Dock").Click()
	u.WaitFor("Dock to turn off", func() bool { return !shell.enabled("dock@example.com") })

	// Settings opens the extension's own window.
	clickButton(u, "Settings of Dock")
	u.WaitFor("the settings window", func() bool { return shell.called("OpenExtensionPrefs dock@example.com") })

	// Remove asks first.
	clickButton(u, "Remove Dock")
	u.WaitFor("the remove dialog", func() bool { return u.HasRole(access.RoleAlertDialog, "Remove Dock?") })
	if !shell.has("dock@example.com") {
		t.Fatal("removed before the user confirmed")
	}
	u.ByRole(access.RoleAlertDialog, "Remove Dock?").ByRole(access.RoleButton, "Remove").Click()
	u.WaitFor("the removal", func() bool { return !shell.has("dock@example.com") })
	u.WaitFor("the row to go", func() bool { return !u.Has("ext:dock@example.com") })
}

func TestExtensionsBrowseAndInstall(t *testing.T) {
	a, shell := extTestApp(t)
	a.extTab.Set(extBrowse)
	u := uitest.New(t, a.Root, uitest.Size(1100, 760))
	defer u.Close()

	// Only extensions for this GNOME release are listed, and no apps.
	clock, dock := "ext-row:"+catalog.ExtensionAppID("clock@example.com"), "ext-row:"+catalog.ExtensionAppID("dock@example.com")
	u.WaitFor("the extensions for GNOME 51", func() bool { return u.Has(clock) && u.Has(dock) })
	if u.Has("ext-row:"+catalog.ExtensionAppID("old@example.com")) || u.Has("row:org.example.Notes") {
		t.Error("the list has an extension for another GNOME release, or an app")
	}
	// Dock is installed already.
	if !u.Get(dock).HasRole(access.RoleButton, "Installed") || !u.Get(clock).HasRole(access.RoleButton, "Install") {
		t.Error("Dock should show as installed and Clock as installable")
	}

	// The top bar searches extensions here.
	u.ByRole(access.RoleTextField, "Search extensions").Type("clock")
	u.WaitFor("the search", func() bool { return u.Has(clock) && !u.Has(dock) })

	// A no in GNOME's dialog installs nothing and is not a failure.
	shell.mu.Lock()
	shell.answer = "cancelled"
	shell.mu.Unlock()
	u.Get(clock).ByRole(access.RoleButton, "Install").Click()
	u.WaitFor("the install request", func() bool { return shell.called("InstallRemoteExtension clock@example.com") })
	u.WaitFor("the cancelled job to end", func() bool {
		_, busy := a.running(catalog.ExtensionAppID("clock@example.com"))
		return !busy
	})
	if shell.has("clock@example.com") {
		t.Fatal("installed after a no")
	}

	// Its page installs it.
	shell.mu.Lock()
	shell.answer = "successful"
	shell.mu.Unlock()
	u.Get(clock).ByRole(access.RoleLink, "Clock details").Click()
	page := "extension:" + catalog.ExtensionAppID("clock@example.com")
	waitText(t, u, page, "GNOME 51")
	waitText(t, u, page, "Second paragraph.")
	clickButton(u, "Install")
	u.WaitFor("the install", func() bool { return shell.has("clock@example.com") })
	waitText(t, u, page, "Turned on")
	waitText(t, u, page, "Added by you")
}

// TestExtensionNotForThisShell checks that an extension without a version for the running GNOME can't be
// installed.
func TestExtensionNotForThisShell(t *testing.T) {
	a, _ := extTestApp(t)
	a.openExtension(catalog.ExtensionAppID("old@example.com"))
	u := uitest.New(t, a.Root, uitest.Size(1100, 760))
	defer u.Close()
	page := "extension:" + catalog.ExtensionAppID("old@example.com")
	waitText(t, u, page, "Not for GNOME 51")
	if u.HasRole(access.RoleButton, "Install") {
		t.Error("Install offered for an extension this GNOME can't run")
	}
}

// TestNoExtensionsOffGNOME checks that the sidebar has no Extensions page without GNOME Shell.
func TestNoExtensionsOffGNOME(t *testing.T) {
	a, _, _ := testApp(t)
	u := uitest.New(t, a.Root, uitest.Size(1100, 760))
	defer u.Close()
	u.WaitFor("the Notes row", func() bool { return u.Has("row:org.example.Notes") })
	if u.HasName("Extensions") {
		t.Error("an Extensions item without GNOME Shell")
	}
}

// TestExtensionUpdatesWait checks that the Updates page lists an extension whose update GNOME Shell has
// downloaded, as waiting for the next login, and that Refresh asks the Shell to look for more.
func TestExtensionUpdatesWait(t *testing.T) {
	a, shell := extTestApp(t)
	a.goTo(view{page: pageUpdates})
	u := uitest.New(t, a.Root, uitest.Size(1100, 760))
	defer u.Close()

	// Nothing waits yet. Opening the page asked the Shell to look.
	u.WaitFor("an empty Updates page", func() bool { return u.HasName("Everything is up to date") })
	u.WaitFor("the first check", func() bool { return shell.count("CheckForUpdates ") == 1 })
	if u.Has("ext-update:dock@example.com") {
		t.Fatal("an extension update listed before there is one")
	}

	// The Shell downloads an update for Dock. The page asks the Shell again by itself.
	writeUpdate(t, a.s.Extensions.UpdatesDir, "dock@example.com", "111")
	shell.set("dock@example.com", "hasUpdate", true)
	u.Advance(6 * time.Second)
	u.WaitFor("the waiting update", func() bool { return u.Has("ext-update:dock@example.com") })
	waitText(t, u, "ext-update:dock@example.com", "Version 110 to 111")
	waitText(t, u, "ext-update:dock@example.com", "At next login")
	if u.HasName("Everything is up to date") {
		t.Error("the page says everything is up to date with an extension update waiting")
	}
	// Nothing to press for it: Update all is for apps.
	if u.HasRole(access.RoleButton, "Update 1 app") {
		t.Error("an Update button for an extension, which only GNOME can update")
	}

	// Refresh asks the Shell to look again, whenever it last did.
	clickButton(u, "Refresh")
	u.WaitFor("the second check", func() bool { return shell.count("CheckForUpdates ") == 2 })

	// The Extensions page says which version waits.
	a.goTo(view{page: pageExtensions})
	waitText(t, u, "ext:dock@example.com", "version 111 installs at next login")
}

// TestRestoreExtensions checks that restoring an export installs its extensions through GNOME Shell and
// switches on the ones that are here but off.
func TestRestoreExtensions(t *testing.T) {
	a, shell := extTestApp(t)
	ex := session.Export{Version: session.ExportVersion, System: "Bazzite", Extensions: []session.ExportedExtension{
		{UUID: "clock@example.com", Name: "Clock", Enabled: true},                     // not here: install
		{UUID: "caffeine@example.com", Name: "Caffeine", Enabled: true, System: true}, // here but off: switch on
		{UUID: "dock@example.com", Name: "Dock", Enabled: true},                       // here and on
		{UUID: "old@example.com", Name: "Old", Enabled: true},                         // not for GNOME 51
	}}
	path := filepath.Join(t.TempDir(), "export.json")
	if err := writeExport(path, ex); err != nil {
		t.Fatal(err)
	}
	p, err := a.planRestore(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	if len(p.exts) != 4 || len(p.extTodo()) != 2 {
		t.Fatalf("plan = %+v", p.exts)
	}
	a.goTo(view{page: pageInstalled})
	a.restore.Set(p)
	u := uitest.New(t, a.Root, uitest.Size(1100, 760))
	defer u.Close()

	const title = "Restore apps from Bazzite"
	u.WaitFor("the restore dialog", func() bool { return u.HasRole(access.RoleAlertDialog, title) })
	d := u.ByRole(access.RoleAlertDialog, title)
	list := u.Get("restore-list").Text()
	for _, want := range []string{"Clock · GNOME extension", "Caffeine · GNOME extension, here already: switch on",
		"Old · GNOME extension (no version for GNOME 51)"} {
		if !strings.Contains(list, want) {
			t.Errorf("the dialog doesn't list %q:\n%s", want, list)
		}
	}
	if want := "0 apps to install, 0 already here. 2 extensions to restore; GNOME asks you about each one it installs."; !u.HasName(want) {
		t.Errorf("the dialog doesn't say %q", want)
	}
	d.ByRole(access.RoleButton, "Restore").Click()
	u.WaitFor("Clock to be installed", func() bool { return shell.has("clock@example.com") })
	u.WaitFor("Caffeine to be switched on", func() bool { return shell.enabled("caffeine@example.com") })
	if shell.has("old@example.com") {
		t.Error("installed an extension this GNOME can't run")
	}

	// An export made here lists what a restore elsewhere needs: the user's, and the system's that are on.
	got, err := a.s.Export(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	var uuids []string
	for _, e := range got.Extensions {
		uuids = append(uuids, e.UUID)
	}
	if want := []string{"caffeine@example.com", "clock@example.com", "dock@example.com"}; !slices.Equal(uuids, want) {
		t.Errorf("exported %q, want %q", uuids, want)
	}
}
