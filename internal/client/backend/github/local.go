package github

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
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
	// Unreadable says why uas could not read the AppImage's own files (its launcher and icon), if it could not.
	Unreadable error
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
	var unreadable error
	if err != nil {
		if !appimage.IsType2(f) || !errors.Is(err, appimage.ErrDwarFS) {
			return nil, fmt.Errorf("%s is not an AppImage uas can add: %w", filepath.Base(path), err)
		}
		// Still manageable: the launcher gets its name from the catalog or the file name, and a generic icon.
		info, unreadable = &appimage.Info{Desktop: map[string]string{}}, err
	}
	file := &File{Path: abs, Info: info, Unreadable: unreadable}
	raw, err := appimage.UpdateInfo(f)
	if err == nil {
		file.Update, err = appimage.ParseUpdate(raw)
	}
	if err != nil && info.ElectronUpdate != nil {
		// Apps built with electron-builder say where their updates come from inside the image instead.
		if u, eerr := info.ElectronFeed(arch()); eerr == nil {
			file.Update, err = u, nil
		}
	}
	if err != nil {
		if known, ok := knownFeeds[fileID(file)]; ok {
			file.Update, err = appimage.ParseUpdate(known)
		}
	}
	file.UpdateErr = err
	return file, nil
}

// knownFeeds are where some AppImages that don't say where their updates come from get them, by the ID uas
// keeps them under. Each feed publishes a checksum of the AppImage.
var knownFeeds = map[string]string{
	// CurseForge's app-update.yml names a GitLab provider with no project; the app finds its feed in code.
	"curseforge": appimage.UpdateElectron + "|https://curseforge.overwolf.com/electron/linux/latest-linux.yml",
}

// updateSource is the update information an added AppImage follows: its own, else a known feed.
func updateSource(a state.FileAppImage) string {
	if a.Update != "" {
		return a.Update
	}
	return knownFeeds[a.ID]
}

func arch() string {
	if runtime.GOARCH == "arm64" {
		return "aarch64"
	}
	return "x86_64"
}

// take copies the user's file into Dirs.AppImages as a temporary file and returns its path and checksums.
// The user's file stays until the add has succeeded.
func (b *Backend) take(f *File, k string) (tmp string, sum sums, err error) {
	if b.owns(f.Path) {
		return "", sums{}, errors.New("this AppImage is already managed by uas")
	}
	if err := os.MkdirAll(b.Dirs.AppImages, 0o755); err != nil {
		return "", sums{}, err
	}
	for _, d := range []string{b.Dirs.Icons, b.Dirs.Applications} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			return "", sums{}, err
		}
	}
	src, err := os.Open(f.Path)
	if err != nil {
		return "", sums{}, err
	}
	defer src.Close()
	tmp = filepath.Join(b.Dirs.AppImages, "."+k+".adding")
	dst, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		return "", sums{}, err
	}
	h := newHasher()
	// On Linux, copying file to file uses copy_file_range, which shares the data on btrfs instead of
	// copying it.
	_, err = io.Copy(dst, io.TeeReader(src, h))
	if cerr := dst.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		os.Remove(tmp)
		return "", sums{}, err
	}
	return tmp, h.sums(), nil
}

