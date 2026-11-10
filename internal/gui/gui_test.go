package gui

import (
	"context"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/tyler-builds/jamb/access"
	"github.com/tyler-builds/jamb/ui/uitest"

	"github.com/tyler-builds/uas/internal/catalog"
	"github.com/tyler-builds/uas/internal/client/backend"
	"github.com/tyler-builds/uas/internal/client/session"
	"github.com/tyler-builds/uas/internal/client/settings"
	"github.com/tyler-builds/uas/internal/client/telemetry"
)

// fakeBackend installs into memory.
type fakeBackend struct {
	src catalog.SourceID

	mu        sync.Mutex
	installed []backend.InstalledApp
	removed   []string
	updated   []string
	available []backend.AvailableUpdate
}

func (f *fakeBackend) Source() catalog.SourceID        { return f.src }
func (f *fakeBackend) Supports(l catalog.Listing) bool { return l.Source == f.src }
func (f *fakeBackend) Installed(context.Context) ([]backend.InstalledApp, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]backend.InstalledApp(nil), f.installed...), nil
}
func (f *fakeBackend) Install(_ context.Context, l catalog.Listing, p func(backend.Progress)) error {
	p(backend.Progress{Message: "Downloading… 50%"})
	f.mu.Lock()
	defer f.mu.Unlock()
	f.installed = append(f.installed, backend.InstalledApp{Source: f.src, Ref: l.Ref, AppID: l.AppStreamID,
		Name: l.Name, Version: l.Version, Scope: "user"})
	return nil
}
func (f *fakeBackend) Update(_ context.Context, a backend.InstalledApp, _ func(backend.Progress)) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.updated = append(f.updated, a.Ref)
	f.available = nil
	return nil
}
func (f *fakeBackend) Updates(context.Context) ([]backend.AvailableUpdate, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]backend.AvailableUpdate(nil), f.available...), nil
}
func (f *fakeBackend) Remove(_ context.Context, a backend.InstalledApp, _ func(backend.Progress)) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.removed = append(f.removed, a.Ref)
	f.installed = nil
	return nil
}

func testApp(t *testing.T) (*App, *fakeBackend, *fakeBackend) {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "catalog.db")
	trust := &catalog.Trust{Score: 45, Flags: []string{"New project: created less than 30 days ago"},
		Signals: []catalog.TrustSignal{{Name: "Stars", Detail: "3 stars", Points: 2, Max: 25}}}
	apps := []catalog.App{
		{ID: "org.example.Notes", Kind: catalog.KindDesktopApp, Name: "Notes", Summary: "Take notes",
			IconURL: "https://invalid.example/notes.png", Popularity: 100, Description: "A **notes** app.",
			Listings: []catalog.Listing{{Source: catalog.SourceFlathub, Ref: "app/org.example.Notes/x86_64/stable",
				AppStreamID: "org.example.Notes", Name: "Notes", Version: "1.0", Verified: true}}},
		{ID: "github:example/tool", Kind: catalog.KindDesktopApp, Name: "tool", Summary: "A new tool",
			Listings: []catalog.Listing{{Source: catalog.SourceGitHub, Ref: "example/tool", Name: "tool",
				Version: "0.1", Trust: trust, Assets: []catalog.Asset{{Kind: catalog.AssetAppImage, Name: "tool.AppImage",
					Size: 1 << 20, SHA256: strings.Repeat("a", 64)}}}}},
	}
	if err := catalog.Write(context.Background(), path, apps, catalog.BuildInfo{BuiltAt: time.Now(), Apps: 2}); err != nil {
		t.Fatal(err)
	}
	fp := &fakeBackend{src: catalog.SourceFlathub}
	gh := &fakeBackend{src: catalog.SourceGitHub}
	s := &session.Session{
		Settings:    settings.Default(),
		Backends:    map[catalog.SourceID]backend.Backend{catalog.SourceFlathub: fp, catalog.SourceGitHub: gh},
		Telemetry:   telemetry.Nop{},
		CatalogPath: path,
	}
	a := New(s)
	// Point the image cache at the test's directory, and skip the catalog check at start (it would go to
	// the network).
	a.icons.dir = filepath.Join(dir, "images")
	a.startup.Do(func() {})
	return a, fp, gh
}

