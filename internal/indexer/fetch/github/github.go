// Package github finds Linux apps that are distributed only as GitHub release files.
//
// It runs a set of repository searches through the GraphQL API, keeps repos whose latest stable release has a
// Linux AppImage, Flatpak bundle or RPM for the target architecture, and scores each with the trust package.
// Repos below MinScore, archived repos and forks are left out.
package github

import (
	"bytes"
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
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/tyler-builds/uas/internal/catalog"
	"github.com/tyler-builds/uas/internal/indexer/fetch"
	"github.com/tyler-builds/uas/internal/indexer/trust"
)

// Endpoint is GitHub's GraphQL API.
const Endpoint = "https://api.github.com/graphql"

// DefaultQueries find candidate repos. Searches return at most 1,000 results, so broad ones are sliced by
// stars.
var DefaultQueries = []string{
	"appimage in:readme archived:false fork:false stars:>=2000",
	"appimage in:readme archived:false fork:false stars:500..1999",
	"appimage in:readme archived:false fork:false stars:200..499",
	"topic:appimage archived:false fork:false stars:>=20",
	"topic:linux-app archived:false fork:false stars:>=50",
}

// DefaultMinScore leaves out repos with little evidence behind them.
const DefaultMinScore = 35

// Fetcher searches GitHub.
type Fetcher struct {
	Client   *http.Client
	Token    string // a GitHub token; the GraphQL API requires one
	Arch     string // "x86_64" or "aarch64"
	Queries  []string
	MinScore int
	Endpoint string
	Log      *slog.Logger
	Now      func() time.Time
	// CacheDir, if set, keeps the last results; a fresh enough copy (see CacheFor) is reused instead of
	// searching again. A full search takes several minutes and GitHub data changes slowly.
	CacheDir string
	CacheFor time.Duration
}

type cached struct {
	Arch     string            `json:"arch"`
	Queries  []string          `json:"queries"`
	Saved    time.Time         `json:"saved"`
	Listings []catalog.Listing `json:"listings"`
}

func (f *Fetcher) cachePath() string { return filepath.Join(f.CacheDir, "github-listings.json") }

// fromCache returns cached listings if they are fresh and were made with the same settings.
func (f *Fetcher) fromCache(queries []string, now time.Time) ([]catalog.Listing, bool) {
	if f.CacheDir == "" || f.CacheFor <= 0 {
		return nil, false
	}
	b, err := os.ReadFile(f.cachePath())
	if err != nil {
		return nil, false
	}
	var c cached
	if json.Unmarshal(b, &c) != nil || c.Arch != f.Arch || !slices.Equal(c.Queries, queries) ||
		now.Sub(c.Saved) > f.CacheFor {
		return nil, false
	}
	if f.Log != nil {
		f.Log.Info("github results reused from cache", "saved", c.Saved, "listings", len(c.Listings))
	}
	return c.Listings, true
}

func (f *Fetcher) saveCache(queries []string, now time.Time, listings []catalog.Listing) error {
	if f.CacheDir == "" {
		return nil
	}
	b, err := json.Marshal(cached{Arch: f.Arch, Queries: queries, Saved: now, Listings: listings})
	if err != nil {
		return err
	}
	if err := os.MkdirAll(f.CacheDir, 0o755); err != nil {
		return err
	}
	tmp := f.cachePath() + ".tmp"
	if err := os.WriteFile(tmp, b, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, f.cachePath())
}

// Source implements fetch.Fetcher.
func (f *Fetcher) Source() catalog.SourceID { return catalog.SourceGitHub }

