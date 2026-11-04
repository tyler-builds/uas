// Package copr fetches desktop apps from Fedora COPR, the community build service.
//
// COPR has tens of thousands of projects, so the fetcher narrows them down before downloading anything:
// projects that are listed on COPR's front page, have AppStream metadata turned on, and build for one of the
// indexed Fedora releases. Each remaining project's repo metadata is read for apps, and the project gets a
// trust score from its build history. Projects below MinScore are left out.
package copr

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/tyler-builds/uas/internal/catalog"
	"github.com/tyler-builds/uas/internal/indexer/appstream"
	"github.com/tyler-builds/uas/internal/indexer/fetch"
	"github.com/tyler-builds/uas/internal/indexer/fetch/rpmrepo"
	"github.com/tyler-builds/uas/internal/indexer/trust"
)

// Defaults for the public COPR instance.
const (
	DefaultAPIBase      = "https://copr.fedorainfracloud.org/api_3"
	DefaultDownloadBase = "https://download.copr.fedorainfracloud.org/results/"
	DefaultMinScore     = 30
)

// Fetcher reads COPR.
type Fetcher struct {
	Client   *http.Client
	CacheDir string
	Arch     string
	// Releases to index; if empty, the two newest Fedora releases COPR builds for.
	Releases     []int
	MinScore     int
	APIBase      string
	DownloadBase string // chroot repos must live under this URL
	Workers      int
	Log          *slog.Logger
	Now          func() time.Time
}

// Source implements fetch.Fetcher.
func (f *Fetcher) Source() catalog.SourceID { return catalog.SourceCOPR }

type project struct {
	FullName    string            `json:"full_name"`
	Owner       string            `json:"ownername"`
	Name        string            `json:"name"`
	Description string            `json:"description"`
	Homepage    string            `json:"homepage"`
	Contact     string            `json:"contact"`
	AppStream   bool              `json:"appstream"`
	Unlisted    bool              `json:"unlisted_on_hp"`
	ChrootRepos map[string]string `json:"chroot_repos"`
}

var (
	ownerName   = regexp.MustCompile(`^@?[A-Za-z0-9][A-Za-z0-9_.-]*$`)
	projectName = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.+-]*$`)
	packageName = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._+-]*$`)
)

// Fetch implements fetch.Fetcher.
func (f *Fetcher) Fetch(ctx context.Context) ([]catalog.Listing, error) {
	if f.Arch != "x86_64" && f.Arch != "aarch64" {
		return nil, fmt.Errorf("copr: unsupported architecture %q", f.Arch)
	}
	if f.APIBase == "" {
		f.APIBase = DefaultAPIBase
	}
	if f.DownloadBase == "" {
		f.DownloadBase = DefaultDownloadBase
	}
	if f.MinScore == 0 {
		f.MinScore = DefaultMinScore
	}
	workers := max(f.Workers, 1)
	if f.Workers == 0 {
		workers = 8
	}

	projects, err := f.listProjects(ctx)
	if err != nil {
		return nil, fmt.Errorf("copr: list projects: %w", err)
	}
	releases := f.Releases
	if len(releases) == 0 {
		releases = newestReleases(projects, f.Arch)
	}
	var candidates []project
	for _, p := range projects {
		if !p.AppStream || p.Unlisted || !ownerName.MatchString(p.Owner) || !projectName.MatchString(p.Name) ||
			p.FullName != p.Owner+"/"+p.Name {
			continue
		}
		for _, r := range releases {
			if _, ok := p.ChrootRepos[chroot(r, f.Arch)]; ok {
				candidates = append(candidates, p)
				break
			}
		}
	}
	f.log().Info("copr projects to check", "projects", len(projects), "candidates", len(candidates),
		"releases", releases)

	var (
		mu       sync.Mutex
		listings []catalog.Listing
		failures int
		lowScore int
		wg       sync.WaitGroup
		work     = make(chan project)
	)
	for range workers {
		wg.Go(func() {
			for p := range work {
				ls, err := f.project(ctx, p, releases)
				mu.Lock()
				switch {
				case err != nil:
					failures++
					f.log().Warn("copr project skipped", "project", p.FullName, "err", err)
				case len(ls) > 0 && ls[0].Trust.Score < f.MinScore:
					lowScore++
				default:
					listings = append(listings, ls...)
				}
				mu.Unlock()
			}
		})
	}
	for _, p := range candidates {
		select {
		case work <- p:
		case <-ctx.Done():
		}
	}
	close(work)
	wg.Wait()
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	if len(candidates) > 0 && failures == len(candidates) {
		return nil, errors.New("copr: every project failed")
	}
	f.log().Info("copr projects checked", "listings", len(listings), "failed", failures, "below_min_score", lowScore)
	sort.Slice(listings, func(i, j int) bool { return listings[i].Ref < listings[j].Ref })
	return listings, nil
}

