// Package session is the uas client engine shared by the CLI and the desktop app: settings, the detected
// system, the backends it supports, and the catalog.
package session

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/tyler-builds/uas/internal/catalog"
	"github.com/tyler-builds/uas/internal/client/backend"
	"github.com/tyler-builds/uas/internal/client/backend/copr"
	"github.com/tyler-builds/uas/internal/client/backend/flatpak"
	"github.com/tyler-builds/uas/internal/client/backend/github"
	"github.com/tyler-builds/uas/internal/client/backend/homebrew"
	"github.com/tyler-builds/uas/internal/client/backend/rpmostree"
	"github.com/tyler-builds/uas/internal/client/platform"
	"github.com/tyler-builds/uas/internal/client/recommend"
	"github.com/tyler-builds/uas/internal/client/refresh"
	"github.com/tyler-builds/uas/internal/client/run"
	"github.com/tyler-builds/uas/internal/client/settings"
	"github.com/tyler-builds/uas/internal/client/state"
	"github.com/tyler-builds/uas/internal/client/telemetry"
)

// RepoDir is where enabled package repos are configured.
const RepoDir = "/etc/yum.repos.d"

// Session is a loaded client.
type Session struct {
	SettingsPath string
	Settings     settings.Settings
	Platform     platform.Info
	Backends     map[catalog.SourceID]backend.Backend
	Telemetry    telemetry.Sink
	CatalogPath  string
}

// Load reads settings and detects the system. scope, if set, overrides the Flatpak scope setting.
func Load(ctx context.Context, scope, catalogPath string) (*Session, error) {
	path, err := settings.Path()
	if err != nil {
		return nil, err
	}
	st, err := settings.Load(path)
	if err != nil {
		return nil, err
	}
	if scope != "" {
		st.FlatpakScope = settings.FlatpakScope(scope)
		if !st.FlatpakScope.Valid() {
			return nil, fmt.Errorf("unknown scope %q: use user or system", scope)
		}
	}
	s := &Session{
		SettingsPath: path,
		Settings:     st,
		Platform:     platform.Detect(platform.System()),
		Backends:     map[catalog.SourceID]backend.Backend{},
		Telemetry:    telemetry.ForSettings(st.Telemetry),
		CatalogPath:  catalogPath,
	}
	p := s.Platform
	if p.Flatpak != "" {
		s.Backends[catalog.SourceFlathub] = &flatpak.Backend{Runner: run.Exec{}, Path: p.Flatpak,
			Scope: st.FlatpakScope}
	}
	if p.Brew != "" {
		s.Backends[catalog.SourceHomebrew] = &homebrew.Backend{
			Runner: run.Exec{Env: []string{"HOMEBREW_NO_ENV_HINTS=1"}}, Path: p.Brew}
	}
	if gh, err := s.githubBackend(); err == nil {
		s.Backends[catalog.SourceGitHub] = gh
	}
	// Layering needs an image-based system. Package-based Fedora (dnf) is outside the Bazzite slice.
	if p.Atomic && p.RPMOSTree != "" && p.FedoraRelease() > 0 {
		ostree := rpmostree.New(ctx, run.Exec{}, p.RPMOSTree, "rpm", p.FedoraRelease())
		s.Backends[catalog.SourceFedora] = ostree
		statePath, err := state.DefaultPath()
		if err == nil && p.Pkexec != "" {
			store := state.Store{Path: statePath}
			s.Backends[catalog.SourceCOPR] = &copr.Backend{Ostree: ostree, Runner: run.Exec{},
				Pkexec: p.Pkexec, RepoDir: RepoDir, State: store}
			// Packages layered from COPR show up under COPR, not Fedora.
			ostree.Exclude = func(pkg string) bool {
				st, err := store.Load()
				if err != nil {
					return false
				}
				_, ok := st.FindCOPR(pkg)
				return ok
			}
		}
	}
	return s, nil
}

// SaveSettings writes the settings and applies the ones that take effect at once.
func (s *Session) SaveSettings(st settings.Settings) error {
	if err := settings.Save(s.SettingsPath, st); err != nil {
		return err
	}
	s.Settings = st
	if fp, ok := s.Backends[catalog.SourceFlathub].(*flatpak.Backend); ok {
		fp.Scope = st.FlatpakScope
	}
	s.Telemetry = telemetry.ForSettings(st.Telemetry)
	return nil
}

// Available reports whether this system can install l.
func (s *Session) Available(l catalog.Listing) bool {
	b := s.Backends[l.Source]
	return b != nil && b.Supports(l)
}

// NeedsRestart reports whether changes from a source take effect only after a restart.
func (s *Session) NeedsRestart(src catalog.SourceID) bool {
	r, ok := s.Backends[src].(backend.Restarter)
	return ok && r.NeedsRestart()
}

// Choose picks the listing of app to install, from want if it is set.
func (s *Session) Choose(app *catalog.App, want catalog.SourceID) (recommend.Choice, error) {
	return recommend.Choose(app, s.Available, want)
}

