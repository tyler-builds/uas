// Package gnome lists GNOME Shell extensions on extensions.gnome.org, for the Shell to install.
//
// The site's v1 API lists the extensions that have a version for one Shell release, 100 a page. The list
// doesn't say which releases an extension supports, so the fetcher asks once per release, from GNOME 45 (the
// first whose extensions still load today: 45 changed the extension format) up to the first release with
// none, and records the releases each extension turned up under. That is about 90 pages. The site publishes
// no rate limit and its settings set none, so pages are read one a second, and the result is cached like the
// other slow sources.
//
// Every version on the site is reviewed by GNOME's extension reviewers before it is listed, so all of them
// are kept (about 2,000).
package gnome

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/tyler-builds/uas/internal/catalog"
	"github.com/tyler-builds/uas/internal/indexer/fetch"
)

// API is the site's list of extensions.
const API = "https://extensions.gnome.org/api/v1/extensions/"

// Site is where the extensions' own pages are.
const Site = "https://extensions.gnome.org"

// FirstShell is the oldest GNOME Shell release indexed.
const FirstShell = 45

// every spaces requests: the site publishes no rate limit, so this stays well clear of any.
const every = time.Second

// Fetcher lists extensions.
type Fetcher struct {
	Client *http.Client
	API    string // API, or a test server
	// First is the oldest Shell release to list; FirstShell when zero.
	First int
	// MaxShells caps how many releases are asked about, should the site never answer with an empty one.
	MaxShells int
	Every     time.Duration // the pause between requests; every when zero
	Log       *slog.Logger
}

// Source implements fetch.Fetcher.
func (f *Fetcher) Source() catalog.SourceID { return catalog.SourceGNOMEExtensions }

type extension struct {
	UUID    string `json:"uuid"`
	Name    string `json:"name"`
	Creator struct {
		Username    string `json:"username"`
		DisplayName string `json:"display_name"`
	} `json:"creator"`
	Description string `json:"description"`
	Downloads   int    `json:"downloads"`
	Screenshot  string `json:"screenshot"`
	Icon        string `json:"icon"`
	URL         string `json:"url"`
	Link        string `json:"link"` // the path of its page on the site
}

type page struct {
	Count   int         `json:"count"`
	Next    string      `json:"next"`
	Results []extension `json:"results"`
}

// Fetch implements fetch.Fetcher.
func (f *Fetcher) Fetch(ctx context.Context) ([]catalog.Listing, error) {
	api, first, maxShells, wait := f.API, f.First, f.MaxShells, f.Every
	if api == "" {
		api = API
	}
	if first == 0 {
		first = FirstShell
	}
	if maxShells == 0 {
		maxShells = 40
	}
	if wait == 0 {
		wait = every
	}
	byUUID := map[string]*catalog.Listing{}
	requests := 0
	for shell := first; shell < first+maxShells; shell++ {
		found := 0
		// Oldest first: new extensions then land on the last page, so paging never skips one.
		for n := 1; ; n++ {
			if requests > 0 {
				select {
				case <-ctx.Done():
					return nil, ctx.Err()
				case <-time.After(wait):
				}
			}
			requests++
			q := url.Values{"shell_version": {strconv.Itoa(shell)}, "page_size": {"100"}, "page": {strconv.Itoa(n)},
				"ordering": {"created"}}
			var p page
			if err := f.get(ctx, api+"?"+q.Encode(), &p); err != nil {
				return nil, fmt.Errorf("gnome extensions: GNOME %d, page %d: %w", shell, n, err)
			}
			for _, e := range p.Results {
				found++
				l := byUUID[e.UUID]
				if l == nil {
					nl, ok := toListing(e)
					if !ok {
						continue
					}
					l = &nl
					byUUID[e.UUID] = l
				}
				if v := strconv.Itoa(shell); len(l.Shells) == 0 || l.Shells[len(l.Shells)-1] != v {
					l.Shells = append(l.Shells, v)
				}
			}
			if p.Next == "" || len(p.Results) == 0 {
				break
			}
		}
		if found == 0 {
			break // a release nothing supports yet: the newest one is behind us
		}
	}
	listings := make([]catalog.Listing, 0, len(byUUID))
	for _, l := range byUUID {
		listings = append(listings, *l)
	}
	sort.Slice(listings, func(i, j int) bool { return listings[i].Ref < listings[j].Ref })
	if f.Log != nil {
		f.Log.Info("gnome extensions listed", "extensions", len(listings), "requests", requests)
	}
	if len(listings) == 0 {
		return nil, fmt.Errorf("gnome extensions: the site listed none for GNOME %d", first)
	}
	return listings, nil
}

