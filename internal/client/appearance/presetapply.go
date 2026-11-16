package appearance

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"

	looks "github.com/tyler-builds/uas/internal/appearance"
	"github.com/tyler-builds/uas/internal/catalog"
	"github.com/tyler-builds/uas/internal/client/backend"
	"github.com/tyler-builds/uas/internal/client/state"
)

// Capture reads the desktop's settings as preset parts: every setting the desktop supports, with where to
// install it from when uas installed it, and the wallpaper, whose picture is copied into the store when it
// is the user's own (see WallpaperRef).
func (in *Installer) Capture(ctx context.Context) (map[string]Part, error) {
	if in.Desktop == nil {
		return nil, ErrUnsupported
	}
	recs, err := in.Records()
	if err != nil {
		return nil, err
	}
	parts := map[string]Part{}
	var errs []error
	for _, key := range SettingKeys {
		if IsWallpaperKey(key) || !in.Desktop.Supports(key) {
			continue
		}
		v, err := in.Desktop.Get(ctx, key)
		if err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", KeyWord(key), err))
			continue
		}
		if v == "" && !EmptyValue(key) {
			continue
		}
		parts[KeyPart(key)] = Part{Value: v, From: OriginOf(recs, key, v)}
	}
	if w, ok, err := in.captureWallpaper(ctx); err != nil {
		errs = append(errs, fmt.Errorf("wallpaper: %w", err))
	} else if ok {
		parts[PartWallpaper] = w
	}
	if len(parts) == 0 {
		return nil, errors.Join(append(errs, errors.New("couldn't read any of the desktop's settings"))...)
	}
	return parts, nil
}

// OriginOf is the catalog item uas installed a setting's value from, for a preset part, or nil.
func OriginOf(recs []state.AppearanceInstall, key, value string) *Origin {
	for _, r := range recs {
		if !slices.Contains(keysOf(r), key) || !inUse(r, key, value) {
			continue
		}
		o := &Origin{ID: r.ID}
		if r.Kind != string(catalog.KindFont) {
			o.Variant = value
		}
		return o
	}
	return nil
}

// Families lists the font families fontconfig knows, by their first name, sorted. It returns nil when
// fontconfig can't be asked.
func (in *Installer) Families(ctx context.Context) []string {
	primary, _ := in.fontFamilies(ctx)
	return primary
}

// fontFamilies asks fontconfig for the families it knows: by their first name, and by every name a font
// gives itself ("Fira Code Light" as well as "Fira Code"). Both are nil when fontconfig can't be asked.
func (in *Installer) fontFamilies(ctx context.Context) (primary, all []string) {
	if in.Runner == nil {
		return nil, nil
	}
	out, err := in.Runner.Run(ctx, "fc-list", []string{":", "family"}, nil)
	if err != nil {
		return nil, nil
	}
	seen, seenAll := map[string]bool{}, map[string]bool{}
	for _, line := range strings.Split(out, "\n") {
		for i, f := range strings.Split(line, ",") {
			f = strings.TrimSpace(strings.ReplaceAll(f, `\-`, "-"))
			if f == "" {
				continue
			}
			if l := strings.ToLower(f); !seenAll[l] {
				seenAll[l] = true
				all = append(all, f)
			}
			if l := strings.ToLower(f); i == 0 && !seen[l] {
				seen[l] = true
				primary = append(primary, f)
			}
		}
	}
	byName := func(a, b string) int { return strings.Compare(strings.ToLower(a), strings.ToLower(b)) }
	slices.SortFunc(primary, byName)
	slices.SortFunc(all, byName)
	return primary, all
}

// Step is what applying a preset does with one of its parts.
type Step string

const (
	StepSet         Step = "set"         // it is on the system, and is set
	StepSame        Step = "same"        // the setting has it already
	StepInstall     Step = "install"     // it is installed from the catalog first, then set
	StepMissing     Step = "missing"     // it isn't on the system and can't be installed, so it is skipped
	StepUnsupported Step = "unsupported" // the desktop can't change the setting from uas, so it is skipped
	StepUnknown     Step = "unknown"     // a part this version of uas can't apply, so it is skipped
	// StepUserThemes is a Shell theme while the User Themes extension isn't running: it is skipped unless
	// the user agrees to turn the extension on (ApplyOptions.UserThemes). Listing and Variant say what is
	// installed first then.
	StepUserThemes Step = "user-themes"
)