// clickButton waits until a button is there and ready (not disabled or busy), then clicks it.
func clickButton(u *uitest.UI, name string) {
	u.WaitFor("the "+name+" button", func() bool {
		if !u.HasRole(access.RoleButton, name) {
			return false
		}
		b := u.ByRole(access.RoleButton, name)
		return !b.Disabled() && !b.States().Has(access.StateBusy)
	})
	u.ByRole(access.RoleButton, name).Click()
}

func waitText(t *testing.T, u *uitest.UI, key, want string) {
	t.Helper()
	u.WaitFor(want, func() bool { return u.Has(key) && strings.Contains(u.Get(key).Text(), want) })
}

func TestBrowseAndInstall(t *testing.T) {
	a, fp, _ := testApp(t)
	u := uitest.New(t, a.Root, uitest.Size(1100, 760))
	defer u.Close()

	u.WaitFor("the Notes row", func() bool { return u.Has("row:org.example.Notes") })
	u.Click("row:org.example.Notes")
	u.WaitFor("the app page", func() bool { return u.Has("detail:org.example.Notes") })
	waitText(t, u, "detail:org.example.Notes", "Recommended: the only source that offers it")

	clickButton(u, "Install")
	u.WaitFor("the install to finish", func() bool {
		apps, _ := fp.Installed(context.Background())
		return len(apps) == 1
	})
	waitText(t, u, "detail:org.example.Notes", "Installed")
	// No update yet, so no Update button, and the version as text.
	waitText(t, u, "detail:org.example.Notes", "Version 1.0")
	if u.HasRole(access.RoleButton, "Update") {
		t.Error("an Update button with no update available")
	}

	// Remove asks first.
	clickButton(u, "Remove")
	u.WaitFor("the remove dialog", func() bool { return u.HasRole(access.RoleAlertDialog, "Remove Notes?") })
	if a.removing.Peek() == nil {
		t.Fatal("Remove did not ask first")
	}
	// The page and the dialog both have a "Remove" button: press the dialog's.
	u.ByRole(access.RoleAlertDialog, "Remove Notes?").ByRole(access.RoleButton, "Remove").Click()
	u.WaitFor("the removal", func() bool {
		fp.mu.Lock()
		defer fp.mu.Unlock()
		return len(fp.removed) == 1
	})
}

func TestGitHubInstallAsksWithTrustScore(t *testing.T) {
	a, _, gh := testApp(t)
	u := uitest.New(t, a.Root, uitest.Size(1100, 760))
	defer u.Close()

	u.WaitFor("the search field", func() bool { return u.HasRole(access.RoleTextField, "Search apps") })
	u.ByRole(access.RoleTextField, "Search apps").Type("tool")
	u.WaitFor("the tool row", func() bool { return u.Has("row:github:example/tool") })
	u.Get("row:github:example/tool").ByRole(access.RoleLink, "tool details").Click()
	waitText(t, u, "detail:github:example/tool", "Trust score")

	clickButton(u, "Install") // the row's Install button installs from the recommended source
	u.WaitFor("the confirmation", func() bool { return a.confirm.Peek() != nil })
	if apps, _ := gh.Installed(context.Background()); len(apps) != 0 {
		t.Fatal("installed before the user confirmed")
	}
	u.WaitFor("the confirmation dialog", func() bool {
		return u.HasRole(access.RoleAlertDialog, "Install tool from GitHub · 0.1 (trust 45)?")
	})
	u.Press("escape")
	if a.confirm.Peek() != nil {
		t.Error("Escape did not cancel")
	}
}

