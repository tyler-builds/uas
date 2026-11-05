// Package github installs apps from GitHub releases: AppImages, and Flatpak bundles.
//
// An AppImage is downloaded, checked against the SHA-256 GitHub publishes for it, made executable and given
// a launcher (desktop entry and icon) read from inside the image without running it. A Flatpak bundle is
// downloaded and checked the same way, then installed for the user with flatpak, which also fetches the
// runtime it needs from Flathub. uas records each install in its state file, since nothing else ties it to
// its GitHub release. RPMs from releases are listed in the catalog but not installable.
package github

import (
	"context"
	"crypto/sha1"
	"crypto/sha256"
	"crypto/sha512"
	"encoding/hex"
	"errors"
	"fmt"
	"hash"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/tyler-builds/uas/internal/catalog"
	"github.com/tyler-builds/uas/internal/client/appimage"
	"github.com/tyler-builds/uas/internal/client/backend"
	"github.com/tyler-builds/uas/internal/client/run"
	"github.com/tyler-builds/uas/internal/client/state"
)

// Dirs are where installs go.
type Dirs struct {
	AppImages    string // ~/.local/share/uas/appimages
	Icons        string // ~/.local/share/uas/icons
	Applications string // ~/.local/share/applications
}

// DefaultDirs returns the standard locations under $XDG_DATA_HOME.
func DefaultDirs() (Dirs, error) {
	data, err := state.DataDir()
	if err != nil {
		return Dirs{}, err
	}
	return Dirs{
		AppImages:    filepath.Join(data, "uas", "appimages"),
		Icons:        filepath.Join(data, "uas", "icons"),
		Applications: filepath.Join(data, "applications"),
	}, nil
}

// maxAppImage caps a download when the catalog gives no size.
const maxAppImage = 4 << 30

var repoPattern = regexp.MustCompile(`^[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+$`)

// Backend installs AppImages.
type Backend struct {
	State  state.Store
	Dirs   Dirs
	Client *http.Client
	// Latest returns the current catalog listing for a repo, for updates.
	Latest func(ctx context.Context, repo string) (catalog.Listing, error)
	// Runner runs flatpak for bundles and, if set, update-desktop-database after launchers change.
	Runner run.Runner
	// Flatpak is the flatpak binary. Without it, Flatpak bundles aren't installable.
	Flatpak string
	Now     func() time.Time
}

// Source implements backend.Backend.
func (b *Backend) Source() catalog.SourceID { return catalog.SourceGitHub }

// Supports implements backend.Backend: the listing needs an AppImage, or a Flatpak bundle when flatpak is
// installed, with a published checksum.
func (b *Backend) Supports(l catalog.Listing) bool {
	_, ok := b.Pick(l)
	return l.Source == catalog.SourceGitHub && ok
}

// Pick returns the release file to install: the AppImage, else the Flatpak bundle (assets come best first).
func (b *Backend) Pick(l catalog.Listing) (catalog.Asset, bool) {
	for _, a := range l.Assets {
		if a.SHA256 == "" || !validAsset(l.Ref, a) {
			continue
		}
		if a.Kind == catalog.AssetAppImage || (a.Kind == catalog.AssetFlatpak && b.Flatpak != "") {
			return a, true
		}
	}
	return catalog.Asset{}, false
}

func validAsset(repo string, a catalog.Asset) bool {
	return repoPattern.MatchString(repo) && !strings.Contains(repo, "..") &&
		strings.HasPrefix(a.URL, "https://github.com/"+repo+"/releases/download/") && len(a.SHA256) == 64
}

// key is a file-name-safe form of "owner/repo".
func key(repo string) string { return strings.ReplaceAll(repo, "/", "__") }

