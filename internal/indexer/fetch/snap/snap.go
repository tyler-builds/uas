// Package snap lists apps in the Snap Store, for installing with snap.
//
// The store's v1 search API lists every stable snap for an architecture, 100 a page (about 90 pages). Its
// rate limits aren't published, so pages are read one a second, and the result is cached like the other slow
// sources. Most snaps are tests and abandoned experiments, so a snap is kept only with a title, a summary
// and an icon, and either a verified or starred publisher or the AppStream ID of a known app (its common IDs,
// which also merge it with that app's other listings). Of about 8,800 snaps that keeps about a thousand: a
// section and screenshots alone let in over 4,000, most of them unmaintained. Snaps with no apps are runtimes,
// bases and themes (gnome-46-2404, qogir-themes), which other snaps use, so they are left out too.
package snap

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

	"github.com/tyler-builds/uas/internal/catalog"
	"github.com/tyler-builds/uas/internal/indexer/fetch"
)

// API is the Snap Store's search endpoint.
const API = "https://api.snapcraft.io/api/v1/snaps/search"

// every spaces requests: the store publishes no rate limit, so this stays well clear of any.
const every = time.Second

const fields = "package_name,title,summary,description,developer_name,developer_validation,publisher,license," +
	"media,version,last_updated,confinement,common_ids,website,contact,sections,apps"

// Fetcher lists snaps.
type Fetcher struct {
	Client *http.Client
	Arch   string // "x86_64" or "aarch64"
	API    string // API, or a test server
	// MaxPages caps the listing (100 snaps a page).
	MaxPages int
	Every    time.Duration // the pause between requests; every when zero
	Log      *slog.Logger
}

// Source implements fetch.Fetcher.
func (f *Fetcher) Source() catalog.SourceID { return catalog.SourceSnap }

type media struct {
	Type   string `json:"type"`
	URL    string `json:"url"`
	Width  int    `json:"width"`
	Height int    `json:"height"`
}

type pkg struct {
	Name        string    `json:"package_name"`
	Title       string    `json:"title"`
	Summary     string    `json:"summary"`
	Description string    `json:"description"`
	Developer   string    `json:"developer_name"`
	Validation  string    `json:"developer_validation"` // "verified", "starred", "unproven"
	Publisher   string    `json:"publisher"`
	License     string    `json:"license"`
	Media       []media   `json:"media"`
	Version     string    `json:"version"`
	Updated     time.Time `json:"last_updated"`
	Confinement string    `json:"confinement"`
	CommonIDs   []string  `json:"common_ids"`
	Apps        []string  `json:"apps"` // commands; none for runtimes, bases and themes
	Website     string    `json:"website"`
	Contact     string    `json:"contact"`
	Sections    []struct {
		Name string `json:"name"`
	} `json:"sections"`
}

type page struct {
	Embedded struct {
		Packages []pkg `json:"clickindex:package"`
	} `json:"_embedded"`
	Links struct {
		Next *struct {
			Href string `json:"href"`
		} `json:"next"`
	} `json:"_links"`
}

// Fetch implements fetch.Fetcher.
func (f *Fetcher) Fetch(ctx context.Context) ([]catalog.Listing, error) {
	var arch string
	switch f.Arch {
	case "x86_64":
		arch = "amd64"
	case "aarch64":
		arch = "arm64"
	default:
		return nil, fmt.Errorf("snap: unsupported architecture %q", f.Arch)
	}
	api, maxPages, wait := f.API, f.MaxPages, f.Every
	if api == "" {
		api = API
	}
	if maxPages == 0 {
		maxPages = 200
	}
	if wait == 0 {
		wait = every
	}
	var listings []catalog.Listing
	seen := map[string]bool{}
	total := 0
	for n := 1; n <= maxPages; n++ {
		if n > 1 {
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-time.After(wait):
			}
		}
		q := url.Values{"page_size": {"100"}, "page": {strconv.Itoa(n)}, "fields": {fields}}
		var p page
		if err := f.get(ctx, api+"?"+q.Encode(), arch, &p); err != nil {
			return nil, fmt.Errorf("snap: page %d: %w", n, err)
		}
		for _, s := range p.Embedded.Packages {
			total++
			if seen[s.Name] {
				continue
			}
			seen[s.Name] = true
			if l, ok := toListing(s); ok {
				listings = append(listings, l)
			}
		}
		if p.Links.Next == nil || len(p.Embedded.Packages) == 0 {
			break
		}
	}
	if f.Log != nil {
		f.Log.Info("snaps checked", "snaps", total, "kept", len(listings))
	}
	sort.Slice(listings, func(i, j int) bool { return listings[i].Ref < listings[j].Ref })
	return listings, nil
}

