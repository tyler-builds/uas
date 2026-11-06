// Package fedora fetches desktop apps from Fedora's repositories.
//
// Fedora publishes AppStream metadata in the appstream-data package rather than in the repo metadata, so the
// fetcher reads the repos' package lists (primary.xml) for versions, then pulls the AppStream XML out of
// appstream-data. Only components whose package is in the repos are kept.
package fedora

import (
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"image/png"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/tyler-builds/uas/internal/catalog"
	"github.com/tyler-builds/uas/internal/indexer/appstream"
	"github.com/tyler-builds/uas/internal/indexer/changelog"
	"github.com/tyler-builds/uas/internal/indexer/fetch"
	"github.com/tyler-builds/uas/internal/indexer/fetch/rpmrepo"
)

// DefaultMirror is Fedora's primary download server.
const DefaultMirror = "https://dl.fedoraproject.org/pub/fedora/linux"

// Fetcher reads Fedora's release and updates repositories.
type Fetcher struct {
	Client   *http.Client
	CacheDir string
	Arch     string // "x86_64" or "aarch64"
	Mirror   string // default DefaultMirror
	// Releases to index. If empty, the two newest releases on the mirror are used, which covers systems that
	// have a newer release staged for the next boot.
	Releases []int
	// IconDir, when set, is where icons that appstream-data ships as files (for apps with no icon on the
	// web) are saved, named by their SHA-256, to be published under IconBase/icons/.
	IconDir  string
	IconBase string
	Log      *slog.Logger
}

// Source implements fetch.Fetcher.
func (f *Fetcher) Source() catalog.SourceID { return catalog.SourceFedora }

// Fetch implements fetch.Fetcher.
func (f *Fetcher) Fetch(ctx context.Context) ([]catalog.Listing, error) {
	if f.Arch != "x86_64" && f.Arch != "aarch64" {
		return nil, fmt.Errorf("fedora: unsupported architecture %q", f.Arch)
	}
	if f.Mirror == "" {
		f.Mirror = DefaultMirror
	}
	releases := f.Releases
	if len(releases) == 0 {
		var err error
		if releases, err = f.detectReleases(ctx); err != nil {
			return nil, fmt.Errorf("fedora: %w", err)
		}
	}
	var all []catalog.Listing
	for _, r := range releases {
		ls, err := f.fetchRelease(ctx, r)
		if err != nil {
			return nil, fmt.Errorf("fedora %d: %w", r, err)
		}
		f.log().Info("fedora release indexed", "release", r, "listings", len(ls))
		all = append(all, ls...)
	}
	return all, nil
}

func (f *Fetcher) log() *slog.Logger {
	if f.Log == nil {
		return slog.New(slog.NewTextHandler(io.Discard, nil))
	}
	return f.Log
}

func (f *Fetcher) releaseRepo(n int) string {
	return fmt.Sprintf("%s/releases/%d/Everything/%s/os", f.Mirror, n, f.Arch)
}

func (f *Fetcher) updatesRepo(n int) string {
	return fmt.Sprintf("%s/updates/%d/Everything/%s", f.Mirror, n, f.Arch)
}

// detectReleases finds the two newest releases on the mirror. Old releases move to an archive server, so
// it scans upward past gaps and stops after two missing releases in a row once it has found one.
func (f *Fetcher) detectReleases(ctx context.Context) ([]int, error) {
	var found []int
	misses := 0
	for n := 38; n < 200; n++ {
		ok, err := f.exists(ctx, f.releaseRepo(n)+"/repodata/repomd.xml")
		if err != nil {
			return nil, err
		}
		if ok {
			found, misses = append(found, n), 0
			continue
		}
		if len(found) > 0 {
			if misses++; misses == 2 {
				break
			}
		}
	}
	if len(found) == 0 {
		return nil, errors.New("no releases found on the mirror")
	}
	if len(found) > 2 {
		found = found[len(found)-2:]
	}
	return found, nil
}

