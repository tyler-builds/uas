package github

import (
	"context"
	"crypto/sha1"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/tyler-builds/uas/internal/catalog"
	"github.com/tyler-builds/uas/internal/client/appimage"
	"github.com/tyler-builds/uas/internal/client/backend"
	"github.com/tyler-builds/uas/internal/client/state"
)

// File is an AppImage file the user wants uas to manage.
type File struct {
	Path string
	Info *appimage.Info
	// Update is where the AppImage says its updates come from; UpdateErr says why it has none uas can follow.
	Update    appimage.Update
	UpdateErr error
}

// ReadFile checks that path is an AppImage uas can manage and reads its launcher, icon and update
// information, without running it.
func ReadFile(path string) (*File, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return nil, err
	}
	st, err := os.Stat(abs)
	if err != nil {
		return nil, err
	}
	if !st.Mode().IsRegular() {
		return nil, fmt.Errorf("%s is not a file", path)
	}
	f, err := os.Open(abs)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	info, err := appimage.InspectAt(f)
	if err != nil {
		return nil, fmt.Errorf("%s is not an AppImage uas can add: %w", filepath.Base(path), err)
	}
	file := &File{Path: abs, Info: info}
	raw, err := appimage.UpdateInfo(f)
	if err == nil {
		file.Update, err = appimage.ParseUpdate(raw)
	}
	file.UpdateErr = err
	return file, nil
}

// take copies the user's file into Dirs.AppImages as a temporary file and returns its path and checksums.
// The user's file stays until the add has succeeded.
func (b *Backend) take(f *File, k string) (tmp, sum256, sum1 string, err error) {
	if b.owns(f.Path) {
		return "", "", "", errors.New("this AppImage is already managed by uas")
	}
	if err := os.MkdirAll(b.Dirs.AppImages, 0o755); err != nil {
		return "", "", "", err
	}
	for _, d := range []string{b.Dirs.Icons, b.Dirs.Applications} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			return "", "", "", err
		}
	}
	src, err := os.Open(f.Path)
	if err != nil {
		return "", "", "", err
	}
	defer src.Close()
	tmp = filepath.Join(b.Dirs.AppImages, "."+k+".adding")
	dst, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		return "", "", "", err
	}
	h256, h1 := sha256.New(), sha1.New()
	// On Linux, copying file to file uses copy_file_range, which shares the data on btrfs instead of
	// copying it.
	_, err = io.Copy(dst, io.TeeReader(src, io.MultiWriter(h256, h1)))
	if cerr := dst.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		os.Remove(tmp)
		return "", "", "", err
	}
	return tmp, hex.EncodeToString(h256.Sum(nil)), hex.EncodeToString(h1.Sum(nil)), nil
}

// Adopt adds an AppImage file as the install of a GitHub listing, so it updates from the catalog like an
// AppImage uas downloaded. The file is moved into uas's AppImage folder.
func (b *Backend) Adopt(ctx context.Context, f *File, l catalog.Listing, progress func(backend.Progress)) error {
	if l.Source != catalog.SourceGitHub || !repoPattern.MatchString(l.Ref) {
		return backend.ErrWrongSource
	}
	k := key(l.Ref)
	tmp, sum256, _, err := b.take(f, k)
	if err != nil {
		return err
	}
	defer os.Remove(tmp)
	st, err := b.State.Load()
	if err != nil {
		return err
	}
	old, _ := st.Find(l.Ref)
	version, asset := fileVersion(f.Info), filepath.Base(f.Path)
	if a, ok := b.Pick(l); ok && a.SHA256 == sum256 {
		version, asset = l.Version, a.Name
	}
	p, err := b.place(ctx, tmp, k, l.Name, l.Summary, "github:"+l.Ref, old.IconFile, f.Info)
	if err != nil {
		return err
	}
	if old.Kind == catalog.AssetFlatpak {
		b.uninstallBundle(ctx, old.FlatpakRef, progress)
	}
	st.Put(state.GitHubInstall{Repo: l.Ref, Name: p.name, Version: version, Asset: asset, SHA256: sum256,
		Path: p.path, DesktopFile: p.desktopFile, IconFile: p.iconFile, InstalledAt: b.now()})
	if err := b.State.Save(st); err != nil {
		return err
	}
	report(progress, "Added "+p.name+"; it updates from github.com/"+l.Ref)
	os.Remove(f.Path)
	return nil
}

// Local manages AppImages added from files that are not in the catalog. Each updates from where its own
// update information points: a GitHub release or a .zsync file. A download is used only if it matches a
// checksum published with it.
type Local struct {
	GH *Backend
	// API is GitHub's REST API (DefaultAPI). Checks are unauthenticated, which GitHub limits to 60 an hour,
	// so results are kept for an hour.
	API string

	mu     sync.Mutex
	latest map[string]cachedFile
}

