package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"text/tabwriter"

	"github.com/tyler-builds/uas/internal/catalog"
	"github.com/tyler-builds/uas/internal/client/appearance"
	"github.com/tyler-builds/uas/internal/client/backend"
	"github.com/tyler-builds/uas/internal/client/paths"
	"github.com/tyler-builds/uas/internal/client/session"
)

const presetUsage = `usage: uas appearance preset <command>

A preset is a look saved under a name: the GTK theme, icons, cursor, fonts, the light or dark style, the
accent color and the wallpaper, each optional. Applying one sets every part it has, installing fonts and themes it needs from
the catalog first, and leaves the rest alone.

commands:
  list                          your presets, and which is in use (also "uas appearance presets")
  show <name>                   its parts, and what applying it would do here
  save [-replace] [-skip P,...] <name>
                                save the current look as a preset; -skip leaves parts out
  new [-replace] [-gtk T] [-icons T] [-cursor T] [-interface F] [-document F] [-monospace F]
      [-scheme default|light|dark] [-accent C] [-wallpaper P [-wallpaper-dark P] [-fit F]] <name>
                                make a preset from parts: a theme's name, or a catalog ID such as
                                pling:1166289@Papirus-Dark; a font family, with a size or not
                                ("Inter 11"), or a catalog ID such as google-fonts:inter; a picture
                                (jpg, png, webp, jxl or svg) for the wallpaper, and another for the
                                dark style; how it fits: zoom (the default), centered, scaled,
                                stretched, spanned, wallpaper (tiled) or none
  set <name> <part> <value>|unset
                                change one part, or leave it out
  set <name> wallpaper-dark <picture>|same
  set <name> fit <F>            change the wallpaper's dark picture, or its fit
  apply [-y] [-n] [-no-install] <name>
                                apply it; -n shows what it would do, -no-install skips what isn't here
  undo                          go back to the look from before the last apply
  rename <name> <new name>
  copy <name> [<new name>]      copy it, as "<name> copy" unless you name the copy
  delete <name>

parts: gtk, icons, cursor, interface, document, monospace, scheme, accent, wallpaper

A GTK theme with files for GTK 4 apps has them copied into ~/.config/gtk-4.0 when the preset is applied,
as "uas appearance use gtk" does; undo puts back what was there.

A picture of your own is copied into ~/.local/share/uas/wallpapers, so the preset keeps working when the
original is moved or deleted; the system's wallpapers (/usr/share/backgrounds) are used where they are.
`

// presetParts are the parts the command line makes, with their flags' descriptions.
var presetParts = []struct{ part, help string }{
	{appearance.PartGTK, "the GTK theme: a theme's name, or a catalog ID such as pling:1357889@Orchis-Dark"},
	{appearance.PartIcons, "the icon theme: a theme's name, or a catalog ID such as pling:1166289@Papirus-Dark"},
	{appearance.PartCursor, "the cursor theme: a theme's name, or a catalog ID"},
	{appearance.PartInterface, `the interface font: a family, with a size or not ("Inter 11"), or a catalog ID`},
	{appearance.PartDocument, "the document font"},
	{appearance.PartMonospace, "the monospace font"},
	{appearance.PartScheme, "the style: default, light or dark"},
	{appearance.PartAccent, "the accent color: " + strings.Join(appearance.AccentColors, ", ")},
}

// partNameList are the names of the parts, for messages.
var partNameList = []string{appearance.PartGTK, appearance.PartIcons, appearance.PartCursor, appearance.PartInterface,
	appearance.PartDocument, appearance.PartMonospace, appearance.PartScheme, appearance.PartAccent,
	appearance.PartWallpaper}

