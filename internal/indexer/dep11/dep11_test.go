package dep11

import (
	"strings"
	"testing"

	"github.com/tyler-builds/uas/internal/indexer/appstream"
)

const sample = `---
File: DEP-11
Version: '1.0'
Origin: debian-trixie-main
MediaBaseUrl: https://appstream.debian.org/media/trixie
---
Type: desktop-application
ID: org.gnome.gitg
Package: gitg
ProjectLicense: GPL-2.0+
Name:
  de: gitg
  C: gitg
Summary:
  C: Graphical user interface for git
Description:
  C: <p>gitg is a graphical user interface for git.</p>
    <ul><li>Browse repositories</li><li>Commit files</li></ul>
DeveloperName:
  C: The GNOME Project
Categories: [Development, RevisionControl]
Url:
  homepage: https://wiki.gnome.org/Apps/Gitg
Icon:
  cached:
  - name: gitg_org.gnome.gitg.png
    width: 64
    height: 64
  remote:
  - url: org/gnome/gitg/8f3a/icons/128x128/gitg_org.gnome.gitg.png
    width: 128
    height: 128
  stock: org.gnome.gitg
Screenshots:
- caption:
    C: History
  thumbnails:
  - url: org/gnome/gitg/8f3a/screenshots/image-1_624x351.png
    width: 624
    height: 351
  - url: org/gnome/gitg/8f3a/screenshots/image-1_1248x702.png
    width: 1248
    height: 702
  source-image:
    url: org/gnome/gitg/8f3a/screenshots/image-1_orig.png
    width: 1600
    height: 900
- default: true
  source-image:
    url: https://example.org/main.png
Releases:
- version: 44.0
  unix-timestamp: 1679000000
---
Type: console-application
ID: htop
Package: htop
Name:
  C: htop
Summary:
  C: Interactive process viewer
`

func TestParse(t *testing.T) {
	var got []*appstream.Component
	err := Parse(strings.NewReader(sample), func(c *appstream.Component) error {
		got = append(got, c)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("got %d components", len(got))
	}
	c := got[0]
	if c.ID != "org.gnome.gitg" || c.PkgName != "gitg" || c.Name != "gitg" || c.Developer != "The GNOME Project" ||
		c.License != "GPL-2.0+" || c.URLs["homepage"] != "https://wiki.gnome.org/Apps/Gitg" {
		t.Errorf("component = %+v", c)
	}
	if want := "https://appstream.debian.org/media/trixie/org/gnome/gitg/8f3a/icons/128x128/gitg_org.gnome.gitg.png"; c.BestRemoteIcon() != want {
		t.Errorf("icon = %q", c.BestRemoteIcon())
	}
	if len(c.Screenshots) != 2 || c.Screenshots[0].Source != "https://example.org/main.png" ||
		!strings.HasSuffix(c.Screenshots[1].Thumbnail, "image-1_624x351.png") || c.Screenshots[1].Caption != "History" {
		t.Errorf("screenshots = %+v", c.Screenshots)
	}
	if !strings.Contains(c.Description, "- Browse repositories") {
		t.Errorf("description = %q", c.Description)
	}
	if len(c.Releases) != 1 || c.Releases[0].Version != "44.0" || c.Releases[0].Time.Year() != 2023 {
		t.Errorf("releases = %+v", c.Releases)
	}
	if got[1].Type != "console-application" {
		t.Errorf("second = %+v", got[1])
	}
}

func TestDuplicateKeys(t *testing.T) {
	in := "---\nFile: DEP-11\n---\nType: desktop-application\nID: a\nPackage: a\nName:\n  C: A\n  ca_ES: A\n  ca_ES: A\n---\nType: desktop-application\nID: b\nPackage: b\nName:\n  C: B\n"
	var ids []string
	if err := Parse(strings.NewReader(in), func(c *appstream.Component) error {
		ids = append(ids, c.ID+":"+c.Name)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if len(ids) != 2 || ids[0] != "a:A" {
		t.Errorf("ids = %q", ids)
	}
}
