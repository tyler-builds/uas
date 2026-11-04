// Package appstream reads AppStream catalog XML, the metadata format that Flathub and Fedora publish.
//
// Only untranslated text is kept. Translations are skipped for now.
package appstream

import (
	"encoding/xml"
	"fmt"
	"io"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Component is one entry of an AppStream catalog.
type Component struct {
	Type        string
	ID          string
	Name        string
	Summary     string
	Description string // Markdown
	Developer   string
	License     string
	URLs        map[string]string // by type: homepage, bugtracker, vcs-browser, ...
	Icons       []Icon
	Categories  []string
	Keywords    []string
	Screenshots []Screenshot // the default screenshot first
	Releases    []Release    // newest first
	Custom      map[string]string
	Bundle      Bundle
}

// Icon is an icon reference. For remote icons Value is a URL; for cached and stock icons it is a name.
type Icon struct {
	Type          string
	Value         string
	Width, Height int
	Scale         int
}

// Screenshot is a screenshot with its full-size source image and a mid-size thumbnail.
type Screenshot struct {
	Caption   string
	Source    string
	Thumbnail string
}

// Release is one entry of a component's release history.
type Release struct {
	Version string
	Type    string
	Time    time.Time
}

// Bundle says how the component is packaged, for example a Flatpak ref.
type Bundle struct {
	Type    string
	Ref     string
	Runtime string
}

// BestRemoteIcon returns the URL of the largest remote icon, or "" if there is none.
func (c *Component) BestRemoteIcon() string {
	best, bestSize := "", 0
	for _, ic := range c.Icons {
		if ic.Type != "remote" {
			continue
		}
		size := ic.Width * max(ic.Scale, 1)
		if size > bestSize || best == "" {
			best, bestSize = ic.Value, size
		}
	}
	return best
}

// Latest returns the newest release.
func (c *Component) Latest() (Release, bool) {
	if len(c.Releases) == 0 {
		return Release{}, false
	}
	return c.Releases[0], true
}

// Parse reads an AppStream catalog from r and calls fn for each component, in document order. It stops at the
// first error from the XML or from fn.
func Parse(r io.Reader, fn func(*Component) error) error {
	dec := xml.NewDecoder(r)
	for {
		tok, err := dec.Token()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return fmt.Errorf("appstream: %w", err)
		}
		se, ok := tok.(xml.StartElement)
		if !ok || se.Name.Local != "component" {
			continue
		}
		var xc xComponent
		if err := dec.DecodeElement(&xc, &se); err != nil {
			return fmt.Errorf("appstream: %w", err)
		}
		c, err := xc.convert()
		if err != nil {
			return fmt.Errorf("appstream: component %q: %w", xc.ID, err)
		}
		if err := fn(c); err != nil {
			return err
		}
	}
}

const xmlNamespace = "http://www.w3.org/XML/1998/namespace"

type xText struct {
	Lang string `xml:"http://www.w3.org/XML/1998/namespace lang,attr"`
	Text string `xml:",chardata"`
}

type xInner struct {
	Lang  string `xml:"http://www.w3.org/XML/1998/namespace lang,attr"`
	Inner string `xml:",innerxml"`
}

type xIcon struct {
	Type   string `xml:"type,attr"`
	Width  int    `xml:"width,attr"`
	Height int    `xml:"height,attr"`
	Scale  int    `xml:"scale,attr"`
	Value  string `xml:",chardata"`
}

type xURL struct {
	Type  string `xml:"type,attr"`
	Value string `xml:",chardata"`
}

type xImage struct {
	Type  string `xml:"type,attr"`
	Width int    `xml:"width,attr"`
	Lang  string `xml:"http://www.w3.org/XML/1998/namespace lang,attr"`
	Value string `xml:",chardata"`
}

type xScreenshot struct {
	Type     string   `xml:"type,attr"`
	Captions []xText  `xml:"caption"`
	Images   []xImage `xml:"image"`
}

type xRelease struct {
	Version   string `xml:"version,attr"`
	Type      string `xml:"type,attr"`
	Timestamp string `xml:"timestamp,attr"`
	Date      string `xml:"date,attr"`
}

type xValue struct {
	Key   string `xml:"key,attr"`
	Value string `xml:",chardata"`
}

type xBundle struct {
	Type    string `xml:"type,attr"`
	Runtime string `xml:"runtime,attr"`
	Value   string `xml:",chardata"`
}

type xComponent struct {
	Type            string        `xml:"type,attr"`
	ID              string        `xml:"id"`
	Names           []xText       `xml:"name"`
	Summaries       []xText       `xml:"summary"`
	Descriptions    []xInner      `xml:"description"`
	License         string        `xml:"project_license"`
	DeveloperNames  []xText       `xml:"developer>name"`
	LegacyDeveloper []xText       `xml:"developer_name"`
	Icons           []xIcon       `xml:"icon"`
	URLs            []xURL        `xml:"url"`
	Categories      []string      `xml:"categories>category"`
	Keywords        []xText       `xml:"keywords>keyword"`
	Screenshots     []xScreenshot `xml:"screenshots>screenshot"`
	Releases        []xRelease    `xml:"releases>release"`
	Metadata        []xValue      `xml:"metadata>value"`
	Custom          []xValue      `xml:"custom>value"`
	Bundles         []xBundle     `xml:"bundle"`
}

