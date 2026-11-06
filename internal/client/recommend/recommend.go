// Package recommend picks which source to install an app from.
package recommend

import (
	"fmt"
	"slices"

	"github.com/tyler-builds/uas/internal/catalog"
)

// InstallOrder ranks sources for installing. Sources whose installs need a restart (layering on an
// image-based system) rank after all others, whatever their place here.
var InstallOrder = []catalog.SourceID{catalog.SourceFlathub, catalog.SourceFedora, catalog.SourceDebian,
	catalog.SourceUbuntu, catalog.SourceArch, catalog.SourceHomebrew, catalog.SourceSnap, catalog.SourceGitHub,
	catalog.SourceCodeberg, catalog.SourceCOPR}

// System is what the recommendation needs to know about the system.
type System struct {
	// Available reports whether the system can install a listing.
	Available func(catalog.Listing) bool
	// Restart reports whether installs from a source take effect only after a restart. When nil, Fedora and
	// COPR do, as on an image-based system.
	Restart func(catalog.SourceID) bool
}

func (s System) layered(src catalog.SourceID) bool {
	if s.Restart != nil {
		return s.Restart(src)
	}
	return src == catalog.SourceFedora || src == catalog.SourceCOPR
}

func (s System) available(l catalog.Listing) bool { return s.Available != nil && s.Available(l) }

// Choice is the listing to install and why it was picked.
type Choice struct {
	Listing catalog.Listing
	Reason  string
}

