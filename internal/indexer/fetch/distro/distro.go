// Package distro turns a distribution's AppStream components into listings, for the distributions whose
// packages uas installs with their own package manager (Debian and Ubuntu with apt, Arch with pacman).
package distro

import (
	"regexp"
	"time"

	"github.com/tyler-builds/uas/internal/catalog"
	"github.com/tyler-builds/uas/internal/indexer/appstream"
)

// Package is a binary package in a repository.
type Package struct {
	Name    string
	Version string
	Built   time.Time // zero when the index doesn't say
}

// packageName matches package names uas will pass to a package manager.
var packageName = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._+-]*$`)

// Listing builds the listing of an AppStream component for a distribution release (distro, such as
// "ubuntu-24.04"), or reports false when the component isn't an app or its package isn't in pkgs.
func Listing(c *appstream.Component, src catalog.SourceID, distro string, pkgs map[string]Package) (
	catalog.Listing, bool) {
	var kind catalog.Kind
	switch c.Type {
	case "desktop-application", "desktop":
		kind = catalog.KindDesktopApp
	case "console-application":
		kind = catalog.KindConsoleApp
	default:
		return catalog.Listing{}, false
	}
	p, ok := pkgs[c.PkgName]
	if !ok || c.ID == "" || c.Name == "" || !packageName.MatchString(c.PkgName) {
		return catalog.Listing{}, false
	}
	l := catalog.Listing{
		Source:      src,
		Ref:         distro + "/" + p.Name,
		AppStreamID: c.ID,
		Kind:        kind,
		Name:        c.Name,
		Summary:     c.Summary,
		Description: c.Description,
		Developer:   c.Developer,
		License:     c.License,
		Homepage:    c.URLs["homepage"],
		VCSURL:      c.URLs["vcs-browser"],
		BugTracker:  c.URLs["bugtracker"],
		IconURL:     c.BestRemoteIcon(),
		Categories:  c.Categories,
		Keywords:    c.Keywords,
		Version:     p.Version,
		Released:    p.Built,
		Package:     p.Name,
		Distro:      distro,
	}
	if l.Released.IsZero() {
		if r, ok := c.Latest(); ok && r.Version != "" {
			l.Released = r.Time
		}
	}
	for _, s := range c.Screenshots {
		l.Screenshots = append(l.Screenshots, catalog.Screenshot{URL: s.Source, ThumbURL: s.Thumbnail,
			Caption: s.Caption})
	}
	return l, true
}
