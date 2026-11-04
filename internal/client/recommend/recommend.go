// Package recommend picks which source to install an app from.
package recommend

import (
	"fmt"
	"slices"

	"github.com/tyler-builds/uas/internal/catalog"
)

// Choice is the listing to install and why it was picked.
type Choice struct {
	Listing catalog.Listing
	Reason  string
}

// Choose picks a listing of app that this system can install, as decided by available. If want is set, only
// that source is considered.
//
// The default order is the catalog's source precedence (Flathub first on Bazzite, since it needs no reboot
// and is sandboxed), with verified listings ahead of unverified ones. Trust scores will refine this once
// sources with weaker guarantees (COPR, GitHub) are added.
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
			return Choice{}, fmt.Errorf("%s is not available from %s on this system", app.Name, want)
		}
		return Choice{}, fmt.Errorf("%s is not available from any source this system supports", app.Name)
	}

	rank := func(l catalog.Listing) int {
		r := slices.Index(catalog.SourcePrecedence, l.Source)
		if r < 0 {
			r = len(catalog.SourcePrecedence)
		}
		if !l.Verified {
			r += len(catalog.SourcePrecedence) + 1
		}
		return r
	}
	best := slices.MinFunc(candidates, func(a, b catalog.Listing) int { return rank(a) - rank(b) })

	var reason string
	switch {
	case want != "":
		reason = "you asked for " + string(want)
	case len(app.Listings) == 1:
		reason = "the only source that offers it"
	case len(candidates) == 1:
		reason = "the only one of its sources this system supports"
	case best.Verified:
		reason = "verified publisher on " + string(best.Source)
	default:
		reason = string(best.Source) + " is the preferred source on this system"
	}
	return Choice{Listing: best, Reason: reason}, nil
}
