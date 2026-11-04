package trust

import (
	"slices"
	"testing"
)

func TestCOPRHealthyGroupProject(t *testing.T) {
	var builds []Build
	for i := 0; i < 12; i++ {
		builds = append(builds, Build{State: "succeeded", Ended: now.AddDate(0, -i, 0), Submitter: "alice"})
	}
	tr := COPR(COPRFacts{Owner: "@kde", Homepage: "https://example.org", Description: "Newer KDE apps for Fedora",
		Builds: builds}, now)
	if tr.Score != 100 || len(tr.Flags) != 0 {
		t.Errorf("score = %d, flags = %q", tr.Score, tr.Flags)
	}
	max := 0
	for _, s := range tr.Signals {
		max += s.Max
	}
	if max != 100 {
		t.Errorf("maximums add up to %d", max)
	}
}

func TestCOPRNeglectedProject(t *testing.T) {
	tr := COPR(COPRFacts{Owner: "someone", Builds: []Build{
		{State: "failed", Ended: now.AddDate(-2, 0, 0)},
		{State: "failed", Ended: now.AddDate(-2, 0, 0)},
		{State: "succeeded", Ended: now.AddDate(-3, 0, 0)},
	}}, now)
	if tr.Score > 20 {
		t.Errorf("score = %d", tr.Score)
	}
	for _, want := range []string{"No successful build in the past year", "Most recent builds failed"} {
		if !slices.Contains(tr.Flags, want) {
			t.Errorf("missing flag %q in %q", want, tr.Flags)
		}
	}
}
