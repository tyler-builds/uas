// Package trust scores listings from sources that nobody reviews, such as GitHub releases.
//
// A score is a sum of signals, each with a fixed maximum, so the UI can show exactly why an app scored what
// it did. Flags are warnings shown regardless of the score. The weights are a starting point; see
// docs/trust-score.md.
package trust

import (
	"fmt"
	"math"
	"time"

	"github.com/tyler-builds/uas/internal/catalog"
)

// RepoFacts are what the score is computed from.
type RepoFacts struct {
	Created     time.Time
	Stars       int
	Downloads   int       // downloads of the latest release's assets
	LastRelease time.Time // the latest stable release
	// ReleasesLastYear counts releases, prereleases included, in the 12 months before now. Prereleases count
	// because they show the project is worked on: some publish nightlies and a stable release now and then.
	ReleasesLastYear    int
	PrereleasesLastYear int    // how many of those are prereleases
	MoreReleases        bool   // there may be more than ReleasesLastYear: only the newest few were read
	License             string // SPDX ID, or "" if none was detected
	// HasChecksum is true when the asset uas would install has a published SHA-256.
	HasChecksum bool
}

const day = 24 * time.Hour

// releaseCount says how many releases a repo had: "3 releases", or "8+ releases, 7 of them prereleases".
func releaseCount(f RepoFacts) string {
	s := plural(f.ReleasesLastYear, "release")
	if f.MoreReleases {
		s = fmt.Sprintf("%d+ releases", f.ReleasesLastYear)
	}
	switch p := f.PrereleasesLastYear; {
	case p == 0:
	case p == f.ReleasesLastYear && !f.MoreReleases && p == 1:
		s += ", a prerelease"
	case p == f.ReleasesLastYear:
		s += ", all prereleases"
	default:
		s += fmt.Sprintf(", %d of them prereleases", p)
	}
	return s
}

// GitHub scores a GitHub repo's releases.
func GitHub(f RepoFacts, now time.Time) catalog.Trust {
	var t catalog.Trust
	add := func(name, detail string, points, max int) {
		t.Signals = append(t.Signals, catalog.TrustSignal{Name: name, Detail: detail, Points: points, Max: max})
		t.Score += points
	}

	age := now.Sub(f.Created)
	switch {
	case age < 90*day:
		add("Project age", "under 3 months old", 0, 15)
	case age < 365*day:
		add("Project age", "under a year old", 5, 15)
	case age < 2*365*day:
		add("Project age", "1 to 2 years old", 10, 15)
	default:
		add("Project age", fmt.Sprintf("%d years old", int(age/(365*day))), 15, 15)
	}
	if age < 30*day {
		t.Flags = append(t.Flags, "New project: created less than 30 days ago")
	}

	// Logarithmic: 10 stars ≈ 6, 100 ≈ 12, 1,000 ≈ 19, 10,000+ = 25.
	add("Stars", plural(f.Stars, "star"), logPoints(f.Stars, 4, 25), 25)
	// 100,000+ downloads of the latest release = 10.
	add("Downloads", plural(f.Downloads, "download")+" of the latest release", logPoints(f.Downloads, 5, 10), 10)

	activity := releaseCount(f) + " in the past year"
	switch n := f.ReleasesLastYear; {
	case n >= 4:
		add("Release activity", activity, 15, 15)
	case n >= 2:
		add("Release activity", activity, 10, 15)
	case n == 1:
		add("Release activity", activity, 6, 15)
	default:
		add("Release activity", "no releases in the past year", 0, 15)
	}

	since := now.Sub(f.LastRelease)
	switch {
	case f.LastRelease.IsZero():
		add("Latest release", "no stable release", 0, 15)
	case since <= 183*day:
		add("Latest release", "within the past 6 months", 15, 15)
	case since <= 365*day:
		add("Latest release", "within the past year", 10, 15)
	case since <= 548*day:
		add("Latest release", "within the past 18 months", 5, 15)
	default:
		add("Latest release", "over 18 months ago", 0, 15)
	}
	if !f.LastRelease.IsZero() && since > 548*day {
		t.Flags = append(t.Flags, "No release in over 18 months")
	}

	switch f.License {
	case "", "NOASSERTION", "OTHER":
		add("License", "no recognized license", 0, 10)
	default:
		add("License", f.License, 10, 10)
	}

	if f.HasChecksum {
		add("Checksum", "download has a published SHA-256", 10, 10)
	} else {
		add("Checksum", "no published checksum", 0, 10)
		t.Flags = append(t.Flags, "No published checksum: the download cannot be verified")
	}
	return t
}

// logPoints maps n onto 0..max on a log10 scale that reaches max at 10^decades.
func logPoints(n, decades, max int) int {
	if n <= 0 {
		return 0
	}
	p := int(math.Round(float64(max) * math.Log10(float64(n)+1) / float64(decades)))
	return min(p, max)
}

func plural(n int, word string) string {
	if n == 1 {
		return "1 " + word
	}
	return commas(n) + " " + word + "s"
}

func commas(n int) string {
	s := fmt.Sprint(n)
	for i := len(s) - 3; i > 0; i -= 3 {
		s = s[:i] + "," + s[i:]
	}
	return s
}