func (f *Fetcher) exists(ctx context.Context, url string) (bool, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodHead, url, nil)
	if err != nil {
		return false, err
	}
	req.Header.Set("User-Agent", fetch.UserAgent)
	resp, err := f.Client.Do(req)
	if err != nil {
		return false, err
	}
	resp.Body.Close()
	switch resp.StatusCode {
	case http.StatusOK:
		return true, nil
	case http.StatusNotFound:
		return false, nil
	}
	return false, fmt.Errorf("HEAD %s: %s", url, resp.Status)
}

func (f *Fetcher) fetchRelease(ctx context.Context, n int) ([]catalog.Listing, error) {
	cache := filepath.Join(f.CacheDir, strconv.Itoa(n))
	pkgs := map[string]rpmrepo.Package{}
	// Updates come second, so a package's newest version wins.
	for _, repo := range []string{f.releaseRepo(n), f.updatesRepo(n)} {
		files, err := rpmrepo.Repomd(ctx, f.Client, repo, cache)
		if err != nil {
			return nil, err
		}
		if files["primary"] == "" {
			return nil, fmt.Errorf("repomd for %s has no primary data", repo)
		}
		path, err := fetch.Get(ctx, f.Client, files["primary"], cache)
		if err != nil {
			return nil, err
		}
		if err := rpmrepo.ReadPrimary(path, f.Arch, repo, func(p rpmrepo.Package) { pkgs[p.Name] = p }); err != nil {
			return nil, err
		}
	}

	as, ok := pkgs["appstream-data"]
	if !ok {
		return nil, errors.New("appstream-data package not found")
	}
	rpmPath, err := fetch.Get(ctx, f.Client, as.Location, cache)
	if err != nil {
		return nil, err
	}
	return f.listingsFromAppStreamRPM(rpmPath, n, pkgs)
}

// isCatalogXML matches the AppStream catalog files inside appstream-data.
func isCatalogXML(name string) bool {
	return (strings.Contains(name, "/swcatalog/xml/") || strings.Contains(name, "/app-info/xmls/")) &&
		(strings.HasSuffix(name, ".xml") || strings.HasSuffix(name, ".xml.gz"))
}

func (f *Fetcher) listingsFromAppStreamRPM(rpmPath string, release int, pkgs map[string]rpmrepo.Package) (
	[]catalog.Listing, error) {
	file, err := os.Open(rpmPath)
	if err != nil {
		return nil, err
	}
	defer file.Close()

	var listings []catalog.Listing
	cached := map[int][]string{} // listing index to the file names of its cached icons, best first
	files := 0
	err = extractFiles(file, isCatalogXML, func(name string, data io.Reader) error {
		files++
		r := data
		if strings.HasSuffix(name, ".gz") {
			zr, err := gzip.NewReader(data)
			if err != nil {
				return fmt.Errorf("%s: %w", name, err)
			}
			defer zr.Close()
			r = zr
		}
		return appstream.Parse(r, func(c *appstream.Component) error {
			if l, ok := toListing(c, release, pkgs); ok {
				if l.IconURL == "" {
					if names := cachedIcons(c); len(names) > 0 {
						cached[len(listings)] = names
					}
				}
				listings = append(listings, l)
			}
			return nil
		})
	})
	if err != nil {
		return nil, err
	}
	if files == 0 {
		return nil, errors.New("no AppStream catalog found in appstream-data")
	}
	if f.IconDir != "" && f.IconBase != "" && len(cached) > 0 {
		if err := f.addCachedIcons(rpmPath, listings, cached); err != nil && f.Log != nil {
			f.Log.Warn("fedora icons from appstream-data skipped", "err", err)
		}
	}
	return listings, nil
}

// cachedIcons returns the file names of a component's cached icons, the largest first.
func cachedIcons(c *appstream.Component) []string {
	var icons []appstream.Icon
	for _, ic := range c.Icons {
		if ic.Type == "cached" && ic.Value != "" && !strings.Contains(ic.Value, "/") && ic.Scale <= 1 {
			icons = append(icons, ic)
		}
	}
	sort.SliceStable(icons, func(i, j int) bool { return icons[i].Width > icons[j].Width })
	var names []string
	for _, ic := range icons {
		names = append(names, fmt.Sprintf("%dx%d/%s", ic.Width, ic.Height, ic.Value))
	}
	return names
}

