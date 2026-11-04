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
		c.Reason != "Homebrew installs without a restart" {
		t.Errorf("Choose(tool) = %+v, %v", c, err)
	}
}

func TestTraitsAndAlternatives(t *testing.T) {
	flathub := catalog.Listing{Source: catalog.SourceFlathub, Ref: "app/x", Verified: true, VerifiedAs: "example.org"}
	fedora := catalog.Listing{Source: catalog.SourceFedora, Ref: "fedora-45/x", Distro: "fedora-45"}
	old := catalog.Listing{Source: catalog.SourceFedora, Ref: "fedora-44/x", Distro: "fedora-44"}
	gh := catalog.Listing{Source: catalog.SourceGitHub, Ref: "o/x", Trust: &catalog.Trust{Score: 70}}
	app := &catalog.App{Name: "X", Listings: []catalog.Listing{flathub, fedora, old, gh}}
	available := func(l catalog.Listing) bool { return l.Ref != "fedora-44/x" }

	if tr := Traits(flathub); len(tr) < 2 || tr[0] != "Publisher verified by Flathub (example.org)" {
		t.Errorf("traits = %q", tr)
	}
	got := map[string]string{}
	for _, a := range Alternatives(app, available, flathub) {
		got[a.Listing.Ref] = a.Because
	}
	want := map[string]string{
		"fedora-45/x": "its publisher isn't verified",
		"fedora-44/x": "built for a different Fedora release than this system's next boot",
		"o/x":         "its publisher isn't verified",
	}
	for ref, w := range want {
		if got[ref] != w {
			t.Errorf("because(%s) = %q, want %q", ref, got[ref], w)
		}
	}
	// Against an unverified choice, the next reasons show.
	unverified := flathub
	unverified.Verified = false
	for _, a := range Alternatives(app, available, unverified) {
		if a.Listing.Ref == "fedora-45/x" && a.Because != "it would be layered into the system image, which needs a restart" {
			t.Errorf("fedora because = %q", a.Because)
		}
		if a.Listing.Ref == "o/x" && a.Because != "nobody reviews it (trust score 70/100)" {
			t.Errorf("github because = %q", a.Because)
		}
	}
}
