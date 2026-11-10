package gui

import (
	"context"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/tyler-builds/jamb/access"
	"github.com/tyler-builds/jamb/ui/uitest"

	"github.com/tyler-builds/uas/internal/client/backend"
	"github.com/tyler-builds/uas/internal/client/gearlever"
	"github.com/tyler-builds/uas/internal/client/session"
)

// shotMigrations is what a scan might find: an AppImage Gear Lever set up that the catalog has, one it set up
// that updates from its own site, a download nothing set up, a copy of an app uas already manages, and
// records Gear Lever keeps of AppImages that are gone.
func shotMigrations() session.MigrationScan {
	return session.MigrationScan{
		Apps: []session.Migration{
			{Path: "/home/me/AppImages/helium.appimage", Name: "Helium", Version: "0.21.1", From: "Gear Lever",
				Folder: "~/AppImages", Updates: "github.com/imputnet/helium-linux", Catalog: true, Suggested: true},
			{Path: "/home/me/AppImages/curseforge.appimage", Name: "CurseForge", Version: "1.325.0", From: "Gear Lever",
				Folder: "~/AppImages", Updates: "curseforge.overwolf.com", Suggested: true},
			{Path: "/home/me/Downloads/Notes-2.0.AppImage", Name: "Notes", Version: "2.0", Folder: "~/Downloads"},
			{Path: "/home/me/Downloads/Ghostty-1.3.AppImage", Name: "Ghostty", Version: "1.3", Folder: "~/Downloads",
				Blocked: "uas already manages Ghostty 1.4.0"},
		},
		Leftovers: []gearlever.Entry{{Name: "T3 Code (Beta)", Path: "/home/me/AppImages/t3_code_beta.appimage"},
			{Name: "Ghostty", Path: "/home/me/AppImages/ghostty.appimage"}},
	}
}

// TestMigrate checks that Settings' Migrate scans, lists what it found with the suggested ones picked, and
// hands over what the user picks.
func TestMigrate(t *testing.T) {
	a, _, _ := testApp(t)
	var mu sync.Mutex
	var migrated []string
	cleared := 0
	a.scanMigrations = func(context.Context) (session.MigrationScan, error) { return shotMigrations(), nil }
	a.migrateOne = func(_ context.Context, m session.Migration, p func(backend.Progress)) error {
		mu.Lock()
		defer mu.Unlock()
		migrated = append(migrated, m.Name)
		return nil
	}
	a.clearLeftovers = func(l []gearlever.Entry) error {
		mu.Lock()
		defer mu.Unlock()
		cleared += len(l)
		return nil
	}
	a.goTo(view{page: pageSettings})
	u := uitest.New(t, a.Root, uitest.Size(1100, 2000))
	defer u.Close()

	clickButton(u, "Migrate…")
	const title = "Migrate apps to uas"
	u.WaitFor("the scan's results", func() bool { return u.Has("migrate-list") })
	list := u.Get("migrate-list").Text()
	for _, want := range []string{
		"From Gear Lever, in ~/AppImages. uas will update it from the catalog (github.com/imputnet/helium-linux).",
		"From Gear Lever, in ~/AppImages. uas will update it from curseforge.overwolf.com.",
		"A file in ~/Downloads. It doesn't say where its updates come from, so uas can't update it.",
		"Can't be migrated: uas already manages Ghostty 1.4.0.",
		"T3 Code (Beta), Ghostty. Only Gear Lever's notes and icons of them are removed.",
	} {
		if !strings.Contains(list, want) {
			t.Errorf("the dialog doesn't say %q:\n%s", want, list)
		}
	}
	// What other tools set up is picked; a mere download isn't, and a blocked copy can't be.
	box := func(name string) *uitest.Element { return u.ByRole(access.RoleCheckBox, name) }
	if !box("Helium 0.21.1").Checked() || !box("CurseForge 1.325.0").Checked() || box("Notes 2.0").Checked() ||
		box("Ghostty 1.3").Checked() || !box("Ghostty 1.3").Disabled() {
		t.Error("the wrong apps are picked to begin with")
	}
	d := u.ByRole(access.RoleAlertDialog, title)
	if !d.HasRole(access.RoleButton, "Migrate 2 apps") {
		t.Fatal("the button doesn't count the picked apps")
	}
	// Pick the download too, and leave CurseForge and the leftovers.
	box("Notes 2.0").Click()
	box("CurseForge 1.325.0").Click()
	box("Clear 2 records Gear Lever still keeps of AppImages that are gone").Click()
	u.WaitFor("the button to follow", func() bool { return d.HasRole(access.RoleButton, "Migrate 2 apps") })
	mu.Lock()
	if len(migrated) != 0 {
		t.Fatal("migrated before the user confirmed")
	}
	mu.Unlock()
	d.ByRole(access.RoleButton, "Migrate 2 apps").Click()
	u.WaitFor("the migration", func() bool {
		mu.Lock()
		defer mu.Unlock()
		return slices.Equal(migrated, []string{"Helium", "Notes"})
	})
	u.WaitFor("the job to end", func() bool { _, busy := a.running("migrate"); return !busy })
	mu.Lock()
	defer mu.Unlock()
	if cleared != 0 {
		t.Errorf("cleared %d leftover records the user unpicked", cleared)
	}
	if a.migrate.Peek() != nil {
		t.Error("the dialog stayed open")
	}
}

// TestMigrateNothingFound checks the dialog when the scan finds nothing, and with only leftovers.
func TestMigrateNothingFound(t *testing.T) {
	a, _, _ := testApp(t)
	scan := session.MigrationScan{}
	var cleared atomic.Int32
	a.scanMigrations = func(context.Context) (session.MigrationScan, error) { return scan, nil }
	a.clearLeftovers = func(l []gearlever.Entry) error { cleared.Add(int32(len(l))); return nil }
	a.goTo(view{page: pageSettings})
	u := uitest.New(t, a.Root, uitest.Size(1100, 2000))
	defer u.Close()

	clickButton(u, "Migrate…")
	u.WaitFor("the empty result", func() bool {
		return u.HasName("Nothing to migrate: uas found no AppImages that it doesn't manage already.")
	})
	u.ByRole(access.RoleAlertDialog, "Migrate apps to uas").ByRole(access.RoleButton, "Close").Click()
	u.WaitFor("the dialog to close", func() bool { return a.migrate.Peek() == nil })

	// Only leftovers: the button clears them.
	scan = session.MigrationScan{Leftovers: shotMigrations().Leftovers}
	clickButton(u, "Migrate…")
	u.WaitFor("the leftovers", func() bool { return u.Has("migrate-list") })
	u.ByRole(access.RoleAlertDialog, "Migrate apps to uas").ByRole(access.RoleButton, "Clear records").Click()
	u.WaitFor("the clearing", func() bool { _, busy := a.running("migrate"); return !busy && cleared.Load() == 2 })
}