// runPreset saves, applies and manages presets.
func runPreset(ctx context.Context, w io.Writer, cmd string, args []string) error {
	sub := "list"
	if cmd == "preset" {
		if len(args) == 0 || strings.HasPrefix(args[0], "-") {
			if len(args) > 0 && (args[0] == "-h" || args[0] == "-help" || args[0] == "--help") {
				fmt.Fprint(w, presetUsage)
				return nil
			}
			return fmt.Errorf("appearance preset: give a command\n\n%s", presetUsage)
		}
		sub, args = args[0], args[1:]
	}
	fs := flag.NewFlagSet("appearance preset "+sub, flag.ContinueOnError)
	fs.Usage = func() { fmt.Fprint(fs.Output(), presetUsage) }
	replace := fs.Bool("replace", false, "replace a preset of the same name")
	skip := fs.String("skip", "", "parts to leave out, separated by commas")
	yes := fs.Bool("y", false, "do not ask before installing what the preset needs")
	dry := fs.Bool("n", false, "only show what applying would do")
	noInstall := fs.Bool("no-install", false, "skip the parts that aren't on this system")
	values := map[string]*string{}
	var wallLight, wallDark, wallFit *string
	if sub == "new" {
		for _, p := range presetParts {
			values[p.part] = fs.String(p.part, "", p.help)
		}
		wallLight = fs.String("wallpaper", "", "the wallpaper: a picture of yours (copied into uas's folder) or the system's")
		wallDark = fs.String("wallpaper-dark", "", "the wallpaper with the dark style; the same picture without it")
		wallFit = fs.String("fit", "", "how the wallpaper fits: "+strings.Join(appearance.WallpaperFits, ", "))
	}
	rest, err := parseArgs(fs, args)
	if err != nil {
		return err
	}
	catalogPath, _ := paths.Catalog()
	e, err := session.Load(ctx, "", catalogPath)
	if err != nil {
		return err
	}
	if e.Looks == nil || e.Presets == nil {
		return errors.New("appearance preset: uas couldn't find your home folder")
	}
	store := e.Presets
	n := func(want int, what string) error {
		if len(rest) != want {
			return fmt.Errorf("appearance preset %s: give %s", sub, what)
		}
		return nil
	}
	clash := func(err error) error {
		var ex *appearance.ExistsError
		if errors.As(err, &ex) && (sub == "save" || sub == "new") {
			return fmt.Errorf("%w; add -replace to replace it", err)
		}
		return err
	}

	switch sub {
	case "list":
		if len(rest) > 0 {
			return errors.New("appearance presets: takes no arguments")
		}
		return listPresets(ctx, w, e)
	case "show", "info":
		if err := n(1, "a preset's name"); err != nil {
			return err
		}
		p, err := store.Get(rest[0])
		if err != nil {
			return err
		}
		return showPreset(ctx, w, e, p)
	case "save":
		if err := n(1, "a name for the preset"); err != nil {
			return err
		}
		parts, err := e.Looks.Capture(ctx)
		if errors.Is(err, appearance.ErrUnsupported) {
			return errors.New("appearance preset save: this desktop's settings can't be read by uas")
		} else if err != nil {
			return err
		}
		for _, name := range strings.Split(*skip, ",") {
			if name = strings.TrimSpace(name); name == "" {
				continue
			}
			if _, ok := appearance.PartKey(name); !ok {
				return fmt.Errorf("appearance preset save: %q isn't a part (%s)", name, partNames())
			}
			delete(parts, name)
		}
		p, err := store.Save(appearance.Preset{Name: rest[0], Parts: parts}, *replace)
		if err != nil {
			return clash(err)
		}
		fmt.Fprintf(w, "Saved %s:\n", p.Name)
		return printParts(w, e, p)
	case "new":
		if err := n(1, "a name for the preset"); err != nil {
			return err
		}
		p := appearance.Preset{Name: rest[0], Parts: map[string]appearance.Part{}}
		for _, pp := range presetParts {
			if v := strings.TrimSpace(*values[pp.part]); v != "" {
				part, err := resolvePart(ctx, e, pp.part, v)
				if err != nil {
					return err
				}
				p.Parts[pp.part] = part
			}
		}
		light, dark, fit := strings.TrimSpace(*wallLight), strings.TrimSpace(*wallDark), strings.TrimSpace(*wallFit)
		if light == "" && (dark != "" || fit != "") {
			return errors.New("appearance preset new: -wallpaper-dark and -fit need -wallpaper")
		}
		if light != "" {
			part, err := wallpaperPart(e, light, dark, fit)
			if err != nil {
				return err
			}
			p.Parts[appearance.PartWallpaper] = part
		}
		p, err := store.Save(p, *replace)
		if err != nil {
			return clash(err)
		}
		fmt.Fprintf(w, "Saved %s:\n", p.Name)
		return printParts(w, e, p)
	case "set":
		if err := n(3, "a preset's name, a part and its value (or unset)"); err != nil {
			return err
		}
		part := rest[1]
		switch part {
		case "wallpaper-dark", "dark", "wallpaper-fit", "fit":
			return setWallpaperField(w, e, rest[0], part, rest[2])
		}
		if _, ok := appearance.PartKey(part); !ok {
			return fmt.Errorf("appearance preset set: %q isn't a part (%s)", part, partNames())
		}
		var value appearance.Part
		unset := rest[2] == "unset" || rest[2] == "-"
		if !unset && part == appearance.PartWallpaper {
			// A new picture keeps the dark one and the fit the preset has.
			old, err := store.Get(rest[0])
			if err != nil {
				return err
			}
			if value, err = wallpaperPart(e, rest[2], "", ""); err != nil {
				return err
			}
			if prev, ok := old.Parts[appearance.PartWallpaper]; ok {
				value.Dark = prev.Dark
				if prev.Fit != "" {
					value.Fit = prev.Fit
				}
			}
		} else if !unset {
			if value, err = resolvePart(ctx, e, part, rest[2]); err != nil {
				return err
			}
		}
		p, err := store.Update(rest[0], func(p *appearance.Preset) error {
			if unset {
				delete(p.Parts, part)
			} else {
				p.Parts[part] = value
			}
			return nil
		})
		if err != nil {
			return err
		}
		fmt.Fprintf(w, "Changed %s:\n", p.Name)
		return printParts(w, e, p)
	case "apply", "use":
		if err := n(1, "a preset's name"); err != nil {
			return err
		}
		p, err := store.Get(rest[0])
		if err != nil {
			return err
		}
		return applyPreset(ctx, w, e, p, *yes, *dry, *noInstall)
	case "undo":
		if err := n(0, "nothing else"); err != nil {
			return err
		}
		u, out, err := e.UndoPreset(ctx)
		if errors.Is(err, appearance.ErrUnsupported) {
			return errors.New("appearance preset undo: this desktop's settings can't be changed from uas")
		} else if err != nil {
			return err
		}
		printOutcomes(w, e, out)
		if err := appearance.Failed(out); err != nil {
			return err
		}
		fmt.Fprintf(w, "Back to the look from before %s.\n", u.Applied)
	case "rename":
		if err := n(2, "a preset's name and its new name"); err != nil {
			return err
		}
		p, err := store.Rename(rest[0], rest[1])
		if err != nil {
			return err
		}
		fmt.Fprintf(w, "Renamed to %s.\n", p.Name)
	case "copy", "duplicate":
		if len(rest) != 1 && len(rest) != 2 {
			return errors.New("appearance preset copy: give a preset's name, and a name for the copy or not")
		}
		to := ""
		if len(rest) == 2 {
			to = rest[1]
		}
		p, err := store.Copy(rest[0], to)
		if err != nil {
			return err
		}
		fmt.Fprintf(w, "Copied as %s.\n", p.Name)
	case "delete", "remove":
		if err := n(1, "a preset's name"); err != nil {
			return err
		}
		p, err := store.Get(rest[0])
		if err != nil {
			return err
		}
		if err := store.Delete(p.Name); err != nil {
			return err
		}
		fmt.Fprintf(w, "Deleted %s. The fonts and themes it uses stay installed.\n", p.Name)
	default:
		return fmt.Errorf("appearance preset: unknown command %q\n\n%s", sub, presetUsage)
	}
	return nil
}

