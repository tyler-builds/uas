package gui

import (
	"io/fs"
	"strings"
	"testing"
)

// Every source with a color has a mark: an official logo or a neutral icon, not both.
func TestSourceMarks(t *testing.T) {
	for s := range sourceTints {
		_, isLogo := sourceLogos[s]
		_, isGlyph := sourceGlyphs[s]
		if isLogo == isGlyph {
			t.Errorf("%s: logo %v, neutral icon %v; want exactly one", s, isLogo, isGlyph)
		}
	}
}

// Logos are drawn in their own colors. currentColor would let a theme or high contrast color one, so no logo
// file may use it.
func TestLogosKeepTheirColors(t *testing.T) {
	files, err := fs.Glob(logoFiles, "logos/*.svg")
	if err != nil || len(files) == 0 {
		t.Fatalf("no logos: %v", err)
	}
	for _, f := range files {
		b, err := logoFiles.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(strings.ToLower(string(b)), "currentcolor") {
			t.Errorf("%s uses currentColor", f)
		}
	}
}