func (x *xComponent) convert() (*Component, error) {
	c := &Component{
		Type:      strings.TrimSpace(x.Type),
		ID:        strings.TrimSpace(x.ID),
		Name:      pickText(x.Names),
		Summary:   pickText(x.Summaries),
		License:   strings.TrimSpace(x.License),
		Developer: pickText(x.DeveloperNames),
		URLs:      map[string]string{},
		Custom:    map[string]string{},
	}
	if c.Developer == "" {
		c.Developer = pickText(x.LegacyDeveloper)
	}
	for _, d := range x.Descriptions {
		if d.Lang != "" {
			continue
		}
		md, err := descriptionMarkdown(d.Inner)
		if err != nil {
			return nil, fmt.Errorf("description: %w", err)
		}
		c.Description = md
		break
	}
	for _, ic := range x.Icons {
		c.Icons = append(c.Icons, Icon{Type: ic.Type, Value: strings.TrimSpace(ic.Value), Width: ic.Width,
			Height: ic.Height, Scale: ic.Scale})
	}
	for _, u := range x.URLs {
		if _, seen := c.URLs[u.Type]; !seen {
			c.URLs[u.Type] = strings.TrimSpace(u.Value)
		}
	}
	for _, cat := range x.Categories {
		if cat = strings.TrimSpace(cat); cat != "" {
			c.Categories = append(c.Categories, cat)
		}
	}
	for _, k := range x.Keywords {
		if k.Lang == "" {
			if kw := collapse(k.Text); kw != "" {
				c.Keywords = append(c.Keywords, kw)
			}
		}
	}
	for _, s := range x.Screenshots {
		shot := convertScreenshot(s)
		if shot.Source == "" {
			continue
		}
		if s.Type == "default" {
			c.Screenshots = append([]Screenshot{shot}, c.Screenshots...)
		} else {
			c.Screenshots = append(c.Screenshots, shot)
		}
	}
	for _, r := range x.Releases {
		c.Releases = append(c.Releases, Release{Version: strings.TrimSpace(r.Version), Type: r.Type,
			Time: releaseTime(r)})
	}
	sort.SliceStable(c.Releases, func(i, j int) bool { return c.Releases[i].Time.After(c.Releases[j].Time) })
	// Older catalogs use <metadata>; newer ones use <custom>. Custom wins when both have a key.
	for _, vs := range [][]xValue{x.Metadata, x.Custom} {
		for _, v := range vs {
			c.Custom[v.Key] = strings.TrimSpace(v.Value)
		}
	}
	if len(x.Bundles) > 0 {
		b := x.Bundles[0]
		c.Bundle = Bundle{Type: b.Type, Ref: strings.TrimSpace(b.Value), Runtime: b.Runtime}
	}
	return c, nil
}

// convertScreenshot picks the untranslated source image and the thumbnail closest to 624 pixels wide.
func convertScreenshot(s xScreenshot) Screenshot {
	shot := Screenshot{Caption: pickText(s.Captions)}
	bestDiff := -1
	for _, im := range s.Images {
		if im.Lang != "" {
			continue
		}
		v := strings.TrimSpace(im.Value)
		switch im.Type {
		case "source", "":
			if shot.Source == "" {
				shot.Source = v
			}
		case "thumbnail":
			diff := abs(im.Width - 624)
			if bestDiff < 0 || diff < bestDiff {
				shot.Thumbnail, bestDiff = v, diff
			}
		}
	}
	return shot
}

func releaseTime(r xRelease) time.Time {
	if r.Timestamp != "" {
		if sec, err := strconv.ParseInt(strings.TrimSpace(r.Timestamp), 10, 64); err == nil {
			return time.Unix(sec, 0).UTC()
		}
	}
	if d := strings.TrimSpace(r.Date); d != "" {
		for _, layout := range []string{time.RFC3339, "2006-01-02T15:04:05", "2006-01-02"} {
			if t, err := time.Parse(layout, d); err == nil {
				return t.UTC()
			}
		}
	}
	return time.Time{}
}

// pickText returns the untranslated text, or the first text if every entry is translated.
func pickText(ts []xText) string {
	for _, t := range ts {
		if t.Lang == "" {
			return collapse(t.Text)
		}
	}
	if len(ts) > 0 {
		return collapse(ts[0].Text)
	}
	return ""
}

// collapse trims s and turns each run of whitespace into one space.
func collapse(s string) string { return strings.Join(strings.Fields(s), " ") }

func abs(n int) int {
	if n < 0 {
		return -n
	}
	return n
}