func partNames() string { return strings.Join(partNameList, ", ") }

// picturePath checks a picture given on the command line is there, and returns its absolute path.
func picturePath(raw string) (string, error) {
	p, err := filepath.Abs(raw)
	if err != nil {
		return "", err
	}
	fi, err := os.Stat(p)
	if err != nil {
		return "", fmt.Errorf("wallpaper: %w", err)
	}
	if !fi.Mode().IsRegular() {
		return "", fmt.Errorf("wallpaper: %s isn't a file", raw)
	}
	return p, nil
}

// wallpaperPart makes a preset's wallpaper part from pictures given on the command line: one of the user's
// is copied into the store, one of the system's is named by its path. The fit is zoom unless given.
func wallpaperPart(e *session.Session, light, dark, fit string) (appearance.Part, error) {
	wp := appearance.Wallpaper{Fit: appearance.FitZoom}
	var err error
	if wp.Light, err = picturePath(light); err != nil {
		return appearance.Part{}, err
	}
	if dark != "" {
		if wp.Dark, err = picturePath(dark); err != nil {
			return appearance.Part{}, err
		}
	}
	if fit != "" {
		wp.Fit = appearance.ParseValue(appearance.KeyWallpaperFit, fit)
		if err := appearance.CheckValue(appearance.KeyWallpaperFit, wp.Fit); err != nil {
			return appearance.Part{}, err
		}
	}
	return e.Looks.WallpaperPart(wp)
}

