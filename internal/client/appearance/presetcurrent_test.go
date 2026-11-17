package appearance

import (
	"context"
	"slices"
	"strings"
	"testing"

	"github.com/tyler-builds/uas/internal/catalog"
)

// remembered is the name of the preset uas remembers as in use, or "".
func rememberedName(t *testing.T, s *Presets) string {
	t.Helper()
	r, ok, err := s.Remembered()
	if err != nil {
		t.Fatal(err)
	}
	if !ok {
		return ""
	}
	return r.Name
}

func TestPresetChangedAndUpdate(t *testing.T) {
	w, l := presetWorld(t)
	ctx := context.Background()
	store := testPresets(t)
	lookup := func(id string) (catalog.Listing, bool) { return l, id == "pling:1166289" }
	w.gs.values[KeyAccentColor] = "blue"
	w.gs.values[KeyColorScheme] = SchemeDefault

	work, err := store.Save(Preset{Name: "Work", Parts: map[string]Part{
		PartIcons:     {Value: "Papirus-Dark", From: &Origin{ID: "pling:1166289", Variant: "Papirus-Dark"}},
		PartCursor:    {Value: "Bibata"}, // nowhere to get it from: skipped when applied
		PartInterface: {Value: "Inter"},  // a family without a size
		PartAccent:    {Value: "teal"},
	}}, false)
	if err != nil {
		t.Fatal(err)
	}
	if rememberedName(t, store) != "" {
		t.Fatal("a new preset is remembered")
	}
	plan, _ := w.in.PlanPreset(ctx, work, lookup)
	if _, err := w.in.ApplyPreset(ctx, store, work, plan, ApplyOptions{}); err != nil {
		t.Fatal(err)
	}
	if rememberedName(t, store) != "Work" {
		t.Fatalf("remembered %q after apply", rememberedName(t, store))
	}
	status := func(name string) PresetStatus {
		p, err := store.Get(name)
		if err != nil {
			t.Fatal(err)
		}
		r, ok, _ := store.Remembered()
		if !ok {
			return StatusOf(p, nil, w.in.Current(ctx))
		}
		return StatusOf(p, &r, w.in.Current(ctx))
	}
	// The cursor was skipped, so the look isn't the preset's; but nothing changed since it was applied.
	if st := status("Work"); st != "" {
		t.Errorf("status after an apply with a part skipped = %q", st)
	}
	if _, ok := w.in.ChangedPreset(ctx, store); ok {
		t.Error("changed right after the apply")
	}

	// Using another font and accent changes the look: Work is changed, and stays as it was.
	before, _ := store.Get("Work")
	if err := w.in.Use(ctx, KeyFont, "Fira Code"); err != nil { // keeps the size: "Fira Code 11"
		t.Fatal(err)
	}
	w.in.Use(ctx, KeyAccentColor, "red")
	if st := status("Work"); st != StatusChanged {
		t.Errorf("status after Use = %q", st)
	}
	if p, ok := w.in.ChangedPreset(ctx, store); !ok || p.Name != "Work" {
		t.Errorf("ChangedPreset = %v %v", p.Name, ok)
	}
	if after, _ := store.Get("Work"); !after.UpdatedAt.Equal(before.UpdatedAt) || after.Parts[PartAccent].Value != "teal" {
		t.Error("the preset changed by itself")
	}
	// A setting it doesn't have is no change of it.
	w.gs.values[KeyMonospaceFont] = "Mono 9"
	if st := status("Work"); st != StatusChanged {
		t.Errorf("status = %q", st)
	}

	// Update saves only what changed since the apply: the font (still without a size) and the accent; the
	// skipped cursor keeps its value.
	p, changed, err := w.in.UpdatePreset(ctx, store, "work")
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(changed, []string{PartAccent, PartInterface}) {
		t.Errorf("changed = %v", changed)
	}
	if p.Parts[PartInterface].Value != "Fira Code" || p.Parts[PartAccent].Value != "red" ||
		p.Parts[PartCursor].Value != "Bibata" || p.Parts[PartIcons].From == nil || p.Sets(PartMonospace) {
		t.Errorf("updated parts = %+v", p.Parts)
	}
	if st := status("Work"); st != "" { // the skipped cursor still isn't the look
		t.Errorf("status after Update = %q", st)
	}
	if _, changed, _ := w.in.UpdatePreset(ctx, store, "Work"); len(changed) != 0 {
		t.Errorf("a second Update changed %v", changed)
	}

	// Renaming keeps it remembered; editing it on its page changes the preset, not the look.
	w.in.Use(ctx, KeyAccentColor, "green")
	if _, err := store.Rename("Work", "Office"); err != nil {
		t.Fatal(err)
	}
	if rememberedName(t, store) != "Office" || status("Office") != StatusChanged {
		t.Errorf("after rename: %q, %q", rememberedName(t, store), status("Office"))
	}
	store.Update("Office", func(p *Preset) error { p.Parts[PartAccent] = Part{Value: "green"}; return nil })
	if st := status("Office"); st != "" {
		t.Errorf("after an edit that matches the look: %q", st)
	}
	// Deleting it forgets it.
	if err := store.Delete("Office"); err != nil {
		t.Fatal(err)
	}
	if rememberedName(t, store) != "" {
		t.Error("a deleted preset is remembered")
	}
}

