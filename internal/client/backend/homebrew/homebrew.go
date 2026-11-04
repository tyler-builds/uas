// Package homebrew installs command-line tools with Homebrew. Homebrew installs into its own prefix
// (/home/linuxbrew/.linuxbrew), needs no root and no restart, and is the usual way to add CLI tools on Bazzite.
package homebrew

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"

	"github.com/tyler-builds/uas/internal/catalog"
	"github.com/tyler-builds/uas/internal/client/backend"
	"github.com/tyler-builds/uas/internal/client/run"
)

var formulaName = regexp.MustCompile(`^[a-z0-9][a-z0-9@._+-]*$`)

// Backend drives the brew command.
type Backend struct {
	Runner run.Runner
	Path   string // the brew binary
}

// Source implements backend.Backend.
func (b *Backend) Source() catalog.SourceID { return catalog.SourceHomebrew }

// Supports implements backend.Backend.
func (b *Backend) Supports(l catalog.Listing) bool {
	return l.Source == catalog.SourceHomebrew && formulaName.MatchString(l.Package)
}

// Installed implements backend.Backend. It lists formulae installed on request; dependencies pulled in by
// other formulae are left out.
func (b *Backend) Installed(ctx context.Context) ([]backend.InstalledApp, error) {
	out, err := b.Runner.Run(ctx, b.Path, []string{"info", "--json=v2", "--installed"}, nil)
	if err != nil {
		return nil, err
	}
	var info struct {
		Formulae []struct {
			Name      string `json:"name"`
			Installed []struct {
				Version            string `json:"version"`
				InstalledOnRequest bool   `json:"installed_on_request"`
			} `json:"installed"`
		} `json:"formulae"`
	}
	if err := json.Unmarshal([]byte(out), &info); err != nil {
		return nil, fmt.Errorf("brew info: %w", err)
	}
	var apps []backend.InstalledApp
	for _, f := range info.Formulae {
		if len(f.Installed) == 0 || !f.Installed[0].InstalledOnRequest {
			continue
		}
		apps = append(apps, backend.InstalledApp{
			Source: catalog.SourceHomebrew, Ref: f.Name, AppID: f.Name, Name: f.Name, Package: f.Name,
			Version: f.Installed[0].Version, Scope: "user", Origin: "homebrew",
		})
	}
	return apps, nil
}

// Updates implements backend.UpdateChecker.
func (b *Backend) Updates(ctx context.Context) ([]backend.AvailableUpdate, error) {
	out, err := b.Runner.Run(ctx, b.Path, []string{"outdated", "--json=v2", "--formula"}, nil)
	if err != nil {
		return nil, err
	}
	var res struct {
		Formulae []struct {
			Name    string `json:"name"`
			Current string `json:"current_version"`
			Pinned  bool   `json:"pinned"`
		} `json:"formulae"`
	}
	if err := json.Unmarshal([]byte(out), &res); err != nil {
		return nil, fmt.Errorf("brew outdated: %w", err)
	}
	var ups []backend.AvailableUpdate
	for _, f := range res.Formulae {
		if !f.Pinned {
			ups = append(ups, backend.AvailableUpdate{Ref: f.Name, Scope: "user", Version: f.Current})
		}
	}
	return ups, nil
}

// Install implements backend.Backend.
func (b *Backend) Install(ctx context.Context, l catalog.Listing, progress func(backend.Progress)) error {
	if l.Source != catalog.SourceHomebrew {
		return backend.ErrWrongSource
	}
	if !b.Supports(l) {
		return fmt.Errorf("homebrew: refusing malformed formula name %q", l.Package)
	}
	return b.run(ctx, progress, "install", "--formula", l.Package)
}

// Update implements backend.Backend.
func (b *Backend) Update(ctx context.Context, a backend.InstalledApp, progress func(backend.Progress)) error {
	if err := check(a); err != nil {
		return err
	}
	return b.run(ctx, progress, "upgrade", "--formula", a.Package)
}

// Remove implements backend.Backend.
func (b *Backend) Remove(ctx context.Context, a backend.InstalledApp, progress func(backend.Progress)) error {
	if err := check(a); err != nil {
		return err
	}
	return b.run(ctx, progress, "uninstall", "--formula", a.Package)
}

func check(a backend.InstalledApp) error {
	if a.Source != catalog.SourceHomebrew {
		return backend.ErrWrongSource
	}
	if !formulaName.MatchString(a.Package) {
		return fmt.Errorf("homebrew: refusing malformed formula name %q", a.Package)
	}
	return nil
}

func (b *Backend) run(ctx context.Context, progress func(backend.Progress), args ...string) error {
	_, err := b.Runner.Run(ctx, b.Path, args, func(line string) {
		if progress != nil {
			progress(backend.Progress{Message: line})
		}
	})
	return err
}
