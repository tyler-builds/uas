package appearance

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/tyler-builds/uas/internal/catalog"
	"github.com/tyler-builds/uas/internal/client/backend"
	"github.com/tyler-builds/uas/internal/client/state"
)

func testPresets(t *testing.T) *Presets {
	dir := t.TempDir()
	s := NewPresets(filepath.Join(dir, "config"), filepath.Join(dir, "data"))
	s.Now = func() time.Time { return time.Date(2026, 11, 16, 20, 0, 0, 0, time.UTC) }
	return s
}

func TestPresetFile(t *testing.T) {
	s := testPresets(t)
	p, err := s.Save(Preset{Name: "  Nord evening ", Parts: map[string]Part{
		PartIcons:  {Value: "Papirus-Dark", From: &Origin{ID: "pling:1166289", Variant: "Papirus-Dark"}},
		PartScheme: {Value: SchemeDark},
	}}, false)
	if err != nil {
		t.Fatal(err)
	}
	if p.Name != "Nord evening" || filepath.Base(p.file) != "nord-evening.json" {
		t.Errorf("saved as %q in %s", p.Name, p.file)
	}
	b, _ := os.ReadFile(p.file)
	var raw map[string]any
	if err := json.Unmarshal(b, &raw); err != nil {
		t.Fatal(err)
	}
	if raw["uas_preset"] != 1.0 || raw["name"] != "Nord evening" || raw["created_at"] != "2026-11-16T20:00:00Z" {
		t.Errorf("file = %s", b)
	}
	if !strings.Contains(string(b), `"icons": {
      "value": "Papirus-Dark",
      "from": {`) {
		t.Errorf("value doesn't come first:\n%s", b)
	}
	icons := raw["parts"].(map[string]any)["icons"].(map[string]any)
	if icons["value"] != "Papirus-Dark" || icons["from"].(map[string]any)["id"] != "pling:1166289" {
		t.Errorf("icons = %v", icons)
	}

	// A file from a newer uas: a part this one doesn't know, and fields it doesn't know, survive a change.
	newer := `{"uas_preset": 2, "name": "Newer", "parts": {
		"wallpaper": {"light": "sha256:ab12", "dark": "sha256:cd34", "fit": "zoom"},
		"icons": {"value": "Papirus", "tint": "blue"}}}`
	if err := os.WriteFile(filepath.Join(s.Dir, "newer.json"), []byte(newer), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Update("newer", func(p *Preset) error {
		p.Parts[PartAccent] = Part{Value: "teal"}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	got, err := s.Get("NEWER")
	if err != nil {
		t.Fatal(err)
	}
	if got.Format != 2 || !slices.Equal(got.PartNames(), []string{PartAccent, PartIcons, PartWallpaper}) {
		t.Errorf("parts = %v, format %d", got.PartNames(), got.Format)
	}
	b, _ = os.ReadFile(got.file)
	for _, want := range []string{`"light": "sha256:ab12"`, `"fit": "zoom"`, `"tint": "blue"`, `"value": "teal"`} {
		if !strings.Contains(string(b), want) {
			t.Errorf("rewritten file lost %s:\n%s", want, b)
		}
	}

	// A file that can't be read is left out of the list, and said.
	os.WriteFile(filepath.Join(s.Dir, "broken.json"), []byte("{"), 0o644)
	list, err := s.List()
	if err == nil || len(list) != 2 || list[0].Name != "Newer" || list[1].Name != "Nord evening" {
		t.Errorf("List = %v, %v", list, err)
	}
}

func TestPresetNames(t *testing.T) {
	s := testPresets(t)
	save := func(name string, replace bool) (Preset, error) {
		return s.Save(Preset{Name: name, Parts: map[string]Part{PartAccent: {Value: "teal"}}}, replace)
	}
	for _, bad := range []string{"", "   ", "tab\there", strings.Repeat("x", 65)} {
		if _, err := save(bad, false); err == nil {
			t.Errorf("saved a preset called %q", bad)
		}
	}
	if _, err := save("Nord!", false); err != nil {
		t.Fatal(err)
	}
	// The same name, whatever its case, is a clash unless it is to be replaced.
	var exists *ExistsError
	if _, err := save("nord!", false); !errors.As(err, &exists) || exists.Name != "Nord!" {
		t.Errorf("a clash gave %v", err)
	}
	if p, err := save("NORD!", true); err != nil || filepath.Base(p.file) != "nord.json" {
		t.Errorf("replace: %v, %v", p.file, err)
	}
	if list, _ := s.List(); len(list) != 1 || list[0].Name != "NORD!" {
		t.Errorf("after replace: %v", list)
	}
	// Another name of the same slug gets a file of its own.
	if p, err := save("Nord?", false); err != nil || filepath.Base(p.file) != "nord-2.json" {
		t.Errorf("second slug: %v, %v", p.file, err)
	}
	for name, want := range map[string]string{"Nord evening": "nord-evening", "  ** ": "preset", "Café 2": "caf-2",
		strings.Repeat("ab ", 40): strings.Repeat("ab-", 16) + "a"} {
		if got := Slug(name); got != want {
			t.Errorf("Slug(%q) = %q, want %q", name, got, want)
		}
	}

	// Rename: not onto another's name, but a change of case is fine, and the file follows the name.
	if _, err := s.Rename("nord?", "nord!"); !errors.As(err, &exists) {
		t.Errorf("renamed onto another: %v", err)
	}
	if p, err := s.Rename("nord!", "Nord!"); err != nil || p.Name != "Nord!" {
		t.Errorf("change of case: %v, %v", p.Name, err)
	}
	p, err := s.Rename("Nord?", "Evening")
	if err != nil || filepath.Base(p.file) != "evening.json" {
		t.Fatalf("rename: %v, %v", p.file, err)
	}
	if _, err := os.Stat(filepath.Join(s.Dir, "nord-2.json")); err == nil {
		t.Error("the old file is still there")
	}
	if _, err := s.Rename("Nope", "x"); !errors.Is(err, ErrNoPreset) {
		t.Errorf("renamed nothing: %v", err)
	}

	// Copies.
	for _, want := range []string{"Evening copy", "Evening copy 2"} {
		c, err := s.Copy("Evening", "")
		if err != nil || c.Name != want || c.Parts[PartAccent].Value != "teal" {
			t.Errorf("Copy = %q %v, %v", c.Name, c.Parts, err)
		}
	}
	if _, err := s.Copy("Evening", "nord!"); !errors.As(err, &exists) {
		t.Errorf("copied onto another: %v", err)
	}
	if err := s.Delete("evening copy"); err != nil {
		t.Fatal(err)
	}
	if err := s.Delete("evening copy"); !errors.Is(err, ErrNoPreset) {
		t.Errorf("deleted twice: %v", err)
	}
	var names []string
	list, _ := s.List()
	for _, p := range list {
		names = append(names, p.Name)
	}
	if !slices.Equal(names, []string{"Evening", "Evening copy 2", "Nord!"}) {
		t.Errorf("presets = %v", names)
	}
}

// presetWorld is a world with Papirus installed by uas and a few fonts.
func presetWorld(t *testing.T) (*world, catalog.Listing) {
	w := newWorld(t)
	w.gs.values[KeyColorScheme] = SchemeDefault
	w.gs.values[KeyAccentColor] = "blue"
	w.gs.fonts = "Adwaita Sans\nAdwaita Mono\nInter,Inter Display\nFira Code,Fira Code Light\n"
	body := tarGz(t, iconTheme("Papirus"), nil)
	a := w.offer("papirus.tar.gz", body, "Papirus", "Papirus-Dark")
	l := catalog.Listing{Source: catalog.SourcePling, Ref: "1166289", Kind: catalog.KindIconTheme, Name: "Papirus",
		Version: "1", Assets: []catalog.Asset{a}}
	return w, l
}

func TestCapture(t *testing.T) {
	w, l := presetWorld(t)
	ctx := context.Background()
	if _, err := w.in.Install(ctx, l, "Papirus-Dark", nil); err != nil {
		t.Fatal(err)
	}
	w.gs.values[KeyIconTheme] = "Papirus-Dark"
	w.gs.values[KeyMonospaceFont] = "Fira Code weight=450 10"
	w.gs.values[KeyAccentColor] = ""
	parts, err := w.in.Capture(ctx)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]Part{
		PartScheme:    {Value: SchemeDefault},
		PartIcons:     {Value: "Papirus-Dark", From: &Origin{ID: "pling:1166289", Variant: "Papirus-Dark"}},
		PartCursor:    {Value: "Adwaita"},
		PartInterface: {Value: "Adwaita Sans 11"},
		PartDocument:  {Value: "Adwaita Sans 12"},
		PartMonospace: {Value: "Fira Code weight=450 10"},
		PartShell:     {}, // the Shell's own look, which is a value of the Shell theme
	}
	gotJSON, _ := json.Marshal(parts)
	wantJSON, _ := json.Marshal(want)
	if string(gotJSON) != string(wantJSON) {
		t.Errorf("Capture =\n%s\nwant\n%s", gotJSON, wantJSON)
	}
	if _, err := (&Installer{State: w.in.State}).Capture(ctx); err != ErrUnsupported {
		t.Errorf("Capture without a desktop: %v", err)
	}
}

func TestPlanApplyUndo(t *testing.T) {
	w, l := presetWorld(t)
	ctx := context.Background()
	store := testPresets(t)
	p := Preset{Name: "Evening", Parts: map[string]Part{
		PartMonospace: {Value: "Fira Code 10"}, // installed by someone else: set
		PartIcons:     {Value: "Papirus-Dark", From: &Origin{ID: "pling:1166289", Variant: "Papirus-Dark"}},
		PartCursor:    {Value: "Bibata"},          // nowhere to get it from: skipped
		PartAccent:    {Value: "teal"},            // set
		PartScheme:    {Value: SchemeDark},        // set
		PartDocument:  {Value: "Adwaita Sans 12"}, // already so
		PartInterface: {Value: "Inter"},           // a family alone: set, keeping the size
		PartShell:     {Value: "Nordic"},          // not on this system, nowhere to get it from: skipped
	}}
	lookup := func(id string) (catalog.Listing, bool) { return l, id == "pling:1166289" }
	plan, err := w.in.PlanPreset(ctx, p, lookup)
	if err != nil {
		t.Fatal(err)
	}
	var steps []string
	for _, it := range plan {
		steps = append(steps, it.Part+":"+string(it.Step))
	}
	wantSteps := []string{"scheme:set", "accent:set", "shell:missing", "icons:install", "cursor:missing",
		"interface:set", "document:same", "monospace:set"}
	if !slices.Equal(steps, wantSteps) {
		t.Errorf("plan = %v\nwant %v", steps, wantSteps)
	}
	if d := Downloads(plan); len(d) != 1 || d[0].Variant != "Papirus-Dark" || !strings.HasPrefix(d[0].Note, "from Pling") {
		t.Errorf("Downloads = %+v", d)
	}

	// The accent fails part-way; the rest are set all the same, in order.
	w.gs.fail = map[string]bool{KeyAccentColor: true}
	w.gs.calls = nil
	var installs []string
	out, err := w.in.ApplyPreset(ctx, store, p, plan, ApplyOptions{Install: func(ctx context.Context,
		l catalog.Listing, v string, pr func(backend.Progress)) (state.AppearanceInstall, error) {
		installs = append(installs, l.Name+"/"+v)
		return w.in.Install(ctx, l, v, pr)
	}})
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(installs, []string{"Papirus/Papirus-Dark"}) {
		t.Errorf("installs = %v", installs)
	}
	var sets []string
	for _, c := range w.gs.calls {
		if strings.HasPrefix(c, "gsettings set ") {
			sets = append(sets, strings.Fields(c)[3])
		}
	}
	if !slices.Equal(sets, []string{"color-scheme", "accent-color", "icon-theme", "font-name", "monospace-font-name"}) {
		t.Errorf("set in order %v", sets)
	}
	for key, want := range map[string]string{KeyColorScheme: SchemeDark, KeyAccentColor: "blue",
		KeyIconTheme: "Papirus-Dark", KeyCursorTheme: "Adwaita", KeyFont: "Inter 11", KeyMonospaceFont: "Fira Code 10"} {
		if w.gs.values[key] != want {
			t.Errorf("%s = %q, want %q", key, w.gs.values[key], want)
		}
	}
	failed := Failed(out)
	if failed == nil || !strings.Contains(failed.Error(), "accent color") {
		t.Errorf("Failed = %v", failed)
	}
	for _, o := range out {
		if done := o.Done; done != (o.Part != PartAccent && !o.Step.Skipped()) {
			t.Errorf("%s: done %v (%s, %v)", o.Part, done, o.Step, o.Err)
		}
	}
	// The look to undo to has the values the apply meant to change, the accent too, and nothing else.
	u, ok, err := store.Undo()
	if err != nil || !ok || u.Applied != "Evening" {
		t.Fatalf("Undo = %+v %v %v", u, ok, err)
	}
	if got := u.Look.PartNames(); !slices.Equal(got, []string{"scheme", "accent", "icons", "interface", "monospace"}) {
		t.Errorf("undo look = %v", got)
	}
	// A failed change saves nothing for Revert.
	if _, ok := savedKey(w.in, KeyAccentColor); ok {
		t.Error("the accent that failed is saved for Revert")
	}
	// Not in use: the accent didn't change.
	if InUse(p, w.in.Current(ctx)) {
		t.Error("the preset is in use with its accent unset")
	}
	w.gs.fail = nil

	// Undo goes back, without installing anything, and only once.
	w.gs.calls = nil
	if _, out, err := w.in.UndoPreset(ctx, store); err != nil || Failed(out) != nil {
		t.Fatalf("UndoPreset: %v, %v", err, Failed(out))
	}
	for key, want := range map[string]string{KeyColorScheme: SchemeDefault, KeyAccentColor: "blue",
		KeyIconTheme: "Adwaita", KeyFont: "Adwaita Sans 11", KeyMonospaceFont: "Adwaita Mono 11"} {
		if w.gs.values[key] != want {
			t.Errorf("after undo, %s = %q, want %q", key, w.gs.values[key], want)
		}
	}
	// Each setting is back to its value from before uas, so there is nothing left to revert.
	if saved, _ := w.in.Saved(); len(saved) != 0 {
		t.Errorf("Saved after undo = %+v", saved)
	}
	if _, _, err := w.in.UndoPreset(ctx, store); err == nil {
		t.Error("undid twice")
	}

	// A part that couldn't be set keeps the preset from being in use.
	if InUse(p, map[string]string{KeyCursorTheme: "Adwaita", KeyAccentColor: "teal"}) {
		t.Error("in use with a part skipped")
	}
	// Without the cursor and the Shell theme, and applied again with nothing failing, the preset is in use;
	// the cursor is left alone.
	delete(p.Parts, PartCursor)
	delete(p.Parts, PartShell)
	w.gs.values[KeyCursorTheme] = "Mine"
	plan, _ = w.in.PlanPreset(ctx, p, lookup)
	if out, err := w.in.ApplyPreset(ctx, store, p, plan, ApplyOptions{}); err != nil || Failed(out) != nil {
		t.Fatal(err, Failed(out))
	}
	if !InUse(p, w.in.Current(ctx)) || w.gs.values[KeyCursorTheme] != "Mine" {
		t.Errorf("in use %v, cursor %q", InUse(p, w.in.Current(ctx)), w.gs.values[KeyCursorTheme])
	}
}

func TestPlanWithoutInstalls(t *testing.T) {
	w, l := presetWorld(t)
	ctx := context.Background()
	p := Preset{Name: "x", Parts: map[string]Part{
		PartIcons:     {Value: "Papirus", From: &Origin{ID: "pling:1166289", Variant: "Papirus"}},
		PartMonospace: {Value: "JetBrains Mono 10", From: &Origin{ID: "google-fonts:jetbrains-mono"}},
		PartAccent:    {Value: "mauve"},
	}}
	// The catalog has neither; then the icons are skipped when installing is turned off.
	plan, _ := w.in.PlanPreset(ctx, p, nil)
	for _, it := range plan {
		if it.Step != StepMissing {
			t.Errorf("%s: %s", it.Part, it.Step)
		}
	}
	plan, _ = w.in.PlanPreset(ctx, p, func(id string) (catalog.Listing, bool) { return l, id == "pling:1166289" })
	out, err := w.in.ApplyPreset(ctx, testPresets(t), p, plan, ApplyOptions{NoInstall: true})
	if err != nil {
		t.Fatal(err)
	}
	for _, o := range out {
		if o.Done || o.Err != nil {
			t.Errorf("%s: %+v", o.Part, o)
		}
	}
	if recs, _ := w.in.Records(); len(recs) != 0 {
		t.Error("installed with installing turned off")
	}
	// Without fontconfig, a font is taken to be there.
	w.gs.fonts = ""
	plan, _ = w.in.PlanPreset(ctx, p, nil)
	if plan[2].Part != PartMonospace || plan[2].Step != StepSet {
		t.Errorf("without fontconfig: %+v", plan[2])
	}
	// On Plasma, the parts it can't set are skipped.
	w.in.Desktop = &Plasma{Runner: w.gs, ChangeIcons: "/bin/true"}
	plan, _ = w.in.PlanPreset(ctx, p, nil)
	if plan[0].Step != StepUnsupported || plan[2].Step != StepUnsupported {
		t.Errorf("on Plasma: %+v", plan)
	}
}

func TestIconFiles(t *testing.T) {
	root := t.TempDir()
	write := func(p, s string) {
		os.MkdirAll(filepath.Dir(filepath.Join(root, p)), 0o755)
		os.WriteFile(filepath.Join(root, p), []byte(s), 0o644)
	}
	write("Base/index.theme", "[Icon Theme]\nDirectories=16x16/places,48x48/places,scalable/apps\n")
	write("Base/16x16/places/folder.png", "png")
	write("Base/48x48/places/folder.png", "png")
	write("Base/scalable/apps/utilities-terminal.svg", "<svg/>")
	write("Dark/index.theme", "[Icon Theme]\nInherits=Base,hicolor\nDirectories=places/symbolic,places/32\n")
	write("Dark/places/symbolic/inode-directory.svg", "<svg/>")
	write("Dark/places/32/inode-directory.svg", "<svg/>")
	themes := []Theme{{Name: "Base", Dir: filepath.Join(root, "Base")}, {Name: "Dark", Dir: filepath.Join(root, "Dark")}}
	got := IconFiles(themes, "Dark", SampleIcons)
	want := []string{filepath.Join(root, "Dark/places/32/inode-directory.svg"),
		filepath.Join(root, "Base/scalable/apps/utilities-terminal.svg")}
	if !slices.Equal(got, want) {
		t.Errorf("IconFiles = %v", got)
	}
	if got := IconFiles(themes, "Base", [][]string{{"folder"}}); len(got) != 1 || !strings.Contains(got[0], "48x48") {
		t.Errorf("largest = %v", got)
	}
	if got := IconFiles(themes, "Nope", SampleIcons); got != nil {
		t.Errorf("unknown theme = %v", got)
	}
}
