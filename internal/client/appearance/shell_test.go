package appearance

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/godbus/dbus/v5"

	"github.com/tyler-builds/uas/internal/catalog"
	"github.com/tyler-builds/uas/internal/client/shellext"
	"github.com/tyler-builds/uas/internal/client/state"
)

// fakeShellBus is GNOME Shell's extension service, in memory: the extensions it knows and its main switch.
// Nothing reaches the real Shell.
type fakeShellBus struct {
	mu    sync.Mutex
	exts  map[string]map[string]any
	allOn bool
	// answer is what the install dialog answers: "successful" or "cancelled".
	answer string
	calls  []string
}

func (f *fakeShellBus) Call(_ context.Context, method string, args []any, out ...any) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	uuid := ""
	if len(args) > 0 {
		uuid, _ = args[0].(string)
	}
	f.calls = append(f.calls, method+" "+uuid)
	switch method {
	case "ListExtensions":
		list := map[string]map[string]dbus.Variant{}
		for id, e := range f.exts {
			list[id] = map[string]dbus.Variant{}
			for k, v := range e {
				list[id][k] = dbus.MakeVariant(v)
			}
		}
		*out[0].(*map[string]map[string]dbus.Variant) = list
	case "InstallRemoteExtension":
		if f.answer == "successful" {
			f.exts[uuid] = map[string]any{"uuid": uuid, "name": "User Themes", "type": 2.0, "state": 1.0,
				"enabled": true}
		}
		*out[0].(*string) = f.answer
	case "EnableExtension", "DisableExtension":
		e, ok := f.exts[uuid]
		if ok {
			e["enabled"] = method == "EnableExtension"
			e["state"] = 1.0
		}
		*out[0].(*bool) = ok
	default:
		return errors.New("unexpected " + method)
	}
	return nil
}

func (f *fakeShellBus) Get(_ context.Context, property string) (any, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if property == "UserExtensionsEnabled" {
		return f.allOn, nil
	}
	return nil, errors.New("unexpected " + property)
}

func (f *fakeShellBus) Set(_ context.Context, property string, value any) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, "set "+property)
	if property == "UserExtensionsEnabled" {
		f.allOn = value.(bool)
	}
	return nil
}

func (f *fakeShellBus) called(prefix string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.ContainsFunc(f.calls, func(c string) bool { return strings.HasPrefix(c, prefix) })
}

// userThemes is the system's User Themes extension, on or off.
func userThemesExt(on bool) map[string]any {
	return map[string]any{"uuid": UserThemesUUID, "name": "User Themes", "type": 1.0, "state": 1.0, "enabled": on}
}

// shellWorld is a world with Shell themes on the system and a fake GNOME Shell whose User Themes is on.
func shellWorld(t *testing.T) (*world, *fakeShellBus) {
	t.Helper()
	w := newWorld(t)
	w.gs.values["shell name"] = ""
	bus := &fakeShellBus{exts: map[string]map[string]any{UserThemesUUID: userThemesExt(true)}, allOn: true,
		answer: "successful"}
	w.in.Shell = &shellext.Manager{Bus: bus}
	w.in.Desktop = &GNOME{Runner: w.gs, ExtensionDirs: []string{filepath.Join(w.in.Data, "gnome-shell", "extensions")}}
	shell := func(dir string) map[string]string {
		return map[string]string{filepath.Join(dir, "gnome-shell", "gnome-shell.css"): "stage {}"}
	}
	writeFiles(t, shell(filepath.Join(w.in.Home, ".themes", "Nordic")))
	writeFiles(t, shell(filepath.Join(w.in.ThemesDir(), "Nordic"))) // hidden by ~/.themes, as User Themes looks
	writeFiles(t, shell(filepath.Join(w.in.ThemesDir(), "Orchis-Dark")))
	writeFiles(t, map[string]string{filepath.Join(w.in.ThemesDir(), "Orchis-Dark", "gtk-3.0", "gtk.css"): "*{}"})
	writeFiles(t, shell(filepath.Join(w.sysIcons, "themes", "Yaru")))
	writeFiles(t, shell(filepath.Join(w.sysIcons, "themes", "Orchis-Dark"))) // hidden by the user's
	// A GTK theme with no Shell theme, and a Shell style that isn't where User Themes looks.
	writeFiles(t, map[string]string{filepath.Join(w.sysIcons, "themes", "adw-gtk3", "gtk-3.0", "gtk.css"): "*{}"})
	writeFiles(t, map[string]string{filepath.Join(w.sysIcons, "themes", "Odd", "gnome-shell", "theme.css"): "x"})
	st, _ := w.in.State.Load()
	st.PutAppearance(state.AppearanceInstall{ID: "pling:1357889", Source: "pling", Ref: "1357889",
		Kind: string(catalog.KindGTKTheme), Name: "Orchis", Themes: []string{"Orchis-Dark"},
		Paths: []string{filepath.Join(w.in.ThemesDir(), "Orchis-Dark")}})
	w.in.State.Save(st)
	return w, bus
}