func (f *Fetcher) log() *slog.Logger {
	if f.Log == nil {
		return slog.New(slog.NewTextHandler(io.Discard, nil))
	}
	return f.Log
}

func (f *Fetcher) now() time.Time {
	if f.Now != nil {
		return f.Now()
	}
	return time.Now()
}

func chroot(release int, arch string) string { return fmt.Sprintf("fedora-%d-%s", release, arch) }

var chrootPattern = regexp.MustCompile(`^fedora-(\d+)-`)

// newestReleases returns the two newest numbered Fedora releases that at least 50 projects build for.
func newestReleases(projects []project, arch string) []int {
	counts := map[int]int{}
	for _, p := range projects {
		for c := range p.ChrootRepos {
			if m := chrootPattern.FindStringSubmatch(c); m != nil && strings.HasSuffix(c, "-"+arch) {
				n, _ := strconv.Atoi(m[1])
				counts[n]++
			}
		}
	}
	var rs []int
	for n, c := range counts {
		if c >= 50 {
			rs = append(rs, n)
		}
	}
	sort.Ints(rs)
	if len(rs) > 2 {
		rs = rs[len(rs)-2:]
	}
	return rs
}

func (f *Fetcher) getJSON(ctx context.Context, u string, v any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return err
	}
	req.Header.Set("User-Agent", fetch.UserAgent)
	resp, err := f.Client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("GET %s: %s", u, resp.Status)
	}
	return json.NewDecoder(io.LimitReader(resp.Body, 256<<20)).Decode(v)
}

// listProjects pages through every COPR project.
func (f *Fetcher) listProjects(ctx context.Context) ([]project, error) {
	const page = 1000
	var all []project
	for offset := 0; ; offset += page {
		var res struct {
			Items []project `json:"items"`
		}
		u := fmt.Sprintf("%s/project/list?limit=%d&offset=%d", f.APIBase, page, offset)
		if err := f.getJSON(ctx, u, &res); err != nil {
			return nil, err
		}
		all = append(all, res.Items...)
		if len(res.Items) < page {
			return all, nil
		}
		if offset > 10_000_000 {
			return nil, errors.New("project list did not end")
		}
	}
}