// Choose picks a listing of app that this system can install. If want is set, only that source is
// considered.
//
// Verified listings come first, then InstallOrder: Flathub (sandboxed, no restart), the distribution's own
// packages, Homebrew, the Snap Store, then release downloads, then anything that needs a restart (layering
// from Fedora and COPR on an image-based system), since that can hold up system updates.
func Choose(app *catalog.App, sys System, want catalog.SourceID) (Choice, error) {
	var candidates []catalog.Listing
	for _, l := range app.Listings {
		if want != "" && l.Source != want {
			continue
		}
		if sys.available(l) {
			candidates = append(candidates, l)
		}
	}
	if len(candidates) == 0 {
		if want != "" {
			return Choice{}, fmt.Errorf("%s is not available from %s on this system", app.Name, want.Name())
		}
		return Choice{}, fmt.Errorf("%s is not available from any source this system supports", app.Name)
	}

	n := len(InstallOrder) + 1
	rank := func(l catalog.Listing) int {
		r := slices.Index(InstallOrder, l.Source)
		if r < 0 {
			r = len(InstallOrder)
		}
		if sys.layered(l.Source) {
			r += n
		}
		if !l.Verified {
			r += 2 * n
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
	case !sys.layered(best.Source) &&
		slices.ContainsFunc(candidates, func(l catalog.Listing) bool { return sys.layered(l.Source) }):
		reason = best.Source.Name() + " installs without a restart"
	default:
		reason = strengths[best.Source]
		if best.Source.Distribution() && !sys.layered(best.Source) {
			reason = "it's your distribution's own package, built and signed by " + best.Source.Name()
		}
	}
	return Choice{Listing: best, Reason: reason}, nil
}

// strengths is the one-line case for each source, used when nothing more specific decides.
var strengths = map[catalog.SourceID]string{
	catalog.SourceFlathub:  "Flathub apps run sandboxed and install without a restart",
	catalog.SourceHomebrew: "Homebrew installs for your user without changing the system",
	catalog.SourceSnap:     "snaps run sandboxed and update themselves",
	catalog.SourceGitHub:   "it is the developer's own release",
	catalog.SourceCodeberg: "it is the developer's own release",
	catalog.SourceFedora:   "Fedora packages are reviewed and signed by Fedora",
	catalog.SourceCOPR:     "the COPR project is the only packaged build",
}

// Traits says what installing a listing means: who reviewed it, whether it is sandboxed, whether it
// needs a restart.
func Traits(l catalog.Listing, sys System) []string {
	var t []string
	layered := sys.layered(l.Source)
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
	case catalog.SourceSnap:
		if l.Verified {
			t = append(t, "Publisher verified by the Snap Store ("+l.VerifiedAs+")")
		} else {
			t = append(t, "Publisher not verified by the Snap Store: anyone can publish a snap")
		}
		if l.Classic {
			t = append(t, "Classic confinement: runs without the snap sandbox, with full access to your system")
		} else {
			t = append(t, "Runs in the snap sandbox")
		}
		t = append(t, "snapd keeps it updated on its own", "Installs without a restart")
	case catalog.SourceGitHub, catalog.SourceCodeberg:
		t = append(t, "Downloaded straight from the developer's "+l.Source.Name()+" releases; no distribution reviews it")
		if l.Trust != nil {
			t = append(t, fmt.Sprintf("Trust score %d/100, from the project's age, stars, downloads and releases",
				l.Trust.Score))
		}
		if l.Source == catalog.SourceGitHub {
			t = append(t, "Checked against the checksum GitHub publishes")
		} else {
			t = append(t, "Checked against the checksum uas recorded when it built the catalog")
		}
		t = append(t, "Runs as an AppImage, not sandboxed", "Installs without a restart")
	case catalog.SourceFedora, catalog.SourceDebian, catalog.SourceUbuntu, catalog.SourceArch:
		t = append(t, "Packaged, reviewed and signed by "+l.Source.Name())
		if layered {
			t = append(t, "Layered into the system image: takes effect after a restart",
				"Layered packages can hold up system updates if they conflict with a new image")
		} else {
			t = append(t, "Installed system-wide with your package manager; asks for your password")
		}
		t = append(t, "Not sandboxed")
	case catalog.SourceCOPR:
		t = append(t, "Built by the owner of the COPR project "+l.Project+"; Fedora doesn't review it")
		if l.Trust != nil {
			t = append(t, fmt.Sprintf("Trust score %d/100, from the project's build history", l.Trust.Score))
		}
		if layered {
			t = append(t, "Layered into the system image: takes effect after a restart",
				"Layered packages can hold up system updates if they conflict with a new image")
		} else {
			t = append(t, "Installed system-wide with dnf; asks for your password")
		}
		t = append(t, "Not sandboxed")
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
func Alternatives(app *catalog.App, sys System, chosen catalog.Listing) []Alternative {
	var alts []Alternative
	for _, l := range app.Listings {
		if l.Source == chosen.Source && l.Ref == chosen.Ref {
			continue
		}
		alts = append(alts, Alternative{Listing: l, Because: because(l, chosen, sys)})
	}
	return alts
}

// because says what makes l worse than chosen for this user. It names a concrete difference (a sandbox, a
// review, a restart), not just the ranking.
func because(l, chosen catalog.Listing, sys System) string {
	available := sys.available(l)
	switch {
	case !available && l.Distro != "" && l.Distro != chosen.Distro && l.Source == chosen.Source:
		return "it's built for a different release than the one this system runs"
	case !available && l.Source == catalog.SourceFedora && sys.layered(l.Source):
		return "it's built for a different Fedora release than the one this system boots next"
	case !available && l.Source.Distribution():
		return "it's for " + l.Source.Name() + ", and this system isn't"
	case !available:
		return "it can't be installed on this system"
	case sys.layered(l.Source) && !sys.layered(chosen.Source):
		what := "layered into the system image"
		if l.Source == catalog.SourceCOPR {
			what = "built by a COPR project owner without review, and layered into the system image"
		}
		return "it would be " + what + ", which needs a restart and can hold up system updates"
	case l.Source.Releases():
		why := "it runs as an AppImage outside any sandbox, and no distribution checks its builds"
		if chosen.Source == catalog.SourceHomebrew || chosen.Source.Distribution() {
			why = "no distribution checks its builds, while " + chosen.Source.Name() + " reviews its packages"
		}
		if l.Trust != nil {
			why += fmt.Sprintf(". Its trust score of %d/100 rates how well kept the project is, not its code",
				l.Trust.Score)
		}
		return why
	case l.Source == catalog.SourceSnap && chosen.Source == catalog.SourceFlathub:
		return "Flathub is preferred where both have it"
	case l.Source == catalog.SourceSnap:
		return chosen.Source.Name() + " is preferred where both have it"
	case (l.Source == catalog.SourceHomebrew || l.Source.Distribution()) && chosen.Source == catalog.SourceFlathub:
		return "it isn't sandboxed"
	case l.Source == catalog.SourceFlathub && chosen.Verified && !l.Verified:
		return chosen.Source.Name() + " has a verified publisher and Flathub's isn't"
	case l.Source == catalog.SourceFlathub:
		return "you picked " + chosen.Source.Name()
	}
	return chosen.Source.Name() + " ranks higher on this system"
}