func TestShellThemeScan(t *testing.T) {
	w, _ := shellWorld(t)
	themes, err := w.in.ShellThemes()
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, th := range themes {
		got = append(got, th.Name+"|"+filepath.Base(filepath.Dir(th.Dir))+"|"+th.By)
	}
	want := []string{"Nordic|.themes|", "Orchis-Dark|themes|pling:1357889", "Yaru|themes|"}
	if !slices.Equal(got, want) {
		t.Errorf("ShellThemes = %v\nwant %v", got, want)
	}
	if !themes[1].User || themes[2].User || !themes[0].Shell {
		t.Errorf("themes = %+v", themes)
	}
	if !w.in.HasShellTheme("Yaru") || w.in.HasShellTheme("Odd") || w.in.HasShellTheme("adw-gtk3") ||
		w.in.HasShellTheme("../Yaru") {
		t.Error("HasShellTheme")
	}
	// A GTK theme says whether it has a Shell theme.
	gtk, _ := w.in.GTKThemes()
	for _, th := range gtk {
		if (th.Name == "Orchis-Dark") != th.Shell {
			t.Errorf("GTK theme %s: Shell %v", th.Name, th.Shell)
		}
	}
}

func TestShellUseRevert(t *testing.T) {
	w, bus := shellWorld(t)
	ctx := context.Background()
	if !w.in.UserThemes(ctx).Ready() {
		t.Fatal("User Themes isn't ready")
	}
	if err := w.in.Use(ctx, KeyShellTheme, "Nordic"); err != nil {
		t.Fatal(err)
	}
	if w.gs.values["shell name"] != "Nordic" || w.in.Current(ctx)[KeyShellTheme] != "Nordic" {
		t.Errorf("shell = %q", w.gs.values["shell name"])
	}
	if s, ok := savedKey(w.in, KeyShellTheme); !ok || s.Previous != "" || s.Value != "Nordic" {
		t.Errorf("saved = %+v %v", s, ok)
	}
	// A theme that isn't on the system is refused, as one User Themes would quietly not find.
	if err := w.in.Use(ctx, KeyShellTheme, "Missing"); err == nil || !strings.Contains(err.Error(), "no GNOME Shell theme") {
		t.Errorf("Use of a missing theme: %v", err)
	}
	if err := w.in.Revert(ctx, KeyShellTheme); err != nil {
		t.Fatal(err)
	}
	if w.gs.values["shell name"] != "" {
		t.Errorf("after revert, shell = %q", w.gs.values["shell name"])
	}
	if _, ok := savedKey(w.in, KeyShellTheme); ok {
		t.Error("still saved after revert")
	}

	// The Shell's own look is a value: using it after a theme goes back to where uas started, and forgets it.
	w.in.Use(ctx, KeyShellTheme, "Yaru")
	if err := w.in.Use(ctx, KeyShellTheme, ""); err != nil || w.gs.values["shell name"] != "" {
		t.Fatalf("Use default: %v, %q", err, w.gs.values["shell name"])
	}
	if _, ok := savedKey(w.in, KeyShellTheme); ok {
		t.Error("the default is saved for revert")
	}
	if ParseValue(KeyShellTheme, "Default") != "" || ValueLabel(KeyShellTheme, "") != "Default" ||
		KeyPart(KeyShellTheme) != PartShell || PartLabel(PartShell) != "Shell theme" {
		t.Error("names of the Shell theme")
	}

	// Revert all puts the Shell theme back with the rest.
	w.in.Use(ctx, KeyShellTheme, "Yaru")
	w.in.Use(ctx, KeyIconTheme, "Adwaita-Ish")
	if err := w.in.RevertAll(ctx); err != nil {
		t.Fatal(err)
	}
	if w.gs.values["shell name"] != "" {
		t.Errorf("after revert all, shell = %q", w.gs.values["shell name"])
	}

	// Removing a GTK theme whose Shell theme is in use reverts the Shell first.
	w.in.Use(ctx, KeyShellTheme, "Orchis-Dark")
	if err := w.in.Remove(ctx, "pling:1357889", nil); err != nil {
		t.Fatal(err)
	}
	if w.gs.values["shell name"] != "" || w.in.HasShellTheme("Orchis-Dark") != true {
		// The system's Orchis-Dark is still there, no longer hidden.
		t.Errorf("after removal, shell = %q", w.gs.values["shell name"])
	}
	if _, err := os.Stat(filepath.Join(w.in.ThemesDir(), "Orchis-Dark")); !os.IsNotExist(err) {
		t.Error("the theme is still there")
	}
	if bus.called("EnableExtension") || bus.called("InstallRemoteExtension") || bus.called("set ") {
		t.Errorf("the extension was changed: %v", bus.calls)
	}
}

