// Package rpmostree installs Fedora packages on image-based systems by layering them with rpm-ostree.
//
// Layering adds packages on top of the system image. Changes go into a new deployment that takes effect at
// the next restart, unless they only add packages and are applied live (see CheckLive). If the system already has an update staged (for example a new Fedora release), new
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
	"strings"
	"sync"

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
	// release returns the Fedora release new layers are built against, asking rpm-ostree once.
	release func() int
	// Exclude, if set, hides layered packages that another backend (COPR) accounts for.
	Exclude func(pkg string) bool
	// Rescan, if set, gets the desktop to show launchers added by a live install (see RescanLaunchers).
	Rescan func()
}

// New creates a backend. Which Fedora release new layers will target is worked out on the first call to
// Release, since asking rpm-ostree takes tens of milliseconds; fallbackRelease (the running system's release)
// is used if rpm-ostree cannot say. ctx bounds that first call.
func New(ctx context.Context, runner run.Runner, path, rpm string, fallbackRelease int) *Backend {
	b := &Backend{Runner: runner, Path: path, RPM: rpm, Rescan: RescanLaunchers}
	b.release = sync.OnceValue(func() int {
		if st, err := b.status(ctx); err == nil && len(st.Deployments) > 0 {
			if r := st.Deployments[0].fedoraRelease(); r > 0 {
				return r
			}
		}
		return fallbackRelease
	})
	return b
}

// Release returns the Fedora release new layers are built against: the release of the newest deployment.
// The first call asks rpm-ostree; later calls return the same answer.
func (b *Backend) Release() int { return b.release() }

// Source implements backend.Backend.
func (b *Backend) Source() catalog.SourceID { return catalog.SourceFedora }

// NeedsRestart implements backend.Restarter. Installs that only add packages can skip the restart; see
// CheckLive.
func (b *Backend) NeedsRestart() bool { return true }

// Supports implements backend.Backend: only listings for the targeted Fedora release can be layered.
func (b *Backend) Supports(l catalog.Listing) bool {
	return l.Source == catalog.SourceFedora && b.Release() > 0 && l.Distro == catalog.FedoraDistro(b.Release()) &&
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
	// Packages layered since the system started wait for a restart, unless they were applied live.
	var waiting []string
	for _, p := range newest.RequestedPackages {
		if !slices.Contains(booted.RequestedPackages, p) {
			waiting = append(waiting, p)
		}
	}
	for _, p := range newest.RequestedLocalPackages {
		if !slices.Contains(booted.RequestedLocalPackages, p) {
			waiting = append(waiting, p)
		}
	}
	live := b.appliedLive(ctx, booted, newest, waiting)
	pending := func(p string) bool { return slices.Contains(waiting, p) && !live[p] }

	var apps []backend.InstalledApp
	for _, p := range newest.RequestedPackages {
		if b.Exclude != nil && b.Exclude(p) {
			continue
		}
		apps = append(apps, backend.InstalledApp{
			Source: catalog.SourceFedora, Ref: p, AppID: p, Name: p, Package: p, Scope: "system",
			Origin: "layered", Pending: pending(p),
		})
	}
	for _, p := range newest.RequestedLocalPackages {
		apps = append(apps, backend.InstalledApp{
			Source: catalog.SourceFedora, Ref: p, AppID: p, Name: p, Scope: "system", Origin: "local file",
			Pending: pending(p),
		})
	}
	return apps, nil
}

// appliedLive returns which of pkgs, layered in newest but not in the booted deployment, the running system
// has anyway because they were applied live. When newest is the commit applied live, all of them; when more
// was layered for the next restart since, the running system's rpm database tells.
func (b *Backend) appliedLive(ctx context.Context, booted, newest deployment, pkgs []string) map[string]bool {
	if booted.LiveReplaced == "" || len(pkgs) == 0 {
		return nil
	}
	live := map[string]bool{}
	if booted.LiveReplaced == newest.Checksum {
		for _, p := range pkgs {
			live[p] = true
		}
		return live
	}
	var names []string
	for _, p := range pkgs {
		if packageName.MatchString(p) {
			names = append(names, p)
		}
	}
	if len(names) == 0 {
		return nil
	}
	// rpm -q fails when any package is missing, and says which on standard output.
	out, _ := b.Runner.Run(ctx, b.RPM, append([]string{"-q"}, names...), nil)
	if strings.TrimSpace(out) == "" {
		return nil // rpm prints a line for each package; it didn't run
	}
	missing := map[string]bool{}
	for _, line := range strings.Split(out, "\n") {
		if p, ok := strings.CutPrefix(strings.TrimSpace(line), "package "); ok {
			if p, ok := strings.CutSuffix(p, " is not installed"); ok {
				missing[p] = true
			}
		}
	}
	for _, p := range names {
		live[p] = !missing[p]
	}
	return live
}

// Install implements backend.Backend.
func (b *Backend) Install(ctx context.Context, l catalog.Listing, progress func(backend.Progress)) error {
	if l.Source != catalog.SourceFedora {
		return backend.ErrWrongSource
	}
	if !b.Supports(l) {
		return fmt.Errorf("rpm-ostree: %s is for %s, but new packages go onto Fedora %d", l.Package, l.Distro,
			b.Release())
	}
	return b.Layer(ctx, l.Package, progress)
}

// Layer adds a package to the system image, unless the running image already has it.
func (b *Backend) Layer(ctx context.Context, pkg string, progress func(backend.Progress)) error {
	return b.layer(ctx, pkg, false, progress)
}

// layer adds a package to the system image and, if live, applies it to the running system too. It never
// passes --allow-replacement: apply-live refuses a transaction that changes packages of the image.
func (b *Backend) layer(ctx context.Context, pkg string, live bool, progress func(backend.Progress)) error {
	if !packageName.MatchString(pkg) {
		return fmt.Errorf("rpm-ostree: refusing malformed package name %q", pkg)
	}
	// rpm -q succeeds only if the running image has the package.
	if _, err := b.Runner.Run(ctx, b.RPM, []string{"-q", "--quiet", pkg}, nil); err == nil {
		return fmt.Errorf("%s: %w", pkg, ErrInImage)
	}
	args := []string{"install", "--idempotent", pkg}
	msg := "Adding " + pkg + " to the system image"
	if live {
		args = []string{"install", "--idempotent", "--apply-live", pkg}
		msg += " and the running system"
	}
	report(progress, msg)
	_, err := b.Runner.Run(ctx, b.Path, args, func(line string) { report(progress, line) })
	return err
}

// Unlayer removes a layered package.
func (b *Backend) Unlayer(ctx context.Context, pkg string, progress func(backend.Progress)) error {
	if !packageName.MatchString(pkg) {
		return fmt.Errorf("rpm-ostree: refusing malformed package name %q", pkg)
	}
	report(progress, "Removing "+pkg+" from the system image")
	_, err := b.Runner.Run(ctx, b.Path, []string{"uninstall", pkg}, func(line string) { report(progress, line) })
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
	if a.Origin != "layered" {
		return fmt.Errorf("rpm-ostree: %s was installed from a local file; remove it with rpm-ostree uninstall", a.Ref)
	}
	return b.Unlayer(ctx, a.Package, progress)
}

type status struct {
	Deployments []deployment `json:"deployments"`
}

type deployment struct {
	Booted                 bool                       `json:"booted"`
	Staged                 bool                       `json:"staged"`
	Version                string                     `json:"version"`
	Checksum               string                     `json:"checksum"`
	BaseChecksum           string                     `json:"base-checksum"` // set when packages are layered
	LiveReplaced           string                     `json:"live-replaced"` // the commit applied live, if any
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
