package recommend

import (
	"testing"

	"github.com/tyler-builds/uas/internal/catalog"
)

func TestChoose(t *testing.T) {
	app := &catalog.App{Name: "Example", Listings: []catalog.Listing{
		{Source: catalog.SourceFlathub, Ref: "flathub-ref"},
		{Source: catalog.SourceFedora, Ref: "fedora-ref", Verified: true},
	}}
	all := func(catalog.SourceID) bool { return true }
	onlyFlathub := func(s catalog.SourceID) bool { return s == catalog.SourceFlathub }
	none := func(catalog.SourceID) bool { return false }

	// A verified listing beats a higher-precedence unverified one.
	if c, err := Choose(app, all, ""); err != nil || c.Listing.Ref != "fedora-ref" {
		t.Errorf("Choose(all) = %+v, %v", c, err)
	}
	if c, err := Choose(app, onlyFlathub, ""); err != nil || c.Listing.Ref != "flathub-ref" ||
		c.Reason != "the only one of its sources this system supports" {
		t.Errorf("Choose(onlyFlathub) = %+v, %v", c, err)
	}
	if c, err := Choose(app, all, catalog.SourceFlathub); err != nil || c.Listing.Ref != "flathub-ref" {
		t.Errorf("Choose(want flathub) = %+v, %v", c, err)
	}
	if _, err := Choose(app, onlyFlathub, catalog.SourceFedora); err == nil {
		t.Error("Choose(want unavailable source) succeeded")
	}
	if _, err := Choose(app, none, ""); err == nil {
		t.Error("Choose(none available) succeeded")
	}
}