// Skipped reports whether the step leaves the setting as it is.
func (s Step) Skipped() bool {
	return s == StepMissing || s == StepUnsupported || s == StepUnknown || s == StepUserThemes
}

// PlanItem is what applying a preset does with one part.
type PlanItem struct {
	Part  string
	Key   string // the setting, or "" for StepUnknown
	Value string
	Step  Step
	// Listing and Variant are what is installed, for StepInstall.
	Listing catalog.Listing
	Variant string
	// Wallpaper is what the wallpaper is set to, by path, for the wallpaper part; Value is its picture.
	Wallpaper Wallpaper
	// Note explains a skipped part, or for StepInstall where it comes from.
	Note string
}

// Lookup returns a font's or theme's catalog listing by its catalog ID, and false when the catalog doesn't
// have it.
type Lookup func(id string) (catalog.Listing, bool)

// PlanPreset decides what applying a preset does with each of its parts, in PartOrder.
func (in *Installer) PlanPreset(ctx context.Context, p Preset, lookup Lookup) ([]PlanItem, error) {
	return in.plan(ctx, p, lookup, true)
}

// plan is PlanPreset. Without check, every value is taken to be on the system, as the values of a look the
// desktop had are.
func (in *Installer) plan(ctx context.Context, p Preset, lookup Lookup, check bool) ([]PlanItem, error) {
	if in.Desktop == nil {
		return nil, ErrUnsupported
	}
	if lookup == nil {
		lookup = func(string) (catalog.Listing, bool) { return catalog.Listing{}, false }
	}
	themes, err := in.Themes()
	if err != nil {
		return nil, err
	}
	gtkThemes, err := in.GTKThemes()
	if err != nil {
		return nil, err
	}
	gtk4, _ := in.GTK4()
	recs, _ := in.Records()
	var userThemes *UserThemes // asked only for a preset with a Shell theme
	var fams []string
	famsRead := false
	var items []PlanItem
	for _, name := range p.PartNames() {
		part := p.Parts[name]
		it := PlanItem{Part: name, Value: part.Value}
		key, ok := PartKey(name)
		switch {
		case !ok:
			it.Step, it.Note = StepUnknown, "a newer version of uas sets this"
			items = append(items, it)
			continue
		case !in.Desktop.Supports(key):
			it.Key, it.Step, it.Note = key, StepUnsupported, "this desktop's setting can't be changed from uas"
			items = append(items, it)
			continue
		}
		it.Key = key
		if name == PartWallpaper {
			items = append(items, in.planWallpaper(ctx, it, part))
			continue
		}
		if name == PartShell {
			if userThemes == nil {
				u := in.UserThemes(ctx)
				userThemes = &u
			}
			items = append(items, in.planShell(ctx, it, part, lookup, check, *userThemes))
			continue
		}
		if err := CheckValue(key, part.Value); err != nil {
			it.Step, it.Note = StepMissing, err.Error()
			items = append(items, it)
			continue
		}
		cur, err := in.Desktop.Get(ctx, key)
		if err == nil && sameValue(key, cur, part.Value) && !in.gtk4Due(key, part, gtk4) {
			it.Step = StepSame
			items = append(items, it)
			continue
		}
		present := !check
		switch {
		case present:
		case key == KeyIconTheme || key == KeyCursorTheme:
			present = slices.ContainsFunc(themes, func(t Theme) bool {
				return t.Name == part.Value && ((key == KeyIconTheme && t.Icons) || (key == KeyCursorTheme && t.Cursors))
			})
		case key == KeyGTKTheme:
			present = slices.Contains(builtinGTKThemes, part.Value) ||
				slices.ContainsFunc(gtkThemes, func(t Theme) bool { return t.Name == part.Value })
		case slices.Contains(FontKeys, key):
			if !famsRead {
				_, fams = in.fontFamilies(ctx)
				famsRead = true
			}
			fam := FontFamily(part.Value)
			present = fams == nil || slices.ContainsFunc(fams, func(f string) bool { return strings.EqualFold(f, fam) }) ||
				slices.ContainsFunc(recs, func(r state.AppearanceInstall) bool {
					return slices.ContainsFunc(r.Families, func(f string) bool { return strings.EqualFold(f, fam) })
				})
		default:
			present = true
		}
		if present {
			it.Step = StepSet
			items = append(items, it)
			continue
		}
		it.Step = StepMissing
		it.Note = "isn't on this system"
		if part.From != nil {
			if l, ok := lookup(part.From.ID); ok && installable(l, key, part) {
				it.Step, it.Listing = StepInstall, l
				if l.Kind != catalog.KindFont {
					it.Variant = part.From.Variant
					if it.Variant == "" {
						it.Variant = part.Value
					}
				}
				it.Note = installNote(l, it.Variant)
			} else {
				it.Note = "isn't on this system, and the catalog no longer has it"
			}
		} else {
			it.Note = "isn't on this system; it came with the system it was saved on"
		}
		items = append(items, it)
	}
	return items, nil
}

