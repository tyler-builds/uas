// Package pkgmgr installs distribution packages with the system's package manager: apt on Debian and Ubuntu,
// dnf on Fedora (not image-based Fedora, which layers with rpm-ostree), and pacman on Arch.
//
// Installing, updating and removing need root, so they run through pkexec, which asks for the user's password
// in a desktop prompt (or directly when uas already runs as root, as in a container). Listing installed
// packages and updates doesn't. A package manager lists every package, libraries too, so Installed keeps the
// packages the catalog lists as apps for this release (Known).
package pkgmgr

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"

	"github.com/tyler-builds/uas/internal/catalog"
	"github.com/tyler-builds/uas/internal/client/backend"
	"github.com/tyler-builds/uas/internal/client/run"
)

// Kind is a package manager.
type Kind string

const (
	Apt    Kind = "apt"
	DNF    Kind = "dnf"
	Pacman Kind = "pacman"
)

// Backend drives one package manager for one distribution release.
type Backend struct {
	Kind   Kind
	From   catalog.SourceID // fedora, debian, ubuntu or arch
	Distro string           // the release's listings: "ubuntu-24.04", "fedora-45", "arch"
	Runner run.Runner
	// Root is the command that runs a command as root, such as ["/usr/bin/pkexec"]; empty when uas runs as
	// root already.
	Root []string
	// Known returns the packages the catalog lists for Distro.
	Known func(ctx context.Context) (map[string]bool, error)
}

