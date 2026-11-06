// Package forge finds Linux apps released on code forges other than GitHub: Codeberg and GitLab.
//
// Each forge's fetcher lists popular repos and reads their recent releases. A repo whose latest stable
// release has an AppImage or Flatpak bundle for the target architecture becomes a listing, scored like a
// GitHub repo (trust.GitHub). Neither forge publishes checksums for release files, so the indexer downloads
// each file it lists once, records its SHA-256 in the signed catalog, and reads the AppImage for the app's
// name, icon and screenshots on the way. Both are kept per release file, so only new releases are downloaded.
package forge

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/tyler-builds/uas/internal/catalog"
	"github.com/tyler-builds/uas/internal/client/appimage"
	"github.com/tyler-builds/uas/internal/indexer/changelog"
	"github.com/tyler-builds/uas/internal/indexer/fetch"
	"github.com/tyler-builds/uas/internal/indexer/fetch/github"
	"github.com/tyler-builds/uas/internal/indexer/trust"
)

// Repo is what a forge says about one repo.
type Repo struct {
	Path        string // "owner/repo", or "group/subgroup/project" on GitLab
	Name        string
	Description string
	Homepage    string
	WebURL      string
	Owner       string
	Stars       int
	Created     time.Time
	Archived    bool
	Fork        bool
	Mirror      bool
	License     string // SPDX ID, or ""
	// Releases are the newest few releases, newest first.
	Releases []Release
}

// Release is one release of a repo.
type Release struct {
	Tag        string
	Published  time.Time
	Prerelease bool
	Draft      bool
	Files      []github.ReleaseFile
	Notes      string // Markdown
	URL        string // the release's page
}

// recentReleases is how many releases are read per repo, for release activity.
const recentReleases = 8

// DefaultMinScore leaves out repos with little evidence behind them, as for GitHub.
const DefaultMinScore = github.DefaultMinScore

// MediaVersion changes whenever what is read from release files changes, so cached facts are read again.
const MediaVersion = 1

// Builder turns repos into listings: it picks release files, scores the repo, and records each file's
// checksum and what its AppImage says.
type Builder struct {
	Source   catalog.SourceID
	Client   *http.Client
	Arch     string
	MinScore int
	// MediaDir keeps checksums and facts per release file (under forge/), and icons (github.IconDir).
	MediaDir string
	IconBase string
	// FreeHost, when set, is the forge's name if it hosts only free software (Codeberg).
	FreeHost string
	Log      *slog.Logger
	Now      func() time.Time
}

// Listings builds the listings of repos, leaving out those without a Linux file, below MinScore, or whose
// files couldn't be downloaded.
func (b *Builder) Listings(ctx context.Context, repos []Repo) []catalog.Listing {
	now := time.Now()
	if b.Now != nil {
		now = b.Now()
	}
	minScore := b.MinScore
	if minScore == 0 {
		minScore = DefaultMinScore
	}
	var candidates []catalog.Listing
	skipped := 0
	for _, r := range repos {
		l, ok := b.listing(r, now)
		if !ok {
			continue
		}
		if l.Trust.Score < minScore {
			skipped++
			continue
		}
		candidates = append(candidates, l)
	}

	// Download and check each listing's files, a few at a time.
	out := make([]*catalog.Listing, len(candidates))
	work := make(chan int)
	var wg sync.WaitGroup
	for range downloadWorkers {
		wg.Go(func() {
			for i := range work {
				if l, ok := b.pin(ctx, candidates[i]); ok {
					out[i] = &l
				}
			}
		})
	}
	for i := range candidates {
		work <- i
	}
	close(work)
	wg.Wait()

	var listings []catalog.Listing
	for _, l := range out {
		if l != nil {
			listings = append(listings, *l)
		}
	}
	if b.Log != nil {
		b.Log.Info(string(b.Source)+" repos checked", "repos", len(repos), "kept", len(listings),
			"below_min_score", skipped, "unpinned", len(candidates)-len(listings))
	}
	return listings
}

const downloadWorkers = 4

// assetPrefix is where a repo's release files must be. GitLab release links are reached through the
// project's /-/releases/<tag>/downloads/ address, whatever they point at.
func assetPrefix(src catalog.SourceID, path string) string {
	if src == catalog.SourceGitLab {
		return "https://" + src.Host() + "/" + path + "/-/"
	}
	return "https://" + src.Host() + "/" + path + "/releases/download/"
}