// Installed lists installed apps from every backend, sorted by name.
func (s *Session) Installed(ctx context.Context) ([]backend.InstalledApp, error) {
	var all []backend.InstalledApp
	for _, src := range catalog.SourcePrecedence {
		b := s.Backends[src]
		if b == nil {
			continue
		}
		apps, err := b.Installed(ctx)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", src, err)
		}
		all = append(all, apps...)
	}
	sort.SliceStable(all, func(i, j int) bool { return strings.ToLower(all[i].Name) < strings.ToLower(all[j].Name) })
	return all, nil
}

// InstalledCopy returns the installed copy of l, if there is one.
func (s *Session) InstalledCopy(ctx context.Context, l catalog.Listing) (backend.InstalledApp, bool, error) {
	apps, err := s.Installed(ctx)
	if err != nil {
		return backend.InstalledApp{}, false, err
	}
	for _, a := range apps {
		if a.Matches(l) {
			return a, true, nil
		}
	}
	return backend.InstalledApp{}, false, nil
}

// FindInstalled returns the installed apps that an ID (an app ID, package name or ref) names, optionally only
// those in scope.
func (s *Session) FindInstalled(ctx context.Context, id, scope string) ([]backend.InstalledApp, error) {
	installed, err := s.Installed(ctx)
	if err != nil {
		return nil, err
	}
	names := s.names(ctx, id)
	var matches []backend.InstalledApp
	for _, a := range installed {
		if (names[a.AppID] || names[a.Ref] || (a.Package != "" && names[a.Package])) &&
			(scope == "" || a.Scope == scope) {
			matches = append(matches, a)
		}
	}
	return matches, nil
}

// names returns every name an installed copy of the app may go by: the ID given, plus the refs and package
// names of its catalog listings. The catalog is optional here; without it only the ID is used.
func (s *Session) names(ctx context.Context, id string) map[string]bool {
	names := map[string]bool{id: true}
	db, err := catalog.Open(s.CatalogPath)
	if err != nil {
		return names
	}
	defer db.Close()
	app, err := db.Resolve(ctx, id)
	if err != nil {
		return names
	}
	names[app.ID] = true
	for _, l := range app.Listings {
		names[l.Ref] = true
		if l.Package != "" {
			names[l.Package] = true
		}
	}
	return names
}

// Updates returns the available updates of every source that can check, by InstalledApp.Key. A source whose
// check fails is left out, and its error is returned alongside the others' results.
func (s *Session) Updates(ctx context.Context) (map[string]string, error) {
	type result struct {
		src catalog.SourceID
		ups []backend.AvailableUpdate
		err error
	}
	var checks []catalog.SourceID
	for _, src := range catalog.SourcePrecedence {
		if _, ok := s.Backends[src].(backend.UpdateChecker); ok {
			checks = append(checks, src)
		}
	}
	results := make(chan result, len(checks))
	for _, src := range checks {
		go func() {
			ups, err := s.Backends[src].(backend.UpdateChecker).Updates(ctx)
			results <- result{src, ups, err}
		}()
	}
	updates := map[string]string{}
	var errs []error
	for range checks {
		r := <-results
		if r.err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", r.src.Name(), r.err))
			continue
		}
		for _, u := range r.ups {
			updates[backend.UpdateKey(r.src, u.Scope, u.Ref)] = u.Version
		}
	}
	return updates, errors.Join(errs...)
}

// Install installs l.
func (s *Session) Install(ctx context.Context, l catalog.Listing, progress func(backend.Progress)) error {
	b := s.Backends[l.Source]
	if b == nil {
		return fmt.Errorf("%s is not supported on this system", l.Source)
	}
	err := b.Install(ctx, l, progress)
	s.emit("install", l.Source, err)
	return err
}

// Remove removes an installed app.
func (s *Session) Remove(ctx context.Context, a backend.InstalledApp, progress func(backend.Progress)) error {
	b := s.Backends[a.Source]
	if b == nil {
		return fmt.Errorf("%s is not supported on this system", a.Source)
	}
	err := b.Remove(ctx, a, progress)
	s.emit("remove", a.Source, err)
	return err
}

// Update updates an installed app.
func (s *Session) Update(ctx context.Context, a backend.InstalledApp, progress func(backend.Progress)) error {
	b := s.Backends[a.Source]
	if b == nil {
		return fmt.Errorf("%s is not supported on this system", a.Source)
	}
	err := b.Update(ctx, a, progress)
	s.emit("update", a.Source, err)
	return err
}

func (s *Session) emit(name string, src catalog.SourceID, err error) {
	result := "ok"
	if err != nil {
		result = "error"
	}
	s.Telemetry.Emit(telemetry.Event{Name: name, Fields: map[string]string{"source": string(src), "result": result}})
}

