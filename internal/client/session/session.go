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
	"sync"
	"time"

	"github.com/tyler-builds/uas/internal/catalog"
	"github.com/tyler-builds/uas/internal/client/backend"
	"github.com/tyler-builds/uas/internal/client/backend/copr"
	"github.com/tyler-builds/uas/internal/client/backend/flatpak"
	"github.com/tyler-builds/uas/internal/client/backend/github"
	"github.com/tyler-builds/uas/internal/client/backend/homebrew"
	"github.com/tyler-builds/uas/internal/client/backend/pkgmgr"
	"github.com/tyler-builds/uas/internal/client/backend/rpmostree"
	snapbackend "github.com/tyler-builds/uas/internal/client/backend/snap"
	"github.com/tyler-builds/uas/internal/client/platform"
	"github.com/tyler-builds/uas/internal/client/recommend"
	"github.com/tyler-builds/uas/internal/client/refresh"
	"github.com/tyler-builds/uas/internal/client/run"
	"github.com/tyler-builds/uas/internal/client/selfupdate"
	"github.com/tyler-builds/uas/internal/client/settings"
	"github.com/tyler-builds/uas/internal/client/state"
	"github.com/tyler-builds/uas/internal/client/sysapps"
	"github.com/tyler-builds/uas/internal/client/telemetry"
	"github.com/tyler-builds/uas/internal/client/ujust"
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
	// Tools are the system's ujust recipes, on Universal Blue images; nil elsewhere.
	Tools *ujust.Tools
	// SysApps are the apps that came with the system, by AppStream ID (see SystemApps). Nil reads them on
	// first use; tests set it.
	SysApps map[string]sysapps.App
	sysOnce sync.Once
}

// SystemApps returns the apps installed with the system rather than by a uas backend, such as Steam on
// Bazzite's image, by AppStream ID. It reads them once.
func (s *Session) SystemApps() map[string]sysapps.App {
	s.sysOnce.Do(func() {
		if s.SysApps == nil {
			s.SysApps = sysapps.Scan(sysapps.MetainfoDirs, sysapps.AppDirs)
		}
	})
	return s.SysApps
}

// SystemApp returns the app's copy that came with the system, matched by the AppStream IDs of the app and its
// listings.
func (s *Session) SystemApp(app *catalog.App) (sysapps.App, bool) {
	sys := s.SystemApps()
	ids := []string{app.ID}
	for _, l := range app.Listings {
		ids = append(ids, l.AppStreamID)
	}
	for _, id := range ids {
		if a, ok := sys[strings.TrimSuffix(id, ".desktop")]; ok && id != "" {
			return a, true
		}
	}
	return sysapps.App{}, false
}