// Installed implements backend.Backend.
func (b *Backend) Installed(context.Context) ([]backend.InstalledApp, error) {
	st, err := b.State.Load()
	if err != nil {
		return nil, err
	}
	var apps []backend.InstalledApp
	for _, g := range st.GitHub {
		apps = append(apps, backend.InstalledApp{Source: catalog.SourceGitHub, Ref: g.Repo, AppID: g.Repo,
			Name: g.Name, Version: g.Version, Scope: "user", Origin: "github", Icon: iconURL(g.IconFile)})
	}
	return apps, nil
}

// Install implements backend.Backend.
func (b *Backend) Install(ctx context.Context, l catalog.Listing, progress func(backend.Progress)) error {
	if l.Source != catalog.SourceGitHub {
		return backend.ErrWrongSource
	}
	a, ok := b.Pick(l)
	if !ok {
		return fmt.Errorf("github: %s has no AppImage or Flatpak bundle with a published checksum", l.Ref)
	}
	for _, d := range []string{b.Dirs.AppImages, b.Dirs.Icons, b.Dirs.Applications} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			return err
		}
	}
	if a.Kind == catalog.AssetFlatpak {
		return b.installBundle(ctx, l, a, progress)
	}

	k := key(l.Ref)
	tmp := filepath.Join(b.Dirs.AppImages, "."+k+".download")
	defer os.Remove(tmp)
	if err := b.download(ctx, a, tmp, progress); err != nil {
		return err
	}

	report(progress, "Reading the app's launcher and icon")
	info, err := appimage.Inspect(tmp)
	if err != nil {
		// Still installable; the launcher falls back to catalog details and a generic icon.
		report(progress, "Could not read the launcher from the AppImage: "+err.Error())
		info = &appimage.Info{}
	}
	st, err := b.State.Load()
	if err != nil {
		return err
	}
	old, _ := st.Find(l.Ref)
	p, err := b.place(ctx, tmp, k, l.Name, l.Summary, "github:"+l.Ref, old.IconFile, info)
	if err != nil {
		return err
	}
	if old.Kind == catalog.AssetFlatpak { // it was installed as a bundle before
		b.uninstallBundle(ctx, old.FlatpakRef, progress)
	}
	st.Put(state.GitHubInstall{Repo: l.Ref, Name: p.name, Version: l.Version, Asset: a.Name, SHA256: a.SHA256,
		Path: p.path, DesktopFile: p.desktopFile, IconFile: p.iconFile, InstalledAt: b.now()})
	return b.State.Save(st)
}

// placed is an AppImage in place with its launcher.
type placed struct {
	path, desktopFile, iconFile string
	name                        string // the launcher's name
}

// place makes the AppImage at tmp, in Dirs.AppImages, executable and moves it to <k>.AppImage, then writes its
// icon and launcher from info. name and summary fill in what the AppImage's own entry lacks; source is
// recorded in the launcher; oldIcon is the icon of the copy it replaces, removed if the new one differs.
func (b *Backend) place(ctx context.Context, tmp, k, name, summary, source, oldIcon string,
	info *appimage.Info) (placed, error) {
	final := filepath.Join(b.Dirs.AppImages, k+".AppImage")
	if err := os.Chmod(tmp, 0o755); err != nil {
		return placed{}, err
	}
	if err := os.Rename(tmp, final); err != nil {
		return placed{}, err
	}
	icon := "application-x-executable"
	iconFile := ""
	if len(info.Icon) > 0 {
		iconFile = filepath.Join(b.Dirs.Icons, k+info.IconExt)
		if err := writeFile(iconFile, info.Icon, 0o644); err != nil {
			return placed{}, err
		}
		icon = iconFile
	}
	if oldIcon != "" && oldIcon != iconFile && b.owns(oldIcon) {
		os.Remove(oldIcon)
	}
	desktop, err := appimage.DesktopFile(appimage.Entry{Name: name, Comment: summary, AppImage: final,
		Icon: icon, Source: source}, info.Desktop)
	if err != nil {
		return placed{}, err
	}
	desktopFile := filepath.Join(b.Dirs.Applications, "uas-"+k+".desktop")
	if err := writeFile(desktopFile, []byte(desktop), 0o644); err != nil {
		return placed{}, err
	}
	b.refreshLaunchers(ctx)
	if n := info.Desktop["Name"]; n != "" {
		name = n
	}
	return placed{path: final, desktopFile: desktopFile, iconFile: iconFile, name: name}, nil
}

