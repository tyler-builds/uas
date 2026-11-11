package github

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"

	"github.com/tyler-builds/uas/internal/catalog"
)

// repoPath matches a GitHub repo's path: "owner/name".
var repoPath = regexp.MustCompile(`^[A-Za-z0-9-]{1,39}/[A-Za-z0-9_.-]{1,100}$`)

// StatusError is a GitHub REST response other than 200 OK.
type StatusError struct {
	Path   string
	Code   int
	Status string
}

func (e *StatusError) Error() string { return fmt.Sprintf("%s: %s", e.Path, e.Status) }

// ErrNotFound is returned by Live for a repo GitHub doesn't have (or doesn't show without signing in).
var ErrNotFound = errors.New("GitHub has no such public repo")

// ErrRateLimited is returned by Live when GitHub refuses more requests from this address for now.
var ErrRateLimited = errors.New("GitHub's limit for lookups without an account (60 an hour) is used up; " +
	"try again in an hour")

// ErrNoRelease is returned by Live for a repo with no stable release.
var ErrNoRelease = errNoRelease

// restRepo is the part of GitHub's REST answer for a repo that a listing is built from.
type restRepo struct {
	FullName    string    `json:"full_name"`
	Name        string    `json:"name"`
	Description string    `json:"description"`
	Homepage    string    `json:"homepage"`
	HTMLURL     string    `json:"html_url"`
	Stars       int       `json:"stargazers_count"`
	CreatedAt   time.Time `json:"created_at"`
	Archived    bool      `json:"archived"`
	Fork        bool      `json:"fork"`
	License     *struct {
		SPDXID string `json:"spdx_id"`
	} `json:"license"`
	Owner struct {
		Login string `json:"login"`
	} `json:"owner"`
}

// restRelease is one release in GitHub's REST answer.
type restRelease struct {
	TagName     string    `json:"tag_name"`
	PublishedAt time.Time `json:"published_at"`
	Prerelease  bool      `json:"prerelease"`
	Draft       bool      `json:"draft"`
	Body        string    `json:"body"`
	HTMLURL     string    `json:"html_url"`
	Assets      []struct {
		Name          string `json:"name"`
		URL           string `json:"browser_download_url"`
		Size          int64  `json:"size"`
		Digest        string `json:"digest"`
		DownloadCount int    `json:"download_count"`
	} `json:"assets"`
}

// Live reads one repo through GitHub's REST API and builds its listing as the indexer would, for uas's
// Install from URL on the user's machine: the same release file picking, trust score and release notes. It
// needs no token: GitHub allows 60 requests an hour from an address without one, and Live makes two (the
// repo and its newest releases). With describe set it also reads what the release's AppImage says about the
// app (its name, icon, categories and metainfo, with range requests outside the API), and the README or the
// repo's files for screenshots and an icon, which takes up to two more API requests; MediaDir (which describe
// needs) keeps what it read per release file, and the icons.
//
// Unlike the catalog, Live keeps archived repos, forks and repos with little evidence behind them, flagged
// in the trust score, and a latest release with no Linux file (no Assets), so the caller can say why there is
// nothing to install.
func (f *Fetcher) Live(ctx context.Context, ownerRepo string, describe bool) (catalog.Listing, error) {
	if !repoPath.MatchString(ownerRepo) || strings.Contains(ownerRepo, "..") {
		return catalog.Listing{}, fmt.Errorf("%q is not a GitHub repo (owner/name)", ownerRepo)
	}
	var rr restRepo
	if err := f.liveGet(ctx, "/repos/"+ownerRepo, 1<<20, &rr); err != nil {
		return catalog.Listing{}, err
	}
	var rels []restRelease
	if err := f.liveGet(ctx, "/repos/"+rr.FullName+"/releases?per_page="+recentReleasesText, 16<<20, &rels); err != nil {
		return catalog.Listing{}, err
	}
	r := repo{NameWithOwner: rr.FullName, Name: rr.Name, Description: rr.Description, HomepageURL: rr.Homepage,
		URL: rr.HTMLURL, StargazerCount: rr.Stars, CreatedAt: rr.CreatedAt, IsArchived: rr.Archived, IsFork: rr.Fork}
	if rr.License != nil {
		r.LicenseInfo = &struct {
			SpdxID string `json:"spdxId"`
		}{rr.License.SPDXID}
	}
	r.Owner.Login = rr.Owner.Login
	for _, rel := range rels {
		n := release{TagName: rel.TagName, PublishedAt: rel.PublishedAt, IsPrerelease: rel.Prerelease,
			IsDraft: rel.Draft, Description: rel.Body, URL: rel.HTMLURL}
		for _, a := range rel.Assets {
			n.ReleaseAssets.Nodes = append(n.ReleaseAssets.Nodes, asset{Name: a.Name, DownloadURL: a.URL,
				Size: a.Size, Digest: a.Digest, DownloadCount: a.DownloadCount})
		}
		r.Releases.Nodes = append(r.Releases.Nodes, n)
	}
	// GitHub lists releases newest first; its latest release is the newest stable one.
	for i, rel := range r.Releases.Nodes {
		if !rel.IsDraft && !rel.IsPrerelease {
			r.LatestRelease = &r.Releases.Nodes[i]
			break
		}
	}
	arch := f.Arch
	if arch == "" {
		arch = "x86_64"
	}
	l, err := listingOf(r, arch, f.now())
	if err != nil {
		return catalog.Listing{}, fmt.Errorf("%s %w", rr.FullName, err)
	}
	if rr.Archived {
		l.Trust.Flags = append(l.Trust.Flags, "Archived: the developer has stopped working on it")
	}
	if rr.Fork {
		l.Trust.Flags = append(l.Trust.Flags, "A fork: a copy of another project, made by someone else")
	}
	if describe && len(l.Assets) > 0 && f.MediaDir != "" {
		fc, _ := f.factsFor(ctx, l)
		f.apply(&l, fc)
	}
	l.Live = true
	return l, nil
}

// liveGet is rest with Live's errors: ErrNotFound and ErrRateLimited.
func (f *Fetcher) liveGet(ctx context.Context, path string, limit int64, v any) error {
	err := f.rest(ctx, path, limit, v)
	var se *StatusError
	if errors.As(err, &se) {
		switch se.Code {
		case http.StatusNotFound:
			return ErrNotFound
		case http.StatusForbidden, http.StatusTooManyRequests:
			return ErrRateLimited
		}
	}
	if err != nil {
		var ue *url.Error
		if errors.As(err, &ue) {
			return fmt.Errorf("could not reach GitHub: %w", ue.Err)
		}
	}
	return err
}

func (f *Fetcher) now() time.Time {
	if f.Now != nil {
		return f.Now()
	}
	return time.Now()
}
