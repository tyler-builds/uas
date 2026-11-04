package trust

import (
	"fmt"
	"time"

	"github.com/tyler-builds/uas/internal/catalog"
)

// Build is one COPR build, newest first in COPRFacts.
type Build struct {
	State     string // "succeeded", "failed", "canceled", "running", ...
	Ended     time.Time
	Submitter string
}

// COPRFacts are what a COPR project's score is computed from.
type COPRFacts struct {
	Owner       string // "@group" for group projects
	Homepage    string
	Contact     string
	Description string
	Builds      []Build // recent builds, newest first
}

// COPR scores a COPR project. Every COPR project is built and signed by COPR, but nobody reviews what goes
// into it, so the score looks at upkeep: recent successful builds, build health, activity and who maintains it.
func COPR(f COPRFacts, now time.Time) catalog.Trust {
	var t catalog.Trust
	add := func(name, detail string, points, max int) {
		t.Signals = append(t.Signals, catalog.TrustSignal{Name: name, Detail: detail, Points: points, Max: max})
		t.Score += points
	}

	var lastOK time.Time
	succeeded, failed, lastYear := 0, 0, 0
	submitters := map[string]bool{}
	for _, b := range f.Builds {
		switch b.State {
		case "succeeded":
			succeeded++
			if b.Ended.After(lastOK) {
				lastOK = b.Ended
			}
		case "failed":
			failed++
		}
		if !b.Ended.IsZero() && now.Sub(b.Ended) <= 365*day {
			lastYear++
		}
		if b.Submitter != "" {
			submitters[b.Submitter] = true
		}
	}

	since := now.Sub(lastOK)
	switch {
	case lastOK.IsZero():
		add("Latest successful build", "none in recent builds", 0, 25)
	case since <= 91*day:
		add("Latest successful build", "within the past 3 months", 25, 25)
	case since <= 183*day:
		add("Latest successful build", "within the past 6 months", 18, 25)
	case since <= 365*day:
		add("Latest successful build", "within the past year", 10, 25)
	default:
		add("Latest successful build", "over a year ago", 0, 25)
	}
	if lastOK.IsZero() || since > 365*day {
		t.Flags = append(t.Flags, "No successful build in the past year")
	}

	if done := succeeded + failed; done == 0 {
		add("Build health", "no finished builds", 0, 20)
	} else {
		rate := succeeded * 100 / done
		detail := fmt.Sprintf("%d%% of the last %d builds succeeded", rate, done)
		switch {
		case rate >= 90:
			add("Build health", detail, 20, 20)
		case rate >= 75:
			add("Build health", detail, 14, 20)
		case rate >= 50:
			add("Build health", detail, 8, 20)
		default:
			add("Build health", detail, 0, 20)
			t.Flags = append(t.Flags, "Most recent builds failed")
		}
	}

	switch {
	case lastYear >= 10:
		add("Activity", plural(lastYear, "build")+" in the past year", 15, 15)
	case lastYear >= 4:
		add("Activity", plural(lastYear, "build")+" in the past year", 10, 15)
	case lastYear >= 1:
		add("Activity", plural(lastYear, "build")+" in the past year", 5, 15)
	default:
		add("Activity", "no builds in the past year", 0, 15)
	}

	switch {
	case len(f.Owner) > 1 && f.Owner[0] == '@':
		add("Maintainers", "maintained by the "+f.Owner[1:]+" group", 15, 15)
	case len(submitters) >= 2:
		add("Maintainers", plural(len(submitters), "person")+" submitted recent builds", 15, 15)
	default:
		add("Maintainers", "a personal project of "+f.Owner, 5, 15)
	}

	if f.Homepage != "" || f.Contact != "" {
		add("Project details", "homepage or contact listed", 10, 10)
	} else {
		add("Project details", "no homepage or contact", 0, 10)
	}
	if len(f.Description) >= 20 {
		add("Description", "describes what it provides", 10, 10)
	} else {
		add("Description", "little or no description", 0, 10)
	}
	add("Signing", "packages signed with the project's COPR key", 5, 5)
	return t
}
