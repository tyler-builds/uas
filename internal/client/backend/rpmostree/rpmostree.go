// Package rpmostree installs Fedora packages on image-based systems by layering them with rpm-ostree.
//
// Layering adds packages on top of the system image. Changes go into a new deployment that takes effect at
// the next restart. If the system already has an update staged (for example a new Fedora release), new
// layers go onto that staged deployment, so the backend targets the release of the newest deployment, not
// the running one.
package rpmostree

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strconv"

	"github.com/tyler-builds/uas/internal/catalog"
	"github.com/tyler-builds/uas/internal/client/backend"
	"github.com/tyler-builds/uas/internal/client/run"
)

// ErrUpdatesWithSystem is returned by Update: layered packages are updated along with the system image.
var ErrUpdatesWithSystem = errors.New("layered packages update together with the system; run \"ujust update\" " +
	"(or \"rpm-ostree upgrade\") and restart")

// ErrInImage is returned when a package is already part of the system image.
var ErrInImage = errors.New("already part of the system image")

var packageName = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._+-]*$`)

// Backend drives rpm-ostree.
type Backend struct {
	Runner run.Runner
	Path   string // the rpm-ostree binary
	RPM    string // the rpm binary, used to check what the running image contains
	// Release is the Fedora release new layers are built against.
	Release int
}

// New creates a backend and works out which Fedora release new layers will target. fallbackRelease (the
// running system's release) is used if rpm-ostree cannot say.
func New(ctx context.Context, runner run.Runner, path, rpm string, fallbackRelease int) *Backend {
	b := &Backend{Runner: runner, Path: path, RPM: rpm, Release: fallbackRelease}
	if st, err := b.status(ctx); err == nil && len(st.Deployments) > 0 {
		if r := st.Deployments[0].fedoraRelease(); r > 0 {
			b.Release = r
		}
	}
	return b
}

// Source implements backend.Backend.
func (b *Backend) Source() catalog.SourceID { return catalog.SourceFedora }

// NeedsRestart implements backend.Restarter.
func (b *Backend) NeedsRestart() bool { return true }

// Supports implements backend.Backend: only listings for the targeted Fedora release can be layered.
func (b *Backend) Supports(l catalog.Listing) bool {
	return l.Source == catalog.SourceFedora && b.Release > 0 && l.Distro == catalog.FedoraDistro(b.Release) &&
		packageName.MatchString(l.Package)
}

// Installed implements backend.Backend. It lists packages layered in the newest deployment. Packages that
// are part of the image itself are not listed, since they cannot be removed.
func (b *Backend) Installed(ctx context.Context) ([]backend.InstalledApp, error) {
	st, err := b.status(ctx)
	if err != nil {
		return nil, err
	}
	if len(st.Deployments) == 0 {
		return nil, nil
	}
	newest := st.Deployments[0]
	var booted deployment
	for _, d := range st.Deployments {
		if d.Booted {
			booted = d
		}
	}
	var apps []backend.InstalledApp
	for _, p := range newest.RequestedPackages {
		apps = append(apps, backend.InstalledApp{
			Source: catalog.SourceFedora, Ref: p, AppID: p, Name: p, Package: p, Scope: "system",
			Origin: "layered", Pending: !slices.Contains(booted.RequestedPackages, p),
		})
	}
	for _, p := range newest.RequestedLocalPackages {
		apps = append(apps, backend.InstalledApp{
			Source: catalog.SourceFedora, Ref: p, AppID: p, Name: p, Scope: "system", Origin: "local file",
			Pending: !slices.Contains(booted.RequestedLocalPackages, p),
		})
	}
	return apps, nil
}

// Install implements backend.Backend.
func (b *Backend) Install(ctx context.Context, l catalog.Listing, progress func(backend.Progress)) error {
	if l.Source != catalog.SourceFedora {
		return backend.ErrWrongSource
	}
	if !b.Supports(l) {
		return fmt.Errorf("rpm-ostree: %s is for %s, but new packages go onto Fedora %d", l.Package, l.Distro,
			b.Release)
	}
	// rpm -q succeeds only if the running image has the package.
	if _, err := b.Runner.Run(ctx, b.RPM, []string{"-q", "--quiet", l.Package}, nil); err == nil {
		return fmt.Errorf("%s: %w", l.Package, ErrInImage)
	}
	report(progress, "Adding "+l.Package+" to the system image")
	_, err := b.Runner.Run(ctx, b.Path, []string{"install", "--idempotent", l.Package},
		func(line string) { report(progress, line) })
	return err
}

// Update implements backend.Backend.
func (b *Backend) Update(context.Context, backend.InstalledApp, func(backend.Progress)) error {
	return ErrUpdatesWithSystem
}

// Remove implements backend.Backend.
func (b *Backend) Remove(ctx context.Context, a backend.InstalledApp, progress func(backend.Progress)) error {
	if a.Source != catalog.SourceFedora {
		return backend.ErrWrongSource
	}
	if a.Origin != "layered" || !packageName.MatchString(a.Package) {
		return fmt.Errorf("rpm-ostree: %s was installed from a local file; remove it with rpm-ostree uninstall", a.Ref)
	}
	report(progress, "Removing "+a.Package+" from the system image")
	_, err := b.Runner.Run(ctx, b.Path, []string{"uninstall", a.Package}, func(line string) { report(progress, line) })
	return err
}

type status struct {
	Deployments []deployment `json:"deployments"`
}

type deployment struct {
	Booted                 bool                       `json:"booted"`
	Staged                 bool                       `json:"staged"`
	Version                string                     `json:"version"`
	RequestedPackages      []string                   `json:"requested-packages"`
	RequestedLocalPackages []string                   `json:"requested-local-packages"`
	BaseCommitMeta         map[string]json.RawMessage `json:"base-commit-meta"`
}

var fcRelease = regexp.MustCompile(`\.fc(\d+)\.`)

// fedoraRelease reads the Fedora release from the deployment's kernel version, such as
// "7.2.14-300.fc45.x86_64". Container-based images keep it in the image manifest's annotations.
func (d deployment) fedoraRelease() int {
	var kernel string
	if raw, ok := d.BaseCommitMeta["ostree.linux"]; ok {
		_ = json.Unmarshal(raw, &kernel)
	}
	if kernel == "" {
		if raw, ok := d.BaseCommitMeta["ostree.manifest"]; ok {
			var manifestJSON string
			if json.Unmarshal(raw, &manifestJSON) == nil {
				var m struct {
					Annotations map[string]string `json:"annotations"`
				}
				if json.Unmarshal([]byte(manifestJSON), &m) == nil {
					kernel = m.Annotations["ostree.linux"]
				}
			}
		}
	}
	if m := fcRelease.FindStringSubmatch(kernel); m != nil {
		n, _ := strconv.Atoi(m[1])
		return n
	}
	return 0
}

func (b *Backend) status(ctx context.Context) (status, error) {
	out, err := b.Runner.Run(ctx, b.Path, []string{"status", "--json"}, nil)
	if err != nil {
		return status{}, err
	}
	var st status
	if err := json.Unmarshal([]byte(out), &st); err != nil {
		return status{}, fmt.Errorf("rpm-ostree status: %w", err)
	}
	return st, nil
}

func report(progress func(backend.Progress), msg string) {
	if progress != nil {
		progress(backend.Progress{Message: msg})
	}
}