// planShell decides what applying does with a Shell theme part. The Shell's own look needs nothing: without
// User Themes running the Shell has it anyway. A theme is set when it is on the system, installed first when
// it came with a GTK theme of the catalog, and in either case waits for the user's say (StepUserThemes) while
// User Themes isn't running.
func (in *Installer) planShell(ctx context.Context, it PlanItem, part Part, lookup Lookup, check bool,
	u UserThemes) PlanItem {
	cur, err := in.Desktop.Get(ctx, KeyShellTheme)
	if part.Value == "" {
		it.Step = StepSet
		if err != nil || cur == "" || !u.Ready() {
			it.Step = StepSame
		}
		return it
	}
	if !looks.ThemeName.MatchString(part.Value) {
		it.Step, it.Note = StepMissing, fmt.Sprintf("%q isn't a theme name uas takes", part.Value)
		return it
	}
	if err == nil && cur == part.Value && u.Ready() {
		it.Step = StepSame
		return it
	}
	it.Step = StepSet
	if check && !in.HasShellTheme(part.Value) {
		it.Step, it.Note = StepMissing, "isn't on this system"
		switch {
		case part.From == nil:
			it.Note = "isn't on this system; it came with the system it was saved on"
		default:
			if l, ok := lookup(part.From.ID); ok && installable(l, KeyShellTheme, part) {
				it.Step, it.Listing, it.Variant = StepInstall, l, part.From.Variant
				if it.Variant == "" {
					it.Variant = part.Value
				}
				it.Note = installNote(l, it.Variant)
			} else {
				it.Note = "isn't on this system, and the catalog no longer has it"
			}
		}
	}
	if !it.Step.Skipped() && !u.Ready() {
		it.Step, it.Note = StepUserThemes, u.Why()
	}
	return it
}

// planWallpaper decides what applying does with a wallpaper part: it is skipped when a picture can't be
// found.
func (in *Installer) planWallpaper(ctx context.Context, it PlanItem, part Part) PlanItem {
	it.Value = part.Light
	switch {
	case part.Light == "":
		it.Step, it.Note = StepMissing, "it has no picture"
		return it
	case part.Fit != "" && !slices.Contains(WallpaperFits, part.Fit):
		it.Step, it.Note = StepMissing, fmt.Sprintf("%q isn't a fit uas knows", part.Fit)
		return it
	}
	w, missing := in.PartWallpaper(part)
	if missing != "" {
		it.Value = missing
		it.Step, it.Note = StepMissing, "the picture can't be found"
		if missing != part.Light {
			it.Note = "the dark picture can't be found"
		}
		return it
	}
	it.Value, it.Wallpaper, it.Step = w.Light, w, StepSet
	if cur, err := in.CurrentWallpaper(ctx); err == nil && sameWallpaper(cur, w) {
		it.Step = StepSame
	}
	return it
}

