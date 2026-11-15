// Package appearance installs fonts and icon and cursor themes for the user, from the catalog, and uses them
// on the desktop.
//
// Everything goes into the user's own folders, with no root: fonts into ~/.local/share/fonts/uas/<item>/,
// themes into ~/.local/share/icons/<theme>/ (not ~/.icons, which Flatpak apps can't see). Every download is
// checked against the SHA-256 the catalog pins before anything is unpacked, and archives are unpacked by
// internal/appearance, which refuses anything that isn't plainly a theme or a font; nothing from an archive
// is ever run. Each install is recorded in uas's state file, with what it wrote, so it can be removed and
// updated.
package appearance

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	looks "github.com/tyler-builds/uas/internal/appearance"
	"github.com/tyler-builds/uas/internal/catalog"
	"github.com/tyler-builds/uas/internal/client/backend"
	"github.com/tyler-builds/uas/internal/client/run"
	"github.com/tyler-builds/uas/internal/client/state"
)

// PlingAPI is OpenDesktop's OCS API, which serves the Pling stores. The same paths under gnome-look.org
// answer 410 Gone.
const PlingAPI = "https://api.opendesktop.org/ocs/v1"

// Installer installs and removes fonts and themes and uses them on the desktop.
type Installer struct {
	// Data is $XDG_DATA_HOME (~/.local/share): fonts go in Data/fonts/uas, themes in Data/icons.
	Data string
	// Home is the user's home folder, for ~/.icons, which hides a theme of the same name in Data/icons.
	Home string
	// SystemData are $XDG_DATA_DIRS, whose icons folders hold the system's themes.
	SystemData []string
	// Cache is where font files downloaded for previews are kept, by SHA-256, so installing reuses them.
	Cache  string
	State  state.Store
	Client *http.Client
	Runner run.Runner
	// Desktop changes the desktop's settings; nil where uas only installs.
	Desktop  Desktop
	PlingAPI string // PlingAPI, or a test server
	Limits   looks.Limits
	// Now is the clock; nil is time.Now.
	Now func() time.Time

	mu sync.Mutex // one change to the state file at a time
}

// New returns an installer for the user's own folders.
func New(r run.Runner, desktop Desktop) (*Installer, error) {
	data, err := state.DataDir()
	if err != nil {
		return nil, err
	}
	statePath, err := state.DefaultPath()
	if err != nil {
		return nil, err
	}
	home, _ := os.UserHomeDir()
	cache := ""
	if c, err := os.UserCacheDir(); err == nil {
		cache = filepath.Join(c, "uas", "fonts")
	}
	sys := filepath.SplitList(os.Getenv("XDG_DATA_DIRS"))
	if len(sys) == 0 {
		sys = []string{"/usr/local/share", "/usr/share"}
	}
	return &Installer{Data: data, Home: home, SystemData: sys, Cache: cache, State: state.Store{Path: statePath},
		Client: &http.Client{Timeout: 30 * time.Minute}, Runner: r, Desktop: desktop, PlingAPI: PlingAPI,
		Limits: looks.DefaultLimits}, nil
}

func (in *Installer) now() time.Time {
	if in.Now != nil {
		return in.Now()
	}
	return time.Now()
}

// IconsDir is where themes are installed: ~/.local/share/icons.
func (in *Installer) IconsDir() string { return filepath.Join(in.Data, "icons") }

// FontsDir is where fonts are installed: ~/.local/share/fonts/uas.
func (in *Installer) FontsDir() string { return filepath.Join(in.Data, "fonts", "uas") }

// EnsureFontsDir creates ~/.local/share/fonts if it is missing. Flatpak shows apps the user's fonts only
// if the folder is there when the app starts, so uas makes it early.
func (in *Installer) EnsureFontsDir() error {
	return os.MkdirAll(filepath.Join(in.Data, "fonts"), 0o755)
}

// Records returns what uas installed.
func (in *Installer) Records() ([]state.AppearanceInstall, error) {
	st, err := in.State.Load()
	return st.Appearance, err
}

// Saved returns the settings uas changed, with their earlier values.
func (in *Installer) Saved() ([]state.SavedSetting, error) {
	st, err := in.State.Load()
	return st.Saved, err
}

