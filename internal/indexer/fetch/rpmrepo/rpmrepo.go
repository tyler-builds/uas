// Package rpmrepo reads RPM repository metadata (repomd.xml and primary.xml), as served by Fedora and COPR.
package rpmrepo

import (
	"context"
	"encoding/xml"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/tyler-builds/uas/internal/indexer/fetch"
)

// Repomd downloads a repo's repomd.xml and returns the URL of each metadata file by type, such as "primary"
// or "appstream".
func Repomd(ctx context.Context, client *http.Client, repo, cacheDir string) (map[string]string, error) {
	repo = strings.TrimSuffix(repo, "/")
	path, err := fetch.Get(ctx, client, repo+"/repodata/repomd.xml", cacheDir)
	if err != nil {
		return nil, err
	}
	r, err := fetch.OpenDecompressed(path)
	if err != nil {
		return nil, err
	}
	defer r.Close()
	var md struct {
		Data []struct {
			Type     string `xml:"type,attr"`
			Location struct {
				Href string `xml:"href,attr"`
			} `xml:"location"`
		} `xml:"data"`
	}
	if err := xml.NewDecoder(r).Decode(&md); err != nil {
		return nil, fmt.Errorf("repomd: %w", err)
	}
	files := map[string]string{}
	for _, d := range md.Data {
		href, err := Relative(d.Location.Href)
		if err != nil {
			return nil, err
		}
		files[d.Type] = repo + "/" + href
	}
	return files, nil
}

var safeHref = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._+~^/-]*$`)

// Relative checks that a path from repo metadata stays inside the repo.
func Relative(href string) (string, error) {
	if !safeHref.MatchString(href) || strings.Contains(href, "..") {
		return "", fmt.Errorf("unexpected path in repo metadata: %q", href)
	}
	return href, nil
}

// Package is one binary package from primary.xml.
type Package struct {
	Name     string
	Version  string // "[epoch:]version-release"
	Built    time.Time
	Location string // URL of the .rpm
}

// ReadPrimary streams a primary.xml file (compressed or not) and calls fn for each package built for arch or
// noarch. repo is the repo's base URL, used to make package locations absolute.
func ReadPrimary(path, arch, repo string, fn func(Package)) error {
	repo = strings.TrimSuffix(repo, "/")
	r, err := fetch.OpenDecompressed(path)
	if err != nil {
		return err
	}
	defer r.Close()
	dec := xml.NewDecoder(r)
	for {
		tok, err := dec.Token()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return fmt.Errorf("primary: %w", err)
		}
		se, ok := tok.(xml.StartElement)
		if !ok || se.Name.Local != "package" {
			continue
		}
		var x struct {
			Name    string `xml:"name"`
			Arch    string `xml:"arch"`
			Version struct {
				Epoch string `xml:"epoch,attr"`
				Ver   string `xml:"ver,attr"`
				Rel   string `xml:"rel,attr"`
			} `xml:"version"`
			Time struct {
				Build int64 `xml:"build,attr"`
			} `xml:"time"`
			Location struct {
				Href string `xml:"href,attr"`
			} `xml:"location"`
		}
		if err := dec.DecodeElement(&x, &se); err != nil {
			return fmt.Errorf("primary: %w", err)
		}
		if x.Arch != arch && x.Arch != "noarch" {
			continue
		}
		href, err := Relative(x.Location.Href)
		if err != nil {
			return err
		}
		version := x.Version.Ver + "-" + x.Version.Rel
		if x.Version.Epoch != "" && x.Version.Epoch != "0" {
			version = x.Version.Epoch + ":" + version
		}
		p := Package{Name: x.Name, Version: version, Location: repo + "/" + href}
		if x.Time.Build > 0 {
			p.Built = time.Unix(x.Time.Build, 0).UTC()
		}
		fn(p)
	}
}