func TestMinimumWindowSize(t *testing.T) {
	a, _, _ := testApp(t)
	u := uitest.New(t, a.Root, uitest.Size(760, 520))
	defer u.Close()
	u.WaitFor("the Notes row", func() bool { return u.Has("row:org.example.Notes") })
	if w := u.Runtime().ContentWidth(); w > 760 {
		t.Errorf("Discover is %v px wide at the minimum window width of 760", w)
	}
	// At this size the card is below the fold; open it from the keyboard, as a user tabbing there would.
	u.Get("row:org.example.Notes").Focus()
	u.Press("enter")
	u.WaitFor("the app page", func() bool { return u.Has("detail:org.example.Notes") })
	u.Settle()
	if w := u.Runtime().ContentWidth(); w > 760 {
		t.Errorf("the app page is %v px wide at the minimum window width of 760", w)
	}
}

func TestUpdateShownOnlyWhenAvailable(t *testing.T) {
	a, fp, _ := testApp(t)
	ref := "app/org.example.Notes/x86_64/stable"
	fp.installed = []backend.InstalledApp{{Source: catalog.SourceFlathub, Ref: ref, AppID: "org.example.Notes",
		Name: "Notes", Version: "1.0", Scope: "user"}}
	fp.available = []backend.AvailableUpdate{{Ref: ref, Scope: "user", Version: "1.1"}}
	a.goTo(view{page: pageUpdates})
	u := uitest.New(t, a.Root, uitest.Size(1100, 760))
	defer u.Close()

	clickButton(u, "Update 1 app")
	u.WaitFor("the update", func() bool {
		fp.mu.Lock()
		defer fp.mu.Unlock()
		return len(fp.updated) == 1
	})
	u.WaitFor("the update buttons to go", func() bool {
		return !u.HasRole(access.RoleButton, "Update to 1.1") &&
			!u.HasRole(access.RoleButton, "Update 1 app")
	})
}

// TestSearchAsYouType searches what the field holds on every keystroke, so the page always matches the
// field, however fast the typing.
func TestSearchAsYouType(t *testing.T) {
	a, _, _ := testApp(t)
	u := uitest.New(t, a.Root, uitest.Size(1100, 760))
	defer u.Close()
	u.WaitFor("the search field", func() bool { return u.HasRole(access.RoleTextField, "Search apps") })
	field := u.ByRole(access.RoleTextField, "Search apps")
	for i, c := range "tool" {
		field.Type(string(c))
		if got, want := a.search.Peek(), "tool"[:i+1]; got != want {
			t.Fatalf("after typing %q, search = %q", want, got)
		}
	}
	u.WaitFor("the tool row", func() bool { return u.Has("row:github:example/tool") })
}

// Permissions implements backend.PermissionEditor, with a set that shows each kind of row.
func (f *fakeBackend) Permissions(context.Context, string) ([]backend.Permission, error) {
	return []backend.Permission{
		{Key: "network", Label: "Network", Detail: "Connect to the internet and the local network", Allowed: true},
		{Key: "host", Label: "All files", Detail: "Read and change every file you can, including system files"},
		{Key: "home", Label: "Home folder", Detail: "Read and change the files in your home folder", Allowed: true,
			Changed: true},
		{Key: "downloads", Label: "Downloads folder", Detail: "Read and change the files in Downloads",
			Allowed: true, Note: "Included in access to the home folder"},
		{Key: "devices", Label: "All devices", Detail: "Webcams, game controllers, USB devices and the like"},
		{Key: "audio", Label: "Sound", Detail: "Play sound and use microphones", Allowed: true},
	}, nil
}

func (f *fakeBackend) SetPermission(context.Context, string, string, bool) error { return nil }
func (f *fakeBackend) ResetPermissions(context.Context, string) error            { return nil }

