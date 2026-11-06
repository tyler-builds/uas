// Package copr installs packages from COPR projects by enabling the project's repo and installing the package:
// layered with rpm-ostree on image-based Fedora, with dnf on package-based Fedora.
//
// Enabling a repo writes a file to /etc/yum.repos.d, which needs root: uas writes the file to a temporary
// location and copies it into place with pkexec, so the user sees one password prompt per project. The file
// matches what "dnf copr enable" writes, including the project's signing key, which dnf and rpm-ostree check
// packages against.
package copr

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/tyler-builds/uas/internal/catalog"
	"github.com/tyler-builds/uas/internal/client/backend"
	"github.com/tyler-builds/uas/internal/client/backend/rpmostree"
	"github.com/tyler-builds/uas/internal/client/run"
	"github.com/tyler-builds/uas/internal/client/state"
)

const (
	host         = "copr.fedorainfracloud.org"
	downloadBase = "https://download.copr.fedorainfracloud.org/results/"
)

var (
	ownerName   = regexp.MustCompile(`^@?[A-Za-z0-9][A-Za-z0-9_.-]*$`)
	projectName = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.+-]*$`)
	packageName = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._+-]*$`)
)

// Backend installs COPR packages.
type Backend struct {
	// Ostree layers packages on image-based Fedora. When it is nil, packages are installed with dnf, for
	// Fedora release DNFRelease.
	Ostree     *rpmostree.Backend
	DNFRelease int
	Runner     run.Runner
	// Root is the command that runs a command as root, such as ["/usr/bin/pkexec"]; empty when uas runs as
	// root already.
	Root    []string
	RepoDir string // /etc/yum.repos.d
	State   state.Store
	Now     func() time.Time
}

// Source implements backend.Backend.
func (b *Backend) Source() catalog.SourceID { return catalog.SourceCOPR }

// NeedsRestart implements backend.Restarter: layered packages take effect after a restart, dnf's at once.
func (b *Backend) NeedsRestart() bool { return b.Ostree != nil }

// release is the Fedora release packages are installed for: the one new layers target, or the running one.
func (b *Backend) release() int {
	if b.Ostree != nil {
		return b.Ostree.Release()
	}
	return b.DNFRelease
}

// Supports implements backend.Backend: the listing must be for the Fedora release packages go onto.
func (b *Backend) Supports(l catalog.Listing) bool {
	_, _, err := splitProject(l.Project)
	return l.Source == catalog.SourceCOPR && err == nil && packageName.MatchString(l.Package) &&
		b.release() > 0 && l.Distro == catalog.FedoraDistro(b.release())
}

func splitProject(full string) (owner, name string, err error) {
	owner, name, ok := strings.Cut(full, "/")
	if !ok || !ownerName.MatchString(owner) || !projectName.MatchString(name) {
		return "", "", fmt.Errorf("copr: bad project name %q", full)
	}
	return owner, name, nil
}

// RepoFileName is the name "dnf copr enable" uses, so uas and dnf agree on whether a repo is enabled.
func RepoFileName(project string) (string, error) {
	owner, name, err := splitProject(project)
	if err != nil {
		return "", err
	}
	if g, ok := strings.CutPrefix(owner, "@"); ok {
		owner = "group_" + g
	}
	return "_copr:" + host + ":" + owner + ":" + name + ".repo", nil
}

// RepoFile returns the contents of the repo file for a project.
func RepoFile(project string) (string, error) {
	owner, name, err := splitProject(project)
	if err != nil {
		return "", err
	}
	id := owner
	if g, ok := strings.CutPrefix(owner, "@"); ok {
		id = "group_" + g
	}
	base := downloadBase + owner + "/" + name + "/"
	return fmt.Sprintf(`[copr:%s:%s:%s]
name=Copr repo for %s owned by %s
baseurl=%sfedora-$releasever-$basearch/
type=rpm-md
skip_if_unavailable=True
gpgcheck=1
gpgkey=%spubkey.gpg
repo_gpgcheck=0
enabled=1
enabled_metadata=1
`, host, id, name, name, owner, base, base), nil
}