type cachedFile struct {
	file remoteFile
	at   time.Time
}

const latestTTL = time.Hour

var idChars = regexp.MustCompile(`[^A-Za-z0-9_.-]+`)

// fileID is the ID an added AppImage is kept under.
func fileID(f *File) string {
	id := strings.Trim(idChars.ReplaceAllString(f.Info.AppID(), "-"), ".-")
	if id == "" {
		name := strings.TrimSuffix(filepath.Base(f.Path), filepath.Ext(f.Path))
		id = strings.Trim(idChars.ReplaceAllString(name, "-"), ".-")
	}
	if id == "" {
		id = "appimage"
	}
	return id
}

func fileKey(id string) string { return "file__" + id }

// Source implements backend.Backend.
func (l *Local) Source() catalog.SourceID { return catalog.SourceAppImage }

// Supports implements backend.Backend: the catalog has nothing from this source.
func (l *Local) Supports(catalog.Listing) bool { return false }

// Install implements backend.Backend. AppImages are added from files with Add.
func (l *Local) Install(context.Context, catalog.Listing, func(backend.Progress)) error {
	return errors.New("appimage: add AppImage files with Add")
}

// Installed implements backend.Backend.
func (l *Local) Installed(context.Context) ([]backend.InstalledApp, error) {
	st, err := l.GH.State.Load()
	if err != nil {
		return nil, err
	}
	var apps []backend.InstalledApp
	for _, a := range st.AppImages {
		apps = append(apps, backend.InstalledApp{Source: catalog.SourceAppImage, Ref: a.ID, AppID: a.ID,
			Name: a.Name, Version: a.Version, Scope: "user", Origin: updateOrigin(a.Update), Icon: iconURL(a.IconFile)})
	}
	return apps, nil
}

// iconURL is the file:// URL of an icon file, or "".
func iconURL(path string) string {
	if path == "" {
		return ""
	}
	return "file://" + path
}

// updateOrigin says where an added AppImage updates from, for people: "github.com/owner/repo", a host name,
// or "" when it can't update.
func updateOrigin(info string) string {
	u, err := appimage.ParseUpdate(info)
	switch {
	case err != nil:
		return ""
	case u.GitHubRepo() != "":
		return "github.com/" + u.GitHubRepo()
	}
	if host, _, ok := strings.Cut(strings.TrimPrefix(u.URL, "https://"), "/"); ok {
		return host
	}
	return ""
}

// Add moves an AppImage file into uas's AppImage folder and gives it a launcher. An AppImage added before
// with the same ID is replaced.
func (l *Local) Add(ctx context.Context, f *File, progress func(backend.Progress)) (backend.InstalledApp, error) {
	id := fileID(f)
	k := fileKey(id)
	tmp, sum256, sum1, err := l.GH.take(f, k)
	if err != nil {
		return backend.InstalledApp{}, err
	}
	defer os.Remove(tmp)
	st, err := l.GH.State.Load()
	if err != nil {
		return backend.InstalledApp{}, err
	}
	old, _ := st.FindAppImage(id)
	name := strings.TrimSuffix(filepath.Base(f.Path), filepath.Ext(f.Path))
	p, err := l.GH.place(ctx, tmp, k, name, "", "file:"+id, old.IconFile, f.Info)
	if err != nil {
		return backend.InstalledApp{}, err
	}
	update := ""
	if f.UpdateErr == nil {
		update = f.Update.String()
	}
	a := state.FileAppImage{ID: id, Name: p.name, Version: fileVersion(f.Info), Update: update,
		SHA256: sum256, SHA1: sum1, Path: p.path, DesktopFile: p.desktopFile, IconFile: p.iconFile,
		InstalledAt: l.GH.now()}
	st.PutAppImage(a)
	if err := l.GH.State.Save(st); err != nil {
		return backend.InstalledApp{}, err
	}
	os.Remove(f.Path)
	if update == "" {
		report(progress, "Added "+p.name+". It doesn't say where its updates come from, so it won't update.")
	} else {
		report(progress, "Added "+p.name+"; it updates from "+updateOrigin(update))
	}
	return backend.InstalledApp{Source: catalog.SourceAppImage, Ref: id, AppID: id, Name: a.Name,
		Version: a.Version, Scope: "user", Origin: updateOrigin(update)}, nil
}

// latestFile looks up the file an update information points to, from the cache unless fresh is set.
func (l *Local) latestFile(ctx context.Context, info string, fresh bool) (remoteFile, error) {
	u, err := appimage.ParseUpdate(info)
	if err != nil {
		return remoteFile{}, err
	}
	l.mu.Lock()
	c, ok := l.latest[info]
	l.mu.Unlock()
	if ok && !fresh && l.GH.now().Sub(c.at) < latestTTL {
		return c.file, nil
	}
	api := l.API
	if api == "" {
		api = DefaultAPI
	}
	f, err := l.GH.latestFile(ctx, api, u)
	if err != nil {
		return remoteFile{}, err
	}
	l.mu.Lock()
	if l.latest == nil {
		l.latest = map[string]cachedFile{}
	}
	l.latest[info] = cachedFile{f, l.GH.now()}
	l.mu.Unlock()
	return f, nil
}

