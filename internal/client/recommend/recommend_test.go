package recommend

import (
	"strings"
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
	if c, err := Choose(app, System{Available: all}, ""); err != nil || c.Listing.Ref != "fedora-ref" {
		t.Errorf("Choose(all) = %+v, %v", c, err)
	}
	if c, err := Choose(app, System{Available: onlyFlathub}, ""); err != nil || c.Listing.Ref != "flathub-ref" ||
		c.Reason != "the only one of its sources this system supports" {
		t.Errorf("Choose(onlyFlathub) = %+v, %v", c, err)
	}
	if c, err := Choose(app, System{Available: all}, catalog.SourceFlathub); err != nil || c.Listing.Ref != "flathub-ref" {
		t.Errorf("Choose(want flathub) = %+v, %v", c, err)
	}
	if _, err := Choose(app, System{Available: onlyFlathub}, catalog.SourceFedora); err == nil {
		t.Error("Choose(want unavailable source) succeeded")
	}
	if _, err := Choose(app, System{Available: none}, ""); err == nil {
		t.Error("Choose(none available) succeeded")
	}

	// Homebrew beats Fedora layering, since it needs no restart.
	tool := &catalog.App{Name: "Tool", Listings: []catalog.Listing{
		{Source: catalog.SourceFedora, Ref: "fedora-45/tool"},
		{Source: catalog.SourceHomebrew, Ref: "tool"},
	}}
	if c, err := Choose(tool, System{Available: all}, ""); err != nil || c.Listing.Source != catalog.SourceHomebrew ||
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

	if tr := Traits(flathub, System{Available: available}); len(tr) < 2 || tr[0] != "Publisher verified by Flathub (example.org)" {
		t.Errorf("traits = %q", tr)
	}
	got := map[string]string{}
	for _, a := range Alternatives(app, System{Available: available}, flathub) {
		got[a.Listing.Ref] = a.Because
	}
	want := map[string]string{
		"fedora-45/x": "it would be layered into the system image, which needs a restart and can hold up system updates",
		"fedora-44/x": "it's built for a different Fedora release than the one this system boots next",
		"o/x": "it runs as an AppImage outside any sandbox, and no distribution checks its builds. " +
			"Its trust score of 70/100 rates how well kept the project is, not its code",
	}
	for ref, w := range want {
		if got[ref] != w {
			t.Errorf("because(%s) = %q, want %q", ref, got[ref], w)
		}
	}
	// A perfect trust score still loses to Flathub, and says why.
	gh.Trust.Score = 100
	for _, a := range Alternatives(app, System{Available: available}, catalog.Listing{Source: catalog.SourceFlathub, Ref: "app/x"}) {
		if a.Listing.Ref == "o/x" && !strings.Contains(a.Because, "outside any sandbox") {
			t.Errorf("github because = %q", a.Because)
		}
	}
}

func TestPackageBasedSystem(t *testing.T) {
	// On Ubuntu, the distribution's own package beats Homebrew and a snap, and needs no restart.
	app := &catalog.App{Name: "Tool", Listings: []catalog.Listing{
		{Source: catalog.SourceHomebrew, Ref: "tool"},
		{Source: catalog.SourceSnap, Ref: "tool"},
		{Source: catalog.SourceUbuntu, Ref: "ubuntu-24.04/tool", Distro: "ubuntu-24.04", Package: "tool"},
		{Source: catalog.SourceDebian, Ref: "debian-13/tool", Distro: "debian-13", Package: "tool"},
	}}
	sys := System{
		Available: func(l catalog.Listing) bool { return l.Source != catalog.SourceDebian },
		Restart:   func(catalog.SourceID) bool { return false },
	}
	c, err := Choose(app, sys, "")
	if err != nil || c.Listing.Source != catalog.SourceUbuntu ||
		c.Reason != "it's your distribution's own package, built and signed by Ubuntu" {
		t.Fatalf("Choose = %+v, %v", c, err)
	}
	if tr := Traits(c.Listing, sys); tr[1] != "Installed system-wide with your package manager; asks for your password" {
		t.Errorf("traits = %q", tr)
	}
	for _, a := range Alternatives(app, sys, c.Listing) {
		if a.Listing.Source == catalog.SourceDebian && a.Because != "it's for Debian, and this system isn't" {
			t.Errorf("debian because = %q", a.Because)
		}
	}
}