// download fetches an asset to dst, checking its size and SHA-256.
func (b *Backend) download(ctx context.Context, a catalog.Asset, dst string, progress func(backend.Progress)) error {
	_, err := b.fetch(ctx, remoteFile{URL: a.URL, Name: a.Name, Size: a.Size, SHA256: a.SHA256}, dst, progress)
	return err
}

// fetch downloads f to dst, checking its size and every checksum it has (it must have one), and returns the
// file's SHA-256 and SHA-1.
func (b *Backend) fetch(ctx context.Context, f remoteFile, dst string,
	progress func(backend.Progress)) (sums, error) {
	if f.SHA256 == "" && f.SHA512 == "" && f.SHA1 == "" {
		return sums{}, fmt.Errorf("download %s: no published checksum to check it against", f.Name)
	}
	resp, err := b.get(ctx, f.URL)
	if err != nil {
		return sums{}, fmt.Errorf("download %s: %w", f.Name, err)
	}
	defer resp.Body.Close()
	limit := int64(maxAppImage)
	if f.Size > 0 {
		limit = f.Size
	}
	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		return sums{}, err
	}
	h := newHasher()
	pw := &progressWriter{total: f.Size, name: f.Name, report: progress}
	n, err := io.Copy(io.MultiWriter(out, h, pw), io.LimitReader(resp.Body, limit+1))
	if cerr := out.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return sums{}, fmt.Errorf("download %s: %w", f.Name, err)
	}
	if n > limit || (f.Size > 0 && n != f.Size) {
		return sums{}, fmt.Errorf("download %s: got %d bytes, expected %d", f.Name, n, f.Size)
	}
	got := h.sums()
	if (f.SHA256 != "" && got.SHA256 != f.SHA256) || (f.SHA512 != "" && got.SHA512 != f.SHA512) ||
		(f.SHA1 != "" && got.SHA1 != f.SHA1) {
		return sums{}, fmt.Errorf("download %s: checksum mismatch; the file is not the one published", f.Name)
	}
	report(progress, "Checksum verified")
	return got, nil
}

// sums are a file's checksums, in hex.
type sums struct{ SHA256, SHA512, SHA1 string }

// hasher computes sums while a file is written through it.
type hasher struct{ h256, h512, h1 hash.Hash }

func newHasher() *hasher { return &hasher{sha256.New(), sha512.New(), sha1.New()} }

func (h *hasher) Write(p []byte) (int, error) {
	h.h256.Write(p)
	h.h512.Write(p)
	h.h1.Write(p)
	return len(p), nil
}

func (h *hasher) sums() sums {
	return sums{hex.EncodeToString(h.h256.Sum(nil)), hex.EncodeToString(h.h512.Sum(nil)),
		hex.EncodeToString(h.h1.Sum(nil))}
}

// Updates implements backend.UpdateChecker: an app has an update when the release file the catalog offers
// now has a different checksum from the installed one.
func (b *Backend) Updates(ctx context.Context) ([]backend.AvailableUpdate, error) {
	if b.Latest == nil {
		return nil, nil
	}
	st, err := b.State.Load()
	if err != nil {
		return nil, err
	}
	var ups []backend.AvailableUpdate
	for _, g := range st.GitHub {
		l, err := b.Latest(ctx, g.Repo)
		if err != nil {
			continue // no longer in the catalog: nothing to offer
		}
		if a, ok := b.Pick(l); ok && a.SHA256 != g.SHA256 {
			ups = append(ups, backend.AvailableUpdate{Ref: g.Repo, Scope: "user", Version: l.Version})
		}
	}
	return ups, nil
}

