package session

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"time"

	"github.com/tyler-builds/uas/internal/catalog"
	"github.com/tyler-builds/uas/internal/client/backend"
	"github.com/tyler-builds/uas/internal/client/state"
)

// ExportVersion is the format version of an export file.
const ExportVersion = 1

// Export is a list of installed apps and GNOME Shell extensions, saved to install the same ones on another
// machine (or this one, after a reinstall). It carries no file paths or user names.
type Export struct {
	Version    int       `json:"uas_export"`
	ExportedAt time.Time `json:"exported_at"`
	// System is the system it was made on, for people reading the file ("Bazzite 45 (GNOME)").
	System       string        `json:"system,omitempty"`
	FlatpakScope string        `json:"flatpak_scope,omitempty"`
	Apps         []ExportedApp `json:"apps"`
	// FlatpakOverrides are the user's Flatpak permission overrides, by app ID, as flatpak writes them.
	FlatpakOverrides map[string]string `json:"flatpak_overrides,omitempty"`
	// Extensions are the GNOME Shell extensions the user added or switched on (see PlanExtensions). Versions
	// of uas from before extensions ignore them.
	Extensions []ExportedExtension `json:"extensions,omitempty"`
}

// ExportedApp is one installed app.
type ExportedApp struct {
	Source  catalog.SourceID `json:"source"`
	Ref     string           `json:"ref"`
	AppID   string           `json:"app_id,omitempty"`
	Package string           `json:"package,omitempty"`
	Name    string           `json:"name"`
	Scope   string           `json:"scope,omitempty"`
	// FromURL is true for an app installed from a link to its repo, which the catalog didn't have; a restore
	// reads it from the repo again. Versions of uas from before Install from URL list it as not in the
	// catalog.
	FromURL bool `json:"from_url,omitempty"`
}

// Export lists the installed apps, the user's Flatpak permission changes, and in a GNOME session the
// extensions.
func (s *Session) Export(ctx context.Context) (Export, error) {
	apps, err := s.Installed(ctx)
	if err != nil {
		return Export{}, err
	}
	ex := Export{Version: ExportVersion, ExportedAt: time.Now().UTC().Truncate(time.Second),
		System: s.Platform.PrettyName, FlatpakScope: string(s.Settings.FlatpakScope)}
	for _, a := range apps {
		ex.Apps = append(ex.Apps, ExportedApp{Source: a.Source, Ref: a.Ref, AppID: a.AppID, Package: a.Package,
			Name: a.Name, Scope: a.Scope, FromURL: a.FromURL})
		if a.Source == catalog.SourceFlathub {
			if o := readOverride(a.AppID); o != "" {
				if ex.FlatpakOverrides == nil {
					ex.FlatpakOverrides = map[string]string{}
				}
				ex.FlatpakOverrides[a.AppID] = o
			}
		}
	}
	exts, err := s.exportExtensions(ctx)
	if err != nil {
		return Export{}, fmt.Errorf("extensions: %w", err)
	}
	ex.Extensions = exts
	return ex, nil
}

// WriteExport writes an export as indented JSON.
func WriteExport(w io.Writer, ex Export) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(ex)
}

// ReadExport reads an export file.
func ReadExport(r io.Reader) (Export, error) {
	var ex Export
	if err := json.NewDecoder(io.LimitReader(r, 16<<20)).Decode(&ex); err != nil {
		return Export{}, fmt.Errorf("not a uas export: %w", err)
	}
	if ex.Version == 0 {
		return Export{}, errors.New("not a uas export")
	}
	if ex.Version > ExportVersion {
		return Export{}, fmt.Errorf("the export is from a newer uas (format %d); update uas first", ex.Version)
	}
	return ex, nil
}

// RestoreAction says what restoring does with one app.
type RestoreAction string

const (
	RestoreInstall     RestoreAction = "install"     // install it
	RestoreInstalled   RestoreAction = "installed"   // it is installed already
	RestoreUnavailable RestoreAction = "unavailable" // no source this system supports has it
	RestoreManual      RestoreAction = "manual"      // uas can't get it again by itself (an AppImage file)
	RestoreSkip        RestoreAction = "skip"        // another app of the export installs the same app
)

// RestoreItem is one app of a restore plan.
type RestoreItem struct {
	App    ExportedApp
	Action RestoreAction
	// Listing is what to install, for RestoreInstall.
	Listing catalog.Listing
	// Note explains the action, such as "from Flathub instead of Fedora".
	Note  string
	appID string // the catalog app it resolved to
}

