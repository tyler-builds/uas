package trust

import (
	"slices"
	"testing"
	"time"
)

func TestPling(t *testing.T) {
	now := time.Date(2026, 11, 15, 0, 0, 0, 0, time.UTC)
	good := Pling(PlingFacts{Created: now.AddDate(-6, 0, 0), Changed: now.AddDate(0, -1, 0), Downloads: 3_000_000,
		Score: 96}, now)
	if good.Score != 15+25+20+15+5 || len(good.Flags) != 0 {
		t.Errorf("good = %+v", good)
	}
	max := 0
	for _, s := range good.Signals {
		max += s.Max
	}
	if max != 100 {
		t.Errorf("maximum = %d", max)
	}
	// The best Pling item still scores below what a GitHub project can.
	if good.Score > 85 {
		t.Errorf("score %d", good.Score)
	}
	unrated := Pling(PlingFacts{Created: now.AddDate(-5, 0, 0), Changed: now.AddDate(-4, 0, 0), Downloads: 12000,
		Score: 50}, now)
	if !slices.Contains(unrated.Flags, "No ratings yet") || len(unrated.Flags) != 2 {
		t.Errorf("flags = %v", unrated.Flags)
	}
	for _, s := range unrated.Signals {
		if s.Name == "Rating" && s.Points != 0 {
			t.Errorf("unrated got %d rating points", s.Points)
		}
	}
}
