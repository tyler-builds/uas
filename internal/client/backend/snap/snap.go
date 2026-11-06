// Package snap installs apps from the Snap Store with the snap command.
//
// snapd asks for the user's password itself (through polkit) when needed, so uas runs snap as the user.
// Snaps with classic confinement run outside the sandbox and need --classic, which uas passes only for
// listings the catalog marks classic.
package snap

import (
	"context"
	"errors"
	"regexp"
	"strings"

	"github.com/tyler-builds/uas/internal/catalog"
	"github.com/tyler-builds/uas/internal/client/backend"
	"github.com/tyler-builds/uas/internal/client/run"
)

// Backend drives snap.
type Backend struct {
	Runner run.Runner
	Path   string // the snap binary
	// Known returns the snaps the catalog lists, so runtimes and bases (core22, gnome-42-2204) are left out
	// of Installed.
	Known func(ctx context.Context) (map[string]bool, error)
}

var snapName = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,39}$`)

// Source implements backend.Backend.
func (b *Backend) Source() catalog.SourceID { return catalog.SourceSnap }

// Supports implements backend.Backend.
func (b *Backend) Supports(l catalog.Listing) bool {
	return l.Source == catalog.SourceSnap && snapName.MatchString(l.Ref)
}

// Installed implements backend.Backend.
func (b *Backend) Installed(ctx context.Context) ([]backend.InstalledApp, error) {
	out, err := b.Runner.Run(ctx, b.Path, []string{"list", "--unicode=never", "--color=never"}, nil)
	if err != nil {
		return nil, err
	}
	known, err := b.known(ctx)
	if err != nil {
		return nil, err
	}
	var apps []backend.InstalledApp
	for i, line := range strings.Split(out, "\n") {
		f := strings.Fields(line)
		if i == 0 || len(f) < 2 || !known[f[0]] {
			continue // the header, or a base, runtime or snap the catalog doesn't list
		}
		a := backend.InstalledApp{Source: catalog.SourceSnap, Ref: f[0], AppID: f[0], Name: f[0], Version: f[1],
			Scope: "system", Origin: "snap", Package: f[0]}
		apps = append(apps, a)
	}
	return apps, nil
}

func (b *Backend) known(ctx context.Context) (map[string]bool, error) {
	if b.Known == nil {
		return map[string]bool{}, nil
	}
	return b.Known(ctx)
}

// Install implements backend.Backend.
func (b *Backend) Install(ctx context.Context, l catalog.Listing, progress func(backend.Progress)) error {
	if !b.Supports(l) {
		return backend.ErrWrongSource
	}
	args := []string{"install", l.Ref}
	if l.Classic {
		args = append(args, "--classic")
	}
	return b.run(ctx, args, progress)
}

// Update implements backend.Backend.
func (b *Backend) Update(ctx context.Context, a backend.InstalledApp, progress func(backend.Progress)) error {
	if a.Source != catalog.SourceSnap || !snapName.MatchString(a.Ref) {
		return backend.ErrWrongSource
	}
	return b.run(ctx, []string{"refresh", a.Ref}, progress)
}

// Remove implements backend.Backend.
func (b *Backend) Remove(ctx context.Context, a backend.InstalledApp, progress func(backend.Progress)) error {
	if a.Source != catalog.SourceSnap || !snapName.MatchString(a.Ref) {
		return backend.ErrWrongSource
	}
	return b.run(ctx, []string{"remove", a.Ref}, progress)
}

// Updates implements backend.UpdateChecker. snapd also refreshes snaps on its own a few times a day.
func (b *Backend) Updates(ctx context.Context) ([]backend.AvailableUpdate, error) {
	out, err := b.Runner.Run(ctx, b.Path, []string{"refresh", "--list", "--unicode=never", "--color=never"}, nil)
	if err != nil {
		var re *run.Error
		if errors.As(err, &re) && strings.Contains(re.Stderr, "up to date") {
			return nil, nil
		}
		return nil, err
	}
	known, err := b.known(ctx)
	if err != nil {
		return nil, err
	}
	var ups []backend.AvailableUpdate
	for i, line := range strings.Split(out, "\n") {
		f := strings.Fields(line)
		if i == 0 || len(f) < 2 || !known[f[0]] {
			continue
		}
		ups = append(ups, backend.AvailableUpdate{Ref: f[0], Scope: "system", Version: f[1]})
	}
	return ups, nil
}

func (b *Backend) run(ctx context.Context, args []string, progress func(backend.Progress)) error {
	_, err := b.Runner.Run(ctx, b.Path, args, func(line string) {
		if progress != nil && strings.TrimSpace(line) != "" {
			progress(backend.Progress{Message: strings.TrimSpace(line)})
		}
	})
	return err
}
