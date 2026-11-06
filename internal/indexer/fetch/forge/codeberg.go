package forge

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"sort"
	"time"

	"github.com/tyler-builds/uas/internal/catalog"
	"github.com/tyler-builds/uas/internal/indexer/fetch/github"
)

// CodebergAPI is Codeberg's Forgejo API.
const CodebergAPI = "https://codeberg.org/api/v1"

// DefaultCodebergMinStars is the star count from which Codeberg repos are checked.
const DefaultCodebergMinStars = 25

// codebergEvery spaces Codeberg requests. Codeberg allows 2,000 keyless requests per 10 minutes (its
// RateLimit-Policy header: "baseline";q=2000;w=600); two a second is 1,200.
const codebergEvery = 500 * time.Millisecond

// Codeberg lists apps released on Codeberg. It needs no key.
type Codeberg struct {
	Builder
	API      string // CodebergAPI, or a test server
	MinStars int
	// MaxPages caps the search, 50 repos a page.
	MaxPages int
	pace     pacer
}

// NewCodeberg returns a Codeberg fetcher.
func NewCodeberg(b Builder) *Codeberg {
	b.Source, b.FreeHost = catalog.SourceCodeberg, "Codeberg"
	return &Codeberg{Builder: b}
}

// Source implements fetch.Fetcher.
func (f *Codeberg) Source() catalog.SourceID { return catalog.SourceCodeberg }

type cbRepo struct {
	FullName    string    `json:"full_name"`
	Name        string    `json:"name"`
	Description string    `json:"description"`
	Website     string    `json:"website"`
	HTMLURL     string    `json:"html_url"`
	Stars       int       `json:"stars_count"`
	CreatedAt   time.Time `json:"created_at"`
	Archived    bool      `json:"archived"`
	Fork        bool      `json:"fork"`
	Mirror      bool      `json:"mirror"`
	Private     bool      `json:"private"`
	Empty       bool      `json:"empty"`
	Releases    int       `json:"release_counter"`
	Owner       struct {
		Login string `json:"login"`
	} `json:"owner"`
}

type cbRelease struct {
	TagName     string    `json:"tag_name"`
	PublishedAt time.Time `json:"published_at"`
	Prerelease  bool      `json:"prerelease"`
	Draft       bool      `json:"draft"`
	Body        string    `json:"body"`
	HTMLURL     string    `json:"html_url"`
	Assets      []struct {
		Name          string `json:"name"`
		Size          int64  `json:"size"`
		DownloadCount int    `json:"download_count"`
		URL           string `json:"browser_download_url"`
	} `json:"assets"`
}

// Fetch implements fetch.Fetcher.
func (f *Codeberg) Fetch(ctx context.Context) ([]catalog.Listing, error) {
	if f.Arch != "x86_64" && f.Arch != "aarch64" {
		return nil, fmt.Errorf("codeberg: unsupported architecture %q", f.Arch)
	}
	api, minStars, maxPages := f.API, f.MinStars, f.MaxPages
	if api == "" {
		api = CodebergAPI
	}
	if minStars == 0 {
		minStars = DefaultCodebergMinStars
	}
	if maxPages == 0 {
		maxPages = 60
	}
	f.pace.every = codebergEvery
	client := f.Client
	if client == nil {
		client = http.DefaultClient
	}

	// The most starred repos first, until they drop below minStars.
	var candidates []cbRepo
	seen := map[string]bool{}
search:
	for page := 1; page <= maxPages; page++ {
		var res struct {
			Data []cbRepo `json:"data"`
		}
		u := fmt.Sprintf("%s/repos/search?sort=stars&order=desc&limit=50&page=%d", api, page)
		if _, err := getJSON(ctx, client, &f.pace, u, "", &res); err != nil {
			return nil, fmt.Errorf("codeberg: search: %w", err)
		}
		if len(res.Data) == 0 {
			break
		}
		for _, r := range res.Data {
			if r.Stars < minStars {
				break search
			}
			if seen[r.FullName] || r.Private || r.Empty || r.Archived || r.Fork || r.Mirror || r.Releases == 0 {
				continue
			}
			seen[r.FullName] = true
			candidates = append(candidates, r)
		}
	}

	var repos []Repo
	for _, r := range candidates {
		var rels []cbRelease
		u := fmt.Sprintf("%s/repos/%s/releases?limit=%d", api, escapePath(r.FullName), recentReleases)
		if _, err := getJSON(ctx, client, &f.pace, u, "", &rels); err != nil {
			var se *statusError
			if errors.As(err, &se) && se.Code == http.StatusNotFound {
				continue
			}
			return nil, fmt.Errorf("codeberg: releases of %s: %w", r.FullName, err)
		}
		repo := Repo{Path: r.FullName, Name: r.Name, Description: r.Description, Homepage: r.Website,
			WebURL: r.HTMLURL, Owner: r.Owner.Login, Stars: r.Stars, Created: r.CreatedAt}
		for _, rel := range rels {
			out := Release{Tag: rel.TagName, Published: rel.PublishedAt, Prerelease: rel.Prerelease, Draft: rel.Draft,
				Notes: rel.Body, URL: rel.HTMLURL}
			for _, a := range rel.Assets {
				out.Files = append(out.Files, github.ReleaseFile{Name: a.Name, URL: a.URL, Size: a.Size,
					Downloads: a.DownloadCount})
			}
			repo.Releases = append(repo.Releases, out)
		}
		// Newest first, whatever order the API used.
		sort.SliceStable(repo.Releases, func(i, j int) bool {
			return repo.Releases[i].Published.After(repo.Releases[j].Published)
		})
		repos = append(repos, repo)
	}
	listings := f.Listings(ctx, repos)
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	sort.Slice(listings, func(i, j int) bool { return listings[i].Ref < listings[j].Ref })
	return listings, nil
}

// escapePath escapes each segment of an "owner/repo" path.
func escapePath(p string) string {
	var out []byte
	start := 0
	for i := 0; i <= len(p); i++ {
		if i == len(p) || p[i] == '/' {
			if len(out) > 0 {
				out = append(out, '/')
			}
			out = append(out, url.PathEscape(p[start:i])...)
			start = i + 1
		}
	}
	return string(out)
}
