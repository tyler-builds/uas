package github

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"

	"github.com/tyler-builds/uas/internal/catalog"
	"github.com/tyler-builds/uas/internal/client/backend"
	"github.com/tyler-builds/uas/internal/client/backend/flatpak"
	"github.com/tyler-builds/uas/internal/client/state"
)

// bundleRefPattern finds the ref a Flatpak bundle installs, which its header spells out near the start:
// app/org.example.App/x86_64/stable.
var bundleRefPattern = regexp.MustCompile(`app/[A-Za-z][A-Za-z0-9_-]*(?:\.[A-Za-z0-9_-]+)+/(?:x86_64|aarch64)/[A-Za-z0-9][A-Za-z0-9._-]*`)

var refAppID = regexp.MustCompile(`^app/([^/]+)/`)

// bundleHeader is how much of a bundle is searched for its ref.
const bundleHeader = 64 << 10

// bundleRef reads the ref of the app a Flatpak bundle file installs.
func bundleRef(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	head := make([]byte, bundleHeader)
	n, err := io.ReadFull(f, head)
	if err != nil && !errors.Is(err, io.ErrUnexpectedEOF) {
		return "", err
	}
	ref := bundleRefPattern.Find(head[:n])
	if ref == nil {
		return "", errors.New("not a Flatpak bundle: no app ref in its header")
	}
	return string(ref), nil
}

// installBundle installs a Flatpak bundle from a release for the user. Flatpak fetches the runtime it needs
// from Flathub, which is added as a user remote first if missing. When a new version's bundle has another
// branch (some name it after the version), the old branch is removed after the new one is in.
func (b *Backend) installBundle(ctx context.Context, l catalog.Listing, a catalog.Asset,
	progress func(backend.Progress)) error {
	if b.Runner == nil {
		return errors.New("github: no runner for flatpak")
	}
	tmp := filepath.Join(b.Dirs.AppImages, "."+key(l.Ref)+".flatpak")
	defer os.Remove(tmp)
	if err := b.download(ctx, a, tmp, progress); err != nil {
		return err
	}
	ref, err := bundleRef(tmp)
	if err != nil {
		return fmt.Errorf("github: %s: %w", a.Name, err)
	}
	report(progress, "Adding Flathub for the app's runtime")
	if _, err := b.Runner.Run(ctx, b.Flatpak, []string{"remote-add", "--user", "--if-not-exists",
		flatpak.FlathubRemote, flatpak.FlathubRepoFile}, nil); err != nil {
		return err
	}
	report(progress, "Installing "+ref)
	if _, err := b.Runner.Run(ctx, b.Flatpak, []string{"install", "--user", "--noninteractive", "--reinstall",
		"--bundle", tmp}, func(line string) { report(progress, line) }); err != nil {
		return err
	}

	st, err := b.State.Load()
	if err != nil {
		return err
	}
	old, _ := st.Find(l.Ref)
	switch {
	case old.Kind == catalog.AssetFlatpak && old.FlatpakRef != "" && old.FlatpakRef != ref:
		b.uninstallBundle(ctx, old.FlatpakRef, progress)
	case old.Repo != "" && old.Kind != catalog.AssetFlatpak: // it was an AppImage before
		b.removeFiles(old)
		b.refreshLaunchers(ctx)
	}
	st.Put(state.GitHubInstall{Repo: l.Ref, Source: stateSource(l.Source), Kind: catalog.AssetFlatpak,
		FlatpakRef: ref, Name: l.Name,
		Version: l.Version, Asset: a.Name, SHA256: a.SHA256, InstalledAt: b.now()})
	return b.State.Save(st)
}

// uninstallBundle removes an app installed from a bundle. Its data in ~/.var/app is kept.
func (b *Backend) uninstallBundle(ctx context.Context, ref string, progress func(backend.Progress)) error {
	if !bundleRefPattern.MatchString(ref) || b.Runner == nil {
		return fmt.Errorf("github: refusing to uninstall %q", ref)
	}
	report(progress, "Uninstalling "+ref)
	_, err := b.Runner.Run(ctx, b.Flatpak, []string{"uninstall", "--user", "--noninteractive", ref},
		func(line string) { report(progress, line) })
	return err
}

// BundleApps returns the IDs of the apps installed from Flatpak bundles, which the Flatpak backend leaves
// to this one.
func (b *Backend) BundleApps() map[string]bool {
	st, err := b.State.Load()
	if err != nil {
		return nil
	}
	ids := map[string]bool{}
	for _, g := range st.GitHub {
		if g.Kind == catalog.AssetFlatpak {
			if m := refAppID.FindStringSubmatch(g.FlatpakRef); m != nil {
				ids[m[1]] = true
			}
		}
	}
	return ids
}