// setWallpaperField changes a preset's wallpaper's dark picture ("same" uses the light one) or its fit.
func setWallpaperField(w io.Writer, e *session.Session, name, field, raw string) error {
	var dark, fit string
	var err error
	switch field {
	case "wallpaper-dark", "dark":
		if raw != "same" && raw != "unset" && raw != "-" {
			if dark, err = picturePath(raw); err != nil {
				return err
			}
			if dark, err = e.Looks.WallpaperRef(dark); err != nil {
				return err
			}
		}
	default:
		fit = appearance.ParseValue(appearance.KeyWallpaperFit, raw)
		if err := appearance.CheckValue(appearance.KeyWallpaperFit, fit); err != nil {
			return err
		}
	}
	p, err := e.Presets.Update(name, func(p *appearance.Preset) error {
		part, ok := p.Parts[appearance.PartWallpaper]
		if !ok {
			return fmt.Errorf("%s has no wallpaper; set one first: uas appearance preset set %q wallpaper <picture>",
				p.Name, p.Name)
		}
		if fit != "" {
			part.Fit = fit
		} else if dark == part.Light {
			part.Dark = ""
		} else {
			part.Dark = dark
		}
		p.Parts[appearance.PartWallpaper] = part
		return nil
	})
	if err != nil {
		return err
	}
	fmt.Fprintf(w, "Changed %s:\n", p.Name)
	return printParts(w, e, p)
}

// resolvePart reads a part's value as the command line gives it: a theme's or font's name, which is found
// among what uas installed for where it came from, or a catalog ID, which is looked up.
func resolvePart(ctx context.Context, e *session.Session, part, raw string) (appearance.Part, error) {
	key, _ := appearance.PartKey(part)
	if appearance.Choices(key) != nil {
		v := appearance.ParseValue(key, raw)
		if err := appearance.CheckValue(key, v); err != nil {
			return appearance.Part{}, err
		}
		return appearance.Part{Value: v}, nil
	}
	recs, _ := e.Looks.Records()
	font := slices.Contains(appearance.FontKeys, key)
	if id, variant, ok := catalogRef(raw); ok {
		l, err := lookListing(ctx, e, id)
		if err != nil {
			return appearance.Part{}, err
		}
		want := catalog.KindFont
		switch key {
		case appearance.KeyIconTheme:
			want = catalog.KindIconTheme
		case appearance.KeyCursorTheme:
			want = catalog.KindCursorTheme
		case appearance.KeyGTKTheme:
			want = catalog.KindGTKTheme
		}
		if l.Kind != want {
			return appearance.Part{}, fmt.Errorf("%s: %s is a %s, not a %s", part, id, l.Kind, want)
		}
		if font {
			value := l.Name
			for _, r := range recs {
				if r.ID == id && len(r.Families) > 0 {
					value = r.Families[0]
				}
			}
			return appearance.Part{Value: value, From: &appearance.Origin{ID: id}}, nil
		}
		vs := appearance.Variants(l)
		if variant == "" && len(vs) > 0 {
			variant = vs[0]
		}
		if !slices.Contains(vs, variant) {
			return appearance.Part{}, fmt.Errorf("%s: %s has no variant called %q (see \"uas appearance info %s\")",
				part, l.Name, variant, id)
		}
		return appearance.Part{Value: variant, From: &appearance.Origin{ID: id, Variant: variant}}, nil
	}
	p := appearance.Part{Value: raw}
	for _, r := range recs {
		match := slices.Contains(r.Themes, raw) && slices.Contains(appearance.KeysOf(r), key)
		if font {
			match = r.Kind == string(catalog.KindFont) && slices.ContainsFunc(r.Families, func(f string) bool {
				return strings.EqualFold(f, appearance.FontFamily(raw))
			})
		}
		if match {
			p.From = &appearance.Origin{ID: r.ID}
			if !font {
				p.From.Variant = raw
			}
		}
	}
	return p, nil
}

