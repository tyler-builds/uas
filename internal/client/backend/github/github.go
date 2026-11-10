// Package github installs apps from GitHub releases: AppImages, and Flatpak bundles. The same backend
// installs Codeberg releases (Backend.Forge), whose checksums the indexer records in the catalog.
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
	"maps"
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
	// Config is the user's config folder (~/.config), where default-app settings and autostart entries are.
	// uas updates the ones that name an AppImage it takes over; empty leaves them alone.
	Config string
}

// DefaultDirs returns the standard locations under $XDG_DATA_HOME.
func DefaultDirs() (Dirs, error) {
	data, err := state.DataDir()
	if err != nil {
		return Dirs{}, err
	}
	// Without a config folder, default-app settings and autostart entries are left alone.
	config, _ := os.UserConfigDir()
	return Dirs{
		AppImages:    filepath.Join(data, "uas", "appimages"),
		Icons:        filepath.Join(data, "uas", "icons"),
		Applications: filepath.Join(data, "applications"),
		Config:       config,
	}, nil
}

// maxAppImage caps a download when the catalog gives no size.
const maxAppImage = 4 << 30

var repoPattern = regexp.MustCompile(`^[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+$`)

// validRef reports whether ref is a well-formed listing ref of a release source.
func validRef(src catalog.SourceID, ref string) bool {
	if strings.Contains(ref, "..") {
		return false
	}
	switch src {
	case catalog.SourceGitHub:
		return repoPattern.MatchString(ref)
	case catalog.SourceCodeberg:
		r, ok := strings.CutPrefix(ref, src.Host()+"/")
		return ok && repoPattern.MatchString(r)
	}
	return false
}

// assetPrefix is where a release source's files for a repo must be: its release downloads.
func assetPrefix(src catalog.SourceID, ref string) string {
	return "https://" + src.Host() + "/" + src.Repo(ref) + "/releases/download/"
}

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
	// Forge is the release source this backend installs from: GitHub (the default) or Codeberg.
	// Backends for each forge share one state file and one set of folders; refs never collide, because
	// Codeberg refs start with their host.
	Forge catalog.SourceID
}

// Source implements backend.Backend.
func (b *Backend) Source() catalog.SourceID {
	if b.Forge == "" {
		return catalog.SourceGitHub
	}
	return b.Forge
}

// Supports implements backend.Backend: the listing needs an AppImage, or a Flatpak bundle when flatpak is
// installed, with a checksum.
func (b *Backend) Supports(l catalog.Listing) bool {
	_, ok := b.Pick(l)
	return l.Source == b.Source() && ok
}

// Pick returns the release file to install: the AppImage, else the Flatpak bundle (assets come best first).
func (b *Backend) Pick(l catalog.Listing) (catalog.Asset, bool) {
	for _, a := range l.Assets {
		if a.SHA256 == "" || !validAsset(l.Source, l.Ref, a) {
			continue
		}
		if a.Kind == catalog.AssetAppImage || (a.Kind == catalog.AssetFlatpak && b.Flatpak != "") {
			return a, true
		}
	}
	return catalog.Asset{}, false
}

func validAsset(src catalog.SourceID, ref string, a catalog.Asset) bool {
	return validRef(src, ref) && strings.HasPrefix(a.URL, assetPrefix(src, ref)) && len(a.SHA256) == 64
}

// key is a file-name-safe form of a ref: "owner__repo", "codeberg.org__owner__repo".
func key(repo string) string { return strings.ReplaceAll(repo, "/", "__") }

// sourceOf is the release source an install came from; installs recorded before Codeberg have
// none, and are GitHub's.
func sourceOf(g state.GitHubInstall) catalog.SourceID {
	if g.Source == "" {
		return catalog.SourceGitHub
	}
	return catalog.SourceID(g.Source)
}

// stateSource is what the state file records for src: nothing for GitHub, as before.
func stateSource(src catalog.SourceID) string {
	if src == catalog.SourceGitHub {
		return ""
	}
	return string(src)
}

