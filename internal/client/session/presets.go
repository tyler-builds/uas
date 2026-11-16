package session

import (
	"context"
	"errors"

	"github.com/tyler-builds/uas/internal/catalog"
	"github.com/tyler-builds/uas/internal/client/appearance"
	"github.com/tyler-builds/uas/internal/client/backend"
	"github.com/tyler-builds/uas/internal/client/state"
)

// errNoPresets is returned when presets can't be kept: the home folder or the config folder wasn't found.
var errNoPresets = errors.New("presets can't be kept: uas couldn't find your home folder")

// PlanPreset decides what applying a preset does with each of its parts. Fonts and themes that aren't on the
// system are looked up in the catalog, to be installed first.
func (s *Session) PlanPreset(ctx context.Context, p appearance.Preset) ([]appearance.PlanItem, error) {
	if s.Looks == nil || s.Presets == nil {
		return nil, errNoPresets
	}
	var lookup appearance.Lookup
	if db, err := catalog.Open(s.CatalogPath); err == nil {
		defer db.Close()
		lookup = func(id string) (catalog.Listing, bool) {
			app, err := db.App(ctx, id)
			if err != nil || !app.Kind.Appearance() || len(app.Listings) == 0 {
				return catalog.Listing{}, false
			}
			return app.Listings[0], true
		}
	}
	return s.Looks.PlanPreset(ctx, p, lookup)
}

// ApplyPreset applies a preset by its plan (see appearance.Installer.ApplyPreset), installing what it needs
// first unless noInstall is set.
func (s *Session) ApplyPreset(ctx context.Context, p appearance.Preset, plan []appearance.PlanItem, noInstall bool,
	progress func(backend.Progress)) ([]appearance.Outcome, error) {
	if s.Looks == nil || s.Presets == nil {
		return nil, errNoPresets
	}
	return s.Looks.ApplyPreset(ctx, s.Presets, p, plan, appearance.ApplyOptions{NoInstall: noInstall,
		Progress: progress,
		Install: func(ctx context.Context, l catalog.Listing, variant string,
			progress func(backend.Progress)) (state.AppearanceInstall, error) {
			rec, err := s.Looks.Install(ctx, l, variant, progress)
			s.emit("install", l.Source, err)
			return rec, err
		}})
}

// UndoPreset goes back to the look from before the last apply of a preset.
func (s *Session) UndoPreset(ctx context.Context) (appearance.Undo, []appearance.Outcome, error) {
	if s.Looks == nil || s.Presets == nil {
		return appearance.Undo{}, nil, errNoPresets
	}
	return s.Looks.UndoPreset(ctx, s.Presets)
}
