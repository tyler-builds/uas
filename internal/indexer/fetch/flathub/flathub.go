// Package flathub fetches apps from Flathub's AppStream catalog.
package flathub

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/tyler-builds/uas/internal/catalog"
	"github.com/tyler-builds/uas/internal/indexer/appstream"
	"github.com/tyler-builds/uas/internal/indexer/fetch"
)

// URLFormat is where Flathub publishes its AppStream catalog, by CPU architecture.
const URLFormat = "https://dl.flathub.org/repo/appstream/%s/appstream.xml.gz"

// Fetcher reads Flathub's AppStream catalog.
type Fetcher struct {
	Client   *http.Client
	CacheDir string
	Arch     string // "x86_64" or "aarch64"
	// File, if set, is read instead of downloading the catalog.
	File string
}

// Source implements fetch.Fetcher.
func (f *Fetcher) Source() catalog.SourceID { return catalog.SourceFlathub }

// Fetch implements fetch.Fetcher.
func (f *Fetcher) Fetch(ctx context.Context) ([]catalog.Listing, error) {
	if f.Arch != "x86_64" && f.Arch != "aarch64" {
		return nil, fmt.Errorf("flathub: unsupported architecture %q", f.Arch)
	}
	name := f.File
	if name == "" {
		var err error
		name, err = fetch.Get(ctx, f.Client, fmt.Sprintf(URLFormat, f.Arch), f.CacheDir)
		if err != nil {
			return nil, fmt.Errorf("flathub: %w", err)
		}
	}
	r, err := fetch.OpenDecompressed(name)
	if err != nil {
		return nil, fmt.Errorf("flathub: %w", err)
	}
	defer r.Close()
	listings, err := Parse(r, f.Arch)
	if err != nil || f.File != "" {
		return listings, err
	}
	// Popularity is a nice-to-have: without it the catalog still works, just ranked less well.
	if installs, err := f.popular(ctx); err == nil {
		for i := range listings {
			// Flathub counts the last month; times 12 puts it on the same yearly scale as Homebrew's.
			listings[i].Installs = installs[listings[i].AppStreamID] * 12
		}
	}
	return listings, nil
}

// PopularURL lists apps with their installs in the last month, most popular first.
const PopularURL = "https://flathub.org/api/v2/collection/popular"

// popular returns each app's installs in the last month.
func (f *Fetcher) popular(ctx context.Context) (map[string]int, error) {
	installs := map[string]int{}
	for page := 1; page <= 100; page++ {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet,
			fmt.Sprintf("%s?page=%d&per_page=250", PopularURL, page), nil)
		if err != nil {
			return nil, err
		}
		req.Header.Set("User-Agent", fetch.UserAgent)
		resp, err := f.Client.Do(req)
		if err != nil {
			return nil, err
		}
		var res struct {
			Hits []struct {
				AppID    string `json:"app_id"`
				Installs int    `json:"installs_last_month"`
			} `json:"hits"`
			TotalPages int `json:"totalPages"`
		}
		err = json.NewDecoder(io.LimitReader(resp.Body, 64<<20)).Decode(&res)
		resp.Body.Close()
		if err != nil {
			return nil, fmt.Errorf("flathub popular: %w", err)
		}
		if resp.StatusCode != http.StatusOK {
			return nil, fmt.Errorf("flathub popular: %s", resp.Status)
		}
		for _, h := range res.Hits {
			installs[h.AppID] = h.Installs
		}
		if page >= res.TotalPages || len(res.Hits) == 0 {
			break
		}
	}
	return installs, nil
}

// Parse turns a Flathub AppStream catalog into listings. Only desktop and console apps are kept; runtimes,
// add-ons, fonts and the like are skipped.
func Parse(r io.Reader, arch string) ([]catalog.Listing, error) {
	var listings []catalog.Listing
	err := appstream.Parse(r, func(c *appstream.Component) error {
		if l, ok := toListing(c, arch); ok {
			listings = append(listings, l)
		}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("flathub: %w", err)
	}
	return listings, nil
}

func toListing(c *appstream.Component, arch string) (catalog.Listing, bool) {
	var kind catalog.Kind
	switch c.Type {
	case "desktop-application", "desktop": // "desktop" is the old name
		kind = catalog.KindDesktopApp
	case "console-application":
		kind = catalog.KindConsoleApp
	default:
		return catalog.Listing{}, false
	}
	if c.ID == "" || c.Name == "" {
		return catalog.Listing{}, false
	}
	ref := c.Bundle.Ref
	if ref == "" {
		ref = "app/" + c.ID + "/" + arch + "/stable"
	}
	if !strings.HasPrefix(ref, "app/") {
		return catalog.Listing{}, false
	}

	l := catalog.Listing{
		Source:      catalog.SourceFlathub,
		Ref:         ref,
		AppStreamID: c.ID,
		Kind:        kind,
		Name:        c.Name,
		Summary:     c.Summary,
		Description: c.Description,
		Developer:   c.Developer,
		License:     c.License,
		Homepage:    c.URLs["homepage"],
		VCSURL:      c.URLs["vcs-browser"],
		BugTracker:  c.URLs["bugtracker"],
		IconURL:     c.BestRemoteIcon(),
		Categories:  c.Categories,
		Keywords:    c.Keywords,
		Runtime:     c.Bundle.Runtime,
	}
	for _, s := range c.Screenshots {
		l.Screenshots = append(l.Screenshots, catalog.Screenshot{URL: s.Source, ThumbURL: s.Thumbnail,
			Caption: s.Caption})
	}
	if r, ok := c.Latest(); ok {
		l.Version, l.Released = r.Version, r.Time
	}
	if c.Custom["flathub::verification::verified"] == "true" {
		l.Verified = true
		switch {
		case c.Custom["flathub::verification::website"] != "":
			l.VerifiedAs = c.Custom["flathub::verification::website"]
		case c.Custom["flathub::verification::login_name"] != "":
			l.VerifiedAs = c.Custom["flathub::verification::login_provider"] + ":" +
				c.Custom["flathub::verification::login_name"]
		}
	}
	return l, true
}
