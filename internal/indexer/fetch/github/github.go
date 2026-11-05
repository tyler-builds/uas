// Package github finds Linux apps that are distributed only as GitHub release files.
//
// It runs a set of repository searches through the GraphQL API, keeps repos whose latest stable release has a
// Linux AppImage, Flatpak bundle or RPM for the target architecture, and scores each with the trust package.
// Repos below MinScore, archived repos and forks are left out.
//
// Those searches only find repos that call themselves AppImage or Linux apps. A sweep (SweepMinStars) also
// looks at every popular repo pushed in the past year, whatever its readme says, so apps such as an editor
// that just ships an AppImage among its release files are found too.
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
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"

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

// DefaultSweepMinStars is the star count from which the sweep looks at a repo.
const DefaultSweepMinStars = 2000

// sweepPushedWithin is how recently a swept repo must have been pushed to.
const sweepPushedWithin = 365 * 24 * time.Hour

// searchCap is how many results a GitHub search returns at most.
const searchCap = 1000

// DefaultMinScore leaves out repos with little evidence behind them.
const DefaultMinScore = 35

// Fetcher searches GitHub.
type Fetcher struct {
	Client   *http.Client
	Token    string // a GitHub token; the GraphQL API requires one
	Arch     string // "x86_64" or "aarch64"
	Queries  []string
	MinScore int
	// SweepMinStars, when set, also checks every repo with at least this many stars pushed in the past
	// year.
	SweepMinStars int
	// SweepCache, when set, is a file that keeps the repos the sweep found for SweepFor (default a week).
	// The sweep reads some 21,000 repos and takes about 25 minutes; the repos it finds are re-read in full
	// on every run, so versions stay current while new apps show up within a week.
	SweepCache string
	SweepFor   time.Duration
	// MediaDir, when set, is where the names, icons and screenshots read from AppImages and READMEs are kept
	// (see enrich.go). Without it listings carry only what the GitHub API says.
	MediaDir string
	// IconBase is the address icons are published under: an icon is at IconBase/icons/<file>.
	IconBase string
	// RESTEndpoint is GitHub's REST API, and RawEndpoint where it serves repo files, for tests.
	RESTEndpoint string
	RawEndpoint  string
	Endpoint     string
	Log          *slog.Logger
	Now          func() time.Time
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
	if f.SweepMinStars > 0 {
		found, err := f.sweptRepos(ctx, now)
		if err != nil {
			return nil, err
		}
		var ids []string
		for _, id := range found {
			if !seen[id] {
				seen[id] = true
				ids = append(ids, id)
			}
		}
		repos, err := f.reposByID(ctx, ids)
		if err != nil {
			return nil, err
		}
		for _, r := range repos {
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
	}
	if f.MediaDir != "" {
		f.enrich(ctx, listings)
	}
	if f.Log != nil {
		f.Log.Info("github repos checked", "repos", checked, "kept", len(listings), "below_min_score", skipped)
	}
	sort.Slice(listings, func(i, j int) bool { return listings[i].Ref < listings[j].Ref })
	return listings, nil
}

// sweepCache is what SweepCache holds.
type sweepCache struct {
	Key string    `json:"key"`
	At  time.Time `json:"at"`
	IDs []string  `json:"ids"`
}

// sweptRepos returns the IDs of popular repos with a Linux file, from SweepCache while it is fresh.
func (f *Fetcher) sweptRepos(ctx context.Context, now time.Time) ([]string, error) {
	key := fmt.Sprintf("arch=%s min-stars=%d", f.Arch, f.SweepMinStars)
	keep := f.SweepFor
	if keep == 0 {
		keep = 7 * 24 * time.Hour
	}
	if f.SweepCache != "" {
		var c sweepCache
		if raw, err := os.ReadFile(f.SweepCache); err == nil && json.Unmarshal(raw, &c) == nil &&
			c.Key == key && now.Sub(c.At) < keep {
			os.Chtimes(f.SweepCache, now, now) // in use: keep it from cache pruning, which goes by file time
			if f.Log != nil {
				f.Log.Info("github sweep reused", "with_linux_files", len(c.IDs), "age", now.Sub(c.At).Round(time.Minute))
			}
			return c.IDs, nil
		}
	}
	ids, swept, err := f.sweep(ctx, f.SweepMinStars, now.Add(-sweepPushedWithin), map[string]bool{})
	if err != nil {
		return nil, err
	}
	if f.Log != nil {
		f.Log.Info("github sweep", "repos", swept, "with_linux_files", len(ids))
	}
	if f.SweepCache != "" {
		raw, _ := json.Marshal(sweepCache{Key: key, At: now, IDs: ids})
		if err := os.MkdirAll(filepath.Dir(f.SweepCache), 0o755); err == nil {
			err = os.WriteFile(f.SweepCache, raw, 0o644)
		}
		if err != nil && f.Log != nil {
			f.Log.Warn("saving the github sweep failed", "err", err)
		}
	}
	return ids, nil
}

// sweep pages through popular repos pushed since pushed, reading only their latest release's file names, and
// returns the IDs of those not yet seen that have a Linux file for the architecture. Star ranges are split
// until each search fits under GitHub's 1,000-result cap, then read a few at a time.
func (f *Fetcher) sweep(ctx context.Context, minStars int, pushed time.Time, seen map[string]bool) (
	ids []string, swept int, err error) {
	filter := " pushed:>=" + pushed.UTC().Format("2006-01-02") + " archived:false fork:false"
	var queries []string
	ranges := [][2]int{{minStars, 0}} // 0: no upper bound
	for len(ranges) > 0 {
		lo, hi := ranges[0][0], ranges[0][1]
		ranges = ranges[1:]
		q := starRange(lo, hi) + filter
		var count struct {
			Search struct {
				RepositoryCount int `json:"repositoryCount"`
			} `json:"search"`
		}
		if err := f.query(ctx, countQuery, map[string]any{"q": q}, &count); err != nil {
			return nil, 0, fmt.Errorf("github: count %q: %w", q, err)
		}
		if count.Search.RepositoryCount > searchCap && hi != lo {
			mid := lo * 2
			if hi != 0 {
				mid = lo + (hi-lo+1)/2
			}
			ranges = append(ranges, [2]int{lo, mid - 1}, [2]int{mid, hi})
			continue
		}
		queries = append(queries, q)
	}

	// A few searches at once: each page takes seconds, and GitHub allows modest concurrency.
	var mu sync.Mutex
	work := make(chan string)
	errs := make(chan error, sweepWorkers)
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	for range sweepWorkers {
		go func() {
			for q := range work {
				err := f.sweepOne(ctx, q, func(r repo) {
					mu.Lock()
					defer mu.Unlock()
					if r.ID == "" || seen[r.ID] {
						return
					}
					seen[r.ID] = true
					swept++
					rel := r.LatestRelease
					if rel != nil && !rel.IsPrerelease && !rel.IsDraft && len(pickAssets(r.NameWithOwner, rel, f.Arch)) > 0 {
						ids = append(ids, r.ID)
					}
				})
				if err != nil {
					cancel()
					errs <- err
					return
				}
			}
			errs <- nil
		}()
	}
feed:
	for _, q := range queries {
		select {
		case work <- q:
		case <-ctx.Done():
			break feed
		}
	}
	close(work)
	for range sweepWorkers {
		if e := <-errs; e != nil && err == nil {
			err = e
		}
	}
	if err != nil {
		return nil, 0, err
	}
	sort.Strings(ids) // a stable order, whatever order the searches finished in
	return ids, swept, nil
}

// sweepWorkers is how many sweep searches run at once.
const sweepWorkers = 4

// sweepOne pages through one sweep search.
func (f *Fetcher) sweepOne(ctx context.Context, q string, each func(repo)) error {
	var after *string
	pageSize := 100
	for {
		var res struct {
			Search struct {
				PageInfo struct {
					HasNextPage bool   `json:"hasNextPage"`
					EndCursor   string `json:"endCursor"`
				} `json:"pageInfo"`
				Nodes []repo `json:"nodes"`
			} `json:"search"`
		}
		err := f.query(ctx, sweepQuery, map[string]any{"q": q, "after": after, "first": pageSize}, &res)
		if err != nil && strings.Contains(err.Error(), "Resource limits") && pageSize > 5 {
			pageSize /= 2
			continue
		}
		if err != nil {
			return fmt.Errorf("github: sweep %q: %w", q, err)
		}
		for _, r := range res.Search.Nodes {
			each(r)
		}
		if !res.Search.PageInfo.HasNextPage {
			return nil
		}
		after = &res.Search.PageInfo.EndCursor
	}
}

// starRange is a search qualifier for stars from lo to hi, or lo and up when hi is 0.
func starRange(lo, hi int) string {
	if hi == 0 {
		return fmt.Sprintf("stars:>=%d", lo)
	}
	return fmt.Sprintf("stars:%d..%d", lo, hi)
}

// reposByID reads the full details of repos, a page at a time.
func (f *Fetcher) reposByID(ctx context.Context, ids []string) ([]repo, error) {
	var out []repo
	pageSize := 25
	for len(ids) > 0 {
		n := min(pageSize, len(ids))
		var res struct {
			Nodes []repo `json:"nodes"`
		}
		err := f.query(ctx, nodesQuery, map[string]any{"ids": ids[:n]}, &res)
		if err != nil && strings.Contains(err.Error(), "Resource limits") && pageSize > 1 {
			pageSize /= 2
			continue
		}
		if err != nil {
			return nil, fmt.Errorf("github: read repos: %w", err)
		}
		for _, r := range res.Nodes {
			if r.ID != "" {
				out = append(out, r)
			}
		}
		ids = ids[n:]
	}
	return out, nil
}

const countQuery = `query($q: String!) { search(query: $q, type: REPOSITORY, first: 1) { repositoryCount } }`

// sweepQuery reads only what's needed to tell whether a repo ships a Linux file.
const sweepQuery = `query($q: String!, $after: String, $first: Int!) {
  search(query: $q, type: REPOSITORY, first: $first, after: $after) {
    pageInfo { hasNextPage endCursor }
    nodes { ... on Repository { id nameWithOwner
      latestRelease { isPrerelease isDraft releaseAssets(first: 40) { nodes { name downloadUrl } } } } }
  }
}`

// recentReleases is how many of a repo's newest releases are read for its release activity.
const (
	recentReleases     = 8
	recentReleasesText = "8"
)

// repoFields is everything toListing reads about a repo.
const repoFields = `id nameWithOwner name description homepageUrl url stargazerCount createdAt isArchived isFork
      licenseInfo { spdxId }
      owner { login }
      latestRelease { tagName publishedAt isPrerelease isDraft
        releaseAssets(first: 40) { nodes { name downloadUrl size digest downloadCount } } }
      releases(first: ` + recentReleasesText + `, orderBy: {field: CREATED_AT, direction: DESC}) {
        nodes { publishedAt isPrerelease isDraft }
      }`

const nodesQuery = `query($ids: [ID!]!) { nodes(ids: $ids) { ... on Repository { ` + repoFields + ` } } }`

// searchQuery reads, for each repo, its latest release with files plus the dates of recent releases (for
// release cadence). Asking for files of several releases per repo exceeds GitHub's per-query limits.
const searchQuery = `query($q: String!, $after: String, $first: Int!) {
  search(query: $q, type: REPOSITORY, first: $first, after: $after) {
    pageInfo { hasNextPage endCursor }
    nodes { ... on Repository { ` + repoFields + ` } }
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
	releasesLastYear, prereleases, inYear := 0, 0, 0
	for _, rel := range r.Releases.Nodes {
		if now.Sub(rel.PublishedAt) > 365*24*time.Hour {
			continue
		}
		inYear++
		if rel.IsDraft {
			continue
		}
		releasesLastYear++
		if rel.IsPrerelease {
			prereleases++
		}
	}
	// Every release read is from the past year, so there may be more than were read.
	more := inYear == len(r.Releases.Nodes) && len(r.Releases.Nodes) == recentReleases
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
		ReleasesLastYear: releasesLastYear, PrereleasesLastYear: prereleases, MoreReleases: more, License: license, HasChecksum: assets[0].SHA256 != "",
	}, now)

	kind := catalog.KindDesktopApp
	return catalog.Listing{
		Source:    catalog.SourceGitHub,
		Ref:       r.NameWithOwner,
		Kind:      kind,
		Name:      displayName(r.Name, assets),
		Summary:   strings.TrimSpace(r.Description),
		Developer: r.Owner.Login,
		License:   license,
		Homepage:  r.HomepageURL,
		VCSURL:    r.URL,
		Version:   strings.TrimPrefix(latest.TagName, "v"),
		Released:  latest.PublishedAt,
		Assets:    assets,
		Trust:     &t,
		Stars:     r.StargazerCount,
	}, true
}

var (
	otherArchX86 = regexp.MustCompile(`(?i)(aarch64|arm64|armv7|armhf|arm32|[^a-z]arm[^a-z]|i386|i686|x86[-_]?32|ppc64|s390x|riscv)`)
	otherArchARM = regexp.MustCompile(`(?i)(x86[-_]64|amd64|x64|i386|i686|ppc64|s390x|riscv|armv7|armhf)`)
	thisArchX86  = regexp.MustCompile(`(?i)(x86[-_]64|amd64|x64)`)
	thisArchARM  = regexp.MustCompile(`(?i)(aarch64|arm64)`)
)

// kindOf classifies a release file by name.
// displayName is the app's name as its release files spell it, when they spell the repo name: a repo
// "t3code" whose AppImage is "T3-Code-0.1.7-x86_64.AppImage" is "T3 Code". Otherwise it is the repo name.
func displayName(repoName string, assets []catalog.Asset) string {
	for _, a := range assets {
		base := a.Name[:len(a.Name)-len(filepath.Ext(a.Name))]
		var words []string
		for _, w := range strings.FieldsFunc(base, func(r rune) bool { return r == '-' || r == '_' || r == ' ' || r == '.' }) {
			if versionOrArch.MatchString(w) {
				break
			}
			words = append(words, w)
		}
		if len(words) > 0 && squash(strings.Join(words, "")) == squash(repoName) {
			return strings.Join(words, " ")
		}
	}
	return repoName
}

// versionOrArch matches the words in a file name that come after the app's name.
var versionOrArch = regexp.MustCompile(`(?i)^(v?\d.*|x86|x64|amd64|arm64|aarch64|i[36]86|linux|setup|portable|appimage)$`)

// squash lowercases s and drops everything but letters and digits.
func squash(s string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(s) {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			b.WriteRune(r)
		}
	}
	return b.String()
}

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
