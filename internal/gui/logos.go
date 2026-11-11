package gui

import (
	"embed"

	"github.com/tyler-builds/jamb/icons"

	"github.com/tyler-builds/uas/internal/catalog"
)

// logoFiles are the sources' own marks, from Simple Icons (see logos/README.md). They become icons, tinted
// like any other.
//
//go:embed logos/*.svg
var logoFiles embed.FS

func logoIcon(name string) *icons.Icon {
	b, err := logoFiles.ReadFile("logos/" + name + ".svg")
	if err != nil {
		panic(err) // embedded at build time
	}
	return icons.MustNew("uas-"+name, string(b))
}

// sourceIcons give each source its mark: its own logo, or a neutral icon where there is none to use: a
// package for Fedora (its trademark guidelines don't cover the logo in other software), a hammer for COPR
// (packages built by the project owner).
var sourceIcons = map[catalog.SourceID]*icons.Icon{
	catalog.SourceFlathub:  logoIcon("flathub"),
	catalog.SourceFedora:   icons.Package,
	catalog.SourceHomebrew: logoIcon("homebrew"),
	catalog.SourceCOPR:     icons.Hammer,
	catalog.SourceGitHub:   logoIcon("github"),
	catalog.SourceCodeberg: logoIcon("codeberg"),
	catalog.SourceDebian:   logoIcon("debian"),
	catalog.SourceUbuntu:   logoIcon("ubuntu"),
	catalog.SourceArch:     logoIcon("archlinux"),
	catalog.SourceSnap:     logoIcon("snapcraft"),
	catalog.SourceAppImage: icons.FileBox,
}