// InstallNotes are short notes to show before installing l: where it goes, whether it needs a password or
// a restart. Trust details are shown separately.
func (s *Session) InstallNotes(l catalog.Listing) []string {
	var notes []string
	switch l.Source {
	case catalog.SourceFlathub:
		if s.Settings.FlatpakScope == settings.ScopeUser {
			notes = append(notes, "Installs for your user account only.")
		} else {
			notes = append(notes, "Installs for all users. Flatpak will ask for your password.")
		}
	case catalog.SourceHomebrew:
		notes = append(notes, "Installs with Homebrew for your user account. No restart needed.")
	case catalog.SourceGitHub:
		a, _ := l.Asset(catalog.AssetAppImage)
		notes = append(notes, fmt.Sprintf("Downloads %s (%s) from github.com/%s. Not reviewed by any distribution.",
			a.Name, HumanSize(a.Size), l.Ref))
		if !s.Platform.LibFUSE2 {
			notes = append(notes, "FUSE 2 was not found; older AppImages may not start.")
		}
	case catalog.SourceCOPR:
		notes = append(notes, fmt.Sprintf("From the COPR project %s, built by its owner and not reviewed by Fedora.",
			l.Project))
		if name, err := copr.RepoFileName(l.Project); err == nil {
			if _, err := os.Stat(filepath.Join(RepoDir, name)); err != nil {
				notes = append(notes, "Enabling the project's repo needs your password.")
			}
		}
	}
	if l.Source == catalog.SourceFedora || l.Source == catalog.SourceCOPR {
		notes = append(notes, "Adds the package to your system image (layering). It takes effect after a restart.",
			"Layered packages can hold up system updates if they conflict with a new image.")
	}
	return notes
}

// OpenCatalog opens the local catalog, downloading it first if there is none yet. notify, if set, is told
// when a download starts.
func (s *Session) OpenCatalog(ctx context.Context, notify func(string)) (*catalog.DB, error) {
	if _, err := os.Stat(s.CatalogPath); errors.Is(err, os.ErrNotExist) {
		if notify != nil {
			notify("Downloading the app catalog...")
		}
		if _, err := refresh.Refresh(ctx, RefreshOptions(s.CatalogPath)); err != nil {
			return nil, fmt.Errorf("download catalog: %w", err)
		}
	}
	db, err := catalog.Open(s.CatalogPath)
	if errors.Is(err, catalog.ErrSchema) {
		// A catalog from an older (or newer) build of uas: fetch the one published for this format.
		if notify != nil {
			notify("Updating the app catalog to a new format...")
		}
		opt := RefreshOptions(s.CatalogPath)
		os.Remove(refresh.ManifestPath(s.CatalogPath)) // so the refresh doesn't skip the download
		if _, rerr := refresh.Refresh(ctx, opt); rerr != nil {
			return nil, fmt.Errorf("download catalog: %w", rerr)
		}
		return catalog.Open(s.CatalogPath)
	}
	return db, err
}

// RefreshOptions returns the options for downloading the published catalog to catalogPath.
func RefreshOptions(catalogPath string) refresh.Options {
	base := os.Getenv(refresh.BaseURLEnv)
	if base == "" {
		base = refresh.DefaultBaseURL
	}
	return refresh.Options{
		BaseURL:     base,
		TrustedKeys: refresh.TrustedKeys(),
		CatalogPath: catalogPath,
		Client:      &http.Client{Timeout: 5 * time.Minute},
		UserAgent:   "uas",
	}
}

// githubBackend sets up AppImage installs, looking up updates in the session's catalog.
func (s *Session) githubBackend() (*github.Backend, error) {
	dirs, err := github.DefaultDirs()
	if err != nil {
		return nil, err
	}
	statePath, err := state.DefaultPath()
	if err != nil {
		return nil, err
	}
	return &github.Backend{
		State:  state.Store{Path: statePath},
		Dirs:   dirs,
		Client: &http.Client{Timeout: 30 * time.Minute},
		Runner: run.Exec{},
		Latest: func(ctx context.Context, repo string) (catalog.Listing, error) {
			db, err := catalog.Open(s.CatalogPath)
			if err != nil {
				return catalog.Listing{}, err
			}
			defer db.Close()
			app, err := db.Resolve(ctx, repo)
			if err != nil {
				return catalog.Listing{}, err
			}
			for _, l := range app.Listings {
				if l.Source == catalog.SourceGitHub && l.Ref == repo {
					return l, nil
				}
			}
			return catalog.Listing{}, fmt.Errorf("%s is no longer in the catalog", repo)
		},
	}, nil
}

// HumanSize formats a byte count.
func HumanSize(n int64) string {
	switch {
	case n >= 1<<30:
		return fmt.Sprintf("%.1f GB", float64(n)/(1<<30))
	case n >= 1<<20:
		return fmt.Sprintf("%.1f MB", float64(n)/(1<<20))
	case n >= 1<<10:
		return fmt.Sprintf("%.0f KB", float64(n)/(1<<10))
	}
	return fmt.Sprintf("%d bytes", n)
}