// Variants are the variants of a theme listing, in the catalog's order: the theme folders of each of its
// files.
func Variants(l catalog.Listing) []string {
	var out []string
	for _, a := range l.Assets {
		for _, t := range a.Themes {
			if !slices.Contains(out, t) {
				out = append(out, t)
			}
		}
	}
	return out
}

// AssetFor returns the theme archive of a listing that holds a variant, or the first when variant is empty.
func AssetFor(l catalog.Listing, variant string) (catalog.Asset, bool) {
	for _, a := range l.Assets {
		if a.Kind == catalog.AssetTheme && (variant == "" || slices.Contains(a.Themes, variant)) {
			return a, true
		}
	}
	return catalog.Asset{}, false
}

// Digest is the digest of a family of font files: the SHA-256 of their names and SHA-256s, one a line,
// sorted. It changes when any file does.
func Digest(assets []catalog.Asset) string {
	lines := make([]string, 0, len(assets))
	for _, a := range assets {
		lines = append(lines, a.Name+" "+a.SHA256)
	}
	sort.Strings(lines)
	sum := sha256.Sum256([]byte(strings.Join(lines, "\n")))
	return hex.EncodeToString(sum[:])
}

// Pinned is the SHA-256 an install of l from a variant's file would record, for comparing with what is
// installed; "" when the listing has no such file.
func Pinned(l catalog.Listing, variant string) string {
	switch l.Kind {
	case catalog.KindFont:
		if a, ok := l.Asset(catalog.AssetFontArchive); ok {
			return a.SHA256
		}
		return Digest(fontFiles(l))
	}
	a, ok := AssetFor(l, variant)
	if !ok {
		return ""
	}
	return a.SHA256
}

func fontFiles(l catalog.Listing) []catalog.Asset {
	var out []catalog.Asset
	for _, a := range l.Assets {
		if a.Kind == catalog.AssetFont {
			out = append(out, a)
		}
	}
	return out
}

// Install installs a font or theme listing for the user, replacing an earlier install of it. For a theme,
// variant picks the file to install (the one that holds that theme folder); every theme in the file is
// installed. It returns what it recorded.
func (in *Installer) Install(ctx context.Context, l catalog.Listing, variant string,
	progress func(backend.Progress)) (state.AppearanceInstall, error) {
	if progress == nil {
		progress = func(backend.Progress) {}
	}
	id := catalog.AppearanceAppID(l.Source, l.Ref)
	in.mu.Lock()
	defer in.mu.Unlock()
	st, err := in.State.Load()
	if err != nil {
		return state.AppearanceInstall{}, err
	}
	old, hadOld := st.FindAppearance(id)
	stage, err := in.stage()
	if err != nil {
		return state.AppearanceInstall{}, err
	}
	defer os.RemoveAll(stage)

	rec := state.AppearanceInstall{ID: id, Source: string(l.Source), Ref: l.Ref, Kind: string(l.Kind), Name: l.Name,
		Version: l.Version, InstalledAt: in.now().UTC()}
	switch l.Kind {
	case catalog.KindIconTheme, catalog.KindCursorTheme:
		err = in.installTheme(ctx, st, l, variant, stage, &rec, progress)
	case catalog.KindFont:
		err = in.installFont(ctx, l, stage, &rec, progress)
	default:
		err = fmt.Errorf("%s isn't a font or a theme", l.Name)
	}
	if err != nil {
		return state.AppearanceInstall{}, err
	}
	// What the earlier install wrote and this one didn't replace goes.
	if hadOld {
		for _, p := range old.Paths {
			if !slices.Contains(rec.Paths, p) && in.owned(p) {
				os.RemoveAll(p)
			}
		}
	}
	st.PutAppearance(rec)
	if err := in.State.Save(st); err != nil {
		return state.AppearanceInstall{}, err
	}
	if l.Kind == catalog.KindFont {
		in.fcCache(ctx, progress)
	}
	return rec, nil
}

// stage makes a fresh folder for one install's downloads and unpacking, next to where the result goes, so
// moving it into place is a rename.
func (in *Installer) stage() (string, error) {
	dir := filepath.Join(in.Data, "uas", "staging")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	return os.MkdirTemp(dir, "install-")
}