// Installed implements backend.Backend.
func (b *Backend) Installed(context.Context) ([]backend.InstalledApp, error) {
	st, err := b.State.Load()
	if err != nil {
		return nil, err
	}
	var apps []backend.InstalledApp
	src := b.Source()
	for _, g := range st.GitHub {
		if sourceOf(g) != src {
			continue
		}
		apps = append(apps, backend.InstalledApp{Source: src, Ref: g.Repo, AppID: g.Repo,
			Name: g.Name, Version: g.Version, Scope: "user", Origin: string(src), Icon: iconURL(g.IconFile),
			Desktop: g.DesktopFile})
	}
	return apps, nil
}

// Install implements backend.Backend.
func (b *Backend) Install(ctx context.Context, l catalog.Listing, progress func(backend.Progress)) error {
	if l.Source != b.Source() {
		return backend.ErrWrongSource
	}
	a, ok := b.Pick(l)
	if !ok {
		return fmt.Errorf("%s: %s has no AppImage or Flatpak bundle with a checksum", l.Source, l.Ref)
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
	p, err := b.place(ctx, tmp, k, launcher{name: l.Name, summary: l.Summary, source: string(l.Source) + ":" + l.Ref,
		oldIcon: old.IconFile, wmclass: old.WMClass, info: info})
	if err != nil {
		return err
	}
	if old.Kind == catalog.AssetFlatpak { // it was installed as a bundle before
		b.uninstallBundle(ctx, old.FlatpakRef, progress)
	}
	st.Put(state.GitHubInstall{Repo: l.Ref, Source: stateSource(l.Source), Name: p.name, Version: l.Version, Asset: a.Name, SHA256: a.SHA256,
		Path: p.path, DesktopFile: p.desktopFile, IconFile: p.iconFile, WMClass: old.WMClass, InstalledAt: b.now()})
	return b.State.Save(st)
}

// placed is an AppImage in place with its launcher.
type placed struct {
	path, desktopFile, iconFile string
	name                        string // the launcher's name
}

// launcher is what uas writes a launcher from.
type launcher struct {
	name, summary string // fill in what the AppImage's own entry lacks
	source        string // recorded in the launcher, such as "github:owner/repo"
	oldIcon       string // the icon of the copy it replaces: kept if the AppImage gives none, else removed
	wmclass       string // the StartupWMClass to write over the AppImage's own, from a launcher uas took over
	info          *appimage.Info
}

// place makes the AppImage at tmp, in Dirs.AppImages, executable and moves it to <k>.AppImage, then writes its
// icon and launcher.
func (b *Backend) place(ctx context.Context, tmp, k string, l launcher) (placed, error) {
	final := filepath.Join(b.Dirs.AppImages, k+".AppImage")
	if err := os.Chmod(tmp, 0o755); err != nil {
		return placed{}, err
	}
	if err := os.Rename(tmp, final); err != nil {
		return placed{}, err
	}
	return b.writeLauncher(ctx, final, k, l)
}

// writeLauncher writes the icon and launcher of the AppImage at final.
func (b *Backend) writeLauncher(ctx context.Context, final, k string, l launcher) (placed, error) {
	icon := "application-x-executable"
	iconFile := ""
	switch {
	case len(l.info.Icon) > 0:
		iconFile = filepath.Join(b.Dirs.Icons, k+l.info.IconExt)
		if err := writeFile(iconFile, l.info.Icon, 0o644); err != nil {
			return placed{}, err
		}
		icon = iconFile
	case l.oldIcon != "" && b.owns(l.oldIcon):
		if _, err := os.Stat(l.oldIcon); err == nil {
			iconFile, icon = l.oldIcon, l.oldIcon
		}
	}
	if l.oldIcon != "" && l.oldIcon != iconFile && b.owns(l.oldIcon) {
		os.Remove(l.oldIcon)
	}
	from := maps.Clone(l.info.Desktop)
	if from == nil {
		from = map[string]string{}
	}
	if l.wmclass != "" {
		from["StartupWMClass"] = l.wmclass
	}
	desktop, err := appimage.DesktopFile(appimage.Entry{Name: l.name, Comment: l.summary, AppImage: final,
		Icon: icon, Source: l.source, WMClass: strings.TrimSuffix(l.info.DesktopName, ".desktop")}, from)
	if err != nil {
		return placed{}, err
	}
	desktopFile := filepath.Join(b.Dirs.Applications, "uas-"+k+".desktop")
	if err := writeFile(desktopFile, []byte(desktop), 0o644); err != nil {
		return placed{}, err
	}
	b.refreshLaunchers(ctx)
	name := l.name
	if n := l.info.Desktop["Name"]; n != "" {
		name = n
	}
	return placed{path: final, desktopFile: desktopFile, iconFile: iconFile, name: name}, nil
}

// RepairLaunchers rewrites the launchers that an older version of uas wrote (see appimage.LauncherVersion),
// for the AppImages installed from GitHub and added from files, reading each AppImage again without running
// it. It also takes over launchers another tool left pointing at an AppImage uas has since moved, matched by
// the app's name.
func (b *Backend) RepairLaunchers(ctx context.Context) error {
	st, err := b.State.Load()
	if err != nil {
		return err
	}
	changed := false
	repair := func(path, desktopFile, iconFile, k, name, source string, wmclass *string) (placed, bool) {
		if path == "" || desktopFile == "" || !b.owns(path) {
			return placed{}, false
		}
		broken := b.brokenLaunchersNamed(name)
		if launcherCurrent(desktopFile) && len(broken) == 0 {
			return placed{}, false
		}
		if _, err := os.Stat(path); err != nil {
			return placed{}, false
		}
		info, err := appimage.Inspect(path)
		if err != nil {
			info = &appimage.Info{}
		}
		if wm := adoptLauncherDetails(info, broken); wm != "" {
			*wmclass = wm
		}
		p, err := b.writeLauncher(ctx, path, k, launcher{name: name, summary: launcherKey(desktopFile, "Comment"),
			source: source, oldIcon: iconFile, wmclass: *wmclass, info: info})
		if err != nil {
			return placed{}, false
		}
		b.takeOver(ctx, broken, p.desktopFile, nil)
		changed = true
		return p, true
	}
	for i, g := range st.GitHub {
		if g.Kind == catalog.AssetFlatpak {
			continue
		}
		if p, ok := repair(g.Path, g.DesktopFile, g.IconFile, key(g.Repo), g.Name, string(sourceOf(g))+":"+g.Repo,
			&st.GitHub[i].WMClass); ok {
			st.GitHub[i].DesktopFile, st.GitHub[i].IconFile = p.desktopFile, p.iconFile
		}
	}
	for i, a := range st.AppImages {
		if p, ok := repair(a.Path, a.DesktopFile, a.IconFile, fileKey(a.ID), a.Name, "file:"+a.ID,
			&st.AppImages[i].WMClass); ok {
			st.AppImages[i].DesktopFile, st.AppImages[i].IconFile = p.desktopFile, p.iconFile
		}
	}
	if !changed {
		return nil
	}
	return b.State.Save(st)
}

// launcherCurrent reports whether a launcher was written by this version of uas.
func launcherCurrent(desktopFile string) bool {
	return launcherKey(desktopFile, "X-UAS-Launcher") == fmt.Sprint(appimage.LauncherVersion)
}

// launcherKey reads one key of a launcher uas wrote.
func launcherKey(desktopFile, k string) string {
	b, err := os.ReadFile(desktopFile)
	if err != nil {
		return ""
	}
	for _, line := range strings.Split(string(b), "\n") {
		if v, ok := strings.CutPrefix(line, k+"="); ok {
			return v
		}
	}
	return ""
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
		if sourceOf(g) != b.Source() {
			continue
		}
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
	if a.Source != b.Source() {
		return backend.ErrWrongSource
	}
	if b.Latest == nil {
		return fmt.Errorf("%s: no catalog to check for updates", a.Source)
	}
	st, err := b.State.Load()
	if err != nil {
		return err
	}
	cur, ok := st.Find(a.Ref)
	if !ok {
		return fmt.Errorf("%s: %s is not installed", a.Source, a.Ref)
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
	if a.Source != b.Source() {
		return backend.ErrWrongSource
	}
	st, err := b.State.Load()
	if err != nil {
		return err
	}
	g, ok := st.Find(a.Ref)
	if !ok {
		return fmt.Errorf("%s: %s is not installed", a.Source, a.Ref)
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