// Adopt adds an AppImage file as the install of a GitHub listing, so it updates from the catalog like an
// AppImage uas downloaded. The file is moved into uas's AppImage folder.
func (b *Backend) Adopt(ctx context.Context, f *File, l catalog.Listing, progress func(backend.Progress)) error {
	if l.Source != catalog.SourceGitHub || !repoPattern.MatchString(l.Ref) {
		return backend.ErrWrongSource
	}
	k := key(l.Ref)
	tmp, sum, err := b.take(f, k)
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
	if a, ok := b.Pick(l); ok && a.SHA256 == sum.SHA256 {
		version, asset = l.Version, a.Name
	}
	others := b.launchersOf(f.Path)
	wmclass := adoptLauncherDetails(f.Info, others)
	if wmclass == "" {
		wmclass = old.WMClass
	}
	p, err := b.place(ctx, tmp, k, launcher{name: l.Name, summary: l.Summary, source: "github:" + l.Ref,
		oldIcon: old.IconFile, wmclass: wmclass, info: f.Info})
	if err != nil {
		return err
	}
	if old.Kind == catalog.AssetFlatpak {
		b.uninstallBundle(ctx, old.FlatpakRef, progress)
	}
	st.Put(state.GitHubInstall{Repo: l.Ref, Name: p.name, Version: version, Asset: asset, SHA256: sum.SHA256,
		Path: p.path, DesktopFile: p.desktopFile, IconFile: p.iconFile, WMClass: wmclass, InstalledAt: b.now()})
	if err := b.State.Save(st); err != nil {
		return err
	}
	b.takeOver(ctx, others, p.desktopFile, progress)
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
		// The file name without its version, so a newer download of the app replaces this one.
		name := strings.ToLower(appimage.NameFromFile(f.Path))
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
			Name: a.Name, Version: a.Version, Scope: "user", Origin: updateOrigin(updateSource(a)),
			Icon: iconURL(a.IconFile)})
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
	tmp, sum, err := l.GH.take(f, k)
	if err != nil {
		return backend.InstalledApp{}, err
	}
	defer os.Remove(tmp)
	st, err := l.GH.State.Load()
	if err != nil {
		return backend.InstalledApp{}, err
	}
	old, _ := st.FindAppImage(id)
	name := appimage.NameFromFile(f.Path)
	if name == "" {
		name = strings.TrimSuffix(filepath.Base(f.Path), filepath.Ext(f.Path))
	}
	others := l.GH.launchersOf(f.Path)
	wmclass := adoptLauncherDetails(f.Info, others)
	if wmclass == "" {
		wmclass = old.WMClass
	}
	p, err := l.GH.place(ctx, tmp, k, launcher{name: name, source: "file:" + id, oldIcon: old.IconFile,
		wmclass: wmclass, info: f.Info})
	if err != nil {
		return backend.InstalledApp{}, err
	}
	update := ""
	if f.UpdateErr == nil {
		update = f.Update.String()
	}
	a := state.FileAppImage{ID: id, Name: p.name, Version: fileVersion(f.Info), Update: update,
		SHA256: sum.SHA256, SHA512: sum.SHA512, SHA1: sum.SHA1, Path: p.path, DesktopFile: p.desktopFile,
		IconFile: p.iconFile, WMClass: wmclass, InstalledAt: l.GH.now()}
	st.PutAppImage(a)
	if err := l.GH.State.Save(st); err != nil {
		return backend.InstalledApp{}, err
	}
	l.GH.takeOver(ctx, others, p.desktopFile, progress)
	os.Remove(f.Path)
	if f.Unreadable != nil {
		report(progress, "uas couldn't read the app's own launcher and icon ("+f.Unreadable.Error()+
			"), so it shows a generic icon.")
	}
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
	changed := false
	for i, a := range st.AppImages {
		src := updateSource(a)
		if src == "" {
			continue
		}
		f, err := l.latestFile(ctx, src, false)
		if err != nil {
			continue
		}
		if f.SHA512 != "" && a.SHA512 == "" && fillSums(&st.AppImages[i]) == nil {
			a, changed = st.AppImages[i], true
		}
		if same, ok := f.sameAs(a.SHA256, a.SHA512, a.SHA1); ok && !same {
			ups = append(ups, backend.AvailableUpdate{Ref: a.ID, Scope: "user", Version: f.Version})
		}
	}
	if changed {
		if err := l.GH.State.Save(st); err != nil {
			return ups, err
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
	src := updateSource(a)
	if src == "" {
		return fmt.Errorf("%s doesn't say where its updates come from; download a new version and add it", a.Name)
	}
	report(progress, "Checking "+updateOrigin(src)+" for a new version")
	f, err := l.latestFile(ctx, src, true)
	if err != nil {
		return err
	}
	if f.SHA512 != "" && a.SHA512 == "" {
		if err := fillSums(&a); err != nil {
			return err
		}
	}
	same, ok := f.sameAs(a.SHA256, a.SHA512, a.SHA1)
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
	sum, err := l.GH.fetch(ctx, f, tmp, progress)
	if err != nil {
		return err
	}
	info, err := appimage.Inspect(tmp)
	if errors.Is(err, appimage.ErrDwarFS) {
		info = &appimage.Info{Desktop: map[string]string{}}
	} else if err != nil {
		return fmt.Errorf("the new %s is not an AppImage uas can read: %w", f.Name, err)
	}
	p, err := l.GH.place(ctx, tmp, k, launcher{name: a.Name, source: "file:" + a.ID, oldIcon: a.IconFile,
		wmclass: a.WMClass, info: info})
	if err != nil {
		return err
	}
	// Keep following the new file's update information, if it changed.
	if raw, err := updateInfoOf(p.path); err == nil {
		if u, err := appimage.ParseUpdate(raw); err == nil {
			a.Update = u.String()
		}
	}
	a.Name, a.Path, a.DesktopFile, a.IconFile = p.name, p.path, p.desktopFile, p.iconFile
	a.SHA256, a.SHA512, a.SHA1 = sum.SHA256, sum.SHA512, sum.SHA1
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

// fillSums computes the checksums of an AppImage added before uas kept its SHA-512.
func fillSums(a *state.FileAppImage) error {
	f, err := os.Open(a.Path)
	if err != nil {
		return err
	}
	defer f.Close()
	h := newHasher()
	if _, err := io.Copy(h, f); err != nil {
		return err
	}
	sum := h.sums()
	a.SHA256, a.SHA512, a.SHA1 = sum.SHA256, sum.SHA512, sum.SHA1
	return nil
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
