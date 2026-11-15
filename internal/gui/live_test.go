package gui

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/tyler-builds/jamb/access"
	"github.com/tyler-builds/jamb/ui/uitest"

	"github.com/tyler-builds/uas/internal/catalog"
	"github.com/tyler-builds/uas/internal/client/backend"
	"github.com/tyler-builds/uas/internal/client/session"
	"github.com/tyler-builds/uas/internal/client/settings"
	"github.com/tyler-builds/uas/internal/client/sysapps"
	"github.com/tyler-builds/uas/internal/client/telemetry"
)

// liveBackend layers Fedora packages, as rpm-ostree does: htop can install without a restart, akmods can't.
type liveBackend struct {
	fakeBackend
	checks, liveInstalls int
	// prompt is true when a check would ask for a password (polkit doesn't allow it as things stand).
	prompt bool
}

func (f *liveBackend) NeedsRestart() bool { return true }

func (f *liveBackend) CheckWithoutPrompt(context.Context) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return !f.prompt
}

func (f *liveBackend) CheckLive(_ context.Context, l catalog.Listing) backend.Live {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.checks++
	if l.Package == "htop" {
		return backend.Live{OK: true}
	}
	return backend.Live{Why: "it includes akmods, which works only after a restart"}
}

func (f *liveBackend) InstallLive(ctx context.Context, l catalog.Listing, p func(backend.Progress)) error {
	f.mu.Lock()
	f.liveInstalls++
	f.mu.Unlock()
	return f.Install(ctx, l, p)
}

func (f *liveBackend) counts() (checks, live int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.checks, f.liveInstalls
}

func liveTestApp(t *testing.T) (*App, *liveBackend) {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "catalog.db")
	pkg := func(id, name string) catalog.App {
		return catalog.App{ID: id, Kind: catalog.KindDesktopApp, Name: name, Summary: "A tool",
			Listings: []catalog.Listing{{Source: catalog.SourceFedora, Ref: "fedora-45/" + name, Package: name,
				Distro: "fedora-45", Name: name, Version: "1.0"}}}
	}
	apps := []catalog.App{pkg("htop", "htop"), pkg("akmods", "akmods")}
	if err := catalog.Write(context.Background(), path, apps, catalog.BuildInfo{BuiltAt: time.Now(), Apps: 2}); err != nil {
		t.Fatal(err)
	}
	fedora := &liveBackend{fakeBackend: fakeBackend{src: catalog.SourceFedora}}
	s := &session.Session{
		Settings:    settings.Default(),
		Backends:    map[catalog.SourceID]backend.Backend{catalog.SourceFedora: fedora},
		Telemetry:   telemetry.Nop{},
		CatalogPath: path,
		SysApps:     map[string]sysapps.App{},
	}
	a := New(s)
	a.icons.dir = filepath.Join(dir, "images")
	a.startup.Do(func() {})
	return a, fedora
}

func TestLiveMarkAndDialogShareTheCheck(t *testing.T) {
	a, fedora := liveTestApp(t)
	u := uitest.New(t, a.Root, uitest.Size(1100, 760))
	defer u.Close()

	a.openApp("htop")
	u.WaitFor("the mark", func() bool { return u.HasRole(access.RoleImage, liveLabel) })

	// Install opens the dialog with the choice, from the page's check: no second dry run.
	clickButton(u, "Install")
	title := "Install htop from Fedora 45 · 1.0?"
	u.WaitFor("the dialog", func() bool { return u.HasRole(access.RoleAlertDialog, title) })
	u.WaitFor("the choice", func() bool { return u.HasName("Install now (no restart needed)") })
	if checks, _ := fedora.counts(); checks != 1 {
		t.Errorf("%d checks, want 1", checks)
	}
	u.ByRole(access.RoleAlertDialog, title).ByRole(access.RoleButton, "Install").Click()
	u.WaitFor("the live install", func() bool { _, live := fedora.counts(); return live == 1 })

	// Installed now: no mark, and no check after the job.
	u.WaitFor("the installed state", func() bool { return !u.HasRole(access.RoleButton, "Install") })
	u.WaitForQueries()
	if u.HasRole(access.RoleImage, liveLabel) {
		t.Error("the mark is still shown for an installed app")
	}
	if checks, _ := fedora.counts(); checks != 1 {
		t.Errorf("%d checks after the install, want 1", checks)
	}
}

func TestNoLiveMarkWhenItNeedsARestart(t *testing.T) {
	a, fedora := liveTestApp(t)
	u := uitest.New(t, a.Root, uitest.Size(1100, 760))
	defer u.Close()

	a.openApp("akmods")
	u.WaitFor("the check", func() bool { checks, _ := fedora.counts(); return checks == 1 })
	u.WaitForQueries()
	if u.HasRole(access.RoleImage, liveLabel) {
		t.Error("the mark is shown for a package that needs a restart")
	}
	clickButton(u, "Install")
	u.WaitFor("the reason", func() bool {
		return u.HasRole(access.RoleAlertDialog, "Install akmods from Fedora 45 · 1.0?") &&
			u.HasName("It takes effect after a restart: it includes akmods, which works only after a restart.")
	})
}

// Without authorization granted up front, opening the page runs no dry run, which would ask for a password;
// the install dialog still checks, since the user asked to install.
func TestNoPageCheckWhenItWouldPrompt(t *testing.T) {
	a, fedora := liveTestApp(t)
	fedora.prompt = true
	u := uitest.New(t, a.Root, uitest.Size(1100, 760))
	defer u.Close()

	a.openApp("htop")
	u.WaitFor("the app page", func() bool { return u.Has("detail:htop") })
	u.WaitForQueries()
	if checks, _ := fedora.counts(); checks != 0 {
		t.Errorf("the page ran %d checks", checks)
	}
	if u.HasRole(access.RoleImage, liveLabel) {
		t.Error("the mark is shown without a check")
	}

	clickButton(u, "Install")
	u.WaitFor("the choice", func() bool { return u.HasName("Install now (no restart needed)") })
	if checks, _ := fedora.counts(); checks != 1 {
		t.Errorf("%d checks after Install, want 1", checks)
	}
	u.Press("escape")

	// Once polkit allows it without asking, opening the page again checks and marks it.
	fedora.mu.Lock()
	fedora.prompt = false
	fedora.mu.Unlock()
	a.openApp("akmods")
	u.WaitFor("the akmods page", func() bool { return u.Has("detail:akmods") })
	a.openApp("htop")
	u.WaitFor("the mark", func() bool { return u.HasRole(access.RoleImage, liveLabel) })
}
