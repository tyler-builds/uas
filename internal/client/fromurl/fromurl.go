// Package fromurl finds the app a link points at, for Install from URL: the user pastes a link to an app's
// repo, release page or release file, and uas installs it even when its catalog doesn't have it (a project
// too new or too small for the catalog's bar).
//
// Parse reads the link. Resolver reads the repo and its newest releases from GitHub's API, as the indexer
// does for the catalog (github.Fetcher.Live): the same release file picking, trust score and release notes,
// and what the AppImage says about the app (its name, icon and description). Lookups need no account, which
// GitHub limits to 60 an hour per address, so results are kept for an hour.
package fromurl

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/tyler-builds/uas/internal/catalog"
	ghfetch "github.com/tyler-builds/uas/internal/indexer/fetch/github"
)

// Link is a repo a link points at.
type Link struct {
	Source catalog.SourceID // catalog.SourceGitHub or catalog.SourceCodeberg
	Repo   string           // "owner/name", as written in the link
}

// Ref is the ref a release listing of the repo has: "owner/name" on GitHub, "codeberg.org/owner/name" on
// Codeberg.
func (l Link) Ref() string {
	if l.Source == catalog.SourceGitHub {
		return l.Repo
	}
	return l.Source.Host() + "/" + l.Repo
}

// URL is the repo's web page.
func (l Link) URL() string { return "https://" + l.Source.Host() + "/" + l.Repo }

// AppID is the ID the catalog gives an app with only this repo's release listing: "github:owner/name".
func (l Link) AppID() string { return string(l.Source) + ":" + l.Ref() }

var (
	ownerName = regexp.MustCompile(`^[A-Za-z0-9](?:[A-Za-z0-9-]{0,38})$`)
	repoName  = regexp.MustCompile(`^[A-Za-z0-9_.-]{1,100}$`)
)

// notOwners are first path parts of GitHub and Codeberg pages that aren't users or organizations.
var notOwners = map[string]bool{
	"about": true, "apps": true, "collections": true, "contact": true, "enterprise": true, "explore": true,
	"features": true, "issues": true, "login": true, "marketplace": true, "new": true, "notifications": true,
	"orgs": true, "organizations": true, "pricing": true, "pulls": true, "search": true, "settings": true,
	"sponsors": true, "topics": true, "trending": true, "users": true, "user": true, "repo": true, "api": true,
	"assets": true, "codespaces": true, "site": true,
}

// ErrNotLink is returned by Parse for text that isn't a link to a repo uas can read.
var ErrNotLink = errors.New("not a link to a GitHub or Codeberg repo")

// Parse reads a link to a repo: its page ("https://github.com/owner/name", with or without "https://" or
// ".git"), any page in it (its releases, a release, a file in the code), a release file's download link, or
// a git address ("git@github.com:owner/name.git"). Codeberg links are read the same way. A bare
// "owner/name" is not a link: it would be ambiguous with an app's name.
func Parse(raw string) (Link, error) {
	s := strings.TrimSpace(raw)
	if s == "" || strings.ContainsAny(s, " \t\n") {
		return Link{}, ErrNotLink
	}
	if rest, ok := strings.CutPrefix(s, "git@"); ok {
		// git@github.com:owner/name.git
		host, path, ok := strings.Cut(rest, ":")
		if !ok {
			return Link{}, ErrNotLink
		}
		s = "https://" + host + "/" + path
	}
	if !strings.Contains(s, "://") {
		s = "https://" + s
	}
	u, err := url.Parse(s)
	if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.User != nil {
		return Link{}, ErrNotLink
	}
	var src catalog.SourceID
	switch strings.TrimPrefix(strings.ToLower(u.Hostname()), "www.") {
	case "github.com":
		src = catalog.SourceGitHub
	case "codeberg.org":
		src = catalog.SourceCodeberg
	default:
		return Link{}, ErrNotLink
	}
	parts := strings.Split(strings.Trim(u.Path, "/"), "/")
	if len(parts) < 2 {
		return Link{}, ErrNotLink
	}
	owner, name := parts[0], strings.TrimSuffix(parts[1], ".git")
	if !ownerName.MatchString(owner) || notOwners[strings.ToLower(owner)] || !repoName.MatchString(name) ||
		name == "." || name == ".." {
		return Link{}, ErrNotLink
	}
	return Link{Source: src, Repo: owner + "/" + name}, nil
}

