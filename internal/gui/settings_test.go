package gui

import (
	"context"
	"sync/atomic"
	"testing"

	"github.com/tyler-builds/jamb/access"
	"github.com/tyler-builds/jamb/ui/uitest"

	"github.com/tyler-builds/uas/internal/version"
)

// TestRestartAfterSelfUpdate checks that once an update of uas itself is installed, the About row's button
// is Restart, which restarts the app, and not Update again.
func TestRestartAfterSelfUpdate(t *testing.T) {
	old := version.Version
	version.Version = "0.3.1" // a released build; builds from source have no update button
	defer func() { version.Version = old }()

	a, _, _ := testApp(t)
	restarts := 0
	a.Restart = func() { restarts++ }
	// As the Update button's job leaves it when the new programs are in place.
	a.updatedTo.Set("0.4.0")
	a.goTo(view{page: pageSettings})
	// Tall enough that About, the last section, is on screen.
	u := uitest.New(t, a.Root, uitest.Size(1100, 2000))
	defer u.Close()

	waitText(t, u, "settings", "uas 0.4.0 is installed. Restart uas to start using it.")
	if u.HasRole(access.RoleButton, "Update to 0.4.0") {
		t.Error("still offering the update that is installed")
	}
	// A running install would be cut off by a restart, so the button waits for it.
	a.setJob("org.example.Notes", job{title: "Installing Notes"})
	u.WaitFor("Restart to wait for the job", func() bool {
		return u.HasRole(access.RoleButton, "Restart") && u.ByRole(access.RoleButton, "Restart").Disabled()
	})
	a.setJob("org.example.Notes", job{title: "Installing Notes", done: true})
	clickButton(u, "Restart")
	if restarts != 1 {
		t.Errorf("Restart ran %d times, want 1", restarts)
	}

	// Without a way to restart (the app run some other way), the row still says to.
	a.Restart = nil
	a.updatedTo.Set("0.4.1")
	waitText(t, u, "settings", "uas 0.4.1 is installed. Restart uas to start using it.")
	if u.HasRole(access.RoleButton, "Restart") {
		t.Error("a Restart button with nothing to restart with")
	}
}

// TestUninstall checks that Settings → About uninstalls uas after asking, and then closes the app.
func TestUninstall(t *testing.T) {
	old := version.Version
	version.Version = "0.3.1"
	defer func() { version.Version = old }()

	a, _, _ := testApp(t)
	var uninstalls, quits atomic.Int32
	a.Uninstall = func(context.Context) error { uninstalls.Add(1); return nil }
	a.Quit = func() { quits.Add(1) }
	a.goTo(view{page: pageSettings})
	u := uitest.New(t, a.Root, uitest.Size(1100, 2200))
	defer u.Close()

	clickButton(u, "Uninstall uas")
	// Cancel keeps uas.
	clickButton(u, "Cancel")
	clickButton(u, "Uninstall uas")
	clickButton(u, "Uninstall")
	u.WaitFor("the app to close", func() bool { return quits.Load() == 1 })
	if n := uninstalls.Load(); n != 1 {
		t.Errorf("Uninstall ran %d times, want 1", n)
	}

	// A build from source has no Uninstall row: what is installed isn't it.
	version.Version = "dev"
	a2, _, _ := testApp(t)
	a2.Uninstall = a.Uninstall
	a2.goTo(view{page: pageSettings})
	u2 := uitest.New(t, a2.Root, uitest.Size(1100, 2200))
	defer u2.Close()
	waitText(t, u2, "settings", "built from source")
	if u2.HasRole(access.RoleButton, "Uninstall uas") {
		t.Error("a build from source offers to uninstall")
	}
}