// iconFile matches the icon files in appstream-data: /usr/share/swcatalog/icons/fedora/128x128/app.png
// (older releases: /usr/share/app-info/icons/fedora/...).
var iconFile = regexp.MustCompile(`/(?:swcatalog|app-info)/icons/[^/]+/(\d+x\d+/[^/]+\.png)$`)

// addCachedIcons reads the icon files that listings name out of appstream-data, saves them in IconDir under
// their hash and points the listings at their published address.
func (f *Fetcher) addCachedIcons(rpmPath string, listings []catalog.Listing, cached map[int][]string) error {
	want := map[string]bool{}
	for _, names := range cached {
		for _, n := range names {
			want[n] = true
		}
	}
	found := map[string][]byte{} // "128x128/app.png" to its contents
	file, err := os.Open(rpmPath)
	if err != nil {
		return err
	}
	defer file.Close()
	err = extractFiles(file, func(name string) bool {
		m := iconFile.FindStringSubmatch(name)
		return m != nil && want[m[1]]
	}, func(name string, data io.Reader) error {
		b, err := io.ReadAll(io.LimitReader(data, 1<<20))
		if err == nil {
			found[iconFile.FindStringSubmatch(name)[1]] = b
		}
		return err
	})
	if err != nil {
		return err
	}
	if err := os.MkdirAll(f.IconDir, 0o755); err != nil {
		return err
	}
	base := strings.TrimSuffix(f.IconBase, "/") + "/icons/"
	added := 0
	for i, names := range cached {
		for _, n := range names {
			b, ok := found[n]
			if _, err := png.DecodeConfig(bytes.NewReader(b)); !ok || err != nil {
				continue // missing, or not a picture (appstream-data has a few empty files)
			}
			sum := sha256.Sum256(b)
			name := hex.EncodeToString(sum[:]) + ".png"
			if err := os.WriteFile(filepath.Join(f.IconDir, name), b, 0o644); err != nil {
				return err
			}
			listings[i].IconURL = base + name
			added++
			break
		}
	}
	if f.Log != nil {
		f.Log.Info("fedora icons from appstream-data", "apps", added)
	}
	return nil
}

var packageName = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._+-]*$`)

func toListing(c *appstream.Component, release int, pkgs map[string]rpmrepo.Package) (catalog.Listing, bool) {
	var kind catalog.Kind
	switch c.Type {
	case "desktop-application", "desktop":
		kind = catalog.KindDesktopApp
	case "console-application":
		kind = catalog.KindConsoleApp
	default:
		return catalog.Listing{}, false
	}
	p, ok := pkgs[c.PkgName]
	if !ok || c.ID == "" || c.Name == "" || !packageName.MatchString(c.PkgName) {
		return catalog.Listing{}, false
	}
	distro := catalog.FedoraDistro(release)
	l := catalog.Listing{
		Source:      catalog.SourceFedora,
		Ref:         distro + "/" + p.Name,
		AppStreamID: c.ID,
		Kind:        kind,
		Name:        c.Name,
		Summary:     c.Summary,
		Description: c.Description,
		Developer:   c.Developer,
		License:     c.License,
		Homepage:    c.URLs["homepage"],
		VCSURL:      c.URLs["vcs-browser"],
		BugTracker:  c.URLs["bugtracker"],
		IconURL:     c.BestRemoteIcon(),
		Categories:  c.Categories,
		Keywords:    c.Keywords,
		Version:     p.Version,
		Released:    p.Built,
		Package:     p.Name,
		Distro:      distro,
	}
	for _, s := range c.Screenshots {
		l.Screenshots = append(l.Screenshots, catalog.Screenshot{URL: s.Source, ThumbURL: s.Thumbnail,
			Caption: s.Caption})
	}
	changelog.FromAppStream(&l, c.Releases)
	return l, true
}
