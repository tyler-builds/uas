package session

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/tyler-builds/uas/internal/catalog"
	"github.com/tyler-builds/uas/internal/client/fromurl"
)

// links is the session's lookup of apps by link, made on first use.
func (s *Session) links() *fromurl.Resolver {
	s.linksOnce.Do(func() {
		if s.Links != nil {
			return
		}
		dir := ""
		if c, err := os.UserCacheDir(); err == nil {
			dir = filepath.Join(c, "uas", "live")
		}
		s.Links = &fromurl.Resolver{Arch: s.Platform.Arch, CacheDir: dir}
	})
	return s.Links
}

// AppForLink returns the app a link points at: the catalog's app when the catalog has the repo (by its
// release listing or its source code link), else the app read from the repo's releases just now
// (catalog.App.Live), which the catalog leaves out, often for being new or small.
func (s *Session) AppForLink(ctx context.Context, db *catalog.DB, link fromurl.Link) (*catalog.App, error) {
	app, err := db.ByRepo(ctx, link.Source, link.Repo)
	if err == nil || !errors.Is(err, catalog.ErrNotFound) {
		return app, err
	}
	l, err := s.links().Listing(ctx, link, true)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", strings.TrimPrefix(link.URL(), "https://"), err)
	}
	if !strings.EqualFold(l.Ref, link.Ref()) {
		// The repo was renamed or moved: the catalog may have it under its new name.
		if app, err := db.ByRepo(ctx, l.Source, l.Source.Repo(l.Ref)); err == nil {
			return app, nil
		}
	}
	return catalog.LiveApp(l), nil
}

// ResolveApp finds an app by what a person or a page names it with: an app ID, ref or package name the
// catalog knows (see catalog.DB.Resolve), a link to the app's repo (see AppForLink), or the ID of an app
// read from its repo ("github:owner/name", see catalog.LiveApp).
func (s *Session) ResolveApp(ctx context.Context, db *catalog.DB, id string) (*catalog.App, error) {
	if fromurl.IsLink(id) {
		link, err := fromurl.Parse(id)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", id, err)
		}
		return s.AppForLink(ctx, db, link)
	}
	app, err := db.Resolve(ctx, id)
	if err == nil || !errors.Is(err, catalog.ErrNotFound) {
		return app, err
	}
	if link, ok := liveLink(id); ok {
		return s.AppForLink(ctx, db, link)
	}
	return nil, err
}

// liveLink reads the ID of an app from one release listing as a link: "github:owner/name",
// "codeberg:codeberg.org/owner/name".
func liveLink(id string) (fromurl.Link, bool) {
	s, ref, ok := strings.Cut(id, ":")
	src := catalog.SourceID(s)
	if !ok || !src.Releases() {
		return fromurl.Link{}, false
	}
	repo := src.Repo(ref)
	link, err := fromurl.Parse("https://" + src.Host() + "/" + repo)
	if err != nil || link.Ref() != ref {
		return fromurl.Link{}, false
	}
	return link, true
}

// liveListing reads a release source's repo now, for updates and restores of apps installed from a link.
func (s *Session) liveListing(ctx context.Context, src catalog.SourceID, ref string) (catalog.Listing, error) {
	return s.links().Listing(ctx, fromurl.Link{Source: src, Repo: src.Repo(ref)}, false)
}

// Unavailable says why this system can't install a listing read from a link (see catalog.Listing.Live), or
// "" when it can. Catalog listings that can't be installed are left out of an app's page instead.
func (s *Session) Unavailable(l catalog.Listing) string {
	if s.Available(l) {
		return ""
	}
	release := "Its latest release"
	if l.Version != "" {
		release += ", " + l.Version + ","
	}
	arch := s.Platform.Arch
	if arch == "" {
		arch = "this computer"
	}
	var appImage, bundle, rpm *catalog.Asset
	for i, a := range l.Assets {
		switch a.Kind {
		case catalog.AssetAppImage:
			appImage = &l.Assets[i]
		case catalog.AssetFlatpak:
			bundle = &l.Assets[i]
		case catalog.AssetRPM:
			rpm = &l.Assets[i]
		}
	}
	switch {
	case appImage != nil && appImage.SHA256 == "":
		return fmt.Sprintf("%s has an AppImage (%s), but %s publishes no checksum for it, so uas can't check the "+
			"download and won't install it.", release, appImage.Name, l.Source.Name())
	case bundle != nil && bundle.SHA256 == "":
		return fmt.Sprintf("%s has a Flatpak bundle (%s), but %s publishes no checksum for it, so uas can't "+
			"check the download and won't install it.", release, bundle.Name, l.Source.Name())
	case bundle != nil && s.Platform.Flatpak == "":
		return fmt.Sprintf("%s has only a Flatpak bundle (%s), and Flatpak isn't installed.", release, bundle.Name)
	case rpm != nil:
		return fmt.Sprintf("%s has no AppImage or Flatpak bundle for %s, which is what uas installs from a "+
			"project's releases. It ships system packages (%s); uas installs packages only from your "+
			"system's own repos and COPR, never from a project's releases.", release, arch, rpm.Name)
	}
	return fmt.Sprintf("%s has no AppImage or Flatpak bundle for %s, which is what uas installs from a "+
		"project's releases.", release, arch)
}

// linkNotes are the install notes of a listing read from a link.
func linkNotes(l catalog.Listing) []string {
	where := l.Source.Host() + "/" + l.Source.Repo(l.Ref)
	return []string{
		"Not in uas's catalog: uas read it from " + where + " just now, because you gave its link. " +
			"Nobody has reviewed it; check its trust score first.",
		"uas installs only its release file, checked against the checksum " + l.Source.Name() +
			" publishes, and never runs scripts from the project.",
	}
}
