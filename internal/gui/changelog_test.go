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

func TestUpdateLabel(t *testing.T) {
	for _, tc := range []struct{ installed, available, want string }{
		{"1.0", "1.1", "Update to 1.1"},
		{"1.0", "1.0", "Update (new build)"},
		{"1.0", "", "Update"},
		{"1.0", "2026.11.06-nightly", "Update"},
	} {
		if got, _ := updateLabel(tc.installed, tc.available); got != tc.want {
			t.Errorf("updateLabel(%q, %q) = %q, want %q", tc.installed, tc.available, got, tc.want)
		}
	}
}

func TestReleaseVersion(t *testing.T) {
	for tag, want := range map[string]string{
		"5.6.2":           "5.6.2",
		"v5.6.2":          "5.6.2",
		"release-5.6.2":   "5.6.2",
		"darktable-5.6.2": "5.6.2",
		"app-v1.2":        "1.2",
		"Release_1.0":     "1.0",
		"nightly":         "nightly",
		"vim9":            "vim9", // no separator: the name is part of the tag
		"2026.11.06":      "2026.11.06",
		"1:1.2-1.fc45":    "1:1.2-1.fc45",
		"build#12":        "build#12",
	} {
		if got := releaseVersion(tag); got != want {
			t.Errorf("releaseVersion(%q) = %q, want %q", tag, got, want)
		}
	}
	cs := []catalog.Change{{Version: "release-5.6.2"}, {Version: "release-5.6.1"}}
	if got := newerChanges(cs, "5.6.1"); len(got) != 1 {
		t.Errorf("newerChanges with tags = %v, want the newest", got)
	}
}