// gtk4Due reports whether a GTK theme part that the setting already has still has files to copy into
// ~/.config/gtk-4.0, which uas hasn't: applying it then does that, so it is set rather than already so.
func (in *Installer) gtk4Due(key string, part Part, have *state.GTK4Files) bool {
	if key != KeyGTKTheme || (part.GTK4 != nil && !*part.GTK4) {
		return false
	}
	return (have == nil || have.Theme != part.Value) && in.HasGTK4Files(part.Value)
}

// sameWallpaper reports whether the desktop's wallpaper is w, by path, leaving out what the desktop doesn't
// have.
func sameWallpaper(cur, w Wallpaper) bool {
	return cur.Light == w.Light && (cur.Dark == "" || cur.Dark == w.DarkOrLight()) &&
		(cur.Fit == "" || cur.Fit == w.Fit)
}

// sameValue reports whether a setting's value is a part's: for a font given without a size, the same
// family.
func sameValue(key, cur, want string) bool {
	if cur == want {
		return true
	}
	if slices.Contains(FontKeys, key) && FontSize(want) == "" {
		return strings.EqualFold(FontFamily(cur), FontFamily(want))
	}
	return false
}

// installable reports whether a listing can be installed for a part.
func installable(l catalog.Listing, key string, part Part) bool {
	switch {
	case slices.Contains(FontKeys, key):
		return l.Kind == catalog.KindFont
	case key == KeyIconTheme && l.Kind != catalog.KindIconTheme, key == KeyCursorTheme && l.Kind != catalog.KindCursorTheme,
		(key == KeyGTKTheme || key == KeyShellTheme) && l.Kind != catalog.KindGTKTheme:
		return false
	}
	v := part.Value
	if part.From != nil && part.From.Variant != "" {
		v = part.From.Variant
	}
	_, ok := AssetFor(l, v)
	return ok
}

// installNote says where an install comes from and how large it is.
func installNote(l catalog.Listing, variant string) string {
	note := "from " + l.Source.Name()
	var size int64
	if l.Kind == catalog.KindFont {
		for _, a := range l.Assets {
			size += a.Size
		}
	} else if a, ok := AssetFor(l, variant); ok {
		size = a.Size
	}
	if size > 0 {
		note += ", " + humanSize(size)
	}
	return note
}

// Downloads are the plan's installs: one for each catalog item, however many parts it is for.
func Downloads(plan []PlanItem) []PlanItem {
	var out []PlanItem
	for _, it := range plan {
		if it.Step == StepInstall && !slices.ContainsFunc(out, func(o PlanItem) bool {
			return o.Listing.Source == it.Listing.Source && o.Listing.Ref == it.Listing.Ref && o.Variant == it.Variant
		}) {
			out = append(out, it)
		}
	}
	return out
}

// Outcome is what applying did with one part.
type Outcome struct {
	PlanItem
	// Done is true when the setting now has the part's value.
	Done bool
	// Err is why it wasn't set, when installing or setting it failed.
	Err error
}

// ApplyOptions change how a preset is applied.
type ApplyOptions struct {
	// NoInstall skips the parts that would be installed first.
	NoInstall bool
	// UserThemes is the user's yes to getting the User Themes extension running (EnableUserThemes) for a
	// Shell theme part that needs it (StepUserThemes); without it such a part is skipped.
	UserThemes bool
	// Install installs a listing; nil is Installer.Install. The session passes one that reports it.
	Install func(ctx context.Context, l catalog.Listing, variant string,
		progress func(backend.Progress)) (state.AppearanceInstall, error)
	Progress func(backend.Progress)
}

