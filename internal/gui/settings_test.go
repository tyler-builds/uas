package gui

import (
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