// PlanRestore decides, for each app of an export, whether to install it and from where: the same source
// when this system supports it, else the source uas would recommend for the app. Layered packages, which
// need a restart, come last.
func (s *Session) PlanRestore(ctx context.Context, ex Export) ([]RestoreItem, error) {
	installed, err := s.Installed(ctx)
	if err != nil {
		return nil, err
	}
	db, err := s.OpenCatalog(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer db.Close()
	var items, layered []RestoreItem
	planned := map[string]string{} // catalog app ID → the name it is installed under
	for _, e := range ex.Apps {
		it := s.planOne(ctx, db, installed, e)
		if it.Action == RestoreInstall {
			// The same app from two sources (Flathub and Fedora) is installed once.
			if name, ok := planned[it.appID]; ok {
				it.Action, it.Note, it.Listing = RestoreSkip, "the same app as "+name, catalog.Listing{}
			} else {
				planned[it.appID] = e.Name
			}
		}
		if it.Action == RestoreInstall && s.NeedsRestart(it.Listing.Source) {
			layered = append(layered, it)
			continue
		}
		items = append(items, it)
	}
	return append(items, layered...), nil
}

func (s *Session) planOne(ctx context.Context, db *catalog.DB, installed []backend.InstalledApp,
	e ExportedApp) RestoreItem {
	it := RestoreItem{App: e}
	for _, in := range installed {
		if in.Source == e.Source && (in.Ref == e.Ref || (e.Package != "" && in.Package == e.Package)) {
			it.Action = RestoreInstalled
			return it
		}
	}
	if e.Source == catalog.SourceAppImage {
		it.Action, it.Note = RestoreManual, "added from a file; add the AppImage again"
		return it
	}
	// The app, by the listing it was installed from, else by its ID or package name.
	var app *catalog.App
	for _, id := range []string{e.Ref, e.AppID, e.Package} {
		if id == "" {
			continue
		}
		if a, err := db.Resolve(ctx, id); err == nil {
			app = a
			break
		}
	}
	if app == nil && e.FromURL && e.Source.Releases() {
		return s.planFromURL(ctx, installed, e)
	}
	if app == nil {
		it.Action, it.Note = RestoreUnavailable, "not in the catalog"
		return it
	}
	it.appID = app.ID
	// Installed under another source already counts: the app is there.
	for _, in := range installed {
		for _, l := range app.Listings {
			if in.Matches(l) {
				it.Action, it.Note = RestoreInstalled, "installed from "+l.Source.Name()
				return it
			}
		}
	}
	for _, l := range app.Listings {
		if l.Source == e.Source && s.Available(l) &&
			(l.Ref == e.Ref || (e.Package != "" && l.Package == e.Package)) {
			it.Action, it.Listing = RestoreInstall, l
			return it
		}
	}
	// Same source, a different listing (another Fedora release), then the recommended one.
	choice, err := s.Choose(app, e.Source)
	if err != nil {
		choice, err = s.Choose(app, "")
	}
	if err != nil {
		it.Action, it.Note = RestoreUnavailable, "no source this system supports has it"
		return it
	}
	it.Action, it.Listing = RestoreInstall, choice.Listing
	if choice.Listing.Source != e.Source {
		it.Note = "from " + choice.Listing.Source.Name() + " instead of " + e.Source.Name()
	}
	return it
}

// RestoreOverrides puts back the Flatpak permission changes of an export, for apps that have none here
// yet. It returns the app IDs whose permissions it restored.
func RestoreOverrides(ex Export) ([]string, error) {
	var restored []string
	var errs []error
	for id, text := range ex.FlatpakOverrides {
		path, err := overridePath(id)
		if err != nil {
			errs = append(errs, err)
			continue
		}
		if _, err := os.Stat(path); err == nil {
			continue // the user has changes here already; keep them
		}
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			errs = append(errs, err)
			continue
		}
		if err := os.WriteFile(path, []byte(text), 0o644); err != nil {
			errs = append(errs, err)
			continue
		}
		restored = append(restored, id)
	}
	return restored, errors.Join(errs...)
}

// overridePath is where flatpak keeps the user's overrides of an app.
func overridePath(appID string) (string, error) {
	if !flatpakAppID.MatchString(appID) {
		return "", fmt.Errorf("invalid Flatpak app ID %q", appID)
	}
	data, err := state.DataDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(data, "flatpak", "overrides", appID), nil
}

// readOverride returns the user's Flatpak overrides of an app, or "".
func readOverride(appID string) string {
	path, err := overridePath(appID)
	if err != nil {
		return ""
	}
	b, err := os.ReadFile(path)
	if err != nil || len(b) > 64<<10 {
		return ""
	}
	return string(b)
}

// flatpakAppID matches Flatpak app IDs, which name override files.
var flatpakAppID = regexp.MustCompile(`^[A-Za-z0-9_-]+(\.[A-Za-z0-9_-]+)+$`)

// planFromURL plans the restore of an app installed from a link that the catalog doesn't have: from its
// repo's current release, read now.
func (s *Session) planFromURL(ctx context.Context, installed []backend.InstalledApp, e ExportedApp) RestoreItem {
	it := RestoreItem{App: e}
	l, err := s.liveListing(ctx, e.Source, e.Ref)
	if err != nil {
		it.Action, it.Note = RestoreUnavailable, "not in the catalog, and its repo can't be read: "+err.Error()
		return it
	}
	for _, in := range installed {
		if in.Matches(l) {
			it.Action = RestoreInstalled
			return it
		}
	}
	if why := s.Unavailable(l); why != "" {
		it.Action, it.Note = RestoreUnavailable, why
		return it
	}
	it.appID = catalog.LiveApp(l).ID
	it.Action, it.Listing = RestoreInstall, l
	it.Note = "from " + l.Source.Host() + "/" + l.Source.Repo(l.Ref) + ", which isn't in uas's catalog"
	return it
}
