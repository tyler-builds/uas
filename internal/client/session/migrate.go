package session

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"

	"github.com/tyler-builds/uas/internal/catalog"
	"github.com/tyler-builds/uas/internal/client/backend"
	"github.com/tyler-builds/uas/internal/client/backend/github"
	"github.com/tyler-builds/uas/internal/client/gearlever"
)

// Migration is an AppImage on this system that uas doesn't manage yet and can take over: one another tool
// (Gear Lever, AppImageLauncher) set up, or a file the user keeps in a folder.
//
// Only AppImages can be migrated. Flatpaks, Homebrew formulae and packages are already handled through
// their own tools, whoever installed them, and an app installed by hand (an unpacked archive, a vendor's
// script) has no uninstaller uas could trust to remove it.
type Migration struct {
	Path string
	Name string
	// Version is the app's version, when the AppImage says.
	Version string
	// From says who set it up: "Gear Lever", "AppImageLauncher", "a launcher" for one with a launcher from
	// something else, or "" for a file with none.
	From string
	// Folder is the folder it is in, with the home folder written "~".
	Folder string
	// Launchers are the launchers other tools wrote for it, which uas replaces with its own.
	Launchers []string
	// Updates is where it will update from once uas manages it, such as "github.com/owner/repo" or a host
	// name; "" when it doesn't say, and then it won't update. Catalog is true when that is the catalog's
	// listing of the app, which uas then keeps current like any app it installed.
	Updates string
	Catalog bool
	// Blocked says why it can't be migrated: uas can't read it, or it already manages this app.
	Blocked string
	// Suggested is whether to offer it selected: another tool set it up, or it is in a folder for AppImages.
	// A file that merely sits in Downloads or on the desktop is listed for the user to pick.
	Suggested bool

	// entry is Gear Lever's record of it, to remove once uas has it.
	entry *gearlever.Entry
}

// MigrationScan is what a scan for apps to migrate found.
type MigrationScan struct {
	Apps []Migration
	// Leftovers are AppImages Gear Lever still has on record, with their icons, though the files are gone:
	// what stays behind when an AppImage is moved out of its folder by something else.
	Leftovers []gearlever.Entry
}

// home is the user's home folder.
func (s *Session) home() (string, error) {
	if s.Home != "" {
		return s.Home, nil
	}
	return os.UserHomeDir()
}

// ScanMigrations looks for AppImages uas could take over. It reads each one without running it, and looks
// only where AppImages are kept: the files other tools' launchers run, Gear Lever's folder, and a few
// well-known folders in the home folder, on their top level.
func (s *Session) ScanMigrations(ctx context.Context) (MigrationScan, error) {
	gh, ok := s.Backends[catalog.SourceGitHub].(*github.Backend)
	if !ok {
		return MigrationScan{}, errors.New("AppImages are not supported on this system")
	}
	home, err := s.home()
	if err != nil {
		return MigrationScan{}, err
	}
	installed, err := s.Installed(ctx)
	if err != nil {
		return MigrationScan{}, err
	}

	var scan MigrationScan
	entries := map[string]*gearlever.Entry{}
	kept := []string{filepath.Join(home, "AppImages"), filepath.Join(home, "Applications")} // folders for AppImages
	for _, e := range gearlever.Entries(home) {
		if e.Gone() {
			scan.Leftovers = append(scan.Leftovers, e)
			continue
		}
		entries[samePathKey(e.Path)] = &e
		kept = append(kept, filepath.Dir(e.Path))
	}
	folders := append(append([]string{}, kept...), filepath.Join(home, "Apps"), filepath.Join(home, ".local", "bin"),
		filepath.Join(home, "bin"), filepath.Join(home, "Desktop"), filepath.Join(home, "Downloads"))

	for _, st := range gh.Strays(folders) {
		m := Migration{Path: st.Path, Name: st.LauncherName, Launchers: st.Launchers,
			Folder: tilde(filepath.Dir(st.Path), home), entry: entries[samePathKey(st.Path)]}
		switch {
		case m.entry != nil:
			m.From = gearlever.Name
		case st.AppImageLauncher:
			m.From = "AppImageLauncher"
		case len(st.Launchers) > 0:
			m.From = "a launcher"
		}
		m.Suggested = m.From != ""
		for _, k := range kept {
			if samePathKey(filepath.Dir(st.Path)) == samePathKey(k) {
				m.Suggested = true
			}
		}
		f, err := github.ReadFile(st.Path)
		if err != nil {
			if m.Name == "" {
				m.Name = filepath.Base(st.Path)
			}
			m.Blocked, m.Suggested = "uas can't read it: "+err.Error(), false
			scan.Apps = append(scan.Apps, m)
			continue
		}
		if n := f.Name(); n != "" {
			m.Name = n
		}
		m.Version = f.Version()
		// What adding it does: the catalog's GitHub listing of the app when there is one, else the file's own
		// update information (see AddAppImage).
		ref, id := "", f.ID()
		if l, ok := s.githubListing(ctx, gh, f); ok {
			ref, m.Updates, m.Catalog = l.Ref, "github.com/"+l.Ref, true
		} else {
			m.Updates = f.UpdatesFrom()
		}
		// Adding a file replaces the copy uas has of the same app, which may be newer than this one.
		for _, in := range installed {
			if (ref != "" && in.Source == catalog.SourceGitHub && in.Ref == ref) ||
				(ref == "" && in.Source == catalog.SourceAppImage && in.AppID == id) {
				m.Blocked, m.Suggested = "uas already manages "+strings.TrimSpace(in.Name+" "+in.Version), false
			}
		}
		scan.Apps = append(scan.Apps, m)
	}
	return scan, nil
}