func (f *Fetcher) get(ctx context.Context, u string, v any) error {
	var lastErr error
	for attempt := 0; attempt < 4; attempt++ {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
		if err != nil {
			return err
		}
		req.Header.Set("User-Agent", fetch.UserAgent)
		req.Header.Set("Accept", "application/json")
		resp, err := f.Client.Do(req)
		if err != nil {
			lastErr = err
			continue
		}
		raw, err := io.ReadAll(io.LimitReader(resp.Body, 64<<20))
		resp.Body.Close()
		switch {
		case err != nil:
			lastErr = err
		case resp.StatusCode == http.StatusOK:
			return json.Unmarshal(raw, v)
		case resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode >= 500:
			lastErr = fmt.Errorf("%s", resp.Status)
			wait := time.Duration(attempt+1) * 10 * time.Second
			if s, err := strconv.Atoi(resp.Header.Get("Retry-After")); err == nil && s > 0 && s <= 300 {
				wait = time.Duration(s) * time.Second
			}
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(wait):
			}
		default:
			return fmt.Errorf("%s: %s", resp.Status, strings.TrimSpace(string(raw[:min(len(raw), 200)])))
		}
	}
	return lastErr
}

// UUID is what an extension's UUID may be made of. The Shell takes it as a D-Bus argument and the site as a
// path, so anything else is left out.
var UUID = regexp.MustCompile(`^[-a-zA-Z0-9@._]{1,200}$`)

// toListing makes a listing of an extension, without its Shell releases.
func toListing(e extension) (catalog.Listing, bool) {
	name := strings.TrimSpace(e.Name)
	if !UUID.MatchString(e.UUID) || name == "" {
		return catalog.Listing{}, false
	}
	desc := strings.TrimSpace(strings.ReplaceAll(e.Description, "\r\n", "\n"))
	developer := strings.TrimSpace(e.Creator.DisplayName)
	if developer == "" {
		developer = e.Creator.Username
	}
	l := catalog.Listing{
		Source:      catalog.SourceGNOMEExtensions,
		Ref:         e.UUID,
		Kind:        catalog.KindShellExtension,
		Name:        name,
		Summary:     summary(desc),
		Description: desc,
		Developer:   developer,
		Installs:    e.Downloads,
	}
	if https(e.Icon) {
		l.IconURL = e.Icon
	}
	if https(e.Screenshot) {
		l.Screenshots = []catalog.Screenshot{{URL: e.Screenshot}}
	}
	// The project's own page, and the extension's page on the site, where its reviews and versions are.
	if strings.HasPrefix(e.URL, "https://") || strings.HasPrefix(e.URL, "http://") {
		l.Homepage = e.URL
	}
	if strings.HasPrefix(e.Link, "/extension/") {
		l.StoreURL = Site + e.Link
	}
	return l, true
}

// https reports whether u is a link to a file on the site.
func https(u string) bool { return strings.HasPrefix(u, Site+"/") }

// summary is the one line shown in lists: the description's first sentence or line, cut at a word when it
// is long.
func summary(desc string) string {
	line, _, _ := strings.Cut(desc, "\n")
	line = strings.Join(strings.Fields(line), " ")
	// The first sentence, unless it ends within a few letters ("v2. Now with...").
	const least = 15
	if len(line) > least {
		if i := strings.Index(line[least:], ". "); i >= 0 {
			line = line[:least+i+1]
		}
	}
	const most = 140
	if utf8.RuneCountInString(line) <= most {
		return line
	}
	r := []rune(line)[:most]
	cut := string(r)
	if i := strings.LastIndexByte(cut, ' '); i > most/2 {
		cut = cut[:i]
	}
	return strings.TrimRight(cut, " ,;:.") + "…"
}
