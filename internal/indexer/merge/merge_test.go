package merge

import (
	"testing"

	"github.com/tyler-builds/uas/internal/catalog"
)

func TestMergeByAppStreamID(t *testing.T) {
	apps := Merge([]catalog.Listing{
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
