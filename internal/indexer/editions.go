package indexer

import (
	"context"
	"log/slog"
	"path/filepath"
	"sort"

	"github.com/tyler-builds/uas/internal/catalog"
	"github.com/tyler-builds/uas/internal/manifest"
)

// An Edition is the catalog for one kind of system: the sources that work everywhere (Flathub, Homebrew and
// release downloads), plus the ones that system can install from. Clients download only their own edition, so
// a Bazzite user doesn't download Ubuntu's packages, and an Ubuntu user only the release they run.
type Edition struct {
	Name string
	keep func(catalog.Listing) bool
}

// Keep reports whether the edition lists l.
func (e Edition) Keep(l catalog.Listing) bool { return e.keep(l) }

// everywhere reports whether a source works on any system.
func everywhere(s catalog.SourceID) bool {
	return s == catalog.SourceFlathub || s == catalog.SourceHomebrew || s.Releases()
}

// Editions returns the editions for a run's listings: fedora-atomic (layering and COPR, no snapd), fedora (dnf
// and COPR),
// one per Debian and Ubuntu release found, arch, and other. All but fedora-atomic include snaps.
func Editions(all []catalog.Listing) []Edition {
	with := func(name string, also func(catalog.Listing) bool) Edition {
		return Edition{Name: name, keep: func(l catalog.Listing) bool { return everywhere(l.Source) || also(l) }}
	}
	snap := func(l catalog.Listing) bool { return l.Source == catalog.SourceSnap }
	eds := []Edition{
		with(manifest.EditionFedoraAtomic, func(l catalog.Listing) bool {
			return l.Source == catalog.SourceFedora || l.Source == catalog.SourceCOPR
		}),
		with(manifest.EditionFedora, func(l catalog.Listing) bool {
			return l.Source == catalog.SourceFedora || l.Source == catalog.SourceCOPR || snap(l)
		}),
		with(manifest.EditionArch, func(l catalog.Listing) bool { return l.Source == catalog.SourceArch || snap(l) }),
		with(manifest.EditionOther, snap),
	}
	releases := map[string]bool{}
	for _, l := range all {
		if (l.Source == catalog.SourceDebian || l.Source == catalog.SourceUbuntu) && l.Distro != "" {
			releases[l.Distro] = true
		}
	}
	var names []string
	for d := range releases {
		names = append(names, d)
	}
	sort.Strings(names)
	for _, d := range names {
		eds = append(eds, with(d, func(l catalog.Listing) bool { return l.Distro == d || snap(l) }))
	}
	return eds
}

// Built is one edition's catalog file.
type Built struct {
	Path string
	Info catalog.BuildInfo
}

// WriteEditions writes every edition's catalog into dir, as <edition>.db.
func (f *Fetched) WriteEditions(ctx context.Context, dir string, log *slog.Logger) (map[string]Built, error) {
	out := map[string]Built{}
	for _, e := range Editions(f.Listings) {
		path := filepath.Join(dir, e.Name+".db")
		info, err := f.Write(ctx, path, e.Keep, log.With("edition", e.Name))
		if err != nil {
			return nil, err
		}
		log.Info("edition written", "edition", e.Name, "apps", info.Apps)
		out[e.Name] = Built{Path: path, Info: info}
	}
	return out, nil
}
