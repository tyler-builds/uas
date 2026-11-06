package gui

import (
	"testing"

	"github.com/tyler-builds/uas/internal/catalog"
)

func TestNewerChanges(t *testing.T) {
	cs := []catalog.Change{{Version: "1.3"}, {Version: "1.2.1"}, {Version: "1.2"}, {Version: "1.1"}}
	for _, tc := range []struct {
		installed string
		want      int
	}{
		{"1.2", 2},
		{"v1.2", 2},
		{"1:1.2-1.fc45", 2},   // a Fedora package
		{"1.2.1-1ubuntu2", 1}, // a Debian package, not 1.2
		{"1.3", 0},
		{"0.9", 1}, // older than the notes go: the newest only
		{"", 1},
	} {
		if got := newerChanges(cs, tc.installed); len(got) != tc.want {
			t.Errorf("newerChanges(%q) = %v, want %d", tc.installed, got, tc.want)
		}
	}
}