var packageName = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._+-]*$`)

// Source implements backend.Backend.
func (b *Backend) Source() catalog.SourceID { return b.From }

// Supports implements backend.Backend: the listing must be for this release.
func (b *Backend) Supports(l catalog.Listing) bool {
	return l.Source == b.From && l.Distro == b.Distro && packageName.MatchString(l.Package)
}

// Installed implements backend.Backend.
func (b *Backend) Installed(ctx context.Context) ([]backend.InstalledApp, error) {
	var name string
	var args []string
	switch b.Kind {
	case Apt:
		name, args = "dpkg-query", []string{"-W", "-f=${Package}\t${Version}\t${db:Status-Status}\n"}
	case DNF:
		name, args = "rpm", []string{"-qa", "--qf", "%{NAME}\t%{VERSION}-%{RELEASE}\tinstalled\n"}
	case Pacman:
		name, args = "pacman", []string{"-Q"}
	default:
		return nil, fmt.Errorf("unknown package manager %q", b.Kind)
	}
	out, err := b.Runner.Run(ctx, name, args, nil)
	if err != nil {
		return nil, err
	}
	known := map[string]bool{}
	if b.Known != nil {
		if known, err = b.Known(ctx); err != nil {
			return nil, err
		}
	}
	var apps []backend.InstalledApp
	for _, line := range strings.Split(out, "\n") {
		var pkg, ver string
		if b.Kind == Pacman {
			f := strings.Fields(line)
			if len(f) != 2 {
				continue
			}
			pkg, ver = f[0], f[1]
		} else {
			f := strings.Split(line, "\t")
			if len(f) != 3 || f[2] != "installed" {
				continue
			}
			pkg, ver = f[0], f[1]
		}
		if !known[pkg] {
			continue
		}
		apps = append(apps, backend.InstalledApp{Source: b.From, Ref: b.Distro + "/" + pkg, AppID: pkg, Name: pkg,
			Version: ver, Scope: "system", Origin: string(b.Kind), Package: pkg})
	}
	return apps, nil
}

// Install implements backend.Backend.
func (b *Backend) Install(ctx context.Context, l catalog.Listing, progress func(backend.Progress)) error {
	if !b.Supports(l) {
		return backend.ErrWrongSource
	}
	report(progress, "Installing "+l.Package+" with "+string(b.Kind)+" (asks for your password)")
	var args []string
	switch b.Kind {
	case Apt:
		args = []string{"env", "DEBIAN_FRONTEND=noninteractive", "apt-get", "install", "-y", "--", l.Package}
	case DNF:
		args = []string{"dnf", "install", "-y", "--", l.Package}
	case Pacman:
		// Without -y: syncing the database alone and installing would be a partial upgrade, which Arch
		// doesn't support. If the package has moved on, pacman says so and a system update fixes it.
		args = []string{"pacman", "-S", "--needed", "--noconfirm", "--", l.Package}
	}
	return b.root(ctx, args, progress)
}

// Update implements backend.Backend.
func (b *Backend) Update(ctx context.Context, a backend.InstalledApp, progress func(backend.Progress)) error {
	if a.Source != b.From || !packageName.MatchString(a.Package) {
		return backend.ErrWrongSource
	}
	var args []string
	switch b.Kind {
	case Apt:
		args = []string{"env", "DEBIAN_FRONTEND=noninteractive", "apt-get", "install", "-y", "--only-upgrade", "--",
			a.Package}
	case DNF:
		args = []string{"dnf", "upgrade", "-y", "--", a.Package}
	case Pacman:
		return errors.New("Arch updates packages together with the whole system: run \"sudo pacman -Syu\"")
	}
	report(progress, "Updating "+a.Package)
	return b.root(ctx, args, progress)
}

// Remove implements backend.Backend.
func (b *Backend) Remove(ctx context.Context, a backend.InstalledApp, progress func(backend.Progress)) error {
	if a.Source != b.From || !packageName.MatchString(a.Package) {
		return backend.ErrWrongSource
	}
	var args []string
	switch b.Kind {
	case Apt:
		args = []string{"env", "DEBIAN_FRONTEND=noninteractive", "apt-get", "remove", "-y", "--", a.Package}
	case DNF:
		args = []string{"dnf", "remove", "-y", "--", a.Package}
	case Pacman:
		args = []string{"pacman", "-R", "--noconfirm", "--", a.Package}
	}
	report(progress, "Removing "+a.Package)
	return b.root(ctx, args, progress)
}

// Updates implements backend.UpdateChecker, from what the package manager already knows. apt and pacman
// read the lists from their last refresh, which the system's own updater keeps current; dnf refreshes its
// metadata in the user's cache. Arch has no per-app updates (see Update), so it reports none.
func (b *Backend) Updates(ctx context.Context) ([]backend.AvailableUpdate, error) {
	var out string
	var err error
	switch b.Kind {
	case Apt:
		out, err = b.Runner.Run(ctx, "apt", []string{"list", "--upgradable"}, nil)
	case DNF:
		out, err = b.Runner.Run(ctx, "dnf", []string{"list", "--upgrades", "--quiet"}, nil)
	default:
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	known := map[string]bool{}
	if b.Known != nil {
		if known, err = b.Known(ctx); err != nil {
			return nil, err
		}
	}
	var ups []backend.AvailableUpdate
	for _, line := range strings.Split(out, "\n") {
		f := strings.Fields(line)
		if len(f) < 2 {
			continue
		}
		var pkg string
		switch b.Kind {
		case Apt: // "gitg/noble-updates 44-1ubuntu0.1 amd64 [upgradable from: 44-1]"
			p, _, ok := strings.Cut(f[0], "/")
			if !ok {
				continue
			}
			pkg = p
		case DNF: // "gitg.x86_64  44-2.fc45  updates"
			i := strings.LastIndex(f[0], ".")
			if i <= 0 {
				continue
			}
			pkg = f[0][:i]
		}
		if known[pkg] {
			ups = append(ups, backend.AvailableUpdate{Ref: b.Distro + "/" + pkg, Scope: "system", Version: f[1]})
		}
	}
	return ups, nil
}

// root runs a command as root, reporting its output.
func (b *Backend) root(ctx context.Context, args []string, progress func(backend.Progress)) error {
	cmd := append(append([]string{}, b.Root...), args...)
	_, err := b.Runner.Run(ctx, cmd[0], cmd[1:], func(line string) { report(progress, line) })
	if err != nil {
		var re *run.Error
		if errors.As(err, &re) && strings.Contains(re.Stderr, "Not authorized") {
			return errors.New("the password prompt was dismissed")
		}
	}
	return err
}

func report(progress func(backend.Progress), msg string) {
	if progress != nil && strings.TrimSpace(msg) != "" {
		progress(backend.Progress{Message: msg})
	}
}
