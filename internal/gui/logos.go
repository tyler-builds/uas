package gui

import (
	"embed"

	"github.com/tyler-builds/jamb/icons"
	"github.com/tyler-builds/jamb/ui"

	"github.com/tyler-builds/uas/internal/catalog"
)

// logoFiles are sources' official logos, byte for byte as their owners publish them (see logos/README.md).
// A logo is never tinted or recolored: it is drawn as an SVG image in its own colors, which high contrast
// leaves alone too.
//
//go:embed logos/*.svg
var logoFiles embed.FS

// sourceLogo is a source's official logo, with the owner's variant for dark backgrounds where it publishes one.
type sourceLogo struct{ light, dark *ui.SVGSource }

func logoFile(name string) *ui.SVGSource {
	b, err := logoFiles.ReadFile("logos/" + name)
	if err != nil {
		panic(err) // embedded at build time
	}
	return ui.MustSVG(b)
}

// sourceLogos are the sources whose official logo uas shows; logos/README.md has the policy behind each.
var sourceLogos = map[catalog.SourceID]sourceLogo{
	catalog.SourceDebian:   {light: logoFile("debian.svg")},
	catalog.SourceGitHub:   {light: logoFile("github-black.svg"), dark: logoFile("github-white.svg")},
	catalog.SourceCodeberg: {light: logoFile("codeberg-blue.svg"), dark: logoFile("codeberg-white.svg")},
}

// sourceGlyphs are neutral icons, drawn in the source's color, for the sources whose logo uas doesn't show:
// those whose owner's policy doesn't clearly allow it here, and COPR and AppImage, which have none.
var sourceGlyphs = map[catalog.SourceID]*icons.Icon{
	catalog.SourceFlathub:  icons.Store,
	catalog.SourceFedora:   icons.Package,
	catalog.SourceHomebrew: icons.Terminal,
	catalog.SourceCOPR:     icons.Hammer,
	catalog.SourceUbuntu:   icons.Package,
	catalog.SourceArch:     icons.Package,
	catalog.SourceSnap:     icons.Box,
	catalog.SourceAppImage: icons.FileBox,
}

// sourceMark is s's mark at size: its official logo in its own colors, or its neutral icon in color c.
func sourceMark(ctx *ui.Context, s catalog.SourceID, size float32, c ui.Color) ui.Element {
	if l, ok := sourceLogos[s]; ok {
		src := l.light
		if ctx.Theme().Dark && l.dark != nil {
			src = l.dark
		}
		return ui.SVG(src).Size(ui.Px(size), ui.Px(size))
	}
	return ui.Icon(sourceGlyphs[s]).Size(size).Color(c)
}