// Installed implements backend.Backend. It lists COPR packages uas installed that are still installed.
func (b *Backend) Installed(ctx context.Context) ([]backend.InstalledApp, error) {
	st, err := b.State.Load()
	if err != nil {
		return nil, err
	}
	if len(st.COPR) == 0 {
		return nil, nil
	}
	if b.Ostree == nil {
		return b.installedRPMs(ctx, st)
	}
	saved := b.Ostree.Exclude
	b.Ostree.Exclude = nil
	layered, err := b.Ostree.Installed(ctx)
	b.Ostree.Exclude = saved
	if err != nil {
		return nil, err
	}
	var apps []backend.InstalledApp
	for _, c := range st.COPR {
		for _, l := range layered {
			if l.Package == c.Package && l.Origin == "layered" {
				apps = append(apps, backend.InstalledApp{Source: catalog.SourceCOPR, Ref: c.Package,
					AppID: c.Package, Name: c.Package, Package: c.Package, Scope: "system",
					Origin: "copr:" + c.Project, Pending: l.Pending})
			}
		}
	}
	return apps, nil
}

// Install implements backend.Backend.
func (b *Backend) Install(ctx context.Context, l catalog.Listing, progress func(backend.Progress)) error {
	if l.Source != catalog.SourceCOPR {
		return backend.ErrWrongSource
	}
	if !b.Supports(l) {
		return fmt.Errorf("copr: %s is for %s, but packages go onto Fedora %d", l.Package, l.Distro, b.release())
	}
	name, err := RepoFileName(l.Project)
	if err != nil {
		return err
	}
	repoFile := filepath.Join(b.RepoDir, name)
	added := false
	if _, err := os.Stat(repoFile); errors.Is(err, os.ErrNotExist) {
		report(progress, "Enabling the COPR repo "+l.Project+" (asks for your password)")
		if err := b.enableRepo(ctx, l.Project, repoFile); err != nil {
			return fmt.Errorf("enable COPR repo: %w", err)
		}
		added = true
	} else if err != nil {
		return err
	}

	if err := b.installPackage(ctx, l.Package, progress); err != nil {
		if added {
			// Leave no repo behind that nothing uses.
			_ = b.disableRepo(ctx, repoFile)
		}
		return err
	}

	st, err := b.State.Load()
	if err != nil {
		return err
	}
	// Whether uas added the repo is a fact about the project, shared by all its packages.
	for _, other := range st.COPR {
		if other.Project == l.Project && other.AddedRepo {
			added = true
		}
	}
	st.PutCOPR(state.COPRInstall{Project: l.Project, Package: l.Package, RepoFile: repoFile, AddedRepo: added,
		InstalledAt: b.now()})
	return b.State.Save(st)
}

// Update implements backend.Backend. Layered packages update with the system image; dnf updates one package.
func (b *Backend) Update(ctx context.Context, a backend.InstalledApp, progress func(backend.Progress)) error {
	if b.Ostree != nil {
		return rpmostree.ErrUpdatesWithSystem
	}
	if a.Source != catalog.SourceCOPR || !packageName.MatchString(a.Package) {
		return backend.ErrWrongSource
	}
	report(progress, "Updating "+a.Package)
	return b.root(ctx, []string{"dnf", "upgrade", "-y", "--", a.Package}, progress)
}

// Remove implements backend.Backend. It removes the layered package and, if uas enabled the repo and no other
// package uas installed uses it, the repo file too.
func (b *Backend) Remove(ctx context.Context, a backend.InstalledApp, progress func(backend.Progress)) error {
	if a.Source != catalog.SourceCOPR {
		return backend.ErrWrongSource
	}
	st, err := b.State.Load()
	if err != nil {
		return err
	}
	c, ok := st.FindCOPR(a.Package)
	if !ok {
		return fmt.Errorf("copr: %s was not installed by uas", a.Package)
	}
	if b.Ostree != nil {
		err = b.Ostree.Unlayer(ctx, c.Package, progress)
	} else {
		report(progress, "Removing "+c.Package)
		err = b.root(ctx, []string{"dnf", "remove", "-y", "--", c.Package}, progress)
	}
	if err != nil {
		return err
	}
	st.DeleteCOPR(c.Package)
	if err := b.State.Save(st); err != nil {
		return err
	}
	if !c.AddedRepo {
		return nil
	}
	for _, other := range st.COPR {
		if other.Project == c.Project {
			return nil
		}
	}
	report(progress, "Disabling the COPR repo "+c.Project+" (asks for your password)")
	return b.disableRepo(ctx, c.RepoFile)
}

