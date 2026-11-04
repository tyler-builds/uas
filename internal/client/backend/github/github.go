// Package github installs AppImages from GitHub releases.
//
// The AppImage is downloaded, checked against the SHA-256 GitHub publishes for it, made executable and given
// a launcher (desktop entry and icon) read from inside the image without running it. uas records the install
// in its state file, since no package manager tracks AppImages. Flatpak bundles and RPMs from releases are
// listed in the catalog but not installable yet.
package github

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
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
	// Runner, if set, runs update-desktop-database after launchers change.
	Runner run.Runner
	Now    func() time.Time
}

// Source implements backend.Backend.
func (b *Backend) Source() catalog.SourceID { return catalog.SourceGitHub }

// Supports implements backend.Backend: the listing needs an AppImage with a published checksum.
func (b *Backend) Supports(l catalog.Listing) bool {
	a, ok := l.Asset(catalog.AssetAppImage)
	return l.Source == catalog.SourceGitHub && ok && a.SHA256 != "" && validAsset(l.Ref, a)
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
			Name: g.Name, Version: g.Version, Scope: "user", Origin: "github"})
	}
	return apps, nil
}

// Install implements backend.Backend.
func (b *Backend) Install(ctx context.Context, l catalog.Listing, progress func(backend.Progress)) error {
	if l.Source != catalog.SourceGitHub {
		return backend.ErrWrongSource
	}
	if !b.Supports(l) {
		return fmt.Errorf("github: %s has no AppImage with a published checksum", l.Ref)
	}
	a, _ := l.Asset(catalog.AssetAppImage)
	for _, d := range []string{b.Dirs.AppImages, b.Dirs.Icons, b.Dirs.Applications} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			return err
		}
	}

	k := key(l.Ref)
	final := filepath.Join(b.Dirs.AppImages, k+".AppImage")
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
	if err := os.Chmod(tmp, 0o755); err != nil {
		return err
	}
	if err := os.Rename(tmp, final); err != nil {
		return err
	}

	st, err := b.State.Load()
	if err != nil {
		return err
	}
	old, _ := st.Find(l.Ref)
	icon := "application-x-executable"
	iconFile := ""
	if len(info.Icon) > 0 {
		iconFile = filepath.Join(b.Dirs.Icons, k+info.IconExt)
		if err := writeFile(iconFile, info.Icon, 0o644); err != nil {
			return err
		}
		icon = iconFile
	}
	if old.IconFile != "" && old.IconFile != iconFile {
		os.Remove(old.IconFile)
	}
	desktop, err := appimage.DesktopFile(appimage.Entry{Name: l.Name, Comment: l.Summary, AppImage: final,
		Icon: icon, Source: "github:" + l.Ref}, info.Desktop)
	if err != nil {
		return err
	}
	desktopFile := filepath.Join(b.Dirs.Applications, "uas-"+k+".desktop")
	if err := writeFile(desktopFile, []byte(desktop), 0o644); err != nil {
		return err
	}
	b.refreshLaunchers(ctx)

	name := info.Desktop["Name"]
	if name == "" {
		name = l.Name
	}
	st.Put(state.GitHubInstall{Repo: l.Ref, Name: name, Version: l.Version, Asset: a.Name, SHA256: a.SHA256,
		Path: final, DesktopFile: desktopFile, IconFile: iconFile, InstalledAt: b.now()})
	return b.State.Save(st)
}

// download fetches an asset to dst, checking its size and SHA-256.
func (b *Backend) download(ctx context.Context, a catalog.Asset, dst string, progress func(backend.Progress)) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, a.URL, nil)
	if err != nil {
		return err
	}
	req.Header.Set("User-Agent", "uas")
	resp, err := b.Client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("download %s: %s", a.Name, resp.Status)
	}
	limit := int64(maxAppImage)
	if a.Size > 0 {
		limit = a.Size
	}
	f, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	h := sha256.New()
	pw := &progressWriter{total: a.Size, name: a.Name, report: progress}
	n, err := io.Copy(io.MultiWriter(f, h, pw), io.LimitReader(resp.Body, limit+1))
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return fmt.Errorf("download %s: %w", a.Name, err)
	}
	if n > limit || (a.Size > 0 && n != a.Size) {
		return fmt.Errorf("download %s: got %d bytes, expected %d", a.Name, n, a.Size)
	}
	if got := hex.EncodeToString(h.Sum(nil)); got != a.SHA256 {
		return fmt.Errorf("download %s: checksum mismatch; the file is not the one GitHub published", a.Name)
	}
	report(progress, "Checksum verified")
	return nil
}

// Updates implements backend.UpdateChecker: an app has an update when the catalog's current AppImage has a
// different checksum from the installed one.
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
		if a, ok := l.Asset(catalog.AssetAppImage); ok && a.SHA256 != "" && a.SHA256 != g.SHA256 {
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
	if asset, ok := l.Asset(catalog.AssetAppImage); ok && asset.SHA256 == cur.SHA256 {
		report(progress, "Already up to date ("+cur.Version+")")
		return nil
	}
	report(progress, fmt.Sprintf("Updating from %s to %s", cur.Version, l.Version))
	return b.Install(ctx, l, progress)
}

// Remove implements backend.Backend. It deletes the AppImage, its launcher and icon. App settings in the
// home folder are left alone.
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
	for _, p := range []string{g.Path, g.DesktopFile, g.IconFile} {
		if p == "" || !b.owns(p) {
			continue
		}
		if err := os.Remove(p); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
	}
	report(progress, "Removed "+g.Name)
	b.refreshLaunchers(ctx)
	st.Delete(a.Ref)
	return b.State.Save(st)
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
