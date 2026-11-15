package rpmostree

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/tyler-builds/uas/internal/catalog"
	"github.com/tyler-builds/uas/internal/client/backend"
	"github.com/tyler-builds/uas/internal/client/state"
)

// cantTell is why a package waits for a restart when rpm-ostree's dry run doesn't settle it.
const cantTell = "uas couldn't check that it only adds packages"

// CheckLive implements backend.LiveInstaller. A package can be applied to the running system ("rpm-ostree
// install --apply-live") when layering it only adds packages: none from the image is upgraded, downgraded,
// replaced or removed, which apply-live refuses without --allow-replacement, and none is a kernel, driver
// or boot package, which would install but work only after a restart. rpm-ostree's dry run says what the
// transaction does; it changes nothing.
func (b *Backend) CheckLive(ctx context.Context, l catalog.Listing) backend.Live {
	if l.Source != catalog.SourceFedora || !b.Supports(l) {
		return no("uas can't layer it here")
	}
	if _, err := b.Runner.Run(ctx, b.RPM, []string{"-q", "--quiet", l.Package}, nil); err == nil {
		return no("it is already on the system")
	}
	st, err := b.status(ctx)
	if err != nil {
		return no(cantTell)
	}
	booted, newest, ok := st.bootedAndNewest()
	if !ok {
		return no(cantTell)
	}
	// New layers go onto the newest deployment. If it's a new image, applying it live would mean changing
	// the whole system.
	if newest.base() != booted.base() {
		return no("a system update is waiting for a restart")
	}
	// An install or removal that starts meanwhile cancels the dry run (see txns).
	var out string
	err = b.txn.check(ctx, func(ctx context.Context) error {
		out, err = b.Runner.Run(ctx, b.Path, []string{"install", "--dry-run", "--idempotent", l.Package}, nil)
		return err
	})
	if err != nil {
		return no(cantTell)
	}
	return b.judge(ctx, out)
}

// judge decides from a dry run's output whether its transaction can be applied live.
func (b *Backend) judge(ctx context.Context, out string) backend.Live {
	plan, ok := parseDryRun(out)
	switch {
	case !ok:
		return no(cantTell)
	case len(plan.other) > 0:
		return no("it would change or remove packages in the system image")
	case len(plan.install) == 0:
		return no(cantTell)
	}
	names := make([]string, 0, len(plan.install))
	for _, p := range plan.install {
		if restartOnly(p.name) {
			return no(fmt.Sprintf("it includes %s, which works only after a restart", p.name))
		}
		if !packageName.MatchString(p.name) {
			return no(cantTell)
		}
		names = append(names, p.name)
	}
	// rpm-ostree doesn't replace packages of the image when layering, but check: a package the running
	// system has in another version would be replaced.
	have, err := b.versions(ctx, names)
	if err != nil {
		return no(cantTell)
	}
	for _, p := range plan.install {
		if v, ok := have[p.name+"."+p.arch]; ok && v != p.version+"-"+p.release {
			return no("it would replace " + p.name + " in the system image")
		}
	}
	return backend.Live{OK: true}
}

func no(why string) backend.Live { return backend.Live{Why: why} }

// InstallLive implements backend.LiveInstaller: it layers the package and applies it to the running system,
// where it stays after a restart too. Call CheckLive first.
func (b *Backend) InstallLive(ctx context.Context, l catalog.Listing, progress func(backend.Progress)) error {
	if l.Source != catalog.SourceFedora {
		return backend.ErrWrongSource
	}
	if !b.Supports(l) {
		return fmt.Errorf("rpm-ostree: %s is for %s, but new packages go onto Fedora %d", l.Package, l.Distro,
			b.Release())
	}
	if err := b.layer(ctx, l.Package, true, progress); err != nil {
		return err
	}
	if b.Rescan != nil {
		report(progress, "Adding it to your app menu")
		b.Rescan()
	}
	return nil
}

// bootedAndNewest returns the running deployment and the newest one, which new layers go onto.
func (s status) bootedAndNewest() (booted, newest deployment, ok bool) {
	if len(s.Deployments) == 0 {
		return deployment{}, deployment{}, false
	}
	i := slices.IndexFunc(s.Deployments, func(d deployment) bool { return d.Booted })
	if i < 0 {
		return deployment{}, deployment{}, false
	}
	return s.Deployments[i], s.Deployments[0], true
}

// base is the commit of the image under the deployment's layered packages.
func (d deployment) base() string {
	if d.BaseChecksum != "" {
		return d.BaseChecksum
	}
	return d.Checksum
}

// dryRun is what "rpm-ostree install --dry-run" would do.
type dryRun struct {
	install []nevra
	// other are the headings of any other part of the transaction, such as "Removing 1 packages:".
	other []string
}

type nevra struct {
	name, version, release, arch string
}

var heading = regexp.MustCompile(`^([A-Za-z]+) \d+ packages?:$`)