// owned reports whether p is somewhere uas installs to, so removing it can't touch anything else.
func (in *Installer) owned(p string) bool {
	for _, base := range []string{in.IconsDir(), in.FontsDir()} {
		if rel, err := filepath.Rel(base, p); err == nil && filepath.IsLocal(rel) && !strings.Contains(rel, string(filepath.Separator)) {
			return true
		}
	}
	return false
}

// installTheme installs the theme file of a variant.
func (in *Installer) installTheme(ctx context.Context, st state.State, l catalog.Listing, variant, stage string,
	rec *state.AppearanceInstall, progress func(backend.Progress)) error {
	kind := l.Kind
	a, ok := AssetFor(l, variant)
	if !ok {
		if variant != "" {
			return fmt.Errorf("%s has no variant called %s", l.Name, variant)
		}
		return fmt.Errorf("%s has no file uas can install", l.Name)
	}
	if a.SHA256 == "" {
		return fmt.Errorf("%s: the catalog has no checksum for %s, so uas won't install it", l.Name, a.Name)
	}
	// Check the names before downloading: a theme of the same name that uas didn't install is never replaced.
	for _, name := range a.Themes {
		if err := in.checkThemeName(st, rec.ID, name); err != nil {
			return err
		}
	}
	progress(backend.Progress{Message: "Asking Pling for a download link…"})
	link, err := in.plingLink(ctx, l.Ref, a)
	if err != nil {
		return err
	}
	progress(backend.Progress{Message: fmt.Sprintf("Downloading %s (%s)…", a.Name, humanSize(a.Size))})
	file := filepath.Join(stage, "download")
	if err := in.download(ctx, link, a, file); err != nil {
		return err
	}
	progress(backend.Progress{Message: "Checking and unpacking…"})
	out := filepath.Join(stage, "out")
	if err := os.Mkdir(out, 0o755); err != nil {
		return err
	}
	themes, err := looks.Extract(file, kind, out, in.Limits)
	if err != nil {
		return fmt.Errorf("%s: %w", a.Name, err)
	}
	var names []string
	for _, t := range themes {
		names = append(names, t.Name)
		if err := in.checkThemeName(st, rec.ID, t.Name); err != nil {
			return err
		}
	}
	if len(a.Themes) > 0 && !sameSet(names, a.Themes) {
		return fmt.Errorf("%s holds other themes than the catalog says (%s, not %s)", a.Name,
			strings.Join(names, ", "), strings.Join(a.Themes, ", "))
	}
	if err := os.MkdirAll(in.IconsDir(), 0o755); err != nil {
		return err
	}
	for _, name := range names {
		dst := filepath.Join(in.IconsDir(), name)
		if _, err := os.Lstat(dst); err == nil {
			// Ours (checkThemeName said so): an earlier install of this item.
			if err := os.RemoveAll(dst); err != nil {
				return err
			}
		}
		if err := move(filepath.Join(out, name), dst); err != nil {
			return err
		}
		rec.Paths = append(rec.Paths, dst)
	}
	rec.Asset, rec.SHA256, rec.Themes = a.Name, a.SHA256, names
	return nil
}

func sameSet(a, b []string) bool {
	a, b = slices.Clone(a), slices.Clone(b)
	slices.Sort(a)
	slices.Sort(b)
	return slices.Equal(a, b)
}

// checkThemeName refuses a theme name that would replace or hide a theme uas didn't install for this item.
func (in *Installer) checkThemeName(st state.State, id, name string) error {
	if !looks.ThemeName.MatchString(name) {
		return fmt.Errorf("a theme is called %q, which uas won't use as a folder name", name)
	}
	for _, other := range st.Appearance {
		if other.ID != id && slices.Contains(other.Themes, name) {
			return fmt.Errorf("%s is installed already, from %s; remove that first", name, other.Name)
		}
	}
	if _, err := os.Lstat(filepath.Join(in.IconsDir(), name)); err == nil {
		if mine, ok := st.FindAppearance(id); !ok || !slices.Contains(mine.Themes, name) {
			return fmt.Errorf("a theme called %s is already in %s, and uas doesn't replace what it didn't install",
				name, in.IconsDir())
		}
	}
	if in.Home != "" {
		if _, err := os.Lstat(filepath.Join(in.Home, ".icons", name)); err == nil {
			return fmt.Errorf("a theme called %s is already in ~/.icons, where it would hide this one", name)
		}
	}
	for _, d := range in.SystemData {
		if _, err := os.Lstat(filepath.Join(d, "icons", name)); err == nil {
			return fmt.Errorf("the system has a theme called %s; installing this one would hide it", name)
		}
	}
	return nil
}