func TestPresetRememberedUndo(t *testing.T) {
	w, _ := presetWorld(t)
	ctx := context.Background()
	store := testPresets(t)
	w.gs.values[KeyAccentColor] = "blue"

	// Saved from the current look: in use and remembered.
	day, _ := store.Save(Preset{Name: "Day", Parts: map[string]Part{PartAccent: {Value: "blue"}}}, false)
	if err := w.in.RememberPreset(ctx, store, day); err != nil {
		t.Fatal(err)
	}
	night, _ := store.Save(Preset{Name: "Night", Parts: map[string]Part{PartAccent: {Value: "purple"}}}, false)
	plan, _ := w.in.PlanPreset(ctx, night, nil)
	w.in.ApplyPreset(ctx, store, night, plan, ApplyOptions{})
	if rememberedName(t, store) != "Night" {
		t.Fatal("Night isn't remembered")
	}
	// Undo goes back to Day's look, and to Day as the preset in use.
	if _, _, err := w.in.UndoPreset(ctx, store); err != nil {
		t.Fatal(err)
	}
	r, ok, _ := store.Remembered()
	if !ok || r.Name != "Day" || StatusOf(day, &r, w.in.Current(ctx)) != StatusInUse {
		t.Errorf("after undo, remembered %+v", r)
	}
	// With none remembered before the apply, Undo leaves none.
	store.remember(nil)
	plan, _ = w.in.PlanPreset(ctx, night, nil)
	w.in.ApplyPreset(ctx, store, night, plan, ApplyOptions{})
	w.in.UndoPreset(ctx, store)
	if rememberedName(t, store) != "" {
		t.Error("remembered after undoing the only apply")
	}

	// A preset uas doesn't remember is updated in every part it has that differs from the look.
	w.gs.values[KeyAccentColor] = "orange"
	w.gs.values[KeyCursorTheme] = "Adwaita"
	store.Save(Preset{Name: "Other", Parts: map[string]Part{PartAccent: {Value: "teal"},
		PartCursor: {Value: "Adwaita"}}}, false)
	p, changed, err := w.in.UpdatePreset(ctx, store, "Other")
	if err != nil || !slices.Equal(changed, []string{PartAccent}) || p.Parts[PartAccent].Value != "orange" {
		t.Errorf("Update = %v %v %+v", changed, err, p.Parts)
	}
	if rememberedName(t, store) != "Other" {
		t.Error("an updated preset isn't remembered")
	}
}

func TestAddToPreset(t *testing.T) {
	w, icons := presetWorld(t)
	gtkW, gtk := gtkWorld(t)
	_ = gtkW
	store := testPresets(t)
	store.Save(Preset{Name: "Work", Parts: map[string]Part{PartInterface: {Value: "Cantarell 12"},
		PartAccent: {Value: "teal"}}}, false)
	font := catalog.Listing{Source: catalog.SourceGoogleFonts, Ref: "inter", Kind: catalog.KindFont, Name: "Inter"}
	cursor := catalog.Listing{Source: catalog.SourcePling, Ref: "1197198", Kind: catalog.KindCursorTheme,
		Name: "Bibata", Assets: []catalog.Asset{{Kind: catalog.AssetTheme, Themes: []string{"Bibata-Modern-Ice"}}}}
	recs, _ := w.in.Records()
	w.gs.calls, w.transp.hits = nil, nil
	for _, tc := range []struct {
		part    string
		value   Part
		want    string
		from    string
		variant string
	}{
		// A font not installed: by the listing's name, keeping the size the preset had.
		{PartInterface, ItemPart(font, "", recs), "Inter 12", "google-fonts:inter", ""},
		{PartIcons, ItemPart(icons, "Papirus-Dark", recs), "Papirus-Dark", "pling:1166289", "Papirus-Dark"},
		{PartCursor, ItemPart(cursor, "", recs), "Bibata-Modern-Ice", "pling:1197198", "Bibata-Modern-Ice"},
		{PartGTK, ItemPart(gtk, "Orchis-Dark", recs), "Orchis-Dark", "pling:1357889", "Orchis-Dark"},
		// A Shell theme that comes with a GTK theme of the catalog.
		{PartShell, ItemPart(gtk, "Orchis-Dark", recs), "Orchis-Dark", "pling:1357889", "Orchis-Dark"},
		// The system's Shell theme, and the Shell's own look.
		{PartShell, Part{Value: "Yaru"}, "Yaru", "", ""},
		{PartShell, Part{}, "", "", ""},
	} {
		p, err := store.AddPart("work", tc.part, tc.value)
		if err != nil {
			t.Fatal(err)
		}
		got := p.Parts[tc.part]
		if got.Value != tc.want || (got.From == nil) != (tc.from == "") ||
			(got.From != nil && (got.From.ID != tc.from || got.From.Variant != tc.variant)) {
			t.Errorf("%s = %+v, want %q from %q", tc.part, got, tc.want, tc.from)
		}
		if tc.part != PartAccent && p.Parts[PartAccent].Value != "teal" {
			t.Error("another part changed")
		}
		if !p.Has(tc.part, tc.want) || (tc.part == PartInterface && !p.Has(tc.part, "Inter")) {
			t.Errorf("Has(%s, %q) is false", tc.part, tc.want)
		}
	}
	// Nothing was applied or downloaded.
	if slices.ContainsFunc(w.gs.calls, func(c string) bool { return strings.Contains(c, " set ") }) ||
		len(w.transp.hits) > 0 {
		t.Errorf("AddPart changed the desktop or downloaded: %v %v", w.gs.calls, w.transp.hits)
	}
	if _, err := store.AddPart("Work", PartWallpaper, Part{Value: "x"}); err == nil {
		t.Error("added a wallpaper as a theme")
	}
	if _, err := store.AddPart("Nope", PartIcons, Part{Value: "x"}); err == nil {
		t.Error("added to a preset that isn't there")
	}
}