// TestAddedAppImageCountsAsInstalled checks that an AppImage the user added from a file shows as the
// installed copy of the catalog's app, which no listing describes it as: on the app's page, in lists and on
// the Installed page.
func TestAddedAppImageCountsAsInstalled(t *testing.T) {
	a, _, _ := testApp(t)
	files := &fakeBackend{src: catalog.SourceAppImage, installed: []backend.InstalledApp{
		{Source: catalog.SourceAppImage, Ref: "notes", AppID: "notes", Name: "Notes", Version: "2.0", Scope: "user"}}}
	a.s.Backends[catalog.SourceAppImage] = files
	u := uitest.New(t, a.Root, uitest.Size(1100, 760))
	defer u.Close()

	// A search result says it is installed, with nothing to install.
	u.WaitFor("the search field", func() bool { return u.HasRole(access.RoleTextField, "Search apps") })
	u.ByRole(access.RoleTextField, "Search apps").Type("notes")
	u.WaitFor("the row to say installed", func() bool {
		return u.Has("row:org.example.Notes") && u.Get("row:org.example.Notes").HasRole(access.RoleButton, "Installed")
	})
	if u.Get("row:org.example.Notes").HasRole(access.RoleButton, "Install") {
		t.Error("the row offers to install an app that is installed")
	}

	// Its page manages that copy instead of offering Install from Flathub.
	u.Get("row:org.example.Notes").ByRole(access.RoleLink, "Notes details").Click()
	u.WaitFor("the app page", func() bool { return u.Has("detail:org.example.Notes") })
	waitText(t, u, "detail:org.example.Notes", "Your AppImage · 2.0")
	// The side panel is about that copy, not about Flathub, which the user didn't install it from.
	waitText(t, u, "detail:org.example.Notes", "You added this app as an AppImage file.")
	waitText(t, u, "detail:org.example.Notes", "It is also on Flathub.")
	if strings.Contains(u.Get("detail:org.example.Notes").Text(), "Recommended:") {
		t.Error("the page recommends a source for an app that is installed another way")
	}
	if u.HasRole(access.RoleButton, "Install") {
		t.Error("the page offers to install an app that is installed")
	}
	clickButton(u, "Remove")
	u.WaitFor("the remove dialog", func() bool { return u.HasRole(access.RoleAlertDialog, "Remove Notes?") })
	u.ByRole(access.RoleAlertDialog, "Remove Notes?").ByRole(access.RoleButton, "Remove").Click()
	u.WaitFor("the AppImage's removal", func() bool {
		files.mu.Lock()
		defer files.mu.Unlock()
		return len(files.removed) == 1 && files.removed[0] == "notes"
	})
	// With it gone, the page offers the app's sources again.
	u.WaitFor("Install to come back", func() bool { return u.HasRole(access.RoleButton, "Install") })
	waitText(t, u, "detail:org.example.Notes", "Recommended:")
}

// TestAddedAppImageOnInstalledPage checks that the Installed page shows an added AppImage as the catalog's
// app and opens its page.
func TestAddedAppImageOnInstalledPage(t *testing.T) {
	a, _, _ := testApp(t)
	a.s.Backends[catalog.SourceAppImage] = &fakeBackend{src: catalog.SourceAppImage, installed: []backend.InstalledApp{
		{Source: catalog.SourceAppImage, Ref: "notes", AppID: "notes", Name: "Notes", Version: "2.0", Scope: "user"}}}
	a.goTo(view{page: pageInstalled})
	u := uitest.New(t, a.Root, uitest.Size(1100, 760))
	defer u.Close()
	row := "installed:appimage|user|notes"
	u.WaitFor("the row", func() bool { return u.Has(row) })
	// The catalog's summary, and a link to its page.
	waitText(t, u, row, "Take notes")
	u.Get(row).ByRole(access.RoleLink, "Notes details").Click()
	u.WaitFor("the app page", func() bool { return u.Has("detail:org.example.Notes") })
}