// Fetch implements fetch.Fetcher.
func (f *Fetcher) Fetch(ctx context.Context) ([]catalog.Listing, error) {
	if f.Token == "" {
		return nil, errors.New("github: no token; set GITHUB_TOKEN")
	}
	if f.Arch != "x86_64" && f.Arch != "aarch64" {
		return nil, fmt.Errorf("github: unsupported architecture %q", f.Arch)
	}
	queries, minScore, now := f.Queries, f.MinScore, time.Now()
	if len(queries) == 0 {
		queries = DefaultQueries
	}
	if minScore == 0 {
		minScore = DefaultMinScore
	}
	if f.Now != nil {
		now = f.Now()
	}
	if ls, ok := f.fromCache(queries, now); ok {
		return ls, nil
	}

	seen := map[string]bool{}
	var listings []catalog.Listing
	checked, skipped := 0, 0
	for _, q := range queries {
		var after *string
		pageSize := 50
		for page := 0; page < 40; {
			var res searchResult
			err := f.query(ctx, searchQuery, map[string]any{"q": q, "after": after, "first": pageSize}, &res)
			if err != nil && strings.Contains(err.Error(), "Resource limits") && pageSize > 5 {
				// Some pages hold repos with very many release files; GitHub refuses those as too costly.
				pageSize /= 2
				continue
			}
			if err != nil {
				return nil, fmt.Errorf("github: search %q: %w", q, err)
			}
			page++
			for _, r := range res.Search.Nodes {
				if r.ID == "" || seen[r.ID] {
					continue
				}
				seen[r.ID] = true
				checked++
				l, ok := toListing(r, f.Arch, now)
				if !ok {
					continue
				}
				if l.Trust.Score < minScore {
					skipped++
					continue
				}
				listings = append(listings, l)
			}
			if !res.Search.PageInfo.HasNextPage {
				break
			}
			after = &res.Search.PageInfo.EndCursor
		}
	}
	if f.Log != nil {
		f.Log.Info("github repos checked", "repos", checked, "kept", len(listings), "below_min_score", skipped)
	}
	sort.Slice(listings, func(i, j int) bool { return listings[i].Ref < listings[j].Ref })
	if err := f.saveCache(queries, now, listings); err != nil && f.Log != nil {
		f.Log.Warn("saving github results failed", "err", err)
	}
	return listings, nil
}

// searchQuery reads, for each repo, its latest release with files plus the dates of recent releases (for
// release cadence). Asking for files of several releases per repo exceeds GitHub's per-query limits.
const searchQuery = `query($q: String!, $after: String, $first: Int!) {
  search(query: $q, type: REPOSITORY, first: $first, after: $after) {
    pageInfo { hasNextPage endCursor }
    nodes { ... on Repository {
      id nameWithOwner name description homepageUrl url stargazerCount createdAt isArchived isFork
      licenseInfo { spdxId }
      owner { login }
      latestRelease { tagName publishedAt isPrerelease isDraft
        releaseAssets(first: 40) { nodes { name downloadUrl size digest downloadCount } } }
      releases(first: 8, orderBy: {field: CREATED_AT, direction: DESC}) {
        nodes { publishedAt isPrerelease isDraft }
      }
    } }
  }
}`

type searchResult struct {
	Search struct {
		PageInfo struct {
			HasNextPage bool   `json:"hasNextPage"`
			EndCursor   string `json:"endCursor"`
		} `json:"pageInfo"`
		Nodes []repo `json:"nodes"`
	} `json:"search"`
}

type repo struct {
	ID             string    `json:"id"`
	NameWithOwner  string    `json:"nameWithOwner"`
	Name           string    `json:"name"`
	Description    string    `json:"description"`
	HomepageURL    string    `json:"homepageUrl"`
	URL            string    `json:"url"`
	StargazerCount int       `json:"stargazerCount"`
	CreatedAt      time.Time `json:"createdAt"`
	IsArchived     bool      `json:"isArchived"`
	IsFork         bool      `json:"isFork"`
	LicenseInfo    *struct {
		SpdxID string `json:"spdxId"`
	} `json:"licenseInfo"`
	Owner struct {
		Login string `json:"login"`
	} `json:"owner"`
	LatestRelease *release `json:"latestRelease"`
	Releases      struct {
		Nodes []release `json:"nodes"`
	} `json:"releases"`
}