// ApplyPreset applies a preset by its plan. What has to be installed is installed first, before anything
// changes; a part whose install fails is skipped. Then the values the apply is about to change are saved
// for UndoPreset, and every part is set in turn. Setting goes on past a part that fails: the outcomes say
// what happened to each. Every change goes through Use, so Revert still goes back to the values from
// before uas. The error is for an apply that couldn't start, when nothing was changed.
func (in *Installer) ApplyPreset(ctx context.Context, store *Presets, p Preset, plan []PlanItem,
	opt ApplyOptions) ([]Outcome, error) {
	if in.Desktop == nil {
		return nil, ErrUnsupported
	}
	progress := opt.Progress
	if progress == nil {
		progress = func(backend.Progress) {}
	}
	install := opt.Install
	if install == nil {
		install = in.Install
	}
	plan = slices.Clone(plan)
	out := make([]Outcome, len(plan))
	// A Shell theme that waits for User Themes: turned on when the user said so, before anything else.
	if opt.UserThemes && slices.ContainsFunc(plan, func(it PlanItem) bool { return it.Step == StepUserThemes }) {
		progress(backend.Progress{Message: "Turning on User Themes…"})
		err := in.EnableUserThemes(ctx)
		for i, it := range plan {
			if it.Step != StepUserThemes {
				continue
			}
			switch {
			case err != nil:
				out[i].Err = fmt.Errorf("turning on User Themes: %w", err)
			case it.Listing.Ref != "":
				plan[i].Step, plan[i].Note = StepInstall, installNote(it.Listing, it.Variant)
			default:
				plan[i].Step, plan[i].Note = StepSet, ""
			}
		}
	}
	installed := map[string]state.AppearanceInstall{} // by catalog ID and variant
	for i, it := range plan {
		out[i].PlanItem = it
		if it.Step != StepInstall || out[i].Err != nil {
			continue
		}
		if opt.NoInstall {
			out[i].Step, out[i].Note = StepMissing, "isn't on this system, and installing was turned off"
			continue
		}
		id := catalog.AppearanceAppID(it.Listing.Source, it.Listing.Ref)
		rec, ok := installed[id+"\x00"+it.Variant]
		if !ok {
			progress(backend.Progress{Message: "Installing " + it.Listing.Name + "…"})
			var err error
			if rec, err = install(ctx, it.Listing, it.Variant, progress); err != nil {
				out[i].Err = fmt.Errorf("installing %s: %w", it.Listing.Name, err)
				continue
			}
			installed[id+"\x00"+it.Variant] = rec
		}
		// What the install calls its font or theme is what the setting takes.
		switch {
		case slices.Contains(FontKeys, it.Key) && len(rec.Families) > 0 &&
			!slices.ContainsFunc(rec.Families, func(f string) bool { return strings.EqualFold(f, FontFamily(it.Value)) }):
			if FontSize(it.Value) != "" {
				out[i].Value = FontValue(it.Value, rec.Families[0])
			} else {
				out[i].Value = rec.Families[0]
			}
		case !slices.Contains(FontKeys, it.Key) && !slices.Contains(rec.Themes, it.Value):
			out[i].Err = fmt.Errorf("%s doesn't have a theme called %s", it.Listing.Name, it.Value)
		case it.Key == KeyShellTheme && !in.HasShellTheme(it.Value):
			out[i].Err = fmt.Errorf("%s doesn't have a GNOME Shell theme", it.Value)
		}
	}

	// The look to go back to: the values of every setting about to change.
	prev := Preset{Format: PresetFormat, Name: "Previous look", Parts: map[string]Part{}}
	recs, _ := in.Records()
	gtk4, _ := in.GTK4()
	for _, o := range out {
		if o.Err != nil || (o.Step != StepSet && o.Step != StepInstall) {
			continue
		}
		if o.Part == PartWallpaper {
			cur, err := in.CurrentWallpaper(ctx)
			if err != nil {
				return nil, fmt.Errorf("reading the wallpaper: %w", err)
			}
			if cur.Light != "" {
				prev.Parts[o.Part] = in.snapshotPart(cur)
			}
			continue
		}
		cur, err := in.Desktop.Get(ctx, o.Key)
		if err != nil {
			return nil, fmt.Errorf("reading %s: %w", KeyWord(o.Key), err)
		}
		if cur == "" && !EmptyValue(o.Key) {
			continue
		}
		pp := Part{Value: cur, From: OriginOf(recs, o.Key, cur)}
		if o.Key == KeyGTKTheme && gtk4 == nil {
			// uas had no files in ~/.config/gtk-4.0: Undo takes its files away and puts the user's back,
			// rather than copying in the earlier theme's.
			pp.GTK4 = new(bool)
		}
		prev.Parts[o.Part] = pp
	}
	if len(prev.Parts) > 0 && store != nil {
		u := Undo{Applied: p.Name, At: store.now(), Look: prev}
		if r, ok, _ := store.Remembered(); ok {
			u.Remembered = &r
		}
		if err := store.saveUndo(u); err != nil {
			return nil, fmt.Errorf("saving the look to undo to: %w", err)
		}
	}

	for i, o := range out {
		if o.Err != nil || (o.Step != StepSet && o.Step != StepInstall) {
			out[i].Done = o.Step == StepSame
			continue
		}
		progress(backend.Progress{Message: "Setting " + KeyWord(o.Key) + "…"})
		if o.Part == PartWallpaper {
			if err := in.setWallpaper(ctx, o.Wallpaper); err != nil {
				out[i].Err = err
				continue
			}
			out[i].Done = true
			continue
		}
		gtk4 := p.Parts[o.Part].GTK4 == nil || *p.Parts[o.Part].GTK4
		if err := in.use(ctx, o.Key, o.Value, gtk4); err != nil {
			out[i].Err = err
			continue
		}
		out[i].Done = true
	}
	// The preset is the one in use now, with the look it gave (an Undo's look isn't a preset).
	// Not knowing it only loses the Changed mark, so a failure to write it doesn't fail the apply.
	if store != nil && slices.ContainsFunc(out, func(o Outcome) bool { return o.Done }) {
		_ = in.RememberPreset(ctx, store, p)
	}
	return out, nil
}

