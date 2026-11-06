// Package debian lists the apps in Debian's and Ubuntu's package archives, for installing with apt.
//
// Each release's AppStream data comes from its DEP-11 files (dists/<codename>/<component>/dep11/
// Components-<arch>.yml.gz) and package versions from its Packages indexes, release and -updates pockets.
// Debian's stable release and Ubuntu's three newest supported releases (from changelogs.ubuntu.com/
// meta-release) are indexed. Listings carry the release as Distro ("debian-13", "ubuntu-24.04"). These are static files on
// mirrors and CDNs, a few dozen per run, most answered 304 Not Modified.
package debian

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"path/filepath"
	"sort"
	"strings"

	"github.com/tyler-builds/uas/internal/catalog"
	"github.com/tyler-builds/uas/internal/indexer/appstream"
	"github.com/tyler-builds/uas/internal/indexer/dep11"
	"github.com/tyler-builds/uas/internal/indexer/fetch"
	"github.com/tyler-builds/uas/internal/indexer/fetch/distro"
)

// Release is one release of the distribution.
type Release struct {
	Codename string // "noble"
	Version  string // "24.04", or "13" for Debian
}

// Fetcher lists one distribution's apps.
type Fetcher struct {
	Distribution catalog.SourceID // catalog.SourceDebian or catalog.SourceUbuntu
	Client       *http.Client
	CacheDir     string
	Arch         string // "x86_64" or "aarch64"
	// Releases, when set, are indexed instead of the ones found automatically.
	Releases []Release
	// Mirror is the archive's base URL; a default for the distribution and architecture when empty.
	Mirror string
	// MetaRelease is Ubuntu's list of releases, for tests.
	MetaRelease string
	Log         *slog.Logger
}

// Source implements fetch.Fetcher.
func (f *Fetcher) Source() catalog.SourceID {
	if f.Distribution == "" {
		return catalog.SourceDebian
	}
	return f.Distribution
}

func (f *Fetcher) debArch() (string, error) {
	switch f.Arch {
	case "x86_64":
		return "amd64", nil
	case "aarch64":
		return "arm64", nil
	}
	return "", fmt.Errorf("%s: unsupported architecture %q", f.Source(), f.Arch)
}

func (f *Fetcher) mirror() string {
	if f.Mirror != "" {
		return strings.TrimSuffix(f.Mirror, "/")
	}
	if f.Source() == catalog.SourceUbuntu {
		if f.Arch == "aarch64" {
			return "https://ports.ubuntu.com/ubuntu-ports"
		}
		return "https://mirrors.edge.kernel.org/ubuntu"
	}
	return "https://deb.debian.org/debian"
}

func (f *Fetcher) components() []string {
	if f.Source() == catalog.SourceUbuntu {
		return []string{"main", "universe", "restricted", "multiverse"}
	}
	return []string{"main", "contrib", "non-free"}
}

// Distro is the Distro value of a release's listings.
func (f *Fetcher) Distro(r Release) string { return string(f.Source()) + "-" + r.Version }

// Fetch implements fetch.Fetcher.
func (f *Fetcher) Fetch(ctx context.Context) ([]catalog.Listing, error) {
	arch, err := f.debArch()
	if err != nil {
		return nil, err
	}
	releases := f.Releases
	if len(releases) == 0 {
		if releases, err = f.discover(ctx); err != nil {
			return nil, err
		}
	}
	var all []catalog.Listing
	for _, r := range releases {
		ls, err := f.release(ctx, r, arch)
		if err != nil {
			return nil, fmt.Errorf("%s %s: %w", f.Source(), r.Codename, err)
		}
		if f.Log != nil {
			f.Log.Info(string(f.Source())+" release indexed", "release", r.Codename, "apps", len(ls))
		}
		all = append(all, ls...)
	}
	sort.Slice(all, func(i, j int) bool { return all[i].Ref < all[j].Ref })
	return all, nil
}

// release lists one release's apps.
func (f *Fetcher) release(ctx context.Context, r Release, arch string) ([]catalog.Listing, error) {
	base := f.mirror() + "/dists/"
	pkgs := map[string]distro.Package{}
	for _, suite := range []string{r.Codename, r.Codename + "-updates"} {
		for _, comp := range f.components() {
			u := base + suite + "/" + comp + "/binary-" + arch + "/Packages.xz"
			if err := f.readPackages(ctx, u, pkgs); err != nil {
				if suite != r.Codename || comp != "main" {
					continue // an -updates pocket or a smaller component may have no index for the architecture
				}
				return nil, err
			}
		}
	}
	distroName := f.Distro(r)
	seen := map[string]bool{}
	var listings []catalog.Listing
	for _, comp := range f.components() {
		u := base + r.Codename + "/" + comp + "/dep11/Components-" + arch + ".yml.gz"
		path, err := fetch.Get(ctx, f.Client, u, filepath.Join(f.CacheDir, string(f.Source())))
		if err != nil {
			if comp == "main" {
				return nil, err
			}
			continue // not every component carries AppStream data
		}
		rc, err := fetch.OpenDecompressed(path)
		if err != nil {
			return nil, err
		}
		err = dep11.Parse(rc, func(c *appstream.Component) error {
			l, ok := distro.Listing(c, f.Source(), distroName, pkgs)
			if ok && !seen[l.Ref] {
				seen[l.Ref] = true
				listings = append(listings, l)
			}
			return nil
		})
		rc.Close()
		if err != nil {
			return nil, fmt.Errorf("%s: %w", u, err)
		}
	}
	return listings, nil
}