func (f *Fetcher) get(ctx context.Context, u, arch string, v any) error {
	var lastErr error
	for attempt := 0; attempt < 4; attempt++ {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
		if err != nil {
			return err
		}
		req.Header.Set("User-Agent", fetch.UserAgent)
		req.Header.Set("X-Ubuntu-Series", "16")
		req.Header.Set("X-Ubuntu-Architecture", arch)
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

var snapName = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,39}$`)

// toListing keeps the snaps worth showing (see the package comment).
func toListing(s pkg) (catalog.Listing, bool) {
	icon := ""
	var shots []catalog.Screenshot
	for _, m := range s.Media {
		switch {
		case m.Type == "icon" && icon == "" && strings.HasPrefix(m.URL, "https://"):
			icon = m.URL
		case m.Type == "screenshot" && strings.HasPrefix(m.URL, "https://") && len(shots) < 4:
			shots = append(shots, catalog.Screenshot{URL: m.URL})
		}
	}
	trusted := s.Validation == "verified" || s.Validation == "starred"
	if !snapName.MatchString(s.Name) || strings.TrimSpace(s.Title) == "" || strings.TrimSpace(s.Summary) == "" ||
		icon == "" || len(s.Apps) == 0 || (!trusted && len(s.CommonIDs) == 0) {
		return catalog.Listing{}, false
	}
	var cats []string
	for _, sec := range s.Sections {
		if c, ok := sectionCategories[sec.Name]; ok {
			cats = append(cats, c)
		}
	}
	appID := ""
	if len(s.CommonIDs) > 0 {
		appID = s.CommonIDs[0]
	}
	license := s.License
	if license == "Other Open Source" || license == "unset" {
		license = ""
	}
	developer := s.Publisher
	if developer == "" {
		developer = s.Developer
	}
	return catalog.Listing{
		Source:      catalog.SourceSnap,
		Ref:         s.Name,
		Package:     s.Name,
		AppStreamID: appID,
		Kind:        catalog.KindDesktopApp,
		Name:        strings.TrimSpace(s.Title),
		Summary:     strings.TrimSpace(s.Summary),
		Description: strings.TrimSpace(s.Description),
		Developer:   developer,
		License:     license,
		Homepage:    s.Website,
		IconURL:     icon,
		Screenshots: shots,
		Categories:  cats,
		Version:     s.Version,
		Released:    s.Updated,
		Verified:    s.Validation == "verified",
		VerifiedAs:  verifiedAs(s),
		Classic:     s.Confinement == "classic",
	}, true
}

func verifiedAs(s pkg) string {
	if s.Validation != "verified" {
		return ""
	}
	return s.Publisher
}

// sectionCategories maps the store's sections to freedesktop categories, for browsing.
var sectionCategories = map[string]string{
	"art-and-design": "Graphics", "photo-and-video": "AudioVideo", "music-and-audio": "AudioVideo",
	"development": "Development", "devices-and-iot": "System", "education": "Education",
	"entertainment": "AudioVideo", "finance": "Office", "games": "Game", "health-and-fitness": "Utility",
	"news-and-weather": "Network", "personalisation": "Utility", "productivity": "Office",
	"science": "Science", "security": "System", "server-and-cloud": "Development", "social": "Network",
	"utilities": "Utility", "books-and-reference": "Office",
}
