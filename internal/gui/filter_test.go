package gui

import (
	"testing"

	"github.com/tyler-builds/uas/internal/catalog"
	"github.com/tyler-builds/uas/internal/client/backend"
)

func TestMatchesFilter(t *testing.T) {
	boxes := backend.InstalledApp{Source: catalog.SourceFlathub, Name: "Boxes", AppID: "org.gnome.Boxes"}
	for q, want := range map[string]bool{
		"":              true,
		"box":           true,
		"gnome boxes":   true,
		"flathub":       true,
		"boxes firefox": false,
		"homebrew":      false,
	} {
		if got := matchesFilter(boxes, q); got != want {
			t.Errorf("matchesFilter(%q) = %v, want %v", q, got, want)
		}
	}
}