// plingFile is a file of a Pling item as its API lists it.
type plingFile struct {
	name, link, md5 string
}

// plingHosts are where Pling's download links may point. They redirect to storage elsewhere, which the
// SHA-256 check covers.
var plingHosts = []string{"pling.com", "opendesktop.org", "pling.cc", "kde-look.org", "gnome-look.org",
	"store.kde.org", "xfce-look.org"}

// plingLink asks Pling for a fresh download link to a file. Links expire after two days and are
// rate-limited on reuse, so the catalog can't hold them. The file is found by name and MD5, since an author
// can reorder an item's files.
func (in *Installer) plingLink(ctx context.Context, ref string, a catalog.Asset) (string, error) {
	if _, err := strconv.Atoi(ref); err != nil {
		return "", fmt.Errorf("not a Pling item: %q", ref)
	}
	api := in.PlingAPI
	if api == "" {
		api = PlingAPI
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, api+"/content/data/"+ref+"?format=json", nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("User-Agent", "uas")
	req.Header.Set("Accept", "application/json")
	resp, err := in.Client.Do(req)
	if err != nil {
		return "", fmt.Errorf("asking Pling for a download link: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("asking Pling for a download link: %s", resp.Status)
	}
	var body struct {
		Status string           `json:"status"`
		Data   []map[string]any `json:"data"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 8<<20)).Decode(&body); err != nil {
		return "", fmt.Errorf("asking Pling for a download link: %w", err)
	}
	if body.Status != "ok" || len(body.Data) == 0 {
		return "", errors.New("Pling no longer lists this item")
	}
	files := plingFiles(body.Data[0])
	var named *plingFile
	for i := range files {
		if files[i].name == a.Name {
			named = &files[i]
			if a.MD5 == "" || strings.EqualFold(files[i].md5, a.MD5) {
				return checkPlingLink(files[i].link)
			}
		}
	}
	if named != nil {
		return "", fmt.Errorf("%s changed on Pling after the catalog was built, so it can't be checked; "+
			"try again once the catalog is updated", a.Name)
	}
	return "", fmt.Errorf("Pling no longer offers %s; try again once the catalog is updated", a.Name)
}

// plingFiles reads the downloadnameN, downloadlinkN and downloadmd5sumN fields of an item.
func plingFiles(item map[string]any) []plingFile {
	var out []plingFile
	for n := 1; n <= 200; n++ {
		name, ok := item["downloadname"+strconv.Itoa(n)].(string)
		if !ok {
			break
		}
		link, _ := item["downloadlink"+strconv.Itoa(n)].(string)
		md5, _ := item["downloadmd5sum"+strconv.Itoa(n)].(string)
		out = append(out, plingFile{name: name, link: link, md5: md5})
	}
	return out
}

// checkPlingLink checks that a download link is Pling's own, over HTTPS, and escapes it: Pling doesn't
// escape the file names in its links.
func checkPlingLink(raw string) (string, error) {
	u, err := url.Parse(strings.ReplaceAll(strings.TrimSpace(raw), " ", "%20"))
	if err != nil || u.Scheme != "https" {
		return "", fmt.Errorf("Pling gave a download link uas won't use: %q", raw)
	}
	host := strings.ToLower(u.Hostname())
	for _, h := range plingHosts {
		if host == h || strings.HasSuffix(host, "."+h) {
			return u.String(), nil
		}
	}
	return "", fmt.Errorf("Pling gave a download link to %s, which isn't Pling's", host)
}

// download fetches a file to dst and checks its size and SHA-256 against the catalog's.
func (in *Installer) download(ctx context.Context, link string, a catalog.Asset, dst string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, link, nil)
	if err != nil {
		return err
	}
	req.Header.Set("User-Agent", "uas")
	client := *in.Client
	client.CheckRedirect = func(r *http.Request, via []*http.Request) error {
		if r.URL.Scheme != "https" {
			return errors.New("a download was redirected away from HTTPS")
		}
		if len(via) > 5 {
			return errors.New("too many redirects")
		}
		return nil
	}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("download %s: %w", a.Name, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("download %s: %s", a.Name, resp.Status)
	}
	f, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	limit := a.Size
	if limit <= 0 {
		limit = 1 << 30
	}
	h := sha256.New()
	n, err := io.Copy(io.MultiWriter(f, h), io.LimitReader(resp.Body, limit+1))
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	switch {
	case err != nil:
		return fmt.Errorf("download %s: %w", a.Name, err)
	case a.Size > 0 && n != a.Size:
		return fmt.Errorf("download %s: got %s, the catalog says %s; it may have changed since the catalog was "+
			"built, so it wasn't installed", a.Name, humanSize(n), humanSize(a.Size))
	}
	if got := hex.EncodeToString(h.Sum(nil)); !strings.EqualFold(got, a.SHA256) {
		return fmt.Errorf("download %s: its SHA-256 doesn't match the catalog's, so it wasn't installed", a.Name)
	}
	return nil
}

// googleFontsRaw and nerdFontsReleases are where font files may be downloaded from.
const (
	googleFontsRaw    = "https://raw.githubusercontent.com/google/fonts/"
	nerdFontsReleases = "https://github.com/ryanoasis/nerd-fonts/releases/download/"
)

// unsafeName is what can't be in the name of a listing's folder under FontsDir.
var unsafeName = regexp.MustCompile(`[^A-Za-z0-9._-]`)

// installFont installs a font family: its font files, or the fonts in its archive.
func (in *Installer) installFont(ctx context.Context, l catalog.Listing, stage string, rec *state.AppearanceInstall,
	progress func(backend.Progress)) error {
	out := filepath.Join(stage, "fonts")
	if err := os.Mkdir(out, 0o755); err != nil {
		return err
	}
	var families []string
	addFamilies := func(fs []string) {
		for _, f := range fs {
			if !slices.Contains(families, f) {
				families = append(families, f)
			}
		}
	}
	if a, ok := l.Asset(catalog.AssetFontArchive); ok {
		if !strings.HasPrefix(a.URL, nerdFontsReleases) || a.SHA256 == "" {
			return fmt.Errorf("%s: uas won't download %s", l.Name, a.URL)
		}
		progress(backend.Progress{Message: fmt.Sprintf("Downloading %s (%s)…", a.Name, humanSize(a.Size))})
		file := filepath.Join(stage, "download")
		if err := in.download(ctx, a.URL, a, file); err != nil {
			return err
		}
		progress(backend.Progress{Message: "Checking the fonts…"})
		fonts, err := looks.ExtractFonts(file, out, in.Limits)
		if err != nil {
			return fmt.Errorf("%s: %w", a.Name, err)
		}
		for _, f := range fonts {
			addFamilies(f.Families)
		}
		rec.Asset, rec.SHA256 = a.Name, a.SHA256
	} else {
		files := fontFiles(l)
		if len(files) == 0 {
			return fmt.Errorf("%s has no font files", l.Name)
		}
		for i, a := range files {
			if !looks.FontFileName.MatchString(a.Name) || strings.ContainsAny(a.Name, "/\\") {
				return fmt.Errorf("%s: a font file is called %q, which uas won't use", l.Name, a.Name)
			}
			progress(backend.Progress{Message: fmt.Sprintf("Downloading %s (%d of %d)…", a.Name, i+1, len(files))})
			src, err := in.FontFile(ctx, a)
			if err != nil {
				return err
			}
			data, err := os.ReadFile(src)
			if err != nil {
				return err
			}
			fams, err := looks.FontFamilies(data)
			if err != nil {
				return fmt.Errorf("%s: %w", a.Name, err)
			}
			addFamilies(fams)
			if err := os.WriteFile(filepath.Join(out, a.Name), data, 0o644); err != nil {
				return err
			}
		}
		rec.SHA256 = Digest(files)
	}
	if err := os.MkdirAll(in.FontsDir(), 0o755); err != nil {
		return err
	}
	dst := filepath.Join(in.FontsDir(), unsafeName.ReplaceAllString(string(l.Source)+"-"+l.Ref, "_"))
	if err := os.RemoveAll(dst); err != nil {
		return err
	}
	if err := move(out, dst); err != nil {
		return err
	}
	rec.Paths, rec.Families = []string{dst}, families
	return nil
}

// FontFile returns a font file of the catalog on disk, downloading it into the cache first and checking it
// against the catalog's SHA-256. Previews use it, and installs reuse what previews downloaded.
func (in *Installer) FontFile(ctx context.Context, a catalog.Asset) (string, error) {
	if a.Kind != catalog.AssetFont || !strings.HasPrefix(a.URL, googleFontsRaw) || len(a.SHA256) != 64 {
		return "", fmt.Errorf("uas won't download %s", a.Name)
	}
	if strings.ContainsAny(a.SHA256, "./") {
		return "", errors.New("odd checksum")
	}
	dir := in.Cache
	if dir == "" {
		dir = filepath.Join(os.TempDir(), "uas-fonts")
	}
	p := filepath.Join(dir, strings.ToLower(a.SHA256)+filepath.Ext(a.Name))
	if ok, _ := fileHash(p, a.SHA256); ok {
		now := time.Now()
		os.Chtimes(p, now, now)
		return p, nil
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	tmp, err := os.MkdirTemp(dir, "download-")
	if err != nil {
		return "", err
	}
	defer os.RemoveAll(tmp)
	file := filepath.Join(tmp, "font")
	if err := in.download(ctx, a.URL, a, file); err != nil {
		return "", err
	}
	if err := os.Rename(file, p); err != nil {
		return "", err
	}
	return p, nil
}

func fileHash(p, want string) (bool, error) {
	f, err := os.Open(p)
	if err != nil {
		return false, err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return false, err
	}
	return strings.EqualFold(hex.EncodeToString(h.Sum(nil)), want), nil
}

// fcCache tells fontconfig about new or removed fonts at once, rather than at its next rescan.
func (in *Installer) fcCache(ctx context.Context, progress func(backend.Progress)) {
	if in.Runner == nil {
		return
	}
	progress(backend.Progress{Message: "Updating the font list…"})
	_, _ = in.Runner.Run(ctx, "fc-cache", []string{"-f", filepath.Join(in.Data, "fonts")}, nil)
}

// move renames src to dst, copying when they are on different file systems.
func move(src, dst string) error {
	err := os.Rename(src, dst)
	if err == nil {
		return nil
	}
	var le *os.LinkError
	if !errors.As(err, &le) {
		return err
	}
	if err := os.CopyFS(dst, os.DirFS(src)); err != nil {
		os.RemoveAll(dst)
		return err
	}
	return os.RemoveAll(src)
}

// Remove removes an install of uas's, by catalog ID. A theme or font in use is first switched back: to what
// it replaced when uas changed the setting, else to the desktop's default.
func (in *Installer) Remove(ctx context.Context, id string, progress func(backend.Progress)) error {
	if progress == nil {
		progress = func(backend.Progress) {}
	}
	in.mu.Lock()
	defer in.mu.Unlock()
	st, err := in.State.Load()
	if err != nil {
		return err
	}
	rec, ok := st.FindAppearance(id)
	if !ok {
		return fmt.Errorf("uas didn't install %s", id)
	}
	if in.Desktop != nil {
		for _, key := range keysOf(rec) {
			if !in.Desktop.Supports(key) {
				continue
			}
			cur, err := in.Desktop.Get(ctx, key)
			if err != nil || !inUse(rec, key, cur) {
				continue
			}
			progress(backend.Progress{Message: "Switching " + strings.ToLower(KeyLabel(key)) + " back…"})
			if err := in.revertLocked(ctx, &st, key); err != nil {
				return err
			}
		}
	}
	progress(backend.Progress{Message: "Removing " + rec.Name + "…"})
	for _, p := range rec.Paths {
		if !in.owned(p) {
			return fmt.Errorf("uas won't remove %s: it isn't where uas installs", p)
		}
		if err := os.RemoveAll(p); err != nil {
			return err
		}
	}
	st.DeleteAppearance(id)
	if err := in.State.Save(st); err != nil {
		return err
	}
	if rec.Kind == string(catalog.KindFont) {
		in.fcCache(ctx, progress)
	}
	return nil
}

// keysOf are the settings an install can be in use for.
func keysOf(rec state.AppearanceInstall) []string {
	switch catalog.Kind(rec.Kind) {
	case catalog.KindIconTheme:
		return []string{KeyIconTheme}
	case catalog.KindCursorTheme:
		return []string{KeyCursorTheme}
	case catalog.KindFont:
		return FontKeys
	}
	return nil
}

// inUse reports whether a setting's value is one of an install's themes or font families.
func inUse(rec state.AppearanceInstall, key, value string) bool {
	if slices.Contains(FontKeys, key) {
		return slices.Contains(rec.Families, FontFamily(value))
	}
	return slices.Contains(rec.Themes, value)
}

// Use sets a desktop setting: a theme's name for KeyIconTheme or KeyCursorTheme, a font family for a font
// key (its size is kept). The value it replaces is saved for Revert, unless uas changed the setting before,
// in which case the value from before that stays saved.
func (in *Installer) Use(ctx context.Context, key, value string) error {
	if in.Desktop == nil || !in.Desktop.Supports(key) {
		return ErrUnsupported
	}
	in.mu.Lock()
	defer in.mu.Unlock()
	st, err := in.State.Load()
	if err != nil {
		return err
	}
	cur, err := in.Desktop.Get(ctx, key)
	if err != nil {
		return err
	}
	if slices.Contains(FontKeys, key) {
		value = FontValue(cur, value)
	}
	saved, ok := st.FindSaved(in.Desktop.Name(), key)
	if !ok {
		saved = state.SavedSetting{Desktop: in.Desktop.Name(), Key: key, Previous: cur}
	}
	saved.Value, saved.At = value, in.now().UTC()
	st.PutSaved(saved)
	// Saved first: if the change works and the save didn't, Revert would have nothing to go back to.
	if err := in.State.Save(st); err != nil {
		return err
	}
	return in.Desktop.Set(ctx, key, value)
}

// Revert puts a setting back to what it was before uas first changed it.
func (in *Installer) Revert(ctx context.Context, key string) error {
	in.mu.Lock()
	defer in.mu.Unlock()
	st, err := in.State.Load()
	if err != nil {
		return err
	}
	if err := in.revertLocked(ctx, &st, key); err != nil {
		return err
	}
	return in.State.Save(st)
}

func (in *Installer) revertLocked(ctx context.Context, st *state.State, key string) error {
	if in.Desktop == nil {
		return ErrUnsupported
	}
	saved, ok := st.FindSaved(in.Desktop.Name(), key)
	var err error
	if ok && saved.Previous != "" {
		err = in.Desktop.Set(ctx, key, saved.Previous)
	} else {
		err = in.Desktop.Reset(ctx, key)
	}
	if err != nil {
		return err
	}
	st.DeleteSaved(in.Desktop.Name(), key)
	return nil
}

// Current returns the desktop's appearance settings it supports, by key.
func (in *Installer) Current(ctx context.Context) map[string]string {
	out := map[string]string{}
	if in.Desktop == nil {
		return out
	}
	for _, key := range append([]string{KeyIconTheme, KeyCursorTheme}, FontKeys...) {
		if !in.Desktop.Supports(key) {
			continue
		}
		if v, err := in.Desktop.Get(ctx, key); err == nil {
			out[key] = v
		}
	}
	return out
}

func humanSize(n int64) string {
	switch {
	case n >= 1<<20:
		return fmt.Sprintf("%.1f MB", float64(n)/(1<<20))
	case n >= 1<<10:
		return fmt.Sprintf("%.0f KB", float64(n)/(1<<10))
	}
	return fmt.Sprintf("%d bytes", n)
}
