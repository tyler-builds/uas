package session

import (
	"context"

	"github.com/tyler-builds/uas/internal/catalog"
	"github.com/tyler-builds/uas/internal/client/backend"
)

// AppIDs are the IDs an app goes by in the catalog, for matching an AppImage added from a file to it (see
// backend.InstalledApp.SameApp): its ID, its listings' AppStream IDs, and the repos of its release listings.
func AppIDs(app *catalog.App) []string {
	ids := []string{app.ID}
	for _, l := range app.Listings {
		if l.AppStreamID != "" {
			ids = append(ids, l.AppStreamID)
		}
		if l.Source.Releases() {
			ids = append(ids, l.Ref)
		}
	}
	return ids
}

// AddedCopy returns the AppImage among installed that the user added from a file and that is app: CurseForge
// downloaded from its site and added, say, when the catalog has CurseForge from Flathub. No listing of the
// app describes that copy, so InstalledApp.Matches doesn't find it.
func AddedCopy(installed []backend.InstalledApp, app *catalog.App) (backend.InstalledApp, bool) {
	ids := AppIDs(app)
	for _, in := range installed {
		if in.SameApp(app.Name, ids...) {
			return in, true
		}
	}
	return backend.InstalledApp{}, false
}

// CatalogApp finds the catalog's app for an installed one, or nil when the catalog doesn't have it: by its
// ID, package name or ref, and for an AppImage added from a file also by its name (see AddedCopy).
func CatalogApp(ctx context.Context, db *catalog.DB, in backend.InstalledApp) (*catalog.App, error) {
	if app, err := db.Resolve(ctx, in.AppID); err == nil {
		return app, nil
	}
	if in.Source != catalog.SourceAppImage {
		return nil, nil
	}
	ids, err := db.Named(ctx, in.Name)
	if err != nil {
		return nil, err
	}
	for _, id := range ids {
		app, err := db.App(ctx, id)
		if err != nil {
			continue
		}
		if in.SameApp(app.Name, AppIDs(app)...) {
			return app, nil
		}
	}
	return nil, nil
}