func TestShellNeedsUserThemes(t *testing.T) {
	ctx := context.Background()
	for _, tc := range []struct {
		name   string
		exts   map[string]map[string]any
		allOn  bool
		why    string
		fix    string
		answer string
		calls  []string
	}{
		{"off", map[string]map[string]any{UserThemesUUID: userThemesExt(false)}, true, "which is off",
			"Turn on User Themes", "successful", []string{"EnableExtension " + UserThemesUUID}},
		{"missing", map[string]map[string]any{}, true, "isn't installed", "Install User Themes", "successful",
			[]string{"InstallRemoteExtension " + UserThemesUUID}},
		{"extensions off", map[string]map[string]any{UserThemesUUID: userThemesExt(true)}, false,
			"extensions are turned off", "Turn on extensions", "successful", []string{"set UserExtensionsEnabled"}},
		{"declined install", map[string]map[string]any{}, true, "isn't installed", "Install User Themes",
			"cancelled", []string{"InstallRemoteExtension " + UserThemesUUID}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w, bus := shellWorld(t)
			bus.exts, bus.allOn, bus.answer = tc.exts, tc.allOn, tc.answer
			u := w.in.UserThemes(ctx)
			if u.Ready() || !strings.Contains(u.Why(), tc.why) || u.Fix() != tc.fix {
				t.Fatalf("UserThemes = %+v: %q, %q", u, u.Why(), u.Fix())
			}
			// Use changes nothing, and says why; it never turns the extension on itself.
			err := w.in.Use(ctx, KeyShellTheme, "Nordic")
			var need *NeedsUserThemesError
			if !errors.As(err, &need) || need.UserThemes != u {
				t.Fatalf("Use = %v", err)
			}
			if w.gs.values["shell name"] != "" || len(bus.calls) == 0 ||
				slices.ContainsFunc(bus.calls, func(c string) bool { return !strings.HasPrefix(c, "ListExtensions") }) {
				t.Errorf("Use changed something: shell %q, calls %v", w.gs.values["shell name"], bus.calls)
			}
			if _, ok := savedKey(w.in, KeyShellTheme); ok {
				t.Error("a refused Use is saved")
			}
			// The Shell's own look needs no extension.
			if err := w.in.Use(ctx, KeyShellTheme, ""); err != nil {
				t.Errorf("Use default: %v", err)
			}
			// When the user asks, it is turned on (or installed, after GNOME's own question).
			err = w.in.EnableUserThemes(ctx)
			if tc.answer == "cancelled" {
				if !errors.Is(err, shellext.ErrCancelled) {
					t.Errorf("EnableUserThemes = %v", err)
				}
				return
			}
			if err != nil || !w.in.UserThemes(ctx).Ready() {
				t.Fatalf("EnableUserThemes = %v, %+v", err, w.in.UserThemes(ctx))
			}
			for _, c := range tc.calls {
				if !bus.called(c) {
					t.Errorf("calls %v, want %s", bus.calls, c)
				}
			}
			if bus.called("DisableExtension") || (tc.allOn && bus.called("set ")) {
				t.Errorf("turned something else: %v", bus.calls)
			}
			if err := w.in.Use(ctx, KeyShellTheme, "Nordic"); err != nil || w.gs.values["shell name"] != "Nordic" {
				t.Errorf("Use after enabling: %v", err)
			}
		})
	}

	// A broken extension can't be fixed by turning it on.
	w, bus := shellWorld(t)
	bus.exts[UserThemesUUID]["state"] = 4.0 // not made for this Shell
	if u := w.in.UserThemes(ctx); u.Ready() || u.Fix() != "" || !strings.Contains(u.Why(), "isn't working") {
		t.Errorf("out of date: %+v %q", u, u.Why())
	}
	// Outside a GNOME session nothing is known, and nothing is stopped.
	w.in.Shell = nil
	if u := w.in.UserThemes(ctx); u.Known || !u.Ready() {
		t.Errorf("without the Shell: %+v", u)
	}
	if err := w.in.EnableUserThemes(ctx); err == nil {
		t.Error("enabled without a Shell")
	}
}