// Updates implements backend.UpdateChecker. An AppImage that can't be checked is left out.
func (l *Local) Updates(ctx context.Context) ([]backend.AvailableUpdate, error) {
	st, err := l.GH.State.Load()
	if err != nil {
		return nil, err
	}
	var ups []backend.AvailableUpdate
	for _, a := range st.AppImages {
		if a.Update == "" {
			continue
		}
		f, err := l.latestFile(ctx, a.Update, false)
		if err != nil {
			continue
		}
		if same, ok := f.sameAs(a.SHA256, a.SHA1); ok && !same {
			ups = append(ups, backend.AvailableUpdate{Ref: a.ID, Scope: "user", Version: f.Version})
		}
	}
	return ups, nil
}

// Update implements backend.Backend: it downloads the file the AppImage's update information points to,
// if it differs from the one installed.
func (l *Local) Update(ctx context.Context, app backend.InstalledApp, progress func(backend.Progress)) error {
	if app.Source != catalog.SourceAppImage {
		return backend.ErrWrongSource
	}
	st, err := l.GH.State.Load()
	if err != nil {
		return err
	}
	a, ok := st.FindAppImage(app.Ref)
	if !ok {
		return fmt.Errorf("appimage: %s is not installed", app.Ref)
	}
	if a.Update == "" {
		return fmt.Errorf("%s doesn't say where its updates come from; download a new version and add it", a.Name)
	}
	report(progress, "Checking "+updateOrigin(a.Update)+" for a new version")
	f, err := l.latestFile(ctx, a.Update, true)
	if err != nil {
		return err
	}
	same, ok := f.sameAs(a.SHA256, a.SHA1)
	if !ok {
		return fmt.Errorf("%s has no published checksum, so uas can't check the download", f.Name)
	}
	if same {
		report(progress, "Already up to date")
		return nil
	}
	k := fileKey(a.ID)
	tmp := filepath.Join(l.GH.Dirs.AppImages, "."+k+".download")
	defer os.Remove(tmp)
	sum256, sum1, err := l.GH.fetch(ctx, f, tmp, progress)
	if err != nil {
		return err
	}
	info, err := appimage.Inspect(tmp)
	if err != nil {
		return fmt.Errorf("the new %s is not an AppImage uas can read: %w", f.Name, err)
	}
	p, err := l.GH.place(ctx, tmp, k, a.Name, "", "file:"+a.ID, a.IconFile, info)
	if err != nil {
		return err
	}
	// Keep following the new file's update information, if it changed.
	if raw, err := updateInfoOf(p.path); err == nil {
		if u, err := appimage.ParseUpdate(raw); err == nil {
			a.Update = u.String()
		}
	}
	a.Name, a.SHA256, a.SHA1, a.Path, a.DesktopFile, a.IconFile = p.name, sum256, sum1, p.path, p.desktopFile,
		p.iconFile
	a.Version = fileVersion(info)
	if a.Version == "" {
		a.Version = f.Version
	}
	st.PutAppImage(a)
	report(progress, "Updated "+a.Name)
	return l.GH.State.Save(st)
}

// fileVersion is the version an AppImage's launcher gives, without a leading "v".
func fileVersion(info *appimage.Info) string {
	v := info.Desktop["X-AppImage-Version"]
	if len(v) > 1 && (v[0] == 'v' || v[0] == 'V') && v[1] >= '0' && v[1] <= '9' {
		return v[1:]
	}
	return v
}

func updateInfoOf(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	return appimage.UpdateInfo(f)
}

// Remove implements backend.Backend. It deletes the AppImage, its launcher and icon; app settings in the
// home folder are left alone.
func (l *Local) Remove(ctx context.Context, app backend.InstalledApp, progress func(backend.Progress)) error {
	if app.Source != catalog.SourceAppImage {
		return backend.ErrWrongSource
	}
	st, err := l.GH.State.Load()
	if err != nil {
		return err
	}
	a, ok := st.FindAppImage(app.Ref)
	if !ok {
		return fmt.Errorf("appimage: %s is not installed", app.Ref)
	}
	if err := l.GH.removeFiles(state.GitHubInstall{Path: a.Path, DesktopFile: a.DesktopFile,
		IconFile: a.IconFile}); err != nil {
		return err
	}
	l.GH.refreshLaunchers(ctx)
	report(progress, "Removed "+a.Name)
	st.DeleteAppImage(a.ID)
	return l.GH.State.Save(st)
}