// SystemAppLabel says where a system app came from: "Included with Bazzite" on an image-based system, where
// it is part of the image, and "Installed on this system" elsewhere.
func (s *Session) SystemAppLabel() string {
	if s.Platform.Atomic && s.Platform.PrettyName != "" {
		return "Included with " + s.Platform.PrettyName
	}
	return "Installed on this system"
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
		Tools:        ujust.Detect(),
	}
	p := s.Platform
	gh, ghErr := s.releaseBackend(catalog.SourceGitHub)
	if ghErr == nil {
		s.Backends[catalog.SourceGitHub] = gh
		s.Backends[catalog.SourceAppImage] = &github.Local{GH: gh}
		if b, err := s.releaseBackend(catalog.SourceCodeberg); err == nil {
			s.Backends[catalog.SourceCodeberg] = b
		}
		// Launchers written by an older uas get what it has learned since; quick when there's nothing to do.
		_ = gh.RepairLaunchers(ctx)
	}
	if p.Flatpak != "" {
		fp := &flatpak.Backend{Runner: run.Exec{}, Path: p.Flatpak, Scope: st.FlatpakScope}
		if ghErr == nil {
			fp.Exclude = gh.BundleApps // apps from GitHub bundles show up under GitHub, not Flathub
		}
		s.Backends[catalog.SourceFlathub] = fp
	}
	if p.Brew != "" {
		s.Backends[catalog.SourceHomebrew] = &homebrew.Backend{
			Runner: run.Exec{Env: []string{"HOMEBREW_NO_ENV_HINTS=1"}}, Path: p.Brew}
	}
	s.addPackageManager()
	if p.Snap != "" {
		s.Backends[catalog.SourceSnap] = &snapbackend.Backend{Runner: run.Exec{}, Path: p.Snap,
			Known: s.knownPackages(catalog.SourceSnap, "")}
	}
	// Layering needs an image-based system. Package-based Fedora uses dnf (addPackageManager).
	if p.Atomic && p.RPMOSTree != "" && p.FedoraRelease() > 0 {
		ostree := rpmostree.New(ctx, run.Exec{}, p.RPMOSTree, "rpm", p.FedoraRelease())
		s.Backends[catalog.SourceFedora] = ostree
		// Ask rpm-ostree for the target release now, off the startup path, so it's known by first use.
		go ostree.Release()
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

// System is what recommendations need to know about this system.
func (s *Session) System() recommend.System {
	return recommend.System{Available: s.Available, Restart: s.NeedsRestart}
}

// NeedsRestart reports whether changes from a source take effect only after a restart.
func (s *Session) NeedsRestart(src catalog.SourceID) bool {
	r, ok := s.Backends[src].(backend.Restarter)
	return ok && r.NeedsRestart()
}

// Choose picks the listing of app to install, from want if it is set.
func (s *Session) Choose(app *catalog.App, want catalog.SourceID) (recommend.Choice, error) {
	return recommend.Choose(app, s.System(), want)
}

// sources are the catalog's sources, then AppImages added from files.
var sources = append(append([]catalog.SourceID{}, catalog.SourcePrecedence...), catalog.SourceAppImage)

// Installed lists installed apps from every backend, sorted by name.
func (s *Session) Installed(ctx context.Context) ([]backend.InstalledApp, error) {
	var all []backend.InstalledApp
	for _, src := range sources {
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
	for _, src := range sources {
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
	var db *catalog.DB
	for range checks {
		r := <-results
		if r.err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", r.src.Name(), r.err))
			continue
		}
		for _, u := range r.ups {
			v := u.Version
			if v == "" {
				// A source that can't tell the new version (Flatpak, until it refreshes its AppStream data):
				// the catalog's version, which is at most a few hours old.
				if db == nil {
					if db, _ = catalog.Open(s.CatalogPath); db != nil {
						defer db.Close()
					}
				}
				if db != nil {
					v, _ = db.ListingVersion(ctx, r.src, u.Ref)
				}
			}
			updates[backend.UpdateKey(r.src, u.Scope, u.Ref)] = v
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

// AddAppImage adds an AppImage file, moving it into uas's AppImage folder with a launcher. When the catalog
// has the app from GitHub (found from where the AppImage says its updates come from, or from its ID), it is
// recorded as that GitHub install and updates from the catalog; otherwise it updates from where the AppImage
// says, or not at all.
func (s *Session) AddAppImage(ctx context.Context, path string, progress func(backend.Progress)) (
	backend.InstalledApp, error) {
	gh, ok := s.Backends[catalog.SourceGitHub].(*github.Backend)
	local, lok := s.Backends[catalog.SourceAppImage].(*github.Local)
	if !ok || !lok {
		return backend.InstalledApp{}, errors.New("AppImages are not supported on this system")
	}
	f, err := github.ReadFile(path)
	if err != nil {
		return backend.InstalledApp{}, err
	}
	var app backend.InstalledApp
	if l, ok := s.githubListing(ctx, gh, f); ok {
		err = gh.Adopt(ctx, f, l, progress)
		app = backend.InstalledApp{Source: catalog.SourceGitHub, Ref: l.Ref, AppID: l.Ref, Name: l.Name,
			Scope: "user", Origin: "github"}
	} else {
		app, err = local.Add(ctx, f, progress)
	}
	s.emit("add-appimage", app.Source, err)
	return app, err
}

// githubListing finds the catalog's GitHub AppImage listing of an AppImage file: of the repo its update
// information names, or of the app its ID names when it names no place.
func (s *Session) githubListing(ctx context.Context, gh *github.Backend, f *github.File) (catalog.Listing, bool) {
	db, err := catalog.Open(s.CatalogPath)
	if err != nil {
		return catalog.Listing{}, false
	}
	defer db.Close()
	// An AppImage that updates from somewhere other than GitHub keeps doing so; one that names no place is
	// looked up by its ID.
	repo := f.Update.GitHubRepo()
	id := repo
	if f.UpdateErr != nil {
		id = f.Info.AppID()
	}
	if id == "" {
		return catalog.Listing{}, false
	}
	app, err := db.Resolve(ctx, id)
	if err != nil {
		return catalog.Listing{}, false
	}
	for _, l := range app.Listings {
		if l.Source != catalog.SourceGitHub || (repo != "" && !strings.EqualFold(l.Ref, repo)) {
			continue
		}
		if a, ok := gh.Pick(l); ok && a.Kind == catalog.AssetAppImage {
			return l, true
		}
	}
	return catalog.Listing{}, false
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
	case catalog.SourceGitHub, catalog.SourceCodeberg:
		var a catalog.Asset
		if gh, ok := s.Backends[l.Source].(*github.Backend); ok {
			a, _ = gh.Pick(l)
		}
		notes = append(notes, fmt.Sprintf("Downloads %s (%s) from %s/%s. Not reviewed by any distribution.",
			a.Name, HumanSize(a.Size), l.Source.Host(), l.Source.Repo(l.Ref)))
		if l.Source != catalog.SourceGitHub {
			notes = append(notes, l.Source.Name()+" publishes no checksum; uas checks the file against the one "+
				"recorded when the catalog was built.")
		}
		switch {
		case a.Kind == catalog.AssetFlatpak:
			notes = append(notes, "A Flatpak bundle: it installs for your user account and runs in a sandbox. "+
				"Flatpak fetches the runtime it needs from Flathub.")
		case !s.Platform.LibFUSE2:
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
	case catalog.SourceSnap:
		notes = append(notes, "Installs with snap for all users. snapd may ask for your password.")
		if l.Classic {
			notes = append(notes, "Classic confinement: this snap runs without the snap sandbox.")
		}
	}
	switch {
	case s.NeedsRestart(l.Source):
		notes = append(notes, "Adds the package to your system image (layering). It takes effect after a restart.",
			"Layered packages can hold up system updates if they conflict with a new image.")
	case l.Source.Distribution():
		if pm, ok := s.Backends[l.Source].(*pkgmgr.Backend); ok {
			notes = append(notes, fmt.Sprintf("Installs with %s for all users. Asks for your password.", pm.Kind))
		}
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
		Edition:     platform.Detect(platform.System()).Edition(),
	}
}

// releaseBackend sets up installs from a forge's releases (GitHub, Codeberg), looking up updates in
// the session's catalog.
func (s *Session) releaseBackend(src catalog.SourceID) (*github.Backend, error) {
	dirs, err := github.DefaultDirs()
	if err != nil {
		return nil, err
	}
	statePath, err := state.DefaultPath()
	if err != nil {
		return nil, err
	}
	return &github.Backend{
		State:   state.Store{Path: statePath},
		Dirs:    dirs,
		Client:  &http.Client{Timeout: 30 * time.Minute},
		Runner:  run.Exec{},
		Flatpak: s.Platform.Flatpak,
		Forge:   src,
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
				if l.Source == src && l.Ref == repo {
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

// permissionEditor returns the Flatpak backend, which also manages the permissions of apps installed from
// Flatpak bundles.
func (s *Session) permissionEditor() (backend.PermissionEditor, error) {
	pe, ok := s.Backends[catalog.SourceFlathub].(backend.PermissionEditor)
	if !ok {
		return nil, errors.New("flatpak is not installed on this system")
	}
	return pe, nil
}

// Permissions returns what an installed Flatpak app may reach outside its sandbox.
func (s *Session) Permissions(ctx context.Context, appID string) ([]backend.Permission, error) {
	pe, err := s.permissionEditor()
	if err != nil {
		return nil, err
	}
	return pe.Permissions(ctx, appID)
}

// SetPermission allows or denies one permission of a Flatpak app, for this user.
func (s *Session) SetPermission(ctx context.Context, appID, key string, allow bool) error {
	pe, err := s.permissionEditor()
	if err != nil {
		return err
	}
	return pe.SetPermission(ctx, appID, key, allow)
}

// ResetPermissions undoes this user's permission changes for a Flatpak app.
func (s *Session) ResetPermissions(ctx context.Context, appID string) error {
	pe, err := s.permissionEditor()
	if err != nil {
		return err
	}
	return pe.ResetPermissions(ctx, appID)
}

// SelfUpdater returns the updater for uas itself, which reads releases from where the catalog comes from.
func (s *Session) SelfUpdater() *selfupdate.Updater {
	opt := RefreshOptions(s.CatalogPath)
	return &selfupdate.Updater{BaseURL: opt.BaseURL, TrustedKeys: opt.TrustedKeys,
		Client: &http.Client{Timeout: 10 * time.Minute}, Arch: s.Platform.Arch}
}

// addPackageManager sets up installs of the distribution's own packages on a package-based system: apt on
// Debian and Ubuntu (and their derivatives), dnf on Fedora, pacman on Arch. They need root, through pkexec.
func (s *Session) addPackageManager() {
	p := s.Platform
	distro := p.Distro()
	if distro == "" {
		return
	}
	var src catalog.SourceID
	var kind pkgmgr.Kind
	switch {
	case strings.HasPrefix(distro, "ubuntu-") && p.AptGet != "":
		src, kind = catalog.SourceUbuntu, pkgmgr.Apt
	case strings.HasPrefix(distro, "debian-") && p.AptGet != "":
		src, kind = catalog.SourceDebian, pkgmgr.Apt
	case distro == "arch" && p.Pacman != "":
		src, kind = catalog.SourceArch, pkgmgr.Pacman
	case strings.HasPrefix(distro, "fedora-") && p.DNF != "":
		src, kind = catalog.SourceFedora, pkgmgr.DNF
	default:
		return
	}
	var root []string
	if os.Geteuid() != 0 {
		if p.Pkexec == "" {
			return // nothing to ask for the password with
		}
		root = []string{p.Pkexec}
	}
	s.Backends[src] = &pkgmgr.Backend{Kind: kind, From: src, Distro: distro, Runner: run.Exec{}, Root: root,
		Known: s.knownPackages(src, distro)}
}

// knownPackages returns the packages the catalog lists for a source and release, read once per catalog.
func (s *Session) knownPackages(src catalog.SourceID, distro string) func(context.Context) (map[string]bool, error) {
	var mu sync.Mutex
	var cached map[string]bool
	var stamp time.Time
	return func(ctx context.Context) (map[string]bool, error) {
		mu.Lock()
		defer mu.Unlock()
		fi, err := os.Stat(s.CatalogPath)
		if err != nil {
			return map[string]bool{}, nil // no catalog yet: nothing is known to be an app
		}
		if cached != nil && fi.ModTime().Equal(stamp) {
			return cached, nil
		}
		db, err := catalog.Open(s.CatalogPath)
		if err != nil {
			return nil, err
		}
		defer db.Close()
		pkgs, err := db.Packages(ctx, src, distro)
		if err != nil {
			return nil, err
		}
		cached, stamp = pkgs, fi.ModTime()
		return pkgs, nil
	}
}