func TestShellSchema(t *testing.T) {
	ctx := context.Background()
	// User Themes installed for the user: its schema is in its own folder, which gsettings reads with
	// --schemadir.
	w, _ := shellWorld(t)
	w.gs.shellSchema = "user"
	g := w.in.Desktop.(*GNOME)
	if _, err := g.Get(ctx, KeyShellTheme); err == nil || !strings.Contains(err.Error(), "User Themes") {
		t.Errorf("Get without the schema: %v", err)
	}
	dir := filepath.Join(g.ExtensionDirs[0], UserThemesUUID, "schemas")
	writeFiles(t, map[string]string{filepath.Join(dir, "gschemas.compiled"): "GVariant"})
	if err := g.Set(ctx, KeyShellTheme, "Nordic"); err != nil {
		t.Fatal(err)
	}
	if v, err := g.Get(ctx, KeyShellTheme); err != nil || v != "Nordic" {
		t.Errorf("Get = %q, %v", v, err)
	}
	want := "gsettings --schemadir " + dir + " set " + userThemesSchema + " name 'Nordic'"
	if !slices.Contains(w.gs.calls, want) {
		t.Errorf("calls %v\nwant %s", w.gs.calls, want)
	}
	// Installed with the system: no --schemadir.
	w2, _ := shellWorld(t)
	w2.in.Desktop.Get(ctx, KeyShellTheme)
	for _, c := range w2.gs.calls {
		if strings.Contains(c, "--schemadir") {
			t.Errorf("schemadir with the system's schema: %s", c)
		}
	}
	// Plasma doesn't take a Shell theme, and says so as for other settings.
	p := &Plasma{Runner: w.gs, ChangeIcons: "/bin/true"}
	w.in.Desktop = p
	if p.Supports(KeyShellTheme) || w.in.Use(ctx, KeyShellTheme, "Nordic") != ErrUnsupported {
		t.Error("Plasma takes a Shell theme")
	}
}

