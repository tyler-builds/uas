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
	all := func(catalog.Listing) bool { return true }
	onlyFlathub := func(l catalog.Listing) bool { return l.Source == catalog.SourceFlathub }
	none := func(catalog.Listing) bool { return false }

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

	// Homebrew beats Fedora layering, since it needs no restart.
	tool := &catalog.App{Name: "Tool", Listings: []catalog.Listing{
		{Source: catalog.SourceFedora, Ref: "fedora-45/tool"},
		{Source: catalog.SourceHomebrew, Ref: "tool"},
	}}
	if c, err := Choose(tool, all, ""); err != nil || c.Listing.Source != catalog.SourceHomebrew ||
		c.Reason != "homebrew installs without a restart" {
		t.Errorf("Choose(tool) = %+v, %v", c, err)
	}
}
