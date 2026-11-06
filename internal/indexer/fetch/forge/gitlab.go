package forge

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/tyler-builds/uas/internal/catalog"
	"github.com/tyler-builds/uas/internal/indexer/fetch/github"
)

// GitLabAPI is GitLab.com's REST API.
const GitLabAPI = "https://gitlab.com/api/v4"

// DefaultGitLabMinStars is the star count from which GitLab projects are checked.
const DefaultGitLabMinStars = 50

// gitlabEvery spaces GitLab requests. With a token GitLab.com allows 2,000 requests a minute; this is 600.
// Keyless requests are limited to about 60 an hour per IP, too few to index with, so a token is required.
const gitlabEvery = 100 * time.Millisecond

// GitLab lists apps released on GitLab.com. It needs a personal access token with the read_api scope.
type GitLab struct {
	Builder
	Token    string
	API      string // GitLabAPI, or a test server
	MinStars int
	// MaxPages caps the project list, 100 projects a page.
	MaxPages int
	pace     pacer
}

// NewGitLab returns a GitLab fetcher.
func NewGitLab(b Builder, token string) *GitLab {
	b.Source = catalog.SourceGitLab
	return &GitLab{Builder: b, Token: token}
}

// Source implements fetch.Fetcher.
func (f *GitLab) Source() catalog.SourceID { return catalog.SourceGitLab }

type glProject struct {
	ID          int       `json:"id"`
	Path        string    `json:"path_with_namespace"`
	Name        string    `json:"name"`
	Description string    `json:"description"`
	WebURL      string    `json:"web_url"`
	Stars       int       `json:"star_count"`
	CreatedAt   time.Time `json:"created_at"`
	Archived    bool      `json:"archived"`
	ForkedFrom  *struct {
		ID int `json:"id"`
	} `json:"forked_from_project"`
	Namespace struct {
		Name string `json:"name"`
	} `json:"namespace"`
	License *struct {
		Key string `json:"key"`
	} `json:"license"`
}

type glRelease struct {
	TagName     string    `json:"tag_name"`
	ReleasedAt  time.Time `json:"released_at"`
	Upcoming    bool      `json:"upcoming_release"`
	Description string    `json:"description"`
	Links       struct {
		Self string `json:"self"`
	} `json:"_links"`
	Assets struct {
		Links []struct {
			Name      string `json:"name"`
			URL       string `json:"url"`
			DirectURL string `json:"direct_asset_url"`
		} `json:"links"`
	} `json:"assets"`
}

// prereleaseTag matches tags of test releases; GitLab has no prerelease flag.
var prereleaseTag = regexp.MustCompile(`(?i)(alpha|beta|[-.]rc\d*|preview|nightly|snapshot|dev)`)

