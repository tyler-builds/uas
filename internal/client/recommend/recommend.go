// Package recommend picks which source to install an app from.
package recommend

import (
	"fmt"
	"slices"

	"github.com/tyler-builds/uas/internal/catalog"
)

// InstallOrder ranks sources for installing on an image-based system.
var InstallOrder = []catalog.SourceID{catalog.SourceFlathub, catalog.SourceHomebrew, catalog.SourceGitHub,
	catalog.SourceFedora, catalog.SourceCOPR}

// layered reports whether a source installs by layering onto the system image.
func layered(s catalog.SourceID) bool { return s == catalog.SourceFedora || s == catalog.SourceCOPR }

// Choice is the listing to install and why it was picked.
type Choice struct {
	Listing catalog.Listing
	Reason  string
}

// Choose picks a listing of app that this system can install, as decided by available. If want is set, only
// that source is considered.
//
// Verified listings come first, then InstallOrder: on an image-based system like Bazzite, Flathub (sandboxed,
// no restart), then Homebrew (no restart), then GitHub, then layering from Fedora and COPR, which needs a
// restart and can hold up system updates. Trust scores will refine this once COPR and GitHub are added.
func Choose(app *catalog.App, available func(catalog.Listing) bool, want catalog.SourceID) (Choice, error) {
	var candidates []catalog.Listing
	for _, l := range app.Listings {
		if want != "" && l.Source != want {
			continue
		}
		if available(l) {
			candidates = append(candidates, l)
		}
	}
	if len(candidates) == 0 {
		if want != "" {
			return Choice{}, fmt.Errorf("%s is not available from %s on this system", app.Name, want.Name())
		}
		return Choice{}, fmt.Errorf("%s is not available from any source this system supports", app.Name)
	}

	rank := func(l catalog.Listing) int {
		r := slices.Index(InstallOrder, l.Source)
		if r < 0 {
			r = len(InstallOrder)
		}
		if !l.Verified {
			r += len(InstallOrder) + 1
		}
		return r
	}
	best := slices.MinFunc(candidates, func(a, b catalog.Listing) int { return rank(a) - rank(b) })

	var reason string
	switch {
	case want != "":
		reason = "you asked for " + want.Name()
	case len(app.Listings) == 1:
		reason = "the only source that offers it"
	case len(candidates) == 1:
		reason = "the only one of its sources this system supports"
	case best.Verified:
		reason = "verified publisher on " + best.Source.Name()
	case !layered(best.Source) && slices.ContainsFunc(candidates, func(l catalog.Listing) bool { return layered(l.Source) }):
		reason = best.Source.Name() + " installs without a restart"
	default:
		reason = best.Source.Name() + " is the preferred source on this system"
	}
	return Choice{Listing: best, Reason: reason}, nil
}