func TestShellPreset(t *testing.T) {
	w, bus := shellWorld(t)
	ctx := context.Background()
	store := testPresets(t)

	// Capture saves the theme by name, with where it came from; the Shell's own look is a part too.
	w.in.Use(ctx, KeyShellTheme, "Orchis-Dark")
	parts, err := w.in.Capture(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if sh := parts[PartShell]; sh.Value != "Orchis-Dark" || sh.From == nil || sh.From.ID != "pling:1357889" ||
		sh.From.Variant != "Orchis-Dark" {
		t.Errorf("captured shell = %+v", sh)
	}
	b, _ := json.Marshal(Part{})
	if string(b) != "{}" {
		t.Errorf("the default look's part = %s", b)
	}
	w.in.Revert(ctx, KeyShellTheme)

	// Applying sets the GTK theme, then the Shell theme, then the cursor: PartOrder.
	p := Preset{Name: "Night", Parts: map[string]Part{PartShell: {Value: "Nordic"}, PartGTK: {Value: "Orchis-Dark"},
		PartCursor: {Value: "Adwaita"}}}
	w.gs.values[KeyCursorTheme] = "Other"
	plan, err := w.in.PlanPreset(ctx, p, nil)
	if err != nil {
		t.Fatal(err)
	}
	var steps []string
	for _, it := range plan {
		steps = append(steps, it.Part+":"+string(it.Step))
	}
	if !slices.Equal(steps, []string{"gtk:set", "shell:set", "cursor:set"}) {
		t.Fatalf("plan = %v", steps)
	}
	w.gs.calls = nil
	if out, err := w.in.ApplyPreset(ctx, store, p, plan, ApplyOptions{}); err != nil || Failed(out) != nil {
		t.Fatal(err, Failed(out))
	}
	var sets []string
	for _, c := range w.gs.calls {
		if strings.Contains(c, " set ") {
			sets = append(sets, strings.Fields(c)[3])
		}
	}
	if !slices.Equal(sets, []string{"gtk-theme", "name", "cursor-theme"}) {
		t.Errorf("set order = %v", sets)
	}
	if !InUse(p, w.in.Current(ctx)) {
		t.Error("not in use after apply")
	}
	// Undo goes back to the Shell's own look.
	if _, out, err := w.in.UndoPreset(ctx, store); err != nil || Failed(out) != nil {
		t.Fatal(err, Failed(out))
	}
	if w.gs.values["shell name"] != "" || w.gs.values[KeyCursorTheme] != "Other" {
		t.Errorf("after undo, shell %q, cursor %q", w.gs.values["shell name"], w.gs.values[KeyCursorTheme])
	}

	// With User Themes off, the part waits for the user's say: skipped without it, nothing turned on.
	bus.exts[UserThemesUUID]["enabled"] = false
	plan, _ = w.in.PlanPreset(ctx, p, nil)
	if plan[1].Step != StepUserThemes || !plan[1].Step.Skipped() || !strings.Contains(plan[1].Note, "which is off") {
		t.Fatalf("plan with User Themes off = %+v", plan[1])
	}
	out, err := w.in.ApplyPreset(ctx, store, p, plan, ApplyOptions{})
	if err != nil || Failed(out) != nil || out[1].Done || w.gs.values["shell name"] != "" || bus.called("EnableExtension") {
		t.Errorf("applied without the user's say: %+v, shell %q, calls %v", out[1], w.gs.values["shell name"], bus.calls)
	}
	if out[0].Done != true || out[2].Done != true {
		t.Errorf("the other parts: %+v", out)
	}
	// With it, the extension is turned on first, then the theme set.
	out, err = w.in.ApplyPreset(ctx, store, p, plan, ApplyOptions{UserThemes: true})
	if err != nil || Failed(out) != nil || !out[1].Done || w.gs.values["shell name"] != "Nordic" ||
		!bus.called("EnableExtension") {
		t.Errorf("applied with the user's say: %+v %v, shell %q", out[1], Failed(out), w.gs.values["shell name"])
	}
	if plan[1].Step != StepUserThemes {
		t.Error("ApplyPreset changed the caller's plan")
	}
	// The Shell's own look needs no extension: with it off, the Shell has it already.
	bus.exts[UserThemesUUID]["enabled"] = false
	plan, _ = w.in.PlanPreset(ctx, Preset{Name: "Plain", Parts: map[string]Part{PartShell: {}}}, nil)
	if plan[0].Step != StepSame {
		t.Errorf("default with User Themes off: %+v", plan[0])
	}
}

func TestShellPresetInstall(t *testing.T) {
	// A Shell theme that came with a GTK theme of the catalog is installed with it, then set.
	w, l := gtkWorld(t)
	ctx := context.Background()
	bus := &fakeShellBus{exts: map[string]map[string]any{UserThemesUUID: userThemesExt(true)}, allOn: true}
	w.in.Shell = &shellext.Manager{Bus: bus}
	w.gs.values["shell name"] = ""
	p := Preset{Name: "Orchis", Parts: map[string]Part{
		PartShell: {Value: "Orchis-Dark", From: &Origin{ID: "pling:1357889", Variant: "Orchis-Dark"}}}}
	lookup := func(id string) (catalog.Listing, bool) { return l, id == "pling:1357889" }
	plan, err := w.in.PlanPreset(ctx, p, lookup)
	if err != nil || len(plan) != 1 || plan[0].Step != StepInstall || plan[0].Variant != "Orchis-Dark" {
		t.Fatalf("plan = %+v %v", plan, err)
	}
	out, err := w.in.ApplyPreset(ctx, testPresets(t), p, plan, ApplyOptions{})
	if err != nil {
		t.Fatal(err)
	}
	// gtkPack's themes have no gnome-shell folder: the install works, and the part says why it wasn't set.
	if out[0].Err == nil || !strings.Contains(out[0].Err.Error(), "doesn't have a GNOME Shell theme") || w.gs.values["shell name"] != "" {
		t.Errorf("outcome = %+v", out[0])
	}
	// The catalog has nothing for a part from elsewhere.
	p.Parts[PartShell] = Part{Value: "Nope", From: &Origin{ID: "pling:1"}}
	plan, _ = w.in.PlanPreset(ctx, p, lookup)
	if plan[0].Step != StepMissing || !strings.Contains(plan[0].Note, "no longer has it") {
		t.Errorf("plan = %+v", plan[0])
	}
}

func TestShellPresetFiles(t *testing.T) {
	// A shell part a hand wrote in the reserved format, the Shell's own look, and a file from before Shell
	// themes read and write back as they were.
	s := testPresets(t)
	os.MkdirAll(s.Dir, 0o755)
	files := map[string]string{
		"hand.json": `{"uas_preset":1,"name":"Hand","created_at":"2026-11-01T20:00:00Z","updated_at":"2026-11-01T20:00:00Z",` +
			`"parts":{"shell":{"value":"Nordic","later":true}}}`,
		"plain.json": `{"uas_preset":1,"name":"Plain","created_at":"2026-11-01T20:00:00Z","updated_at":"2026-11-01T20:00:00Z",` +
			`"parts":{"shell":{},"accent":{"value":"teal"}}}`,
		"old.json": `{"uas_preset":1,"name":"Old","created_at":"2026-11-01T20:00:00Z","updated_at":"2026-11-01T20:00:00Z",` +
			`"parts":{"gtk":{"value":"Orchis-Dark"},"icons":{"value":"Papirus"}}}`,
	}
	for name, body := range files {
		path := filepath.Join(s.Dir, name)
		os.WriteFile(path, []byte(body), 0o644)
		p, err := readPreset(path)
		if err != nil {
			t.Fatal(err)
		}
		if err := s.write(&p); err != nil {
			t.Fatal(err)
		}
		var a, z any
		b, _ := os.ReadFile(path)
		json.Unmarshal(b, &a)
		json.Unmarshal([]byte(body), &z)
		ab, _ := json.Marshal(a)
		zb, _ := json.Marshal(z)
		if string(ab) != string(zb) {
			t.Errorf("%s rewritten as\n%s", name, b)
		}
	}
	if p, _ := s.Get("Hand"); p.Parts[PartShell].Value != "Nordic" {
		t.Errorf("Hand = %+v", p.Parts)
	}
	if p, _ := s.Get("Plain"); !p.Sets(PartShell) || p.Parts[PartShell].Value != "" {
		t.Errorf("Plain = %+v", p.Parts)
	}
	if p, _ := s.Get("Old"); p.Sets(PartShell) {
		t.Error("an old preset sets the Shell theme")
	}
}
