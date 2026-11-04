package trust

import (
	"slices"
	"strings"
	"testing"
	"time"
)

var now = time.Date(2026, 11, 4, 0, 0, 0, 0, time.UTC)

func TestEstablishedProject(t *testing.T) {
	tr := GitHub(RepoFacts{
		Created: now.AddDate(-5, 0, 0), Stars: 12000, Downloads: 250000, LastRelease: now.AddDate(0, -1, 0),
		ReleasesLastYear: 8, License: "GPL-3.0", HasChecksum: true,
	}, now)
	if tr.Score != 100 || len(tr.Flags) != 0 {
		t.Errorf("score = %d, flags = %q", tr.Score, tr.Flags)
	}
	max := 0
	for _, s := range tr.Signals {
		max += s.Max
	}
	if max != 100 {
		t.Errorf("signal maximums add up to %d, want 100", max)
	}
}

func TestRiskyProject(t *testing.T) {
	tr := GitHub(RepoFacts{Created: now.AddDate(0, 0, -10), Stars: 3, LastRelease: now.AddDate(0, 0, -2),
		ReleasesLastYear: 1}, now)
	if tr.Score > 30 {
		t.Errorf("score = %d, want a low score", tr.Score)
	}
	if !slices.ContainsFunc(tr.Flags, func(f string) bool { return strings.HasPrefix(f, "New project") }) ||
		!slices.ContainsFunc(tr.Flags, func(f string) bool { return strings.HasPrefix(f, "No published checksum") }) {
		t.Errorf("flags = %q", tr.Flags)
	}
}

func TestStaleProject(t *testing.T) {
	tr := GitHub(RepoFacts{Created: now.AddDate(-6, 0, 0), Stars: 900, LastRelease: now.AddDate(-2, 0, 0),
		License: "MIT", HasChecksum: true}, now)
	if !slices.Contains(tr.Flags, "No release in over 18 months") {
		t.Errorf("flags = %q", tr.Flags)
	}
}

func TestHelpers(t *testing.T) {
	if got := plural(1234567, "star"); got != "1,234,567 stars" {
		t.Errorf("plural = %q", got)
	}
	for _, c := range []struct{ n, want int }{{0, 0}, {9, 6}, {99, 13}, {999, 19}, {9999, 25}, {1 << 30, 25}} {
		if got := logPoints(c.n, 4, 25); got != c.want {
			t.Errorf("logPoints(%d) = %d, want %d", c.n, got, c.want)
		}
	}
}

func TestReleaseCount(t *testing.T) {
	for _, c := range []struct {
		f    RepoFacts
		want string
	}{
		{RepoFacts{ReleasesLastYear: 3}, "3 releases"},
		{RepoFacts{ReleasesLastYear: 8, PrereleasesLastYear: 7, MoreReleases: true}, "8+ releases, 7 of them prereleases"},
		{RepoFacts{ReleasesLastYear: 8, PrereleasesLastYear: 8, MoreReleases: true}, "8+ releases, all prereleases"},
		{RepoFacts{ReleasesLastYear: 1, PrereleasesLastYear: 1}, "1 release, a prerelease"},
	} {
		if got := releaseCount(c.f); got != c.want {
			t.Errorf("releaseCount(%+v) = %q, want %q", c.f, got, c.want)
		}
	}
}
