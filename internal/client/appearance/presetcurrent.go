package appearance

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/tyler-builds/uas/internal/catalog"
	"github.com/tyler-builds/uas/internal/client/state"
)

// The preset in use, and keeping it up to date. uas remembers the preset last applied, or saved from the
// current look, with the look that gave: the desktop's settings of the preset's parts right after. When they
// change (the user used another font or theme), that preset is "changed": the look no longer matches it, and
// Update saves the current look into it in one step. uas never changes a preset by itself: trying a theme
// leaves every saved preset as it was.
//
// What is remembered follows the preset: renaming it keeps it, deleting it forgets it, and Undo of an apply
// brings back what was remembered before that apply. Changing a part on the preset's page changes the preset,
// not the look, so the preset is then neither changed nor (unless the look has the new part) in use.

// Remembered is the preset last applied, or saved from the current look.
type Remembered struct {
	Name string    `json:"name"`
	At   time.Time `json:"at"`
	// Look is the desktop's settings of the preset's parts right after it was applied or saved, by setting
	// key, as Installer.Current has them.
	Look map[string]string `json:"look"`
}

// Remembered returns the preset last applied or saved from the current look, and false when there is none.
func (s *Presets) Remembered() (Remembered, bool, error) {
	if s.CurrentFile == "" {
		return Remembered{}, false, nil
	}
	b, err := os.ReadFile(s.CurrentFile)
	if errors.Is(err, os.ErrNotExist) {
		return Remembered{}, false, nil
	}
	if err != nil {
		return Remembered{}, false, err
	}
	var r Remembered
	if err := json.Unmarshal(b, &r); err != nil {
		return Remembered{}, false, fmt.Errorf("%s: %w", s.CurrentFile, err)
	}
	return r, r.Name != "", nil
}

// remember writes what is remembered, or with nil forgets it.
func (s *Presets) remember(r *Remembered) error {
	if s.CurrentFile == "" {
		return nil
	}
	if r == nil {
		if err := os.Remove(s.CurrentFile); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(s.CurrentFile), 0o755); err != nil {
		return err
	}
	b, err := json.MarshalIndent(r, "", "  ")
	if err != nil {
		return err
	}
	return writeFile(s.CurrentFile, append(b, '\n'))
}

// renamed points what is remembered at a preset's new name.
func (s *Presets) renamed(from, to string) error {
	r, ok, err := s.Remembered()
	if err != nil || !ok || !strings.EqualFold(r.Name, from) {
		return err
	}
	r.Name = to
	return s.remember(&r)
}

// deleted forgets a preset that was remembered.
func (s *Presets) deleted(name string) error {
	r, ok, err := s.Remembered()
	if err != nil || !ok || !strings.EqualFold(r.Name, name) {
		return err
	}
	return s.remember(nil)
}

// settingKeys are the settings a preset's parts set, as Current has them.
func (p Preset) settingKeys() []string {
	var keys []string
	for name := range p.Parts {
		key, ok := PartKey(name)
		switch {
		case !ok:
		case name == PartWallpaper:
			keys = append(keys, WallpaperKeys...)
		default:
			keys = append(keys, key)
		}
	}
	slices.Sort(keys)
	return keys
}

// lookOf is the desktop's settings of a preset's parts, from current.
func lookOf(p Preset, current map[string]string) map[string]string {
	look := map[string]string{}
	for _, k := range p.settingKeys() {
		if v, ok := current[k]; ok {
			look[k] = v
		}
	}
	return look
}

// RememberPreset remembers a preset as the one in use, with the desktop's look now: after it was applied,
// saved from the current look, or updated.
func (in *Installer) RememberPreset(ctx context.Context, store *Presets, p Preset) error {
	if store == nil {
		return nil
	}
	return store.remember(&Remembered{Name: p.Name, At: store.now(), Look: lookOf(p, in.Current(ctx))})
}

// lookChanged reports whether any of keys has another value now than in a remembered look. A setting the
// desktop no longer reports, or that wasn't remembered, is no change.
func lookChanged(look, current map[string]string, keys []string) bool {
	for _, k := range keys {
		was, ok := look[k]
		now, ok2 := current[k]
		if ok && ok2 && was != now {
			return true
		}
	}
	return false
}

// changedParts are the parts of the remembered preset whose settings changed since it was applied or saved
// and that don't have the setting's value now, in PartOrder: what Update would save. A part the preset was
// changed to on its page, matching the look, isn't one.
func changedParts(p Preset, rem Remembered, current map[string]string) []string {
	var out []string
	for _, name := range p.PartNames() {
		key, ok := PartKey(name)
		if !ok {
			continue
		}
		part := p.Parts[name]
		if name == PartWallpaper {
			if lookChanged(rem.Look, current, WallpaperKeys) && !wallpaperInUse(part, current) {
				out = append(out, name)
			}
			continue
		}
		if lookChanged(rem.Look, current, []string{key}) && !sameValue(key, current[key], part.Value) {
			out = append(out, name)
		}
	}
	return out
}

// PresetStatus is a preset's state against the desktop's look.
type PresetStatus string

const (
	// StatusInUse: every part it has that the desktop supports has its value (InUse).
	StatusInUse PresetStatus = "in use"
	// StatusChanged: it is the preset last applied or saved, and the look changed since; Update saves the
	// current look into it, and applying it again goes back.
	StatusChanged PresetStatus = "changed"
)

