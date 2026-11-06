// Package flatpak installs Flathub apps with the flatpak command.
package flatpak

import (
	"context"
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"github.com/tyler-builds/uas/internal/catalog"
	"github.com/tyler-builds/uas/internal/client/backend"
	"github.com/tyler-builds/uas/internal/client/run"
	"github.com/tyler-builds/uas/internal/client/settings"
)

// Flathub's remote name and the file that describes it, including its signing key.
const (
	FlathubRemote   = "flathub"
	FlathubRepoFile = "https://dl.flathub.org/repo/flathub.flatpakrepo"
)

// refPattern matches the app refs uas passes to flatpak. Checking refs before use means a bad catalog entry
// can never smuggle an option such as "--from" onto the command line.
var refPattern = regexp.MustCompile(`^app/[A-Za-z0-9][A-Za-z0-9_.-]*/(x86_64|aarch64)/[A-Za-z0-9][A-Za-z0-9_.-]*$`)

// installationPattern matches the names of custom Flatpak installations.
var installationPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]*$`)

// Backend drives the flatpak command.
type Backend struct {
	Runner run.Runner
	Path   string // the flatpak binary
	// Scope is where new apps are installed. Updates and removals use the scope the app is in.
	Scope settings.FlatpakScope
	// Exclude, if set, returns the IDs of apps another backend accounts for (bundles from GitHub releases),
	// which are left out of Installed and Updates.
	Exclude func() map[string]bool
}

func (b *Backend) excluded() map[string]bool {
	if b.Exclude == nil {
		return nil
	}
	return b.Exclude()
}

// Source implements backend.Backend.
func (b *Backend) Source() catalog.SourceID { return catalog.SourceFlathub }

// Supports implements backend.Backend.
func (b *Backend) Supports(l catalog.Listing) bool {
	return l.Source == catalog.SourceFlathub && refPattern.MatchString(l.Ref)
}

// Installed implements backend.Backend. It lists apps from every installation, user and system.
func (b *Backend) Installed(ctx context.Context) ([]backend.InstalledApp, error) {
	out, err := b.Runner.Run(ctx, b.Path,
		[]string{"list", "--app", "--columns=application,ref,name,version,origin,installation"}, nil)
	if err != nil {
		return nil, err
	}
	skip := b.excluded()
	var apps []backend.InstalledApp
	for _, line := range strings.Split(out, "\n") {
		f := strings.Split(line, "\t")
		if len(f) != 6 || f[0] == "" || skip[f[0]] {
			continue
		}
		ref := f[1]
		if !strings.HasPrefix(ref, "app/") {
			ref = "app/" + ref
		}
		apps = append(apps, backend.InstalledApp{
			Source: catalog.SourceFlathub, Ref: ref, AppID: f[0], Name: f[2], Version: f[3], Origin: f[4],
			Scope: f[5],
		})
	}
	return apps, nil
}

// Updates implements backend.UpdateChecker. It asks the remotes of the user and system installations;
// an installation with no remotes has nothing to update.
func (b *Backend) Updates(ctx context.Context) ([]backend.AvailableUpdate, error) {
	skip := b.excluded()
	// Flatpak reads an update's version from its local copy of the remote's AppStream data, which it refreshes
	// only now and then. Until it does, an update shows as the installed version, or an older one; such a
	// version is left out, for the caller to fill in from a fresher source.
	installed := map[string]string{}
	if apps, err := b.Installed(ctx); err == nil {
		for _, a := range apps {
			installed[a.Scope+" "+a.Ref] = a.Version
		}
	}
	var ups []backend.AvailableUpdate
	var errs []error
	for _, scope := range []string{string(settings.ScopeUser), string(settings.ScopeSystem)} {
		out, err := b.Runner.Run(ctx, b.Path, []string{"remote-ls", "--" + scope, "--updates", "--app",
			"--columns=application,ref,version"}, nil)
		if err != nil {
			errs = append(errs, err)
			continue
		}
		for _, line := range strings.Split(out, "\n") {
			f := strings.Split(line, "\t")
			if len(f) != 3 || f[1] == "" || skip[f[0]] {
				continue
			}
			ref := f[1]
			if !strings.HasPrefix(ref, "app/") {
				ref = "app/" + ref
			}
			version := f[2]
			if v, ok := installed[scope+" "+ref]; ok && notNewer(version, v) {
				version = ""
			}
			ups = append(ups, backend.AvailableUpdate{Ref: ref, Scope: scope, Version: version})
		}
	}
	if len(errs) == 2 {
		return nil, errs[0]
	}
	return ups, nil
}

// Install implements backend.Backend. In user scope it first adds Flathub as a user remote if needed, which
// needs no password.
func (b *Backend) Install(ctx context.Context, l catalog.Listing, progress func(backend.Progress)) error {
	if l.Source != catalog.SourceFlathub {
		return backend.ErrWrongSource
	}
	if !refPattern.MatchString(l.Ref) {
		return fmt.Errorf("flatpak: refusing malformed ref %q", l.Ref)
	}
	scope, err := scopeFlag(string(b.Scope))
	if err != nil {
		return err
	}
	if err := b.ensureFlathub(ctx, scope, progress); err != nil {
		return err
	}
	report(progress, "Installing "+l.Ref)
	return b.runWithProgress(ctx, []string{"install", scope, "--noninteractive", FlathubRemote, l.Ref}, progress)
}

// Update implements backend.Backend.
func (b *Backend) Update(ctx context.Context, a backend.InstalledApp, progress func(backend.Progress)) error {
	scope, err := b.checkInstalled(a)
	if err != nil {
		return err
	}
	report(progress, "Updating "+a.Ref)
	return b.runWithProgress(ctx, []string{"update", scope, "--noninteractive", a.Ref}, progress)
}

// Remove implements backend.Backend. App data in ~/.var/app is kept, as flatpak does by default.
func (b *Backend) Remove(ctx context.Context, a backend.InstalledApp, progress func(backend.Progress)) error {
	scope, err := b.checkInstalled(a)
	if err != nil {
		return err
	}
	report(progress, "Removing "+a.Ref)
	return b.runWithProgress(ctx, []string{"uninstall", scope, "--noninteractive", a.Ref}, progress)
}

func (b *Backend) checkInstalled(a backend.InstalledApp) (string, error) {
	if a.Source != catalog.SourceFlathub {
		return "", backend.ErrWrongSource
	}
	if !refPattern.MatchString(a.Ref) {
		return "", fmt.Errorf("flatpak: refusing malformed ref %q", a.Ref)
	}
	return scopeFlag(a.Scope)
}

// ensureFlathub adds the Flathub remote to the installation if it is missing.
func (b *Backend) ensureFlathub(ctx context.Context, scope string, progress func(backend.Progress)) error {
	out, err := b.Runner.Run(ctx, b.Path, []string{"remotes", scope, "--columns=name"}, nil)
	if err != nil {
		return err
	}
	for _, name := range strings.Fields(out) {
		if name == FlathubRemote {
			return nil
		}
	}
	report(progress, "Adding Flathub")
	_, err = b.Runner.Run(ctx, b.Path,
		[]string{"remote-add", scope, "--if-not-exists", FlathubRemote, FlathubRepoFile}, nil)
	return err
}

func (b *Backend) runWithProgress(ctx context.Context, args []string, progress func(backend.Progress)) error {
	_, err := b.Runner.Run(ctx, b.Path, args, func(line string) { report(progress, line) })
	return err
}

// scopeFlag turns an installation name into the flatpak option that selects it.
func scopeFlag(scope string) (string, error) {
	switch scope {
	case string(settings.ScopeUser):
		return "--user", nil
	case string(settings.ScopeSystem), "default":
		return "--system", nil
	}
	if installationPattern.MatchString(scope) {
		return "--installation=" + scope, nil
	}
	return "", fmt.Errorf("flatpak: unknown installation %q", scope)
}

func report(progress func(backend.Progress), msg string) {
	if progress != nil {
		progress(backend.Progress{Message: msg})
	}
}

// notNewer reports whether version a is the same as b or, when both are dotted numbers such as "26.08.1",
// older.
func notNewer(a, b string) bool {
	if a == b {
		return true
	}
	pa, pb := strings.Split(a, "."), strings.Split(b, ".")
	for i := 0; i < max(len(pa), len(pb)); i++ {
		na, nb := 0, 0
		var err error
		if i < len(pa) {
			if na, err = strconv.Atoi(pa[i]); err != nil {
				return false
			}
		}
		if i < len(pb) {
			if nb, err = strconv.Atoi(pb[i]); err != nil {
				return false
			}
		}
		if na != nb {
			return na < nb
		}
	}
	return true
}