// catalogRef reads a font's or theme's catalog ID, with "@variant" after it or not.
func catalogRef(s string) (id, variant string, ok bool) {
	id, variant, _ = strings.Cut(s, "@")
	src, ref, found := strings.Cut(id, ":")
	if !found || ref == "" || strings.ContainsAny(id, " ") {
		return "", "", false
	}
	switch catalog.SourceID(src) {
	case catalog.SourcePling, catalog.SourceGoogleFonts, catalog.SourceNerdFonts:
		return id, variant, true
	}
	return "", "", false
}

func listPresets(ctx context.Context, w io.Writer, e *session.Session) error {
	list, err := e.Presets.List()
	if len(list) == 0 {
		if err != nil {
			return err
		}
		fmt.Fprintln(w, `No presets yet. Save the current look with "uas appearance preset save <name>".`)
		return nil
	}
	cur := e.Looks.Current(ctx)
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "NAME\tPARTS\tIN USE")
	for _, p := range list {
		use := ""
		if appearance.InUse(p, cur) {
			use = "yes"
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\n", p.Name, truncate(strings.Join(p.PartNames(), ", "), 60), use)
	}
	tw.Flush()
	return err
}

// printParts lists a preset's parts and values.
func printParts(w io.Writer, e *session.Session, p appearance.Preset) error {
	if len(p.Parts) == 0 {
		fmt.Fprintln(w, "  (no parts: it leaves everything as it is)")
		return nil
	}
	var system []appearance.SystemWallpaper
	if p.Sets(appearance.PartWallpaper) {
		system = e.Looks.SystemWallpapers()
	}
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	for _, name := range p.PartNames() {
		part := p.Parts[name]
		key, _ := appearance.PartKey(name)
		v := appearance.ValueLabel(key, part.Value)
		if name == appearance.PartWallpaper {
			wp := part.Wallpaper()
			if wp.Fit == "" {
				wp.Fit = appearance.FitZoom
			}
			v = wallpaperLabel(system, wp)
		}
		if part.From != nil {
			v += "  (" + part.From.ID + ")"
		}
		fmt.Fprintf(tw, "  %s:\t%s\n", appearance.PartLabel(name), v)
	}
	return tw.Flush()
}

func showPreset(ctx context.Context, w io.Writer, e *session.Session, p appearance.Preset) error {
	fmt.Fprintln(w, p.Name)
	if !p.UpdatedAt.IsZero() {
		fmt.Fprintf(w, "Changed %s\n", p.UpdatedAt.Local().Format("2 January 2006, 15:04"))
	}
	if appearance.InUse(p, e.Looks.Current(ctx)) {
		fmt.Fprintln(w, "In use")
	}
	fmt.Fprintln(w)
	plan, err := e.PlanPreset(ctx, p)
	if errors.Is(err, appearance.ErrUnsupported) {
		fmt.Fprintln(w, "This desktop's settings can't be changed from uas, so it can't be applied here.")
		return printParts(w, e, p)
	} else if err != nil {
		return err
	}
	return printPlan(w, e, plan)
}