// listing builds a listing from a repo's metadata, before its files are checked.
func (b *Builder) listing(r Repo, now time.Time) (catalog.Listing, bool) {
	if r.Archived || r.Fork || r.Mirror {
		return catalog.Listing{}, false
	}
	var latest *Release
	releasesLastYear, prereleases := 0, 0
	for i, rel := range r.Releases {
		if rel.Draft {
			continue
		}
		if latest == nil && !rel.Prerelease {
			latest = &r.Releases[i]
		}
		if now.Sub(rel.Published) <= 365*24*time.Hour {
			releasesLastYear++
			if rel.Prerelease {
				prereleases++
			}
		}
	}
	if latest == nil {
		return catalog.Listing{}, false
	}
	var assets []catalog.Asset
	for _, a := range github.PickAssets(latest.Files, assetPrefix(b.Source, r.Path), b.Arch) {
		// RPMs aren't installable from releases, so they aren't worth downloading to pin.
		if a.Kind != catalog.AssetRPM {
			assets = append(assets, a)
		}
	}
	if len(assets) == 0 {
		return catalog.Listing{}, false
	}
	downloads := 0
	for _, f := range latest.Files {
		downloads += f.Downloads
	}
	more := releasesLastYear == len(r.Releases) && len(r.Releases) == recentReleases
	t := trust.GitHub(trust.RepoFacts{
		Created: r.Created, Stars: r.Stars, Downloads: downloads, LastRelease: latest.Published,
		ReleasesLastYear: releasesLastYear, PrereleasesLastYear: prereleases, MoreReleases: more,
		License: r.License, IndexedChecksum: true, FreeHost: b.FreeHost,
	}, now)
	name := r.Name
	if name == "" {
		name = r.Path[strings.LastIndex(r.Path, "/")+1:]
	}
	l := catalog.Listing{
		Source:    b.Source,
		Ref:       b.Source.Host() + "/" + r.Path,
		Kind:      catalog.KindDesktopApp,
		Name:      github.DisplayName(name, assets),
		Summary:   strings.TrimSpace(r.Description),
		Developer: r.Owner,
		License:   r.License,
		Homepage:  r.Homepage,
		VCSURL:    r.WebURL,
		Version:   strings.TrimPrefix(latest.Tag, "v"),
		Released:  latest.Published,
		Assets:    assets,
		Trust:     &t,
		Stars:     r.Stars,
	}
	for _, rel := range r.Releases {
		if !rel.Draft && !rel.Prerelease && !rel.Published.After(latest.Published) {
			changelog.Add(&l, strings.TrimPrefix(rel.Tag, "v"), rel.Published, rel.Notes, rel.URL)
		}
	}
	return l, true
}

// pinned is what is kept per release file.
type pinned struct {
	SHA256  string        `json:"sha256"`
	Size    int64         `json:"size"`
	Facts   *github.Facts `json:"facts,omitempty"`
	Problem string        `json:"problem,omitempty"` // why the file couldn't be downloaded
	At      time.Time     `json:"at"`
}

// retryProblemsAfter is how long a failed download is remembered before it is tried again.
const retryProblemsAfter = 24 * time.Hour

// pin records each of a listing's files' checksum and size, and fills in what its AppImage says. Files that
// can't be downloaded are dropped, and a listing left with none is dropped.
func (b *Builder) pin(ctx context.Context, l catalog.Listing) (catalog.Listing, bool) {
	var kept []catalog.Asset
	var facts *github.Facts
	for _, a := range l.Assets {
		p := b.pinned(ctx, l.Ref, a)
		if p.Problem != "" {
			if b.Log != nil {
				b.Log.Debug(string(b.Source)+" file skipped", "ref", l.Ref, "file", a.Name, "why", p.Problem)
			}
			continue
		}
		a.SHA256, a.Size = p.SHA256, p.Size
		kept = append(kept, a)
		if facts == nil && p.Facts != nil {
			facts = p.Facts
		}
	}
	if len(kept) == 0 {
		return l, false
	}
	l.Assets = kept
	if facts != nil {
		github.ApplyFacts(&l, *facts, b.IconBase)
	}
	return l, true
}

// pinned returns a file's checksum and facts, from the cache when they are there.
func (b *Builder) pinned(ctx context.Context, ref string, a catalog.Asset) pinned {
	sum := sha256.Sum256([]byte(fmt.Sprintf("v%d|%d|%s", MediaVersion, github.MediaVersion, a.URL)))
	cache := filepath.Join(b.MediaDir, "forge", hex.EncodeToString(sum[:])+".json")
	now := time.Now()
	var p pinned
	if raw, err := os.ReadFile(cache); err == nil && json.Unmarshal(raw, &p) == nil &&
		(p.Problem == "" || now.Sub(p.At) < retryProblemsAfter) {
		os.Chtimes(cache, now, now) // in use: keep it from cache pruning
		if p.Facts != nil && p.Facts.Icon != "" {
			os.Chtimes(filepath.Join(github.IconDir(b.MediaDir), p.Facts.Icon), now, now)
		}
		return p
	}
	p = b.download(ctx, ref, a)
	if ctx.Err() != nil {
		return p // cut short: don't remember a partial read
	}
	p.At = now
	if raw, err := json.Marshal(p); err == nil && os.MkdirAll(filepath.Dir(cache), 0o755) == nil {
		os.WriteFile(cache, raw, 0o644)
	}
	return p
}

