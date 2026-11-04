package merge

import (
	"testing"

	"github.com/tyler-builds/uas/internal/catalog"
)

func TestMergeByAppStreamID(t *testing.T) {
	apps, _ := Merge([]catalog.Listing{
		{Source: catalog.SourceFedora, Ref: "example-browser", AppStreamID: "org.example.Browser.desktop",
			Name: "example-browser", License: "MPL-2.0"},
		{Source: catalog.SourceFlathub, Ref: "app/org.example.Browser/x86_64/stable",
			AppStreamID: "org.example.Browser", Name: "Example Browser"},
		{Source: catalog.SourceFlathub, Ref: "app/org.example.Notes/x86_64/stable",
			AppStreamID: "org.example.Notes", Name: "Notes"},
		// A duplicate ref is dropped.
		{Source: catalog.SourceFlathub, Ref: "app/org.example.Notes/x86_64/stable",
			AppStreamID: "org.example.Notes", Name: "Notes again"},
		{Source: catalog.SourceGitHub, Ref: "example/tool", Name: "tool"},
	})

	if len(apps) != 3 {
		t.Fatalf("got %d apps, want 3: %+v", len(apps), apps)
	}
	// Apps are sorted by ID.
	if apps[0].ID != "github:example/tool" {
		t.Errorf("ID without AppStream ID = %q", apps[0].ID)
	}
	b := apps[1]
	if b.ID != "org.example.Browser" {
		t.Errorf("ID = %q", b.ID)
	}
	// Flathub outranks Fedora for display fields; Fedora fills the gaps.
	if b.Name != "Example Browser" || b.License != "MPL-2.0" {
		t.Errorf("Name = %q, License = %q", b.Name, b.License)
	}
	if len(b.Listings) != 2 || b.Listings[0].Source != catalog.SourceFlathub {
		t.Errorf("Listings = %+v", b.Listings)
	}
	if apps[2].ID != "org.example.Notes" || len(apps[2].Listings) != 1 || apps[2].Name != "Notes" {
		t.Errorf("Notes = %+v", apps[2])
	}
}

func TestMergeByRepo(t *testing.T) {
	apps, _ := Merge([]catalog.Listing{
		{Source: catalog.SourceFlathub, Ref: "app/it.mijorus.gearlever/x86_64/stable", AppStreamID: "it.mijorus.gearlever",
			Name: "Gear Lever", VCSURL: "https://github.com/mijorus/gearlever"},
		{Source: catalog.SourceGitHub, Ref: "mijorus/gearlever", Name: "gearlever",
			VCSURL: "https://github.com/mijorus/gearlever"},
		// An owner page is not a repo.
		{Source: catalog.SourceGitHub, Ref: "example/thing", Name: "thing", VCSURL: "https://github.com/example/thing"},
		{Source: catalog.SourceFedora, Ref: "fedora-45/other", AppStreamID: "other", Name: "Other",
			VCSURL: "https://github.com/example"},
	})
	if len(apps) != 3 || apps[1].ID != "it.mijorus.gearlever" || len(apps[1].Listings) != 2 {
		t.Errorf("apps = %+v", apps)
	}
}

func TestMergeByNameAndURL(t *testing.T) {
	apps, _ := Merge([]catalog.Listing{
		{Source: catalog.SourceFlathub, Ref: "app/app.rednotebook.RedNotebook/x86_64/stable",
			AppStreamID: "app.rednotebook.RedNotebook", Name: "RedNotebook", Homepage: "https://rednotebook.app"},
		{Source: catalog.SourceFedora, Ref: "fedora-45/rednotebook", AppStreamID: "rednotebook",
			Name: "RedNotebook", Homepage: "http://www.rednotebook.app/"},
		// Ambiguous: two Fedora apps match the one Flathub app, so nothing merges.
		{Source: catalog.SourceFlathub, Ref: "app/org.ardour.Ardour/x86_64/stable", AppStreamID: "org.ardour.Ardour",
			Name: "Ardour", Homepage: "https://ardour.org"},
		{Source: catalog.SourceFedora, Ref: "fedora-45/ardour8", AppStreamID: "ardour8", Name: "Ardour",
			Homepage: "https://ardour.org"},
		{Source: catalog.SourceFedora, Ref: "fedora-45/ardour9", AppStreamID: "ardour9", Name: "Ardour",
			Homepage: "https://ardour.org"},
		// Same source: two Flathub apps are never merged with each other.
		{Source: catalog.SourceFlathub, Ref: "app/org.example.A/x86_64/stable", AppStreamID: "org.example.A",
			Name: "Tool", VCSURL: "https://github.com/example/tool"},
		{Source: catalog.SourceFlathub, Ref: "app/org.example.B/x86_64/stable", AppStreamID: "org.example.B",
			Name: "Tool", VCSURL: "https://github.com/example/tool"},
		// A bare forge host does not identify an app.
		{Source: catalog.SourceFedora, Ref: "fedora-45/tool", AppStreamID: "tool", Name: "Tool",
			Homepage: "https://github.com/"},
	})
	ids := map[string]int{}
	for _, a := range apps {
		ids[a.ID] = len(a.Listings)
	}
	if ids["app.rednotebook.RedNotebook"] != 2 || len(ids) != 7 {
		t.Errorf("apps = %v", ids)
	}
}
