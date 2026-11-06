package github

import (
	"strings"
	"testing"

	"github.com/tyler-builds/uas/internal/catalog"
)

func TestForgeSupports(t *testing.T) {
	sha := strings.Repeat("a", 64)
	cases := []struct {
		forge catalog.SourceID
		l     catalog.Listing
		want  bool
	}{
		{catalog.SourceCodeberg, catalog.Listing{Source: catalog.SourceCodeberg, Ref: "codeberg.org/o/app",
			Assets: []catalog.Asset{{Kind: catalog.AssetAppImage, SHA256: sha,
				URL: "https://codeberg.org/o/app/releases/download/v1/App.AppImage"}}}, true},
		// A file from another repo, or another host, is refused.
		{catalog.SourceCodeberg, catalog.Listing{Source: catalog.SourceCodeberg, Ref: "codeberg.org/o/app",
			Assets: []catalog.Asset{{Kind: catalog.AssetAppImage, SHA256: sha,
				URL: "https://codeberg.org/x/app/releases/download/v1/App.AppImage"}}}, false},
		{catalog.SourceCodeberg, catalog.Listing{Source: catalog.SourceCodeberg, Ref: "codeberg.org/o/app",
			Assets: []catalog.Asset{{Kind: catalog.AssetAppImage, SHA256: sha,
				URL: "https://github.com/o/app/releases/download/v1/App.AppImage"}}}, false},
		// GitLab projects can be nested in groups; files go through the project's own pages.
		{catalog.SourceGitLab, catalog.Listing{Source: catalog.SourceGitLab, Ref: "gitlab.com/g/sub/app",
			Assets: []catalog.Asset{{Kind: catalog.AssetAppImage, SHA256: sha,
				URL: "https://gitlab.com/g/sub/app/-/releases/v1/downloads/App.AppImage"}}}, true},
		{catalog.SourceGitLab, catalog.Listing{Source: catalog.SourceGitLab, Ref: "gitlab.com/g/../app",
			Assets: []catalog.Asset{{Kind: catalog.AssetAppImage, SHA256: sha,
				URL: "https://gitlab.com/g/../app/-/releases/v1/downloads/App.AppImage"}}}, false},
		// A listing from another forge isn't this backend's.
		{catalog.SourceGitLab, catalog.Listing{Source: catalog.SourceCodeberg, Ref: "codeberg.org/o/app",
			Assets: []catalog.Asset{{Kind: catalog.AssetAppImage, SHA256: sha,
				URL: "https://codeberg.org/o/app/releases/download/v1/App.AppImage"}}}, false},
	}
	for i, c := range cases {
		b := &Backend{Forge: c.forge}
		if got := b.Supports(c.l); got != c.want {
			t.Errorf("case %d: Supports = %v, want %v", i, got, c.want)
		}
	}
	if (&Backend{}).Source() != catalog.SourceGitHub {
		t.Error("a backend with no forge should be GitHub's")
	}
}