func (b *Backend) enableRepo(ctx context.Context, project, dst string) error {
	content, err := RepoFile(project)
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp("", "uas-copr-*.repo")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.WriteString(content); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return b.root(ctx, []string{"install", "-m", "0644", "-o", "root", "-g", "root", "--", tmp.Name(), dst}, nil)
}

func (b *Backend) disableRepo(ctx context.Context, repoFile string) error {
	// Only ever delete a COPR repo file in the repo directory.
	if filepath.Dir(repoFile) != filepath.Clean(b.RepoDir) || !strings.HasPrefix(filepath.Base(repoFile), "_copr:") {
		return fmt.Errorf("copr: refusing to delete %s", repoFile)
	}
	return b.root(ctx, []string{"rm", "-f", "--", repoFile}, nil)
}

// installPackage layers a package, or installs it with dnf. dnf's -y also accepts the project's signing key,
// which the repo file names; packages it doesn't sign are refused (gpgcheck=1).
func (b *Backend) installPackage(ctx context.Context, pkg string, progress func(backend.Progress)) error {
	if b.Ostree != nil {
		return b.Ostree.Layer(ctx, pkg, progress)
	}
	report(progress, "Installing "+pkg+" with dnf (asks for your password)")
	return b.root(ctx, []string{"dnf", "install", "-y", "--", pkg}, progress)
}

// installedRPMs lists the COPR packages uas installed with dnf that rpm still has.
func (b *Backend) installedRPMs(ctx context.Context, st state.State) ([]backend.InstalledApp, error) {
	out, err := b.Runner.Run(ctx, "rpm", []string{"-qa", "--qf", "%{NAME}\t%{VERSION}-%{RELEASE}\n"}, nil)
	if err != nil {
		return nil, err
	}
	versions := map[string]string{}
	for _, line := range strings.Split(out, "\n") {
		if name, ver, ok := strings.Cut(strings.TrimSpace(line), "\t"); ok {
			versions[name] = ver
		}
	}
	var apps []backend.InstalledApp
	for _, c := range st.COPR {
		if v, ok := versions[c.Package]; ok {
			apps = append(apps, backend.InstalledApp{Source: catalog.SourceCOPR, Ref: c.Package, AppID: c.Package,
				Name: c.Package, Package: c.Package, Version: v, Scope: "system", Origin: "copr:" + c.Project})
		}
	}
	return apps, nil
}

// Updates implements backend.UpdateChecker on package-based Fedora: newer builds of the COPR packages uas
// installed, from dnf's metadata. Layered packages update with the system image, so there are none to list.
func (b *Backend) Updates(ctx context.Context) ([]backend.AvailableUpdate, error) {
	if b.Ostree != nil {
		return nil, nil
	}
	st, err := b.State.Load()
	if err != nil || len(st.COPR) == 0 {
		return nil, err
	}
	mine := map[string]bool{}
	for _, c := range st.COPR {
		mine[c.Package] = true
	}
	out, err := b.Runner.Run(ctx, "dnf", []string{"list", "--upgrades", "--quiet"}, nil)
	if err != nil {
		return nil, err
	}
	var ups []backend.AvailableUpdate
	for _, line := range strings.Split(out, "\n") {
		f := strings.Fields(line) // "gitg.x86_64  44-2.fc45  copr:copr.fedorainfracloud.org:owner:project"
		if len(f) < 2 {
			continue
		}
		i := strings.LastIndex(f[0], ".")
		if i <= 0 || !mine[f[0][:i]] {
			continue
		}
		ups = append(ups, backend.AvailableUpdate{Ref: f[0][:i], Scope: "system", Version: f[1]})
	}
	return ups, nil
}

// root runs a command as root, reporting its output.
func (b *Backend) root(ctx context.Context, args []string, progress func(backend.Progress)) error {
	cmd := append(append([]string{}, b.Root...), args...)
	_, err := b.Runner.Run(ctx, cmd[0], cmd[1:], func(line string) { report(progress, line) })
	var re *run.Error
	if errors.As(err, &re) && strings.Contains(re.Stderr, "Not authorized") {
		return errors.New("the password prompt was dismissed")
	}
	return err
}

func (b *Backend) now() time.Time {
	if b.Now != nil {
		return b.Now()
	}
	return time.Now()
}

func report(progress func(backend.Progress), msg string) {
	if progress != nil && strings.TrimSpace(msg) != "" {
		progress(backend.Progress{Message: msg})
	}
}
