package gui

import (
	"embed"
	"sync"

	"github.com/tyler-builds/jamb/ui"

	"github.com/tyler-builds/uas/internal/catalog"
)

// logoFiles are the sources' own marks, from Simple Icons (see logos/README.md).
//
//go:embed logos/*.svg
var logoFiles embed.FS

var logoNames = map[catalog.SourceID]string{
	catalog.SourceFlathub:  "flathub",
	catalog.SourceFedora:   "fedora",
	catalog.SourceHomebrew: "homebrew",
	catalog.SourceGitHub:   "github",
}

var (
	logosOnce sync.Once
	logos     map[catalog.SourceID]*ui.SVGSource
)

// logo returns a source's parsed logo, or nil for a source without one.
func logo(s catalog.SourceID) *ui.SVGSource {
	logosOnce.Do(func() {
		logos = map[catalog.SourceID]*ui.SVGSource{}
		for id, name := range logoNames {
			b, err := logoFiles.ReadFile("logos/" + name + ".svg")
			if err != nil {
				continue
			}
			if src, err := ui.SVGFromBytes(b); err == nil {
				logos[id] = src
			}
		}
	})
	return logos[s]
}

// sourceMark is a source's logo at size pixels in its color, or its icon for a source without a logo.
func sourceMark(ctx *ui.Context, s catalog.SourceID, size float32) ui.Element {
	c := sourceColor(ctx, s)
	if src := logo(s); src != nil {
		return ui.SVG(src).Size(ui.Px(size), ui.Px(size)).Color(c).Alt(s.Name())
	}
	return ui.Icon(sourceIcons[s]).Size(size).Color(c)
}
