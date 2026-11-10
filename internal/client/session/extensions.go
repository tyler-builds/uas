package session

import (
	"context"
	"errors"
	"fmt"
	"slices"

	"github.com/tyler-builds/uas/internal/catalog"
	"github.com/tyler-builds/uas/internal/client/shellext"
)

// ExtensionUpdates returns the GNOME Shell extensions with an update waiting: the Shell has downloaded it
// and installs it the next time the user logs in. There are none outside a GNOME session.
func (s *Session) ExtensionUpdates(ctx context.Context) ([]shellext.Extension, error) {
	if s.Extensions == nil {
		return nil, nil
	}
	return s.Extensions.Updates(ctx)
}

// ExportedExtension is one GNOME Shell extension of an export: one the user added, or one that came with
// the system and that the user switched on.
type ExportedExtension struct {
	UUID    string `json:"uuid"`
	Name    string `json:"name"`
	Enabled bool   `json:"enabled"`
	// System is true for an extension that came with the system the export was made on.
	System bool `json:"system,omitempty"`
}

// exportExtensions lists the extensions worth restoring: every one the user added, on or off, and the
// system's that are switched on. Their settings are not exported.
func (s *Session) exportExtensions(ctx context.Context) ([]ExportedExtension, error) {
	if s.Extensions == nil {
		return nil, nil
	}
	list, err := s.Extensions.List(ctx)
	if err != nil {
		return nil, err
	}
	var out []ExportedExtension
	for _, e := range list {
		if e.System && !e.Enabled {
			continue // as the system ships it
		}
		out = append(out, ExportedExtension{UUID: e.UUID, Name: e.Name, Enabled: e.Enabled, System: e.System})
	}
	return out, nil
}

// RestoreEnable is the RestoreAction for an extension that is here already and only needs switching on.
const RestoreEnable RestoreAction = "enable"

// ExtensionRestore is one extension of a restore plan.
type ExtensionRestore struct {
	Ext    ExportedExtension
	Action RestoreAction // RestoreInstall, RestoreEnable, RestoreInstalled or RestoreUnavailable
	// Note explains the action, such as "no version for GNOME 51".
	Note string
}

// PlanExtensions decides what restoring does with each extension of an export. One that isn't on this
// system is installed when extensions.gnome.org has a version for this GNOME release. One that is here is
// left as it is, except that it is switched on if it was on in the export: restoring never switches anything
// off.
func (s *Session) PlanExtensions(ctx context.Context, ex Export) ([]ExtensionRestore, error) {
	if len(ex.Extensions) == 0 {
		return nil, nil
	}
	items := make([]ExtensionRestore, 0, len(ex.Extensions))
	if s.Extensions == nil {
		for _, e := range ex.Extensions {
			items = append(items, ExtensionRestore{Ext: e, Action: RestoreUnavailable, Note: "needs the GNOME desktop"})
		}
		return items, nil
	}
	here, err := s.Extensions.List(ctx)
	var major string
	if err == nil {
		major, err = s.Extensions.Major(ctx)
	}
	if err != nil {
		// The apps of the export can still be restored.
		for _, e := range ex.Extensions {
			items = append(items, ExtensionRestore{Ext: e, Action: RestoreUnavailable, Note: "GNOME Shell didn't answer"})
		}
		return items, nil
	}
	db, err := s.OpenCatalog(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer db.Close()
	seen := map[string]bool{}
	for _, e := range ex.Extensions {
		if seen[e.UUID] {
			continue
		}
		seen[e.UUID] = true
		it := ExtensionRestore{Ext: e}
		if i := slices.IndexFunc(here, func(h shellext.Extension) bool { return h.UUID == e.UUID }); i >= 0 {
			it.Action = RestoreInstalled
			if e.Enabled && !here[i].Enabled && here[i].CanChange {
				it.Action = RestoreEnable
			}
			items = append(items, it)
			continue
		}
		app, err := db.App(ctx, catalog.ExtensionAppID(e.UUID))
		switch {
		case db.Info().Extensions == nil:
			it.Action, it.Note = RestoreUnavailable, "this catalog has no extensions yet; refresh it"
		case err != nil || len(app.Listings) == 0:
			it.Action, it.Note = RestoreUnavailable, "not on extensions.gnome.org"
			if e.System {
				it.Note = "came with " + systemName(ex) + "; not on extensions.gnome.org"
			}
		case !slices.Contains(app.Listings[0].Shells, major):
			it.Action, it.Note = RestoreUnavailable, "no version for GNOME "+major
		default:
			it.Action = RestoreInstall
			if !e.Enabled {
				it.Note = "switched off, as it was"
			}
		}
		items = append(items, it)
	}
	return items, nil
}

// systemName names the system an export was made on.
func systemName(ex Export) string {
	if ex.System != "" {
		return ex.System
	}
	return "the other system"
}

// RestoreExtension carries out one item of PlanExtensions. GNOME Shell asks the user before each install,
// in its own dialog; a no there is shellext.ErrCancelled.
func (s *Session) RestoreExtension(ctx context.Context, it ExtensionRestore) error {
	if s.Extensions == nil {
		return errors.New("extensions need the GNOME desktop")
	}
	switch it.Action {
	case RestoreInstall:
		if err := s.Extensions.Install(ctx, it.Ext.UUID); err != nil {
			return err
		}
		if !it.Ext.Enabled {
			// The Shell switches a new extension on; this one was off.
			if err := s.Extensions.SetEnabled(ctx, it.Ext.UUID, false); err != nil {
				return fmt.Errorf("installed, but could not switch it off: %w", err)
			}
		}
	case RestoreEnable:
		return s.Extensions.SetEnabled(ctx, it.Ext.UUID, true)
	}
	return nil
}