// parseDryRun reads the output of "rpm-ostree install --dry-run". After resolving dependencies it lists the
// packages to install:
//
//	Installing 3 packages:
//	  gnome-tweaks-49.0-4.fc45.noarch (fedora)
//	  htop-3.5.3-1.fc45.x86_64 (fedora)
//	Exiting because of '--dry-run' option
//
// ok is false unless the output reaches that last line and every package line can be read.
func parseDryRun(out string) (plan dryRun, ok bool) {
	section := ""
	done := false
	for _, line := range strings.Split(out, "\n") {
		trimmed := strings.TrimSpace(line)
		switch {
		case trimmed == "":
		case strings.HasPrefix(trimmed, "Exiting because of '--dry-run'"):
			done = true
		case line[0] == ' ' || line[0] == '\t':
			if section != "Installing" {
				continue // the packages of another part, already counted by its heading
			}
			p, ok := parseNEVRA(strings.Fields(trimmed)[0])
			if !ok {
				return dryRun{}, false
			}
			plan.install = append(plan.install, p)
		default:
			// A heading starts a part of the transaction; any part but Installing changes the image.
			section = ""
			if m := heading.FindStringSubmatch(trimmed); m != nil && m[1] == "Installing" {
				section = m[1]
			} else if strings.HasSuffix(trimmed, ":") {
				plan.other = append(plan.other, trimmed)
			}
		}
	}
	return plan, done
}

// parseNEVRA splits "hwloc-libs-2.14.0-3.fc45.x86_64", or with an epoch "foo-2:1.0-1.fc45.noarch". The epoch
// is dropped, as rpm -q leaves it out too.
func parseNEVRA(s string) (nevra, bool) {
	dot := strings.LastIndexByte(s, '.')
	if dot < 0 {
		return nevra{}, false
	}
	rest, arch := s[:dot], s[dot+1:]
	r := strings.LastIndexByte(rest, '-')
	if r < 0 {
		return nevra{}, false
	}
	rest, release := rest[:r], rest[r+1:]
	v := strings.LastIndexByte(rest, '-')
	if v <= 0 {
		return nevra{}, false
	}
	name, version := rest[:v], rest[v+1:]
	if _, after, ok := strings.Cut(version, ":"); ok {
		version = after
	}
	if arch == "" || release == "" || version == "" {
		return nevra{}, false
	}
	return nevra{name: name, version: version, release: release, arch: arch}, true
}

// versions returns the version-release of each of the packages the running system has, by "name.arch".
func (b *Backend) versions(ctx context.Context, names []string) (map[string]string, error) {
	args := append([]string{"-q", "--qf", `%{NAME}.%{ARCH} %{VERSION}-%{RELEASE}\n`}, names...)
	out, err := b.Runner.Run(ctx, b.RPM, args, nil)
	// rpm -q fails when any package is missing, and says so ("package foo is not installed") on standard
	// output; it prints a line for each package, so with no output at all it didn't run.
	if strings.TrimSpace(out) == "" {
		if err == nil {
			err = errors.New("rpm -q printed nothing")
		}
		return nil, err
	}
	have := map[string]string{}
	for _, line := range strings.Split(out, "\n") {
		if f := strings.Fields(line); len(f) == 2 {
			have[f[0]] = f[1]
		}
	}
	return have, nil
}

// restartOnly reports whether a package works only after a restart: a kernel, kernel module, driver,
// firmware, boot loader, initramfs or SELinux policy package. Installed live, it would sit unused until
// then.
func restartOnly(name string) bool {
	switch name {
	case "kernel", "shim", "dkms", "akmods", "kmod":
		return true
	case "kernel-headers", "kernel-devel", "kernel-devel-matched", "kernel-tools", "kernel-tools-libs",
		"kernel-doc", "kernel-srpm-macros":
		return false // headers and tools for building and tracing, used from userspace
	}
	for _, p := range []string{"kernel-", "kmod-", "akmod-", "dkms-", "dracut", "grub2", "shim-",
		"systemd-boot", "plymouth", "ostree", "rpm-ostree", "bootc", "bootupd", "selinux-policy", "microcode",
		"linux-firmware", "nvidia-", "xorg-x11-drv-"} {
		if strings.HasPrefix(name, p) {
			return true
		}
	}
	for _, s := range []string{"-firmware", "-selinux", "-dkms"} {
		if strings.HasSuffix(name, s) {
			return true
		}
	}
	return strings.Contains(name, "-kmod")
}

// RescanLaunchers gets the desktop to show launchers that a live apply added to /usr/share/applications.
// GNOME Shell misses them, since the live update doesn't reach its file monitors, but any change to the
// user's own applications folder makes it read every folder again. So this adds a hidden launcher there and
// removes it a moment later.
func RescanLaunchers() {
	data, err := state.DataDir()
	if err != nil {
		return
	}
	dir := filepath.Join(data, "applications")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return
	}
	// Any left behind by an earlier run that didn't finish.
	if old, _ := filepath.Glob(filepath.Join(dir, "uas-rescan-*.desktop")); len(old) > 0 {
		for _, p := range old {
			os.Remove(p)
		}
	}
	f, err := os.CreateTemp(dir, "uas-rescan-*.desktop")
	if err != nil {
		return
	}
	_, err = f.WriteString("[Desktop Entry]\nType=Application\nName=uas\nExec=true\nNoDisplay=true\n")
	f.Close()
	if err == nil {
		time.Sleep(2 * time.Second) // long enough for the desktop to notice it before it goes
	}
	os.Remove(f.Name())
}
