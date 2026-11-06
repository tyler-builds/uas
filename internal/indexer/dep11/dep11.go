// Package dep11 reads DEP-11, the YAML form of AppStream catalogs that Debian and Ubuntu publish beside
// their package indexes (dists/<suite>/<component>/dep11/Components-<arch>.yml.gz).
//
// A file is a header document (with MediaBaseUrl, which icon and screenshot paths are relative to) and then
// one document per component. Components come out as appstream.Component, so the same code turns AppStream
// XML (Fedora, Arch) and DEP-11 into listings.
package dep11

import (
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/tyler-builds/uas/internal/indexer/appstream"
)

// component is the part of a DEP-11 document uas reads.
type component struct {
	Type           string    `yaml:"Type"`
	ID             string    `yaml:"ID"`
	Package        string    `yaml:"Package"`
	Name           localized `yaml:"Name"`
	Summary        localized `yaml:"Summary"`
	Description    localized `yaml:"Description"`
	ProjectLicense string    `yaml:"ProjectLicense"`
	DeveloperName  localized `yaml:"DeveloperName"`
	Developer      struct {
		Name localized `yaml:"name"`
	} `yaml:"Developer"`
	Categories []string             `yaml:"Categories"`
	Keywords   localizedList        `yaml:"Keywords"`
	URL        map[string]string    `yaml:"Url"`
	Icon       map[string]yaml.Node `yaml:"Icon"`
	Screenshot []screenshot         `yaml:"Screenshots"`
	Releases   []release            `yaml:"Releases"`
	Custom     map[string]string    `yaml:"Custom"`
}

type image struct {
	URL    string `yaml:"url"`
	Width  int    `yaml:"width"`
	Height int    `yaml:"height"`
}

type screenshot struct {
	Default    bool      `yaml:"default"`
	Caption    localized `yaml:"caption"`
	Thumbnails []image   `yaml:"thumbnails"`
	Source     image     `yaml:"source-image"`
}

type release struct {
	Version   string `yaml:"version"`
	Type      string `yaml:"type"`
	Timestamp int64  `yaml:"unix-timestamp"`
}

type header struct {
	File         string `yaml:"File"`
	MediaBaseURL string `yaml:"MediaBaseUrl"`
}

// Parse reads a DEP-11 file and calls fn for each component.
func Parse(r io.Reader, fn func(*appstream.Component) error) error {
	dec := yaml.NewDecoder(r)
	var h header
	if err := dec.Decode(&h); err != nil {
		return fmt.Errorf("dep11: header: %w", err)
	}
	if h.File != "DEP-11" {
		return errors.New("dep11: not a DEP-11 file")
	}
	base := strings.TrimSuffix(h.MediaBaseURL, "/")
	for {
		var c component
		err := dec.Decode(&c)
		if err == io.EOF {
			return nil
		}
		// A type error (such as a translation listed twice) leaves the rest of the component decoded.
		var te *yaml.TypeError
		if err != nil && !errors.As(err, &te) {
			return fmt.Errorf("dep11: %w", err)
		}
		if err := fn(c.convert(base)); err != nil {
			return err
		}
	}
}

func (c *component) convert(base string) *appstream.Component {
	out := &appstream.Component{
		Type: c.Type, ID: c.ID, PkgName: c.Package,
		Name: c.Name.C, Summary: c.Summary.C, License: c.ProjectLicense,
		Categories: c.Categories, Keywords: c.Keywords.C, URLs: c.URL, Custom: c.Custom,
	}
	if d := c.Description.C; d != "" {
		if md, err := appstream.DescriptionMarkdown(d); err == nil {
			out.Description = md
		}
	}
	out.Developer = c.Developer.Name.C
	if out.Developer == "" {
		out.Developer = c.DeveloperName.C
	}
	abs := func(u string) string {
		if u == "" || strings.HasPrefix(u, "https://") || strings.HasPrefix(u, "http://") {
			return u
		}
		if base == "" {
			return ""
		}
		return base + "/" + strings.TrimPrefix(u, "/")
	}
	if n, ok := c.Icon["remote"]; ok {
		var remote []image
		if n.Decode(&remote) == nil {
			for _, ic := range remote {
				if u := abs(ic.URL); u != "" {
					out.Icons = append(out.Icons, appstream.Icon{Type: "remote", Value: u, Width: ic.Width,
						Height: ic.Height})
				}
			}
		}
	}
	if n, ok := c.Icon["stock"]; ok {
		var stock string
		if n.Decode(&stock) == nil && stock != "" {
			out.Icons = append(out.Icons, appstream.Icon{Type: "stock", Value: stock})
		}
	}
	// The default screenshot first.
	for pass := 0; pass < 2; pass++ {
		for _, s := range c.Screenshot {
			if s.Default != (pass == 0) {
				continue
			}
			src := abs(s.Source.URL)
			if src == "" {
				continue
			}
			thumb, width := "", 0
			for _, t := range s.Thumbnails { // the largest thumbnail up to 752 wide
				if t.Width <= 752 && t.Width > width {
					thumb, width = abs(t.URL), t.Width
				}
			}
			out.Screenshots = append(out.Screenshots, appstream.Screenshot{Caption: s.Caption.C, Source: src,
				Thumbnail: thumb})
		}
	}
	for _, r := range c.Releases {
		rel := appstream.Release{Version: r.Version, Type: r.Type}
		if r.Timestamp > 0 {
			rel.Time = time.Unix(r.Timestamp, 0).UTC()
		}
		out.Releases = append(out.Releases, rel)
	}
	return out
}

// localized is a translated text, of which uas keeps the untranslated ("C") one. It reads the mapping
// itself, because catalogs sometimes list a translation twice, which a Go map refuses.
type localized struct{ C string }

func (l *localized) UnmarshalYAML(n *yaml.Node) error {
	if n.Kind != yaml.MappingNode {
		return nil
	}
	for i := 0; i+1 < len(n.Content); i += 2 {
		if n.Content[i].Value == "C" {
			return n.Content[i+1].Decode(&l.C)
		}
	}
	return nil
}

// localizedList is a translated list, such as keywords.
type localizedList struct{ C []string }

func (l *localizedList) UnmarshalYAML(n *yaml.Node) error {
	if n.Kind != yaml.MappingNode {
		return nil
	}
	for i := 0; i+1 < len(n.Content); i += 2 {
		if n.Content[i].Value == "C" {
			return n.Content[i+1].Decode(&l.C)
		}
	}
	return nil
}
