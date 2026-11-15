// Package nerdfonts lists the Nerd Fonts: programming fonts patched with icons, from the latest release of
// github.com/ryanoasis/nerd-fonts. Each font is one .tar.xz release file, whose SHA-256 GitHub publishes, so
// nothing needs downloading here. The repo's fonts.json, at the release's tag, adds each font's licence,
// description and patched family name; without it the listings are plainer.
package nerdfonts

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/tyler-builds/uas/internal/catalog"
	"github.com/tyler-builds/uas/internal/indexer/fetch"
)

// The places read.
const (
	API   = "https://api.github.com"
	Raw   = "https://raw.githubusercontent.com"
	Repo  = "ryanoasis/nerd-fonts"
	Store = "https://www.nerdfonts.com/font-downloads"
)

// Fetcher lists Nerd Fonts.
type Fetcher struct {
	Client *http.Client
	API    string // API, or a test server
	Raw    string // Raw, or a test server
	Token  string // a GitHub token, optional
}

// Source implements fetch.Fetcher.
func (f *Fetcher) Source() catalog.SourceID { return catalog.SourceNerdFonts }

type release struct {
	TagName     string    `json:"tag_name"`
	PublishedAt time.Time `json:"published_at"`
	HTMLURL     string    `json:"html_url"`
	Assets      []struct {
		Name      string `json:"name"`
		Size      int64  `json:"size"`
		Digest    string `json:"digest"`
		URL       string `json:"browser_download_url"`
		Downloads int    `json:"download_count"`
	} `json:"assets"`
}

type fontInfo struct {
	Unpatched   string `json:"unpatchedName"`
	Patched     string `json:"patchedName"`
	Folder      string `json:"folderName"`
	License     string `json:"licenseId"`
	Version     string `json:"version"`
	Monospaced  bool   `json:"isMonospaced"`
	Description string `json:"description"`
}

var (
	refName = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,80}$`)
	hexSum  = regexp.MustCompile(`^[0-9a-f]{64}$`)
)

// Fetch implements fetch.Fetcher.
func (f *Fetcher) Fetch(ctx context.Context) ([]catalog.Listing, error) {
	api, raw := f.API, f.Raw
	if api == "" {
		api = API
	}
	if raw == "" {
		raw = Raw
	}
	var rel release
	if err := f.getJSON(ctx, api+"/repos/"+Repo+"/releases/latest", true, &rel); err != nil {
		return nil, fmt.Errorf("nerd fonts: latest release: %w", err)
	}
	info := map[string]fontInfo{}
	var doc struct {
		Fonts []fontInfo `json:"fonts"`
	}
	if err := f.getJSON(ctx, raw+"/"+Repo+"/"+rel.TagName+"/bin/scripts/lib/fonts.json", false, &doc); err == nil {
		for _, fi := range doc.Fonts {
			info[fi.Folder] = fi
		}
	}
	version := strings.TrimPrefix(rel.TagName, "v")
	prefix := "https://github.com/" + Repo + "/releases/download/" + rel.TagName + "/"
	var out []catalog.Listing
	for _, a := range rel.Assets {
		base, ok := strings.CutSuffix(a.Name, ".tar.xz")
		sum, sok := strings.CutPrefix(a.Digest, "sha256:")
		if !ok || !sok || !hexSum.MatchString(sum) || !refName.MatchString(base) ||
			!strings.HasPrefix(a.URL, prefix) {
			continue
		}
		fi := info[base]
		family := base
		if fi.Patched != "" {
			family = fi.Patched
		}
		l := catalog.Listing{
			Source:    catalog.SourceNerdFonts,
			Ref:       base,
			Kind:      catalog.KindFont,
			Name:      family + " Nerd Font",
			Developer: "Nerd Fonts",
			License:   licence(fi.License),
			StoreURL:  Store,
			Homepage:  "https://www.nerdfonts.com/",
			Version:   version,
			Released:  rel.PublishedAt,
			Installs:  a.Downloads,
			Keywords:  []string{"nerd", "icons", "programming", "terminal"},
			Assets: []catalog.Asset{{Kind: catalog.AssetFontArchive, Name: a.Name, URL: a.URL, Size: a.Size,
				SHA256: sum, Downloads: a.Downloads}},
		}
		if fi.Unpatched != "" && fi.Unpatched != family {
			l.Keywords = append(l.Keywords, fi.Unpatched)
		}
		if fi.Monospaced || info[base].Folder == "" {
			l.Categories = []string{"Monospace"}
		}
		desc := strings.TrimSpace(fi.Description)
		if desc == "" {
			desc = family + ", patched with Nerd Fonts' icons"
		}
		l.Summary = desc
		from := fi.Unpatched
		if from == "" {
			from = base
		}
		l.Description = from + " with thousands of icons added for terminals and code editors (Font Awesome, " +
			"Material Design, Devicons, Powerline symbols and more). Installs as three families: " + family +
			" Nerd Font, " + family + " Nerd Font Mono (icons one cell wide, for terminals) and " + family +
			" Nerd Font Propo (proportional)."
		if fi.Version != "" {
			l.Description += "\n\nBased on " + from + " " + fi.Version + "."
		}
		out = append(out, l)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Ref < out[j].Ref })
	if len(out) == 0 {
		return nil, fmt.Errorf("nerd fonts: the release %s has no font with a checksum", rel.TagName)
	}
	return out, nil
}

// licence turns fonts.json's licence IDs into SPDX: the OFL with or without Reserved Font Names is the OFL.
func licence(id string) string {
	switch {
	case strings.HasPrefix(id, "OFL-1.1"):
		return "OFL-1.1"
	case strings.HasPrefix(id, "LicenseRef-"):
		return ""
	}
	return id
}

func (f *Fetcher) getJSON(ctx context.Context, u string, github bool, v any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return err
	}
	req.Header.Set("User-Agent", fetch.UserAgent)
	if github {
		req.Header.Set("Accept", "application/vnd.github+json")
		if f.Token != "" {
			req.Header.Set("Authorization", "Bearer "+f.Token)
		}
	}
	resp, err := f.Client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(io.LimitReader(resp.Body, 16<<20))
	if err != nil {
		return err
	}
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("GET %s: %s", u, resp.Status)
	}
	return json.Unmarshal(b, v)
}