// samePathKey is a path spelled one way, for comparing: cleaned, with /var/home as /home.
func samePathKey(p string) string {
	p = filepath.Clean(p)
	if strings.HasPrefix(p, "/var/home/") {
		p = p[len("/var"):]
	}
	return p
}

// tilde writes a path under the home folder with "~".
func tilde(p, home string) string {
	p, home = samePathKey(p), samePathKey(home)
	if p == home {
		return "~"
	}
	if rel, err := filepath.Rel(home, p); err == nil && !strings.HasPrefix(rel, "..") {
		return "~/" + rel
	}
	return p
}

// Migrate hands an AppImage over to uas: the file moves into uas's folder, uas writes its launcher in place
// of the other tool's (keeping its place in the GNOME dock), and it updates from where the scan said. Then
// what the other tool kept about it is removed: Gear Lever's record and icon. The file is only deleted from
// where it was once uas has its copy in place.
func (s *Session) Migrate(ctx context.Context, m Migration, progress func(backend.Progress)) (backend.InstalledApp, error) {
	if m.Blocked != "" {
		return backend.InstalledApp{}, errors.New(m.Blocked)
	}
	app, err := s.AddAppImage(ctx, m.Path, progress)
	if err != nil {
		return app, err
	}
	if m.entry != nil {
		if err := gearlever.Forget(*m.entry); err != nil {
			// The app is migrated; only Gear Lever's note of it is left.
			return app, errors.New("moved to uas, but Gear Lever's record of it could not be removed: " + err.Error())
		}
	}
	return app, nil
}

// ClearLeftovers removes Gear Lever's records and icons of AppImages that are gone. Where uas now manages
// the app a record was of, what an earlier move left behind is put right first: default-app settings that
// still name Gear Lever's launcher for it (which is gone too) are pointed at uas's, so a browser that was the
// default stays the default, and an app uas can't read gets the launch arguments Gear Lever recorded.
func (s *Session) ClearLeftovers(ctx context.Context, leftovers []gearlever.Entry) error {
	var gone []gearlever.Entry
	for _, e := range leftovers {
		if e.Gone() { // checked again: only a record of a file that isn't there is removed
			gone = append(gone, e)
		}
	}
	if gh, ok := s.Backends[catalog.SourceGitHub].(*github.Backend); ok && len(gone) > 0 {
		installed, _ := s.Installed(ctx)
		for _, e := range gone {
			for _, in := range installed {
				mine := in.Source == catalog.SourceAppImage || in.Source.Releases()
				if mine && in.Desktop != "" && e.Name != "" && strings.EqualFold(in.Name, e.Name) {
					gh.RepointDefaultApps(gearlever.LauncherOf(e.Path), in.Desktop)
				}
			}
			// An app whose own launcher uas can't read lost its launch arguments when uas took it over before
			// it kept them; Gear Lever's record still has them.
			if e.Name != "" {
				gh.RestoreLaunchArgs(ctx, e.Name, e.Args)
			}
		}
	}
	return gearlever.Forget(gone...)
}
