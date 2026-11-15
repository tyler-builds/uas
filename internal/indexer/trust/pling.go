package trust

import (
	"fmt"
	"time"

	"github.com/tyler-builds/uas/internal/catalog"
)

// PlingFacts are what a Pling item's score is computed from.
type PlingFacts struct {
	Created   time.Time
	Changed   time.Time // when the item was last changed
	Downloads int       // over all time; Pling's counts are rough
	// Score is Pling's rating, 0 to 100. Pling gives an item nobody rated exactly 50.
	Score int
}

// Pling scores an icon or cursor theme from the Pling store. Pling verifies no publisher and signs nothing,
// so the score can't reach what a well-kept GitHub project does: 15 of the 100 points (a verified publisher)
// are never given, and the checksum earns half, since uas records it rather than the author publishing one.
func Pling(f PlingFacts, now time.Time) catalog.Trust {
	var t catalog.Trust
	add := func(name, detail string, points, max int) {
		t.Signals = append(t.Signals, catalog.TrustSignal{Name: name, Detail: detail, Points: points, Max: max})
		t.Score += points
	}

	age := now.Sub(f.Created)
	switch {
	case f.Created.IsZero():
		add("Age", "unknown", 0, 15)
	case age < 90*day:
		add("Age", "on Pling for under 3 months", 0, 15)
	case age < 365*day:
		add("Age", "on Pling for under a year", 5, 15)
	case age < 2*365*day:
		add("Age", "on Pling for 1 to 2 years", 10, 15)
	default:
		add("Age", fmt.Sprintf("on Pling for %d years", int(age/(365*day))), 15, 15)
	}

	// Logarithmic: 1,000 ≈ 12, 10,000 ≈ 17, 100,000 ≈ 21, 1,000,000+ = 25.
	add("Downloads", plural(f.Downloads, "download")+" (Pling's count)", logPoints(f.Downloads, 6, 25), 25)

	l := catalog.Listing{Score: f.Score}
	switch {
	case !l.Rated():
		add("Rating", "not rated yet", 0, 20)
		t.Flags = append(t.Flags, "No ratings yet")
	case f.Score >= 80:
		add("Rating", fmt.Sprintf("%d of 100 on Pling", f.Score), 20, 20)
	case f.Score >= 70:
		add("Rating", fmt.Sprintf("%d of 100 on Pling", f.Score), 14, 20)
	case f.Score >= 60:
		add("Rating", fmt.Sprintf("%d of 100 on Pling", f.Score), 8, 20)
	default:
		add("Rating", fmt.Sprintf("%d of 100 on Pling", f.Score), 0, 20)
	}

	since := now.Sub(f.Changed)
	switch {
	case f.Changed.IsZero():
		add("Last change", "unknown", 0, 15)
	case since <= 365*day:
		add("Last change", "within the past year", 15, 15)
	case since <= 2*365*day:
		add("Last change", "within the past 2 years", 10, 15)
	case since <= 3*365*day:
		add("Last change", "within the past 3 years", 5, 15)
	default:
		add("Last change", "over 3 years ago", 0, 15)
		t.Flags = append(t.Flags, "Not changed in over 3 years: it may not cover newer apps")
	}

	add("Checksum", "uas recorded the file's SHA-256 when it built the catalog; Pling publishes only an MD5", 5, 10)
	add("Verified publisher", "Pling doesn't verify who uploads or sign files", 0, 15)
	return t
}
