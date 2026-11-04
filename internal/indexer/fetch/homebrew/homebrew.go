// Package homebrew fetches command-line tools from Homebrew's core formulae.
//
// Homebrew's formula API has no notion of "app" versus "library", so the fetcher keeps formulae that people
// install on purpose: those with a Linux bottle and at least MinInstalls installs-on-request in the past year
// (from Homebrew's public analytics). Keg-only, deprecated and disabled formulae are skipped.
package homebrew

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"strconv"
	"strings"

	"github.com/tyler-builds/uas/internal/catalog"
	"github.com/tyler-builds/uas/internal/indexer/fetch"
)

// API endpoints.
const (
	FormulaURL   = "https://formulae.brew.sh/api/formula.json"
	AnalyticsURL = "https://formulae.brew.sh/api/analytics/install-on-request/365d.json"
)

// DefaultMinInstalls keeps about 2,700 formulae as of late 2026.
const DefaultMinInstalls = 1000

// Fetcher reads Homebrew's formula API and analytics.
type Fetcher struct {
	Client      *http.Client
	CacheDir    string
	Arch        string // "x86_64" or "aarch64"
	MinInstalls int
}

// Source implements fetch.Fetcher.
func (f *Fetcher) Source() catalog.SourceID { return catalog.SourceHomebrew }

// Fetch implements fetch.Fetcher.
func (f *Fetcher) Fetch(ctx context.Context) ([]catalog.Listing, error) {
	bottle := map[string]string{"x86_64": "x86_64_linux", "aarch64": "arm64_linux"}[f.Arch]
	if bottle == "" {
		return nil, fmt.Errorf("homebrew: unsupported architecture %q", f.Arch)
	}
	formulaPath, err := fetch.Get(ctx, f.Client, FormulaURL, f.CacheDir)
	if err != nil {
		return nil, fmt.Errorf("homebrew: %w", err)
	}
	analyticsPath, err := fetch.Get(ctx, f.Client, AnalyticsURL, f.CacheDir)
	if err != nil {
		return nil, fmt.Errorf("homebrew: %w", err)
	}
	installs, err := readAnalytics(analyticsPath)
	if err != nil {
		return nil, fmt.Errorf("homebrew: %w", err)
	}
	r, err := os.Open(formulaPath)
	if err != nil {
		return nil, err
	}
	defer r.Close()
	minInstalls := f.MinInstalls
	if minInstalls <= 0 {
		minInstalls = DefaultMinInstalls
	}
	return Parse(r, installs, bottle, minInstalls)
}

type formula struct {
	Name     string `json:"name"`
	FullName string `json:"full_name"`
	Tap      string `json:"tap"`
	Desc     string `json:"desc"`
	License  string `json:"license"`
	Homepage string `json:"homepage"`
	Versions struct {
		Stable string `json:"stable"`
	} `json:"versions"`
	URLs struct {
		Head struct {
			URL string `json:"url"`
		} `json:"head"`
	} `json:"urls"`
	Bottle struct {
		Stable struct {
			Files map[string]json.RawMessage `json:"files"`
		} `json:"stable"`
	} `json:"bottle"`
	KegOnly    bool `json:"keg_only"`
	Deprecated bool `json:"deprecated"`
	Disabled   bool `json:"disabled"`
}

// formulaName matches core formula names, such as "htop" or "python@3.13".
var formulaName = regexp.MustCompile(`^[a-z0-9][a-z0-9@._+-]*$`)

// Parse turns the formula API's JSON into listings, keeping popular formulae with a bottle for bottleTag.
func Parse(r io.Reader, installs map[string]int, bottleTag string, minInstalls int) ([]catalog.Listing, error) {
	var formulae []formula
	if err := json.NewDecoder(r).Decode(&formulae); err != nil {
		return nil, fmt.Errorf("homebrew: formula.json: %w", err)
	}
	var listings []catalog.Listing
	for _, fm := range formulae {
		_, hasBottle := fm.Bottle.Stable.Files[bottleTag]
		if _, all := fm.Bottle.Stable.Files["all"]; all {
			hasBottle = true
		}
		n := installs[fm.Name]
		if !hasBottle || fm.KegOnly || fm.Deprecated || fm.Disabled || n < minInstalls ||
			fm.Tap != "homebrew/core" || !formulaName.MatchString(fm.Name) {
			continue
		}
		listings = append(listings, catalog.Listing{
			Source:   catalog.SourceHomebrew,
			Ref:      fm.Name,
			Kind:     catalog.KindConsoleApp,
			Name:     fm.Name,
			Summary:  fm.Desc,
			License:  fm.License,
			Homepage: fm.Homepage,
			VCSURL:   repoURL(fm.URLs.Head.URL),
			Version:  fm.Versions.Stable,
			Package:  fm.Name,
			Installs: n,
		})
	}
	return listings, nil
}

// repoURL turns a git clone URL on a known forge into its web address.
func repoURL(clone string) string {
	u, err := url.Parse(clone)
	if err != nil || u.Scheme != "https" {
		return ""
	}
	switch u.Host {
	case "github.com", "gitlab.com", "codeberg.org":
		return "https://" + u.Host + strings.TrimSuffix(u.Path, ".git")
	}
	return ""
}

func readAnalytics(path string) (map[string]int, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var a struct {
		Items []struct {
			Formula string `json:"formula"`
			Count   string `json:"count"`
		} `json:"items"`
	}
	if err := json.Unmarshal(b, &a); err != nil {
		return nil, fmt.Errorf("analytics: %w", err)
	}
	installs := make(map[string]int, len(a.Items))
	for _, it := range a.Items {
		// Counts are formatted like "3,090,775". Entries such as "htop --HEAD" count toward the formula.
		n, err := strconv.Atoi(strings.ReplaceAll(it.Count, ",", ""))
		if err != nil {
			continue
		}
		name, _, _ := strings.Cut(it.Formula, " ")
		installs[name] += n
	}
	return installs, nil
}