// Failed returns the errors of the parts that failed, each named.
func Failed(outcomes []Outcome) error {
	var errs []error
	for _, o := range outcomes {
		if o.Err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", PartWord(o.Part), o.Err))
		}
	}
	return errors.Join(errs...)
}

// UndoPreset goes back to the look from before the last apply of a preset, and forgets it. It installs
// nothing: a part whose font or theme is gone since is skipped. The look is kept when a part failed, so
// Undo can be tried again.
func (in *Installer) UndoPreset(ctx context.Context, store *Presets) (Undo, []Outcome, error) {
	u, ok, err := store.Undo()
	if err != nil {
		return Undo{}, nil, err
	}
	if !ok {
		return Undo{}, nil, errors.New("there is nothing to undo: no preset was applied since the last undo")
	}
	plan, err := in.plan(ctx, u.Look, nil, false)
	if err != nil {
		return u, nil, err
	}
	out, err := in.ApplyPreset(ctx, nil, u.Look, plan, ApplyOptions{NoInstall: true})
	if err != nil {
		return u, out, err
	}
	if Failed(out) == nil {
		// The preset that was in use before the apply is again (or none, when there was none).
		if err = store.remember(u.Remembered); err == nil {
			err = store.clearUndo()
		}
	}
	return u, out, err
}

// InUse reports whether the desktop's settings (as Current returns them) are a preset's: every part it has
// that the desktop supports has its value, and there is at least one.
func InUse(p Preset, current map[string]string) bool {
	n := 0
	for name, part := range p.Parts {
		key, ok := PartKey(name)
		if !ok {
			continue
		}
		cur, ok := current[key]
		if !ok {
			continue
		}
		if name == PartWallpaper {
			if !wallpaperInUse(part, current) {
				return false
			}
			n++
			continue
		}
		if !sameValue(key, cur, part.Value) {
			return false
		}
		n++
	}
	return n > 0
}

// wallpaperInUse reports whether the desktop's wallpaper settings (as Current returns them) are a wallpaper
// part's.
func wallpaperInUse(part Part, current map[string]string) bool {
	if !sameRef(current[KeyWallpaper], part.Light) {
		return false
	}
	if dark, ok := current[KeyWallpaperDark]; ok && dark != "" {
		want := part.Dark
		if want == "" {
			want = part.Light
		}
		if !sameRef(dark, want) {
			return false
		}
	}
	fit := part.Fit
	if fit == "" {
		fit = FitZoom
	}
	cur, ok := current[KeyWallpaperFit]
	return !ok || cur == "" || cur == fit
}
