// Package backend defines how uas installs, updates and removes apps for each source.
package backend

import (
	"context"
	"errors"

	"github.com/tyler-builds/uas/internal/catalog"
)

// InstalledApp is an app installed on this system, whether or not uas installed it.
type InstalledApp struct {
	Source catalog.SourceID `json:"source"`
	// Ref identifies the install to its tool, such as "app/org.mozilla.firefox/x86_64/stable" for Flatpak.
	Ref string `json:"ref"`
	// AppID is the app's ID in the tool, which for Flatpak and Fedora is usually its AppStream ID.
	AppID   string `json:"app_id"`
	Name    string `json:"name"`
	Version string `json:"version,omitempty"`
	// Scope is where the app is installed: "user" or "system" for Flatpak.
	Scope string `json:"scope,omitempty"`
	// Origin is the repo the app came from, such as the Flatpak remote "flathub".
	Origin string `json:"origin,omitempty"`
	// Package is the package name, for package-based sources.
	Package string `json:"package,omitempty"`
	// Pending is true when the change takes effect only after a restart.
	Pending bool `json:"pending,omitempty"`
}

// Matches reports whether a is the installed copy of l.
func (a InstalledApp) Matches(l catalog.Listing) bool {
	if a.Source != l.Source {
		return false
	}
	return a.Ref == l.Ref || (l.Package != "" && a.Package == l.Package)
}

// Progress is a status update from a running job.
type Progress struct {
	Message string
}

// Backend installs apps from one source.
type Backend interface {
	Source() catalog.SourceID
	// Supports reports whether this system can install the listing, for example whether a Fedora listing is
	// for the release the system runs.
	Supports(l catalog.Listing) bool
	// Installed lists apps installed through this backend's tool, including ones uas did not install.
	Installed(ctx context.Context) ([]InstalledApp, error)
	Install(ctx context.Context, l catalog.Listing, progress func(Progress)) error
	Update(ctx context.Context, a InstalledApp, progress func(Progress)) error
	Remove(ctx context.Context, a InstalledApp, progress func(Progress)) error
}

// Restarter is implemented by backends whose changes take effect only after a restart.
type Restarter interface {
	NeedsRestart() bool
}

// ErrWrongSource is returned when a backend is given a listing or app from another source.
var ErrWrongSource = errors.New("listing is from a different source")
