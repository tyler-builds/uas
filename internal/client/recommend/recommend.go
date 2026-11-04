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
		reason = strengths[best.Source]
	}
	return Choice{Listing: best, Reason: reason}, nil
}

// strengths is the one-line case for each source, used when nothing more specific decides.
var strengths = map[catalog.SourceID]string{
	catalog.SourceFlathub:  "Flathub apps run sandboxed and install without a restart",
	catalog.SourceHomebrew: "Homebrew installs for your user without changing the system",
	catalog.SourceGitHub:   "it is the developer's own release",
	catalog.SourceFedora:   "Fedora packages are reviewed and signed by Fedora",
	catalog.SourceCOPR:     "the COPR project is the only packaged build",
}

// Traits says what installing a listing means: who reviewed it, whether it is sandboxed, whether it
// needs a restart.
func Traits(l catalog.Listing) []string {
	var t []string
	switch l.Source {
	case catalog.SourceFlathub:
		if l.Verified {
			v := "Publisher verified by Flathub"
			if l.VerifiedAs != "" {
				v += " (" + l.VerifiedAs + ")"
			}
			t = append(t, v)
		} else {
			t = append(t, "Publisher not verified by Flathub: someone other than the developer may package it")
		}
		t = append(t, "Runs in a sandbox, with the permissions it declares",
			"Installs and updates without a restart")
	case catalog.SourceHomebrew:
		t = append(t, "Built by Homebrew from a formula its maintainers review",
			"Installs for your user only, without a restart or changes to the system",
			"Not sandboxed")
	case catalog.SourceGitHub:
		t = append(t, "Downloaded straight from the developer's GitHub releases; no distribution reviews it")
		if l.Trust != nil {
			t = append(t, fmt.Sprintf("Trust score %d/100, from the project's age, stars, downloads and releases",
				l.Trust.Score))
		}
		t = append(t, "Checked against the checksum GitHub publishes", "Runs as an AppImage, not sandboxed",
			"Installs without a restart")
	case catalog.SourceFedora:
		t = append(t, "Packaged, reviewed and signed by Fedora",
			"Layered into the system image: takes effect after a restart",
			"Layered packages can hold up system updates if they conflict with a new image",
			"Not sandboxed")
	case catalog.SourceCOPR:
		t = append(t, "Built by the owner of the COPR project "+l.Project+"; Fedora doesn't review it")
		if l.Trust != nil {
			t = append(t, fmt.Sprintf("Trust score %d/100, from the project's build history", l.Trust.Score))
		}
		t = append(t, "Layered into the system image: takes effect after a restart",
			"Layered packages can hold up system updates if they conflict with a new image",
			"Not sandboxed")
	}
	return t
}

// Alternative is another listing of the app, and why it wasn't picked.
type Alternative struct {
	Listing catalog.Listing
	Because string
}

// Alternatives explains, for each listing other than chosen, why chosen was preferred or why the listing
// can't be used here.
func Alternatives(app *catalog.App, available func(catalog.Listing) bool, chosen catalog.Listing) []Alternative {
	var alts []Alternative
	for _, l := range app.Listings {
		if l.Source == chosen.Source && l.Ref == chosen.Ref {
			continue
		}
		alts = append(alts, Alternative{Listing: l, Because: because(l, chosen, available(l))})
	}
	return alts
}

// because says what makes l worse than chosen for this user. It names a concrete difference (a sandbox, a
// review, a restart), not just the ranking.
func because(l, chosen catalog.Listing, available bool) string {
	switch {
	case !available && l.Distro != "" && l.Distro != chosen.Distro && layered(l.Source):
		return "it's built for a different Fedora release than the one this system boots next"
	case !available:
		return "it can't be installed on this system"
	case layered(l.Source) && !layered(chosen.Source):
		what := "layered into the system image"
		if l.Source == catalog.SourceCOPR {
			what = "built by a COPR project owner without review, and layered into the system image"
		}
		return "it would be " + what + ", which needs a restart and can hold up system updates"
	case l.Source == catalog.SourceGitHub:
		why := "it runs as an AppImage outside any sandbox, and no distribution checks its builds"
		if chosen.Source == catalog.SourceHomebrew {
			why = "no distribution checks its builds, while Homebrew's maintainers review their formula"
		}
		if l.Trust != nil {
			why += fmt.Sprintf(". Its trust score of %d/100 rates how well kept the project is, not its code",
				l.Trust.Score)
		}
		return why
	case l.Source == catalog.SourceHomebrew && chosen.Source == catalog.SourceFlathub:
		return "it isn't sandboxed"
	case l.Source == catalog.SourceFlathub && chosen.Verified && !l.Verified:
		return "Flathub hasn't verified its publisher"
	case l.Source == catalog.SourceFlathub:
		return "you picked " + chosen.Source.Name()
	}
	return chosen.Source.Name() + " ranks higher on image-based systems like Bazzite"
}
