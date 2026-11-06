// Package arch lists the apps in Arch Linux's repositories, for installing with pacman.
//
// Arch publishes its AppStream catalog as the archlinux-appstream-data package, whose sources are also
// served as plain files: sources.archlinux.org/other/packages/archlinux-appstream-data/<version>/extra/
// Components-x86_64.xml.gz. The current version comes from archlinux.org's package JSON, and package versions
// from the repository database (extra.db) on a mirror: three files a run. Arch is a rolling release, so
// listings carry the Distro "arch". Only x86_64 is indexed: Arch Linux ARM is a separate project.
//
// Arch's cached icons are JPEG XL, which uas doesn't decode, so Arch-only apps have no icon; apps also on
// Flathub get Flathub's.
package arch

import (
	"archive/tar"
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/tyler-builds/uas/internal/catalog"
	"github.com/tyler-builds/uas/internal/indexer/appstream"
	"github.com/tyler-builds/uas/internal/indexer/fetch"
	"github.com/tyler-builds/uas/internal/indexer/fetch/distro"
)

// Distro is the Distro of every Arch listing.
const Distro = "arch"

// Fetcher lists Arch's apps.
type Fetcher struct {
	Client   *http.Client
	CacheDir string
	Arch     string
	// Endpoints, for tests.
	PackageJSON string // archlinux.org's JSON for archlinux-appstream-data
	Sources     string // sources.archlinux.org/other/packages/archlinux-appstream-data
	Mirror      string // a mirror's base, such as https://geo.mirror.pkgbuild.com
	Log         *slog.Logger
}

// Source implements fetch.Fetcher.
func (f *Fetcher) Source() catalog.SourceID { return catalog.SourceArch }

// repos are the repositories whose AppStream data is indexed, and pkgRepos those whose packages it may name
// (Steam is in multilib).
var (
	repos    = []string{"extra"}
	pkgRepos = []string{"core", "extra", "multilib"}
)

// Fetch implements fetch.Fetcher.
func (f *Fetcher) Fetch(ctx context.Context) ([]catalog.Listing, error) {
	if f.Arch != "x86_64" {
		return nil, nil // Arch Linux proper is x86_64 only
	}
	pkgJSON, sources, mirror := f.PackageJSON, f.Sources, f.Mirror
	if pkgJSON == "" {
		pkgJSON = "https://archlinux.org/packages/extra/any/archlinux-appstream-data/json/"
	}
	if sources == "" {
		sources = "https://sources.archlinux.org/other/packages/archlinux-appstream-data"
	}
	if mirror == "" {
		mirror = "https://geo.mirror.pkgbuild.com"
	}
	cache := filepath.Join(f.CacheDir, "arch")
	ver, err := f.dataVersion(ctx, pkgJSON, cache)
	if err != nil {
		return nil, err
	}
	pkgs := map[string]distro.Package{}
	for _, repo := range pkgRepos {
		path, err := fetch.Get(ctx, f.Client, fmt.Sprintf("%s/%s/os/x86_64/%s.db", mirror, repo, repo), cache)
		if err != nil {
			if repo != "extra" {
				continue
			}
			return nil, err
		}
		if err := readDB(path, pkgs); err != nil {
			return nil, fmt.Errorf("arch: %s.db: %w", repo, err)
		}
	}
	var listings []catalog.Listing
	seen := map[string]bool{}
	for _, repo := range repos {
		path, err := fetch.Get(ctx, f.Client, fmt.Sprintf("%s/%s/%s/Components-x86_64.xml.gz", sources, ver, repo),
			cache)
		if err != nil {
			return nil, err
		}
		rc, err := fetch.OpenDecompressed(path)
		if err != nil {
			return nil, err
		}
		err = appstream.Parse(rc, func(c *appstream.Component) error {
			if l, ok := distro.Listing(c, catalog.SourceArch, Distro, pkgs); ok && !seen[l.Ref] {
				seen[l.Ref] = true
				listings = append(listings, l)
			}
			return nil
		})
		rc.Close()
		if err != nil {
			return nil, err
		}
	}
	if f.Log != nil {
		f.Log.Info("arch indexed", "appstream_data", ver, "packages", len(pkgs), "apps", len(listings))
	}
	sort.Slice(listings, func(i, j int) bool { return listings[i].Ref < listings[j].Ref })
	return listings, nil
}

var dataVersion = regexp.MustCompile(`^[0-9]{8}$`)

// dataVersion returns the current version of archlinux-appstream-data, such as "20261017".
func (f *Fetcher) dataVersion(ctx context.Context, u, cache string) (string, error) {
	path, err := fetch.Get(ctx, f.Client, u, cache)
	if err != nil {
		return "", err
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	var p struct {
		PkgVer string `json:"pkgver"`
	}
	if err := json.Unmarshal(b, &p); err != nil {
		return "", fmt.Errorf("arch: %w", err)
	}
	if !dataVersion.MatchString(p.PkgVer) {
		return "", fmt.Errorf("arch: unexpected archlinux-appstream-data version %q", p.PkgVer)
	}
	return p.PkgVer, nil
}

// readDB reads a pacman repository database (a gzip'd tar of <name>-<version>/desc files).
func readDB(path string, pkgs map[string]distro.Package) error {
	rc, err := fetch.OpenDecompressed(path)
	if err != nil {
		return err
	}
	defer rc.Close()
	tr := tar.NewReader(rc)
	n := 0
	for {
		h, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return err
		}
		if !strings.HasSuffix(h.Name, "/desc") {
			continue
		}
		p, err := parseDesc(io.LimitReader(tr, 1<<20))
		if err != nil {
			return err
		}
		if p.Name != "" {
			pkgs[p.Name] = p
			n++
		}
	}
	if n == 0 {
		return errors.New("no packages")
	}
	return nil
}

// parseDesc reads a desc file: "%NAME%\nfoo\n\n%VERSION%\n1.0-1\n\n%BUILDDATE%\n1700000000\n...".
func parseDesc(r io.Reader) (distro.Package, error) {
	var p distro.Package
	sc := bufio.NewScanner(r)
	key := ""
	for sc.Scan() {
		line := sc.Text()
		switch {
		case strings.HasPrefix(line, "%") && strings.HasSuffix(line, "%"):
			key = line
		case line == "":
			key = ""
		case key == "%NAME%":
			p.Name = line
		case key == "%VERSION%":
			p.Version = line
		case key == "%BUILDDATE%":
			if t, err := strconv.ParseInt(line, 10, 64); err == nil {
				p.Built = time.Unix(t, 0).UTC()
			}
		}
	}
	return p, sc.Err()
}