// project reads one project's repos and returns its app listings, all carrying the project's trust score.
func (f *Fetcher) project(ctx context.Context, p project, releases []int) ([]catalog.Listing, error) {
	var listings []catalog.Listing
	for _, r := range releases {
		repo, ok := p.ChrootRepos[chroot(r, f.Arch)]
		if !ok {
			continue
		}
		if !strings.HasPrefix(repo, f.DownloadBase+p.FullName+"/") {
			return nil, fmt.Errorf("repo URL %q is outside COPR's download area", repo)
		}
		ls, err := f.releaseListings(ctx, p, r, repo)
		if err != nil {
			return nil, fmt.Errorf("fedora %d: %w", r, err)
		}
		listings = append(listings, ls...)
	}
	if len(listings) == 0 {
		return nil, nil
	}

	var builds struct {
		Items []struct {
			State     string `json:"state"`
			EndedOn   int64  `json:"ended_on"`
			Submitter string `json:"submitter"`
		} `json:"items"`
	}
	q := url.Values{"ownername": {p.Owner}, "projectname": {p.Name}, "limit": {"30"}, "order": {"id"},
		"order_type": {"DESC"}}
	if err := f.getJSON(ctx, f.APIBase+"/build/list?"+q.Encode(), &builds); err != nil {
		return nil, fmt.Errorf("builds: %w", err)
	}
	facts := trust.COPRFacts{Owner: p.Owner, Homepage: p.Homepage, Contact: p.Contact,
		Description: p.Description}
	for _, b := range builds.Items {
		var ended time.Time
		if b.EndedOn > 0 {
			ended = time.Unix(b.EndedOn, 0).UTC()
		}
		facts.Builds = append(facts.Builds, trust.Build{State: b.State, Ended: ended, Submitter: b.Submitter})
	}
	t := trust.COPR(facts, f.now())
	for i := range listings {
		tt := t
		listings[i].Trust = &tt
	}
	return listings, nil
}

func (f *Fetcher) releaseListings(ctx context.Context, p project, release int, repo string) ([]catalog.Listing, error) {
	files, err := rpmrepo.Repomd(ctx, f.Client, repo, f.CacheDir)
	if err != nil {
		return nil, err
	}
	if files["appstream"] == "" || files["primary"] == "" {
		return nil, nil // AppStream is on for the project but this repo was built without it
	}
	primary, err := fetch.Get(ctx, f.Client, files["primary"], f.CacheDir)
	if err != nil {
		return nil, err
	}
	pkgs := map[string]rpmrepo.Package{}
	err = rpmrepo.ReadPrimary(primary, f.Arch, repo, func(pk rpmrepo.Package) {
		// COPR repos can hold several builds of a package; keep the newest.
		if old, ok := pkgs[pk.Name]; !ok || pk.Built.After(old.Built) {
			pkgs[pk.Name] = pk
		}
	})
	if err != nil {
		return nil, err
	}
	asPath, err := fetch.Get(ctx, f.Client, files["appstream"], f.CacheDir)
	if err != nil {
		return nil, err
	}
	r, err := fetch.OpenDecompressed(asPath)
	if err != nil {
		return nil, err
	}
	defer r.Close()

	distro := catalog.FedoraDistro(release)
	var listings []catalog.Listing
	err = appstream.Parse(r, func(c *appstream.Component) error {
		var kind catalog.Kind
		switch c.Type {
		case "desktop-application", "desktop":
			kind = catalog.KindDesktopApp
		case "console-application":
			kind = catalog.KindConsoleApp
		default:
			return nil
		}
		pk, ok := pkgs[c.PkgName]
		if !ok || c.ID == "" || c.Name == "" || !packageName.MatchString(c.PkgName) {
			return nil
		}
		homepage := c.URLs["homepage"]
		if homepage == "" {
			homepage = p.Homepage
		}
		l := catalog.Listing{
			Source:      catalog.SourceCOPR,
			Ref:         distro + "/" + p.FullName + "/" + pk.Name,
			AppStreamID: c.ID,
			Kind:        kind,
			Name:        c.Name,
			Summary:     c.Summary,
			Description: c.Description,
			Developer:   c.Developer,
			License:     c.License,
			Homepage:    homepage,
			VCSURL:      c.URLs["vcs-browser"],
			BugTracker:  c.URLs["bugtracker"],
			IconURL:     c.BestRemoteIcon(),
			Categories:  c.Categories,
			Keywords:    c.Keywords,
			Version:     pk.Version,
			Released:    pk.Built,
			Package:     pk.Name,
			Distro:      distro,
			Project:     p.FullName,
		}
		for _, s := range c.Screenshots {
			l.Screenshots = append(l.Screenshots, catalog.Screenshot{URL: s.Source, ThumbURL: s.Thumbnail,
				Caption: s.Caption})
		}
		listings = append(listings, l)
		return nil
	})
	return listings, err
}
