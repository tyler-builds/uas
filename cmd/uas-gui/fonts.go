package main

import (
	"os"
	"os/exec"
	"strings"

	"github.com/tyler-builds/jamb/app"
	"github.com/tyler-builds/jamb/ui"
	"golang.org/x/image/font/sfnt"
)

// useSystemFonts registers the desktop's own sans and mono fonts, found with fontconfig.
//
// jamb looks for Linux fonts only at Debian-style paths (/usr/share/fonts/truetype/...), which Fedora and
// Bazzite don't use, so without this every label falls back to the embedded Go fonts. Registering a face
// under the built-in family names ("" and "mono") replaces jamb's choice. See jamb-notes.md.
func useSystemFonts(a *app.App) *app.App {
	if _, err := exec.LookPath("fc-match"); err != nil {
		return a
	}
	faces := []struct {
		family  string
		pattern string
		weight  ui.FontWeight
	}{
		{"", "sans-serif", ui.FontRegular},
		{"", "sans-serif:medium", ui.FontMedium},
		{"", "sans-serif:semibold", ui.FontSemibold},
		{"", "sans-serif:bold", ui.FontBold},
		{"mono", "monospace", ui.FontRegular},
	}
	for _, f := range faces {
		out, err := exec.Command("fc-match", "-f", "%{file}", f.pattern).Output()
		if err != nil {
			continue
		}
		path := strings.TrimSpace(string(out))
		// Only single-face TrueType or OpenType files; a collection or a file jamb can't parse would make
		// Run exit, so check that it parses first.
		if !strings.HasSuffix(path, ".ttf") && !strings.HasSuffix(path, ".otf") {
			continue
		}
		data, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		if _, err := sfnt.Parse(data); err != nil {
			continue
		}
		a = a.Font(f.family, f.weight, data)
	}
	return a
}
