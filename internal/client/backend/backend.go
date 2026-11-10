// Package backend defines how uas installs, updates and removes apps for each source.
package backend

import (
	"context"
	"errors"
	"strings"

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
	// Icon is a file:// URL of the app's icon, for apps whose files uas keeps (AppImages).
	Icon string `json:"icon,omitempty"`
	// Desktop is the path of the app's launcher, for apps whose launcher uas wrote (AppImages).
	Desktop string `json:"desktop,omitempty"`
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

// SameApp reports whether a, an AppImage the user added from a file, is the app the catalog knows by name
// and ids: the app's ID, its listings' AppStream IDs, and the repos of its release listings. An AppImage
// names itself by its AppStream ID when it ships metainfo ("io.github.openpane.OpenPane"), which is the
// app whatever its name; otherwise by its launcher's file name ("curseforge"), which fits the last part of
// an ID ("com.overwolf.curseforge", "github:pingdotgg/t3code") and counts when the names agree too.
func (a InstalledApp) SameApp(name string, ids ...string) bool {
	if a.Source != catalog.SourceAppImage || a.AppID == "" {
		return false
	}
	sameName := name != "" && strings.EqualFold(strings.TrimSpace(a.Name), strings.TrimSpace(name))
	for _, id := range ids {
		id = strings.TrimSuffix(id, ".desktop")
		if id == "" {
			continue
		}
		if strings.Contains(id, ".") && strings.EqualFold(a.AppID, id) {
			return true
		}
		if sameName && strings.EqualFold(a.AppID, IDTail(id)) {
			return true
		}
	}
	return false
}

// IDTail is the last part of an app ID: "curseforge" of "com.overwolf.curseforge", "t3code" of
// "github:pingdotgg/t3code".
func IDTail(id string) string {
	return id[strings.LastIndexAny(id, "./:")+1:]
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

// AvailableUpdate is a newer version of an installed app.
type AvailableUpdate struct {
	Ref     string `json:"ref"`
	Scope   string `json:"scope"`
	Version string `json:"version,omitempty"`
}

// UpdateChecker is implemented by backends that can tell which installed apps have updates. Backends
// without it (layered packages, which update with the system image) offer no per-app updates.
type UpdateChecker interface {
	Updates(ctx context.Context) ([]AvailableUpdate, error)
}

// UpdateKey identifies an installed app among the updates of every source.
func UpdateKey(src catalog.SourceID, scope, ref string) string {
	return string(src) + "|" + scope + "|" + ref
}

// Key returns the app's UpdateKey.
func (a InstalledApp) Key() string { return UpdateKey(a.Source, a.Scope, a.Ref) }

// Restarter is implemented by backends whose changes take effect only after a restart.
type Restarter interface {
	NeedsRestart() bool
}

// Permission is one thing a sandboxed app may or may not reach, such as the network or the home folder.
type Permission struct {
	Key     string `json:"key"`
	Label   string `json:"label"`
	Detail  string `json:"detail"`
	Allowed bool   `json:"allowed"`
	// Changed is true when the user's overrides decide it, rather than the app's own manifest.
	Changed bool `json:"changed"`
	// Note explains an allowed permission that turning off won't fully take away, such as home folder access
	// through access to all files.
	Note string `json:"note,omitempty"`
}

// PermissionEditor is implemented by backends whose apps run in a sandbox the user can open or close
// further (Flatpak). appID is the app's ID in the tool.
type PermissionEditor interface {
	Permissions(ctx context.Context, appID string) ([]Permission, error)
	SetPermission(ctx context.Context, appID, key string, allow bool) error
	ResetPermissions(ctx context.Context, appID string) error
}

// ErrWrongSource is returned when a backend is given a listing or app from another source.
var ErrWrongSource = errors.New("listing is from a different source")