// Fetch implements fetch.Fetcher.
func (f *GitLab) Fetch(ctx context.Context) ([]catalog.Listing, error) {
	if f.Token == "" {
		return nil, errors.New("gitlab: no token; set GITLAB_TOKEN (keyless requests are limited to about 60 an hour)")
	}
	if f.Arch != "x86_64" && f.Arch != "aarch64" {
		return nil, fmt.Errorf("gitlab: unsupported architecture %q", f.Arch)
	}
	api, minStars, maxPages := f.API, f.MinStars, f.MaxPages
	if api == "" {
		api = GitLabAPI
	}
	if minStars == 0 {
		minStars = DefaultGitLabMinStars
	}
	if maxPages == 0 {
		maxPages = 20
	}
	f.pace.every = gitlabEvery
	client := f.Client
	if client == nil {
		client = http.DefaultClient
	}
	now := time.Now()
	if f.Now != nil {
		now = f.Now()
	}
	active := now.AddDate(-1, 0, 0).UTC().Format(time.RFC3339)

	var candidates []glProject
	seen := map[int]bool{}
list:
	for page := 1; page <= maxPages; page++ {
		var res []glProject
		u := fmt.Sprintf("%s/projects?order_by=star_count&sort=desc&per_page=100&page=%d&visibility=public"+
			"&archived=false&last_activity_after=%s", api, page, url.QueryEscape(active))
		if _, err := getJSON(ctx, client, &f.pace, u, f.Token, &res); err != nil {
			return nil, fmt.Errorf("gitlab: list projects: %w", err)
		}
		if len(res) == 0 {
			break
		}
		for _, p := range res {
			if p.Stars < minStars {
				break list
			}
			if seen[p.ID] || p.Archived || p.ForkedFrom != nil {
				continue
			}
			seen[p.ID] = true
			candidates = append(candidates, p)
		}
	}

	var repos []Repo
	for _, p := range candidates {
		var rels []glRelease
		u := fmt.Sprintf("%s/projects/%d/releases?per_page=%d", api, p.ID, recentReleases)
		if _, err := getJSON(ctx, client, &f.pace, u, f.Token, &rels); err != nil {
			var se *statusError
			if errors.As(err, &se) && (se.Code == http.StatusNotFound || se.Code == http.StatusForbidden) {
				continue // releases turned off, or hidden
			}
			return nil, fmt.Errorf("gitlab: releases of %s: %w", p.Path, err)
		}
		repo := Repo{Path: p.Path, Name: p.Name, Description: p.Description, WebURL: p.WebURL,
			Owner: p.Namespace.Name, Stars: p.Stars, Created: p.CreatedAt}
		for _, rel := range rels {
			out := Release{Tag: rel.TagName, Published: rel.ReleasedAt, Draft: rel.Upcoming,
				Prerelease: prereleaseTag.MatchString(rel.TagName), Notes: rel.Description, URL: rel.Links.Self}
			for _, l := range rel.Assets.Links {
				u := l.DirectURL
				if u == "" {
					u = l.URL
				}
				out.Files = append(out.Files, github.ReleaseFile{Name: l.Name, URL: u})
			}
			repo.Releases = append(repo.Releases, out)
		}
		sort.SliceStable(repo.Releases, func(i, j int) bool {
			return repo.Releases[i].Published.After(repo.Releases[j].Published)
		})
		// The license is only on a project's own page, so it's read for projects that ship a Linux file.
		if hasLinuxFile(f.Source(), repo, f.Arch) {
			var full glProject
			if _, err := getJSON(ctx, client, &f.pace, fmt.Sprintf("%s/projects/%d?license=true", api, p.ID),
				f.Token, &full); err == nil && full.License != nil {
				repo.License = spdx(full.License.Key)
			}
		}
		repos = append(repos, repo)
	}
	listings := f.Listings(ctx, repos)
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	sort.Slice(listings, func(i, j int) bool { return listings[i].Ref < listings[j].Ref })
	return listings, nil
}

// hasLinuxFile reports whether a repo's latest stable release has a file uas could list.
func hasLinuxFile(src catalog.SourceID, r Repo, arch string) bool {
	for _, rel := range r.Releases {
		if rel.Draft || rel.Prerelease {
			continue
		}
		return len(github.PickAssets(rel.Files, assetPrefix(src, r.Path), arch)) > 0
	}
	return false
}

// spdx turns GitLab's license key ("gpl-3.0", "mit") into its SPDX ID ("GPL-3.0", "MIT").
func spdx(key string) string {
	if key == "" || key == "other" {
		return ""
	}
	if id, ok := spdxIDs[key]; ok {
		return id
	}
	return strings.ToUpper(key)
}

var spdxIDs = map[string]string{
	"apache-2.0": "Apache-2.0", "bsd-2-clause": "BSD-2-Clause", "bsd-3-clause": "BSD-3-Clause",
	"unlicense": "Unlicense", "0bsd": "0BSD", "bsl-1.0": "BSL-1.0", "zlib": "Zlib", "cc0-1.0": "CC0-1.0",
}