type release struct {
	TagName       string    `json:"tagName"`
	PublishedAt   time.Time `json:"publishedAt"`
	IsPrerelease  bool      `json:"isPrerelease"`
	IsDraft       bool      `json:"isDraft"`
	ReleaseAssets struct {
		Nodes []asset `json:"nodes"`
	} `json:"releaseAssets"`
}

type asset struct {
	Name          string `json:"name"`
	DownloadURL   string `json:"downloadUrl"`
	Size          int64  `json:"size"`
	Digest        string `json:"digest"`
	DownloadCount int    `json:"downloadCount"`
}

// query runs a GraphQL query into out, retrying when GitHub's API times out under load.
func (f *Fetcher) query(ctx context.Context, query string, vars map[string]any, out any) error {
	var err error
	for attempt := 0; attempt < 4; attempt++ {
		if attempt > 0 {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(time.Duration(attempt*attempt) * 2 * time.Second):
			}
		}
		var retry bool
		retry, err = f.queryOnce(ctx, query, vars, out)
		if err == nil || !retry {
			return err
		}
	}
	return err
}

func (f *Fetcher) queryOnce(ctx context.Context, query string, vars map[string]any, dst any) (bool, error) {
	body, err := json.Marshal(map[string]any{"query": query, "variables": vars})
	if err != nil {
		return false, err
	}
	endpoint := f.Endpoint
	if endpoint == "" {
		endpoint = Endpoint
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return false, err
	}
	req.Header.Set("Authorization", "Bearer "+f.Token)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", fetch.UserAgent)
	resp, err := f.Client.Do(req)
	if err != nil {
		return true, err
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 64<<20))
	if err != nil {
		return true, err
	}
	switch resp.StatusCode {
	case http.StatusOK:
	case http.StatusBadGateway, http.StatusServiceUnavailable, http.StatusGatewayTimeout:
		return true, errors.New(resp.Status)
	case http.StatusForbidden, http.StatusTooManyRequests:
		// A secondary rate limit: wait as told (or a minute), then let the caller retry.
		if strings.Contains(string(raw), "rate limit") {
			wait := time.Minute
			if sec, err := strconv.Atoi(resp.Header.Get("Retry-After")); err == nil && sec > 0 && sec <= 300 {
				wait = time.Duration(sec) * time.Second
			}
			select {
			case <-ctx.Done():
				return false, ctx.Err()
			case <-time.After(wait):
			}
			return true, errors.New("secondary rate limit")
		}
		return false, fmt.Errorf("%s: %s", resp.Status, strings.TrimSpace(string(raw[:min(len(raw), 300)])))
	default:
		return false, fmt.Errorf("%s: %s", resp.Status, strings.TrimSpace(string(raw[:min(len(raw), 300)])))
	}
	var out struct {
		Data   json.RawMessage `json:"data"`
		Errors []struct {
			Message string `json:"message"`
		} `json:"errors"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return false, err
	}
	if len(out.Errors) > 0 {
		return false, errors.New(out.Errors[0].Message)
	}
	if len(out.Data) == 0 || string(out.Data) == "null" {
		return false, errors.New("empty response")
	}
	return false, json.Unmarshal(out.Data, dst)
}

func toListing(r repo, arch string, now time.Time) (catalog.Listing, bool) {
	if r.IsArchived || r.IsFork {
		return catalog.Listing{}, false
	}
	latest := r.LatestRelease
	if latest == nil || latest.IsPrerelease || latest.IsDraft {
		return catalog.Listing{}, false
	}
	releasesLastYear := 0
	for _, rel := range r.Releases.Nodes {
		if !rel.IsPrerelease && !rel.IsDraft && now.Sub(rel.PublishedAt) <= 365*24*time.Hour {
			releasesLastYear++
		}
	}
	assets := pickAssets(r.NameWithOwner, latest, arch)
	if len(assets) == 0 {
		return catalog.Listing{}, false
	}
	downloads := 0
	for _, a := range latest.ReleaseAssets.Nodes {
		downloads += a.DownloadCount
	}
	license := ""
	if r.LicenseInfo != nil {
		license = r.LicenseInfo.SpdxID
	}
	t := trust.GitHub(trust.RepoFacts{
		Created: r.CreatedAt, Stars: r.StargazerCount, Downloads: downloads, LastRelease: latest.PublishedAt,
		ReleasesLastYear: releasesLastYear, License: license, HasChecksum: assets[0].SHA256 != "",
	}, now)

	kind := catalog.KindDesktopApp
	return catalog.Listing{
		Source:    catalog.SourceGitHub,
		Ref:       r.NameWithOwner,
		Kind:      kind,
		Name:      r.Name,
		Summary:   strings.TrimSpace(r.Description),
		Developer: r.Owner.Login,
		License:   license,
		Homepage:  r.HomepageURL,
		VCSURL:    r.URL,
		Version:   strings.TrimPrefix(latest.TagName, "v"),
		Released:  latest.PublishedAt,
		Assets:    assets,
		Trust:     &t,
	}, true
}

var (
	otherArchX86 = regexp.MustCompile(`(?i)(aarch64|arm64|armv7|armhf|arm32|[^a-z]arm[^a-z]|i386|i686|x86[-_]?32|ppc64|s390x|riscv)`)
	otherArchARM = regexp.MustCompile(`(?i)(x86[-_]64|amd64|x64|i386|i686|ppc64|s390x|riscv|armv7|armhf)`)
	thisArchX86  = regexp.MustCompile(`(?i)(x86[-_]64|amd64|x64)`)
	thisArchARM  = regexp.MustCompile(`(?i)(aarch64|arm64)`)
)

// kindOf classifies a release file by name.
func kindOf(name string) string {
	n := strings.ToLower(name)
	switch {
	case strings.HasSuffix(n, ".appimage"):
		return catalog.AssetAppImage
	case strings.HasSuffix(n, ".flatpak"):
		return catalog.AssetFlatpak
	case strings.HasSuffix(n, ".rpm") && !strings.HasSuffix(n, ".src.rpm"):
		return catalog.AssetRPM
	}
	return ""
}

// pickAssets returns the best asset of each kind for arch: AppImage first, then Flatpak bundle, then RPM.
// A file naming the architecture beats one that names none; a file naming another architecture is skipped.
// Only files under the repo's own release download URL are accepted.
func pickAssets(repo string, rel *release, arch string) []catalog.Asset {
	other, this := otherArchX86, thisArchX86
	if arch == "aarch64" {
		other, this = otherArchARM, thisArchARM
	}
	prefix := "https://github.com/" + repo + "/releases/download/"
	best := map[string]catalog.Asset{}
	bestExplicit := map[string]bool{}
	for _, a := range rel.ReleaseAssets.Nodes {
		kind := kindOf(a.Name)
		explicit := this.MatchString(a.Name)
		// A file that names no architecture is almost always x86_64, so only x86_64 accepts one.
		if kind == "" || (!explicit && (other.MatchString(a.Name) || arch != "x86_64")) ||
			!strings.HasPrefix(a.DownloadURL, prefix) {
			continue
		}
		if _, ok := best[kind]; ok && (bestExplicit[kind] || !explicit) {
			continue
		}
		sha := ""
		if d, ok := strings.CutPrefix(a.Digest, "sha256:"); ok && len(d) == 64 {
			sha = strings.ToLower(d)
		}
		best[kind] = catalog.Asset{Kind: kind, Name: a.Name, URL: a.DownloadURL, Size: a.Size, SHA256: sha,
			Downloads: a.DownloadCount}
		bestExplicit[kind] = explicit
	}
	var out []catalog.Asset
	for _, k := range []string{catalog.AssetAppImage, catalog.AssetFlatpak, catalog.AssetRPM} {
		if a, ok := best[k]; ok {
			out = append(out, a)
		}
	}
	return out
}