// planValue names a plan item's value: for the wallpaper, its pictures and fit.
func planValue(system []appearance.SystemWallpaper, it appearance.PlanItem) string {
	if it.Part == appearance.PartWallpaper {
		if it.Wallpaper.Light != "" {
			return wallpaperLabel(system, it.Wallpaper)
		}
		return appearance.WallpaperName(system, it.Value)
	}
	return appearance.ValueLabel(it.Key, it.Value)
}

// wallpapersOf is the system's wallpapers, for naming a plan's wallpaper; nil when it has none.
func wallpapersOf(e *session.Session, plan []appearance.PlanItem) []appearance.SystemWallpaper {
	for _, it := range plan {
		if it.Part == appearance.PartWallpaper {
			return e.Looks.SystemWallpapers()
		}
	}
	return nil
}

// printPlan lists what applying a preset would do with each part.
func printPlan(w io.Writer, e *session.Session, plan []appearance.PlanItem) error {
	system := wallpapersOf(e, plan)
	if len(plan) == 0 {
		fmt.Fprintln(w, "It has no parts, so applying it leaves everything as it is.")
		return nil
	}
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "PART\tVALUE\tAPPLYING IT")
	for _, it := range plan {
		action := ""
		switch it.Step {
		case appearance.StepSet:
			action = "sets it"
		case appearance.StepSame:
			action = "nothing: it is so already"
		case appearance.StepInstall:
			action = "installs " + it.Listing.Name + " (" + it.Note + "), then sets it"
		default:
			action = "skips it: " + it.Note
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\n", appearance.PartLabel(it.Part), truncate(planValue(system, it), 60), action)
	}
	return tw.Flush()
}

func applyPreset(ctx context.Context, w io.Writer, e *session.Session, p appearance.Preset, yes, dry,
	noInstall bool) error {
	plan, err := e.PlanPreset(ctx, p)
	if errors.Is(err, appearance.ErrUnsupported) {
		return errors.New("appearance preset apply: this desktop's settings can't be changed from uas")
	} else if err != nil {
		return err
	}
	if err := printPlan(w, e, plan); err != nil {
		return err
	}
	if dry {
		return nil
	}
	if downloads := appearance.Downloads(plan); len(downloads) > 0 && !noInstall && !yes {
		names := make([]string, len(downloads))
		for i, d := range downloads {
			names[i] = d.Listing.Name
		}
		ok, err := confirm(fmt.Sprintf("Install %s first?", strings.Join(names, ", ")))
		if err != nil {
			return err
		}
		if !ok {
			return nil
		}
	}
	progress := func(pr backend.Progress) {
		if pr.Message != "" {
			fmt.Fprintln(w, pr.Message)
		}
	}
	out, err := e.ApplyPreset(ctx, p, plan, noInstall, progress)
	if err != nil {
		return err
	}
	fmt.Fprintln(w)
	printOutcomes(w, e, out)
	failed := appearance.Failed(out)
	what := "Applied"
	if failed != nil {
		what = "Applied part of"
	}
	changed := slices.ContainsFunc(out, func(o appearance.Outcome) bool { return o.Done && o.Step != appearance.StepSame })
	if changed {
		fmt.Fprintf(w, "%s %s. To go back: uas appearance preset undo\n", what, p.Name)
	} else {
		fmt.Fprintf(w, "%s %s.\n", what, p.Name)
	}
	return failed
}

// printOutcomes says what applying did with each part.
func printOutcomes(w io.Writer, e *session.Session, out []appearance.Outcome) {
	var system []appearance.SystemWallpaper
	for _, o := range out {
		if o.Part == appearance.PartWallpaper {
			system = e.Looks.SystemWallpapers()
		}
	}
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	for _, o := range out {
		label := appearance.PartLabel(o.Part) + ":"
		value := planValue(system, o.PlanItem)
		switch {
		case o.Err != nil:
			fmt.Fprintf(tw, "%s\t%s\tfailed: %v\n", label, value, o.Err)
		case o.Step.Skipped():
			fmt.Fprintf(tw, "%s\t%s\tskipped: %s\n", label, value, o.Note)
		case o.Done:
			fmt.Fprintf(tw, "%s\t%s\n", label, value)
		}
	}
	tw.Flush()
}