// IsLink reports whether text looks like a link someone pasted rather than words to search for: it starts
// with a scheme, "www." or a forge's host.
func IsLink(text string) bool {
	s := strings.ToLower(strings.TrimSpace(text))
	for _, p := range []string{"https://", "http://", "www.", "github.com/", "codeberg.org/", "git@"} {
		if strings.HasPrefix(s, p) {
			return true
		}
	}
	return false
}

// ErrCodeberg is returned for a Codeberg repo the catalog doesn't have.
var ErrCodeberg = errors.New("uas installs Codeberg apps only from its catalog: Codeberg publishes no " +
	"checksums for release files, so uas checks them against the ones it records when it builds the catalog")

// TTL is how long a lookup is kept.
const TTL = time.Hour

// Resolver reads repos from their forge, keeping each result for TTL.
type Resolver struct {
	// Client makes the requests; nil uses a client with a one-minute timeout.
	Client *http.Client
	// Arch is the system's architecture, "x86_64" or "aarch64", for picking release files.
	Arch string
	// CacheDir is where what was read from AppImages is kept, with their icons (CacheDir/icons); empty keeps
	// nothing and shows no icons.
	CacheDir string
	// API is GitHub's REST API, for tests; empty is api.github.com.
	API string
	// Raw is where GitHub serves repo files, for tests; empty is raw.githubusercontent.com.
	Raw string
	Now func() time.Time

	mu    sync.Mutex
	cache map[string]cached
}

type cached struct {
	l         catalog.Listing
	described bool
	at        time.Time
}

// Listing reads the repo's current release as a listing (with Live set). With describe set it also reads
// what the release's AppImage and the repo say about the app, for its page; without, it is quicker and
// enough to check for an update. A repo the forge renamed comes back under its new name.
func (r *Resolver) Listing(ctx context.Context, link Link, describe bool) (catalog.Listing, error) {
	if link.Source == catalog.SourceCodeberg {
		return catalog.Listing{}, ErrCodeberg
	}
	if link.Source != catalog.SourceGitHub {
		return catalog.Listing{}, fmt.Errorf("%s: %w", link.Source, ErrNotLink)
	}
	key := strings.ToLower(link.Ref())
	now := r.now()
	r.mu.Lock()
	c, ok := r.cache[key]
	r.mu.Unlock()
	if ok && now.Sub(c.at) < TTL && (c.described || !describe) {
		return c.l, nil
	}
	f := &ghfetch.Fetcher{Client: r.client(), Arch: r.Arch, MediaDir: r.CacheDir, RESTEndpoint: r.API,
		RawEndpoint: r.Raw, UserAgent: "uas", Now: r.Now}
	if r.CacheDir != "" {
		f.IconBase = "file://" + filepath.ToSlash(r.CacheDir)
	}
	l, err := f.Live(ctx, link.Repo, describe)
	if err != nil {
		return catalog.Listing{}, err
	}
	r.mu.Lock()
	if r.cache == nil {
		r.cache = map[string]cached{}
	}
	r.cache[key] = cached{l: l, described: describe, at: now}
	r.cache[strings.ToLower(l.Ref)] = cached{l: l, described: describe, at: now}
	r.mu.Unlock()
	return l, nil
}

func (r *Resolver) client() *http.Client {
	if r.Client != nil {
		return r.Client
	}
	return &http.Client{Timeout: time.Minute}
}

func (r *Resolver) now() time.Time {
	if r.Now != nil {
		return r.Now()
	}
	return time.Now()
}