// download fetches a release file to a temporary file, hashes it and, for an AppImage, reads it.
func (b *Builder) download(ctx context.Context, ref string, a catalog.Asset) pinned {
	dir := filepath.Join(b.MediaDir, "forge")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return pinned{Problem: err.Error()}
	}
	tmp, err := os.CreateTemp(dir, "download-*")
	if err != nil {
		return pinned{Problem: err.Error()}
	}
	defer os.Remove(tmp.Name())
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, a.URL, nil)
	if err != nil {
		tmp.Close()
		return pinned{Problem: err.Error()}
	}
	req.Header.Set("User-Agent", fetch.UserAgent)
	resp, err := b.Client.Do(req)
	if err != nil {
		tmp.Close()
		return pinned{Problem: err.Error()}
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		tmp.Close()
		return pinned{Problem: resp.Status}
	}
	h := sha256.New()
	n, err := io.Copy(io.MultiWriter(tmp, h), io.LimitReader(resp.Body, fetch.MaxDownload+1))
	if cerr := tmp.Close(); err == nil {
		err = cerr
	}
	switch {
	case err != nil:
		return pinned{Problem: err.Error()}
	case n > fetch.MaxDownload:
		return pinned{Problem: "larger than the download limit"}
	case a.Size > 0 && n != a.Size:
		return pinned{Problem: fmt.Sprintf("got %d bytes, the forge said %d", n, a.Size)}
	}
	p := pinned{SHA256: hex.EncodeToString(h.Sum(nil)), Size: n}
	if a.Kind == catalog.AssetAppImage {
		if info, err := appimage.Inspect(tmp.Name()); err == nil {
			fc := github.AppImageFacts(ref, info, b.MediaDir, b.Log)
			p.Facts = &fc
		} else {
			p.Facts = &github.Facts{Problem: err.Error()}
		}
	}
	return p
}

// pacer spaces requests to stay under a forge's rate limit.
type pacer struct {
	mu    sync.Mutex
	every time.Duration
	next  time.Time
}

func (p *pacer) wait(ctx context.Context) error {
	p.mu.Lock()
	now := time.Now()
	at := p.next
	if at.Before(now) {
		at = now
	}
	p.next = at.Add(p.every)
	p.mu.Unlock()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-time.After(time.Until(at)):
		return nil
	}
}

// errRateLimited is returned when a forge keeps refusing requests as over its rate limit.
var errRateLimited = errors.New("rate limited")

// getJSON fetches a JSON document into v, paced, retrying server errors and short rate-limit waits. It
// returns the response headers.
func getJSON(ctx context.Context, c *http.Client, p *pacer, u, token string, v any) (http.Header, error) {
	var lastErr error
	for attempt := 0; attempt < 4; attempt++ {
		if err := p.wait(ctx); err != nil {
			return nil, err
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
		if err != nil {
			return nil, err
		}
		req.Header.Set("User-Agent", fetch.UserAgent)
		req.Header.Set("Accept", "application/json")
		if token != "" {
			req.Header.Set("Authorization", "Bearer "+token)
		}
		resp, err := c.Do(req)
		if err != nil {
			lastErr = err
			continue
		}
		raw, err := io.ReadAll(io.LimitReader(resp.Body, 32<<20))
		resp.Body.Close()
		switch {
		case err != nil:
			lastErr = err
			continue
		case resp.StatusCode == http.StatusOK:
			return resp.Header, json.Unmarshal(raw, v)
		case resp.StatusCode == http.StatusTooManyRequests:
			// Wait if told to wait briefly; a long block ends the fetch, so the last result is reused.
			wait, err := strconv.Atoi(resp.Header.Get("Retry-After"))
			if err != nil || wait > 120 {
				return nil, fmt.Errorf("%w: GET %s (retry after %q)", errRateLimited, u, resp.Header.Get("Retry-After"))
			}
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-time.After(time.Duration(wait+1) * time.Second):
			}
			lastErr = errRateLimited
		case resp.StatusCode >= 500:
			lastErr = &statusError{URL: u, Status: resp.Status, Code: resp.StatusCode}
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-time.After(time.Duration(attempt+1) * serverRetryWait):
			}
		default:
			return nil, &statusError{URL: u, Status: resp.Status, Code: resp.StatusCode,
				Body: strings.TrimSpace(string(raw[:min(len(raw), 800)]))}
		}
	}
	return nil, lastErr
}

// statusError is a response that retrying won't fix, such as 404 Not Found.
// serverRetryWait is how long to wait before the first retry of a server error; later ones wait longer.
var serverRetryWait = 3 * time.Second

type statusError struct {
	URL, Status string
	Code        int
	Body        string // the start of the response, which says why (GitLab: "insufficient_scope")
}

func (e *statusError) Error() string {
	msg := "GET " + e.URL + ": " + e.Status
	if e.Body != "" {
		msg += ": " + e.Body
	}
	return msg
}