// readPackages adds the packages of a Packages index to pkgs; later indexes override earlier ones, so an
// -updates pocket gives the current version.
func (f *Fetcher) readPackages(ctx context.Context, u string, pkgs map[string]distro.Package) error {
	path, err := fetch.Get(ctx, f.Client, u, filepath.Join(f.CacheDir, string(f.Source())))
	if err != nil {
		return err
	}
	rc, err := fetch.OpenDecompressed(path)
	if err != nil {
		return err
	}
	defer rc.Close()
	return parsePackages(rc, pkgs)
}

func parsePackages(r io.Reader, pkgs map[string]distro.Package) error {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 1<<20), 16<<20)
	var name, version string
	flush := func() {
		if name != "" && version != "" {
			pkgs[name] = distro.Package{Name: name, Version: version}
		}
		name, version = "", ""
	}
	for sc.Scan() {
		line := sc.Text()
		switch {
		case line == "":
			flush()
		case strings.HasPrefix(line, "Package: "):
			name = strings.TrimSpace(line[len("Package: "):])
		case strings.HasPrefix(line, "Version: "):
			version = strings.TrimSpace(line[len("Version: "):])
		}
	}
	flush()
	return sc.Err()
}

// ubuntuReleases is how many of the newest supported Ubuntu releases are indexed.
const ubuntuReleases = 3

// discover finds the releases to index: Debian's stable, or Ubuntu's supported releases.
func (f *Fetcher) discover(ctx context.Context) ([]Release, error) {
	if f.Source() == catalog.SourceDebian {
		path, err := fetch.Get(ctx, f.Client, f.mirror()+"/dists/stable/Release", filepath.Join(f.CacheDir, "debian"))
		if err != nil {
			return nil, err
		}
		rc, err := fetch.OpenDecompressed(path)
		if err != nil {
			return nil, err
		}
		defer rc.Close()
		st := stanzas(rc)
		if len(st) == 0 || st[0]["Codename"] == "" || st[0]["Version"] == "" {
			return nil, errors.New("debian: no codename or version in stable's Release file")
		}
		major, _, _ := strings.Cut(st[0]["Version"], ".")
		return []Release{{Codename: st[0]["Codename"], Version: major}}, nil
	}
	meta := f.MetaRelease
	if meta == "" {
		meta = "https://changelogs.ubuntu.com/meta-release"
	}
	path, err := fetch.Get(ctx, f.Client, meta, filepath.Join(f.CacheDir, "ubuntu"))
	if err != nil {
		return nil, err
	}
	rc, err := fetch.OpenDecompressed(path)
	if err != nil {
		return nil, err
	}
	defer rc.Close()
	var out []Release
	for _, s := range stanzas(rc) {
		if s["Supported"] != "1" || s["Dist"] == "" {
			continue
		}
		// "24.04.5 LTS" is release 24.04.
		v := strings.Fields(s["Version"])
		if len(v) == 0 {
			continue
		}
		parts := strings.Split(v[0], ".")
		if len(parts) < 2 {
			continue
		}
		out = append(out, Release{Codename: s["Dist"], Version: parts[0] + "." + parts[1]})
	}
	if len(out) == 0 {
		return nil, errors.New("ubuntu: no supported releases in meta-release")
	}
	// Releases past standard support (14.04 and the like, under paid extended support) are still marked
	// supported; the newest few are what people install on.
	if len(out) > ubuntuReleases {
		out = out[len(out)-ubuntuReleases:]
	}
	return out, nil
}

// stanzas reads RFC 822-style stanzas ("Key: value" lines, blank lines between).
func stanzas(r io.Reader) []map[string]string {
	var out []map[string]string
	cur := map[string]string{}
	sc := bufio.NewScanner(r)
	for sc.Scan() {
		line := sc.Text()
		if strings.TrimSpace(line) == "" {
			if len(cur) > 0 {
				out = append(out, cur)
				cur = map[string]string{}
			}
			continue
		}
		if k, v, ok := strings.Cut(line, ":"); ok && !strings.HasPrefix(line, " ") {
			cur[k] = strings.TrimSpace(v)
		}
	}
	if len(cur) > 0 {
		out = append(out, cur)
	}
	return out
}