// Update implements backend.Backend: it installs the catalog's current release if it differs.
func (b *Backend) Update(ctx context.Context, a backend.InstalledApp, progress func(backend.Progress)) error {
	if a.Source != catalog.SourceGitHub {
		return backend.ErrWrongSource
	}
	if b.Latest == nil {
		return errors.New("github: no catalog to check for updates")
	}
	st, err := b.State.Load()
	if err != nil {
		return err
	}
	cur, ok := st.Find(a.Ref)
	if !ok {
		return fmt.Errorf("github: %s is not installed", a.Ref)
	}
	l, err := b.Latest(ctx, a.Ref)
	if err != nil {
		return err
	}
	if asset, ok := b.Pick(l); ok && asset.SHA256 == cur.SHA256 {
		report(progress, "Already up to date ("+cur.Version+")")
		return nil
	}
	report(progress, fmt.Sprintf("Updating from %s to %s", cur.Version, l.Version))
	return b.Install(ctx, l, progress)
}

// Remove implements backend.Backend. It deletes the AppImage, its launcher and icon, or uninstalls the
// Flatpak bundle. App settings in the home folder are left alone.
func (b *Backend) Remove(ctx context.Context, a backend.InstalledApp, progress func(backend.Progress)) error {
	if a.Source != catalog.SourceGitHub {
		return backend.ErrWrongSource
	}
	st, err := b.State.Load()
	if err != nil {
		return err
	}
	g, ok := st.Find(a.Ref)
	if !ok {
		return fmt.Errorf("github: %s is not installed", a.Ref)
	}
	if g.Kind == catalog.AssetFlatpak {
		if err := b.uninstallBundle(ctx, g.FlatpakRef, progress); err != nil {
			return err
		}
	} else {
		if err := b.removeFiles(g); err != nil {
			return err
		}
		b.refreshLaunchers(ctx)
	}
	report(progress, "Removed "+g.Name)
	st.Delete(a.Ref)
	return b.State.Save(st)
}

// removeFiles deletes an AppImage install's files.
func (b *Backend) removeFiles(g state.GitHubInstall) error {
	for _, p := range []string{g.Path, g.DesktopFile, g.IconFile} {
		if p == "" || !b.owns(p) {
			continue
		}
		if err := os.Remove(p); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
	}
	return nil
}

// owns reports whether p is inside one of the directories uas manages, so a corrupted state file cannot
// make Remove delete anything else.
func (b *Backend) owns(p string) bool {
	for _, d := range []string{b.Dirs.AppImages, b.Dirs.Icons, b.Dirs.Applications} {
		if rel, err := filepath.Rel(d, p); err == nil && !strings.HasPrefix(rel, "..") && !filepath.IsAbs(rel) &&
			!strings.Contains(rel, string(filepath.Separator)) {
			return true
		}
	}
	return false
}

func (b *Backend) refreshLaunchers(ctx context.Context) {
	if b.Runner != nil {
		// Optional: desktops also notice new files on their own.
		_, _ = b.Runner.Run(ctx, "update-desktop-database", []string{"-q", b.Dirs.Applications}, nil)
	}
}

func (b *Backend) now() time.Time {
	if b.Now != nil {
		return b.Now()
	}
	return time.Now()
}

func writeFile(path string, data []byte, perm os.FileMode) error {
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, perm); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

func report(progress func(backend.Progress), msg string) {
	if progress != nil {
		progress(backend.Progress{Message: msg})
	}
}

// progressWriter reports download progress every 5%.
type progressWriter struct {
	total, done int64
	lastPct     int64
	name        string
	report      func(backend.Progress)
}

func (p *progressWriter) Write(b []byte) (int, error) {
	p.done += int64(len(b))
	if p.total > 0 && p.report != nil {
		if pct := p.done * 100 / p.total; pct >= p.lastPct+5 || p.done == p.total {
			p.lastPct = pct
			p.report(backend.Progress{Message: fmt.Sprintf("Downloading %s… %d%%", p.name, pct)})
		}
	}
	return len(b), nil
}