// StatusOf is a preset's state: in use, changed since it was applied (rem is what is remembered, or nil), or
// neither ("").
func StatusOf(p Preset, rem *Remembered, current map[string]string) PresetStatus {
	switch {
	case InUse(p, current):
		return StatusInUse
	case rem != nil && strings.EqualFold(rem.Name, p.Name) && len(changedParts(p, *rem, current)) > 0:
		return StatusChanged
	}
	return ""
}

// ChangedPreset returns the remembered preset when the look changed since it was applied or saved, and false
// otherwise.
func (in *Installer) ChangedPreset(ctx context.Context, store *Presets) (Preset, bool) {
	if store == nil {
		return Preset{}, false
	}
	rem, ok, err := store.Remembered()
	if err != nil || !ok {
		return Preset{}, false
	}
	p, err := store.Get(rem.Name)
	if err != nil {
		return Preset{}, false
	}
	return p, StatusOf(p, &rem, in.Current(ctx)) == StatusChanged
}

// UpdatePreset saves the current look into a preset, and remembers it as the one in use. Only the parts the
// preset has are changed (it never gains a part), and of those, when it is the preset last applied or saved,
// only the ones whose settings changed since: a part that was skipped when it was applied (a theme this
// system doesn't have) keeps its value and where to install it from. A font part without a size stays
// without one. It returns the parts it changed, in PartOrder.
func (in *Installer) UpdatePreset(ctx context.Context, store *Presets, name string) (Preset, []string, error) {
	if in.Desktop == nil {
		return Preset{}, nil, ErrUnsupported
	}
	p, err := store.Get(name)
	if err != nil {
		return Preset{}, nil, err
	}
	rem, ok, _ := store.Remembered()
	tracked := ok && strings.EqualFold(rem.Name, p.Name)
	current := in.Current(ctx)
	due := changedParts(p, rem, current)
	recs, _ := in.Records()
	parts := maps.Clone(p.Parts)
	var changed []string
	for _, name := range p.PartNames() {
		key, ok := PartKey(name)
		if !ok || !in.Desktop.Supports(key) {
			continue
		}
		if tracked && !slices.Contains(due, name) {
			continue
		}
		old := p.Parts[name]
		if name == PartWallpaper {
			if wallpaperInUse(old, current) {
				continue
			}
			w, ok, err := in.captureWallpaper(ctx)
			if err != nil {
				return Preset{}, nil, fmt.Errorf("wallpaper: %w", err)
			}
			if ok {
				w.rest = old.rest
				parts[name], changed = w, append(changed, name)
			}
			continue
		}
		v, ok := current[key]
		if !ok || (v == "" && !EmptyValue(key)) || (v == old.Value || sameValue(key, v, old.Value)) {
			continue
		}
		np := Part{Value: v, From: OriginOf(recs, key, v), rest: old.rest}
		if slices.Contains(FontKeys, key) && old.Value != "" && FontSize(old.Value) == "" {
			np.Value = FontFamily(v)
		}
		parts[name], changed = np, append(changed, name)
	}
	if len(changed) > 0 {
		if p, err = store.Update(p.Name, func(pp *Preset) error { pp.Parts = parts; return nil }); err != nil {
			return Preset{}, nil, err
		}
	}
	return p, changed, in.RememberPreset(ctx, store, p)
}

// ItemPart is the preset part for a font or theme of the catalog, which applying the preset installs when it
// isn't on the system: a theme's variant (its first when none is given), or a font's family, as uas installed
// it or else by the listing's name, without a size.
func ItemPart(l catalog.Listing, variant string, recs []state.AppearanceInstall) Part {
	id := catalog.AppearanceAppID(l.Source, l.Ref)
	if l.Kind == catalog.KindFont {
		value := l.Name
		for _, r := range recs {
			if r.ID == id && len(r.Families) > 0 {
				value = r.Families[0]
			}
		}
		return Part{Value: value, From: &Origin{ID: id}}
	}
	if variant == "" {
		if vs := Variants(l); len(vs) > 0 {
			variant = vs[0]
		}
	}
	return Part{Value: variant, From: &Origin{ID: id, Variant: variant}}
}

// Has reports whether a preset's part is a font or theme: the same theme, or the same font family whatever its
// size.
func (p Preset) Has(part, value string) bool {
	have, ok := p.Parts[part]
	if !ok {
		return false
	}
	key, _ := PartKey(part)
	if slices.Contains(FontKeys, key) {
		return strings.EqualFold(FontFamily(have.Value), FontFamily(value))
	}
	return have.Value == value
}

// AddPart sets one part of a preset to a font or theme and changes nothing else: nothing is applied or
// installed (the part keeps where to install it from, so applying the preset does that). A font without a
// size keeps the size the preset's part had.
func (s *Presets) AddPart(name, part string, value Part) (Preset, error) {
	if _, ok := PartKey(part); !ok || part == PartWallpaper {
		return Preset{}, fmt.Errorf("%q isn't a part a font or theme is added to", part)
	}
	return s.Update(name, func(p *Preset) error {
		old := p.Parts[part]
		key, _ := PartKey(part)
		if slices.Contains(FontKeys, key) && FontSize(value.Value) == "" && FontSize(old.Value) != "" {
			value.Value = FontValue(old.Value, value.Value)
		}
		value.rest = old.rest
		p.Parts[part] = value
		return nil
	})
}
