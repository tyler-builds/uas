package session

import (
	"context"
	"errors"
	"fmt"

	"github.com/tyler-builds/uas/internal/catalog"
	"github.com/tyler-builds/uas/internal/client/appearance"
	"github.com/tyler-builds/uas/internal/client/backend"
	"github.com/tyler-builds/uas/internal/client/state"
)

// errNoLooks is returned when fonts and themes can't be installed: the home folder wasn't found.
var errNoLooks = errors.New("fonts and themes can't be installed: uas couldn't find your home folder")

// InstallLook installs a font or theme listing for the user, from the file that holds variant for a theme
// (the first file when it is empty), and then, when use is set, uses it: a theme as the desktop's icon,
// cursor or GTK theme (the variant, or its first theme), a font for that font setting.
func (s *Session) InstallLook(ctx context.Context, l catalog.Listing, variant, use string,
	progress func(backend.Progress)) (state.AppearanceInstall, error) {
	if s.Looks == nil {
		return state.AppearanceInstall{}, errNoLooks
	}
	rec, err := s.Looks.Install(ctx, l, variant, progress)
	s.emit("install", l.Source, err)
	if err != nil || use == "" {
		return rec, err
	}
	value := variant
	switch {
	case l.Kind == catalog.KindFont:
		if value == "" && len(rec.Families) > 0 {
			value = rec.Families[0]
		}
	case value == "" && len(rec.Themes) > 0:
		value = rec.Themes[0]
	}
	if err := s.Looks.Use(ctx, use, value); err != nil {
		return rec, fmt.Errorf("installed, but could not use it: %w", err)
	}
	return rec, nil
}

// RemoveLook removes a font or theme uas installed, by catalog ID.
func (s *Session) RemoveLook(ctx context.Context, id string, progress func(backend.Progress)) error {
	if s.Looks == nil {
		return errNoLooks
	}
	src := catalog.SourceID("")
	if recs, err := s.Looks.Records(); err == nil {
		for _, r := range recs {
			if r.ID == id {
				src = catalog.SourceID(r.Source)
			}
		}
	}
	err := s.Looks.Remove(ctx, id, progress)
	s.emit("remove", src, err)
	return err
}

// LookUpdates returns the fonts and themes uas installed whose catalog listing has a newer file. Themes and
// fonts uas didn't install are never offered one.
func (s *Session) LookUpdates(ctx context.Context) ([]appearance.Update, error) {
	if s.Looks == nil {
		return nil, nil
	}
	recs, err := s.Looks.Records()
	if err != nil || len(recs) == 0 {
		return nil, err
	}
	db, err := catalog.Open(s.CatalogPath)
	if err != nil {
		return nil, err
	}
	defer db.Close()
	return appearance.Updates(recs, func(id string) (catalog.Listing, bool) {
		app, err := db.App(ctx, id)
		if err != nil || len(app.Listings) == 0 {
			return catalog.Listing{}, false
		}
		return app.Listings[0], true
	}), nil
}

// UpdateLook installs a font's or theme's update, the same variant as before.
func (s *Session) UpdateLook(ctx context.Context, u appearance.Update, progress func(backend.Progress)) error {
	if s.Looks == nil {
		return errNoLooks
	}
	_, err := s.Looks.Install(ctx, u.Listing, u.Variant(), progress)
	s.emit("update", u.Listing.Source, err)
	return err
}

// LookNotes are short notes to show before installing a font or theme listing.
func (s *Session) LookNotes(l catalog.Listing) []string {
	switch l.Source {
	case catalog.SourcePling:
		dir := "~/.local/share/icons"
		if l.Kind == catalog.KindGTKTheme {
			dir = "~/.local/share/themes"
		}
		notes := []string{"Downloads from Pling (gnome-look.org) and installs into " + dir + " for your " +
			"account. Anyone can upload to Pling and nobody reviews it; uas checks the file against the checksum " +
			"recorded when the catalog was built, and installs only images and theme files from it."}
		most := 0
		for _, a := range l.Assets {
			most = max(most, a.LeftOut)
		}
		if most > 0 {
			notes = append(notes, fmt.Sprintf("Its download also holds files that could be run, such as a helper "+
				"script (%d at most); uas leaves them out and installs the rest of the theme.", most))
		}
		return notes
	case catalog.SourceGoogleFonts:
		return []string{"Downloads the font files from Google Fonts' repository on GitHub and installs them into " +
			"~/.local/share/fonts for your account."}
	case catalog.SourceNerdFonts:
		return []string{"Downloads the fonts from the Nerd Fonts releases on GitHub, checked against GitHub's " +
			"checksum, and installs them into ~/.local/share/fonts for your account."}
	}
	return nil
}
