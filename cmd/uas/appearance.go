package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"strings"
	"text/tabwriter"

	"github.com/tyler-builds/uas/internal/catalog"
	"github.com/tyler-builds/uas/internal/client/appearance"
	"github.com/tyler-builds/uas/internal/client/backend"
	"github.com/tyler-builds/uas/internal/client/paths"
	"github.com/tyler-builds/uas/internal/client/session"
)

const appearanceUsage = `usage: uas appearance [command]

Fonts, icon themes, cursor themes and GTK themes, installed for your account: fonts from Google Fonts and
Nerd Fonts, themes from Pling (gnome-look.org). Also the GNOME Shell theme, the light or dark style, the
accent color and the wallpaper.

commands:
  list                          what uas installed, and what the desktop uses (the default)
  search -kind K <words>        find fonts, icons, cursors or gtk themes (K)
  browse -kind K                the most popular fonts, icons, cursors or gtk themes
  info <id>                     show one, with its variants
  install [-variant V] [-use S] <id>
                                install one; -use also uses it for S (see use)
  remove <id>                   remove one uas installed
  use <S> <value>               set S: gtk, shell, icons or cursor (a theme), interface, document or monospace (a
                                font family, or a family and size such as "Inter 11"), scheme (default,
                                light or dark) or accent (blue, teal, green, yellow, orange, red, pink,
                                purple, slate). A GTK theme's files for GTK 4 apps, when it has them, are
                                copied into ~/.config/gtk-4.0 too; yours there are kept aside and put
                                back by revert. A GNOME Shell theme (shell, or "default" for the Shell's
                                own look) needs the User Themes extension; uas asks before turning it on
  use wallpaper <file> [-dark <file>] [-fit F]
                                set the wallpaper (GNOME): a picture (jpg, png, webp, jxl or svg; your own
                                are copied into uas's wallpaper folder) or one of the system's, a second
                                for the dark style, and how it fits: zoom, centered, scaled, stretched,
                                spanned, wallpaper (tiled) or none. "use wallpaper -fit F" changes the fit
  revert <S>|wallpaper|all      put S, the wallpaper, or every setting uas changed, back to what it was
                                before uas
  themes                        the icon, cursor, GTK and GNOME Shell themes on this system
  updates                       fonts and themes uas installed that have a newer version
  update <id>                   install one's newer version
  presets                       your presets: looks saved under a name (see "uas appearance preset")
  preset <command>              save, apply and manage presets
`

// settingKeys are the names "use" and "revert" take for each setting, which are also the names of a
// preset's parts.
var settingKeys = map[string]string{"gtk": appearance.KeyGTKTheme, "shell": appearance.KeyShellTheme,
	"icons":     appearance.KeyIconTheme,
	"cursor":    appearance.KeyCursorTheme,
	"interface": appearance.KeyFont, "document": appearance.KeyDocumentFont, "monospace": appearance.KeyMonospaceFont,
	"scheme": appearance.KeyColorScheme, "style": appearance.KeyColorScheme, "accent": appearance.KeyAccentColor,
	"wallpaper": appearance.KeyWallpaper}

// settingNames lists the names of settingKeys for messages.
const settingNames = "gtk, shell, icons, cursor, interface, document, monospace, scheme, accent or wallpaper"

// lookKinds are the names -kind takes.
var lookKinds = map[string]catalog.Kind{"fonts": catalog.KindFont, "font": catalog.KindFont,
	"icons": catalog.KindIconTheme, "icon": catalog.KindIconTheme, "cursors": catalog.KindCursorTheme,
	"cursor": catalog.KindCursorTheme, "gtk": catalog.KindGTKTheme}

// runAppearance lists, installs and uses fonts and themes.
func runAppearance(ctx context.Context, w io.Writer, args []string) error {
	cmd := "list"
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		cmd, args = args[0], args[1:]
	}
	if cmd == "presets" || cmd == "preset" {
		return runPreset(ctx, w, cmd, args)
	}
	fs := flag.NewFlagSet("appearance "+cmd, flag.ContinueOnError)
	fs.Usage = func() { fmt.Fprint(fs.Output(), appearanceUsage) }
	kind := fs.String("kind", "", "fonts, icons, cursors or gtk")
	variant := fs.String("variant", "", "the theme variant to install (see info)")
	use := fs.String("use", "", "also use it: gtk, icons, cursor (themes), interface, document or monospace (fonts)")
	limit := fs.Int("limit", 20, "maximum number of results")
	dark := fs.String("dark", "", "use wallpaper: the picture for the dark style")
	fit := fs.String("fit", "", "use wallpaper: how it fits the screen: "+strings.Join(appearance.WallpaperFits, ", "))
	rest, err := parseArgs(fs, args)
	if err != nil {
		return err
	}
	catalogPath, _ := paths.Catalog()
	e, err := session.Load(ctx, "", catalogPath)
	if err != nil {
		return err
	}
	if e.Looks == nil {
		return errors.New("appearance: uas couldn't find your home folder")
	}
	one := func(what string) (string, error) {
		if len(rest) != 1 {
			return "", fmt.Errorf("appearance %s: give exactly one %s", cmd, what)
		}
		return rest[0], nil
	}
	setting := func(name string) (string, error) {
		key, ok := settingKeys[name]
		if !ok {
			return "", fmt.Errorf("appearance %s: %q isn't %s", cmd, name, settingNames)
		}
		return key, nil
	}
	progress := func(p backend.Progress) {
		if p.Message != "" {
			fmt.Fprintln(w, p.Message)
		}
	}

	switch cmd {
	case "list":
		return listLooks(ctx, w, e)
	case "search", "browse":
		k, ok := lookKinds[*kind]
		if !ok {
			return fmt.Errorf("appearance %s: give -kind fonts, icons, cursors or gtk", cmd)
		}
		if cmd == "search" && len(rest) == 0 {
			return errors.New("appearance search: give one or more words to search for")
		}
		return searchLooks(ctx, w, e, k, strings.Join(rest, " "), *limit)
	case "info":
		id, err := one("ID (see search)")
		if err != nil {
			return err
		}
		return lookInfo(ctx, w, e, id)
	case "install":
		id, err := one("ID (see search)")
		if err != nil {
			return err
		}
		key := ""
		if *use != "" {
			if key, err = setting(*use); err != nil {
				return err
			}
		}
		l, err := lookListing(ctx, e, id)
		if err != nil {
			return err
		}
		for _, n := range e.LookNotes(l) {
			fmt.Fprintln(w, n)
		}
		rec, err := e.InstallLook(ctx, l, *variant, key, progress)
		if err != nil {
			return err
		}
		what := strings.Join(rec.Themes, ", ")
		if rec.Kind == string(catalog.KindFont) {
			what = strings.Join(rec.Families, ", ")
		}
		fmt.Fprintf(w, "%s is installed: %s\n", rec.Name, what)
		if rec.LeftOut > 0 {
			fmt.Fprintf(w, "Left out %d file(s) of it that could be run, such as scripts; uas never installs those.\n",
				rec.LeftOut)
		}
		if key == appearance.KeyGTKTheme {
			printGTK4(w, e)
		}
		if key != "" {
			presetHint(ctx, w, e)
		}
	case "update":
		id, err := one("ID (see updates)")
		if err != nil {
			return err
		}
		ups, err := e.LookUpdates(ctx)
		if err != nil {
			return err
		}
		for _, u := range ups {
			if u.Install.ID == id {
				if err := e.UpdateLook(ctx, u, progress); err != nil {
					return err
				}
				fmt.Fprintf(w, "%s is updated.\n", u.Install.Name)
				return nil
			}
		}
		return fmt.Errorf("appearance update: %s has no update", id)
	case "remove", "uninstall":
		id, err := one("ID (see list)")
		if err != nil {
			return err
		}
		if err := e.RemoveLook(ctx, id, progress); err != nil {
			return err
		}
		fmt.Fprintf(w, "%s is removed.\n", id)
	case "use":
		if len(rest) > 0 && settingKeys[rest[0]] == appearance.KeyWallpaper {
			return useWallpaper(ctx, w, e, rest[1:], *dark, *fit)
		}
		if *dark != "" || *fit != "" {
			return errors.New("appearance use: -dark and -fit are for the wallpaper")
		}
		if len(rest) != 2 {
			return fmt.Errorf("appearance use: give a setting (%s) and a value", settingNames)
		}
		key, err := setting(rest[0])
		if err != nil {
			return err
		}
		value := appearance.ParseValue(key, rest[1])
		err = e.Looks.Use(ctx, key, value)
		var need *appearance.NeedsUserThemesError
		if errors.As(err, &need) {
			if err = offerUserThemes(ctx, w, e, need.UserThemes); err == nil {
				err = e.Looks.Use(ctx, key, value)
			}
		}
		if err != nil {
			return err
		}
		fmt.Fprintf(w, "%s: %s\n", appearance.KeyLabel(key), appearance.ValueLabel(key, e.Looks.Current(ctx)[key]))
		if key == appearance.KeyGTKTheme {
			printGTK4(w, e)
		}
		if key == appearance.KeyShellTheme && value != "" {
			fmt.Fprintln(w, appearance.ShellNote)
		}
		presetHint(ctx, w, e)
	case "revert":
		name, err := one("setting, or all")
		if err != nil {
			return err
		}
		if name == "all" {
			saved, err := e.Looks.Saved()
			if err != nil {
				return err
			}
			if len(saved) == 0 {
				fmt.Fprintln(w, "uas hasn't changed any settings.")
				return nil
			}
			err = e.Looks.RevertAll(ctx)
			cur := e.Looks.Current(ctx)
			shown := map[string]bool{}
			for _, s := range saved {
				key := appearance.GroupKey(s.Key)
				if shown[key] {
					continue
				}
				shown[key] = true
				fmt.Fprintf(w, "%s: %s\n", appearance.KeyLabel(key), settingValue(e, cur, key))
			}
			return err
		}
		key, err := setting(name)
		if err != nil {
			return err
		}
		if err := e.Looks.Revert(ctx, key); err != nil {
			return err
		}
		fmt.Fprintf(w, "%s: %s\n", appearance.KeyLabel(key), settingValue(e, e.Looks.Current(ctx), key))
	case "themes":
		return listThemes(ctx, w, e)
	case "updates":
		ups, err := e.LookUpdates(ctx)
		if err != nil {
			return err
		}
		if len(ups) == 0 {
			fmt.Fprintln(w, "Everything is up to date.")
			return nil
		}
		tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
		fmt.Fprintln(tw, "NAME\tID\tINSTALLED\tAVAILABLE")
		for _, u := range ups {
			v := u.Version()
			if v == "" {
				v = "a new build"
			}
			fmt.Fprintf(tw, "%s\t%s\t%s\t%s\n", u.Install.Name, u.Install.ID, u.Install.Version, v)
		}
		return tw.Flush()
	default:
		return fmt.Errorf("appearance: unknown command %q\n\n%s", cmd, appearanceUsage)
	}
	return nil
}

// lookListing returns the catalog's listing of a font or theme by its ID.
func lookListing(ctx context.Context, e *session.Session, id string) (catalog.Listing, error) {
	db, err := openCatalog(ctx, e)
	if err != nil {
		return catalog.Listing{}, err
	}
	defer db.Close()
	app, err := db.App(ctx, id)
	if err != nil {
		return catalog.Listing{}, err
	}
	if !app.Kind.Appearance() || len(app.Listings) == 0 {
		return catalog.Listing{}, fmt.Errorf("%s isn't a font or theme; use \"uas install\" for apps", id)
	}
	return app.Listings[0], nil
}

func searchLooks(ctx context.Context, w io.Writer, e *session.Session, k catalog.Kind, q string, limit int) error {
	db, err := openCatalog(ctx, e)
	if err != nil {
		return err
	}
	defer db.Close()
	var rs []catalog.SearchResult
	if q == "" {
		rs, err = db.Browse(ctx, catalog.BrowseQuery{Kind: k, Limit: limit})
	} else {
		rs, err = db.Search(ctx, q, catalog.SearchOptions{Kind: k, Limit: limit})
	}
	if err != nil {
		return err
	}
	if len(rs) == 0 {
		fmt.Fprintln(w, "Nothing found.")
		return nil
	}
	recs, _ := e.Looks.Records()
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "NAME\tID\tBY\tSUMMARY")
	for _, r := range rs {
		name := truncate(r.Name, 30)
		for _, rec := range recs {
			if rec.ID == r.ID {
				name += " (installed)"
			}
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\n", name, r.ID, truncate(r.Developer, 20), truncate(r.Summary, 50))
	}
	return tw.Flush()
}

func lookInfo(ctx context.Context, w io.Writer, e *session.Session, id string) error {
	l, err := lookListing(ctx, e, id)
	if err != nil {
		return err
	}
	fmt.Fprintf(w, "%s (%s)\n", l.Name, id)
	if l.Summary != "" {
		fmt.Fprintln(w, l.Summary)
	}
	fmt.Fprintf(w, "From:      %s\n", l.Source.Name())
	if l.Developer != "" {
		fmt.Fprintf(w, "By:        %s\n", l.Developer)
	}
	if l.Version != "" {
		fmt.Fprintf(w, "Version:   %s\n", l.Version)
	}
	if l.License != "" {
		fmt.Fprintf(w, "License:   %s\n", l.License)
	}
	if l.Installs > 0 {
		fmt.Fprintf(w, "Downloads: %d\n", l.Installs)
	}
	if l.Rated() {
		fmt.Fprintf(w, "Rating:    %d of 100\n", l.Score)
	}
	if l.Rank > 0 {
		fmt.Fprintf(w, "Rank:      #%d on Google Fonts\n", l.Rank)
	}
	if l.Trust != nil {
		fmt.Fprintf(w, "Trust:     %d/100\n", l.Trust.Score)
	}
	for _, a := range l.Assets {
		switch a.Kind {
		case catalog.AssetTheme:
			fmt.Fprintf(w, "File:      %s (%s): %s\n", a.Name, session.HumanSize(a.Size), strings.Join(a.Themes, ", "))
		default:
			fmt.Fprintf(w, "File:      %s (%s)\n", a.Name, session.HumanSize(a.Size))
		}
	}
	if l.StoreURL != "" {
		fmt.Fprintf(w, "Page:      %s\n", l.StoreURL)
	}
	return nil
}

func listLooks(ctx context.Context, w io.Writer, e *session.Session) error {
	recs, err := e.Looks.Records()
	if err != nil {
		return err
	}
	cur := e.Looks.Current(ctx)
	if len(recs) == 0 {
		fmt.Fprintln(w, "uas hasn't installed any fonts or themes.")
	} else {
		tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
		fmt.Fprintln(tw, "NAME\tID\tKIND\tVERSION\tINSTALLED")
		for _, r := range recs {
			what := strings.Join(r.Themes, ", ")
			if r.Kind == string(catalog.KindFont) {
				what = strings.Join(r.Families, ", ")
			}
			fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\n", r.Name, r.ID, r.Kind, r.Version, truncate(what, 60))
		}
		tw.Flush()
	}
	if len(cur) > 0 {
		saved, _ := e.Looks.Saved()
		fmt.Fprintln(w)
		for _, key := range appearance.SettingKeys {
			if _, ok := cur[key]; !ok || appearance.GroupKey(key) != key {
				continue
			}
			line := fmt.Sprintf("%-15s %s", appearance.KeyLabel(key)+":", settingValue(e, cur, key))
			if key == appearance.KeyWallpaper {
				// The wallpaper's settings are one: what revert goes back to is the light picture before uas,
				// or when uas changed only the dark picture or the fit, that.
				prev := map[string]string{}
				changed := false
				for _, k := range appearance.WallpaperKeys {
					prev[k] = cur[k]
					for _, s := range saved {
						if s.Key == k {
							prev[k], changed = s.Previous, true
						}
					}
				}
				if changed {
					line += " (set by uas; revert goes back to " + orDefault(settingValue(e, prev, key)) + ")"
				}
				fmt.Fprintln(w, line)
				continue
			}
			for _, s := range saved {
				if s.Key == key {
					line += " (set by uas; revert goes back to " + orDefault(appearance.ValueLabel(key, s.Previous)) + ")"
				}
			}
			fmt.Fprintln(w, line)
			if g, _ := e.Looks.GTK4(); key == appearance.KeyGTKTheme && g != nil && len(g.Written) > 0 {
				fmt.Fprintf(w, "%-15s %s's, in %s (revert puts back yours)\n", "GTK 4 files:", g.Theme, g.Dir)
			}
		}
	}
	return nil
}

// settingValue names a setting's value for people. The wallpaper is its pictures and fit together: "Default
// Background, dark: Default Background (dark), Zoom".
func settingValue(e *session.Session, cur map[string]string, key string) string {
	if key != appearance.KeyWallpaper {
		return appearance.ValueLabel(key, cur[key])
	}
	return wallpaperLabel(e.Looks.SystemWallpapers(), appearance.Wallpaper{Light: cur[appearance.KeyWallpaper],
		Dark: cur[appearance.KeyWallpaperDark], Fit: cur[appearance.KeyWallpaperFit]})
}

// wallpaperLabel names a wallpaper's pictures (paths or a preset's references) and fit; "" when it has no
// picture.
func wallpaperLabel(system []appearance.SystemWallpaper, wp appearance.Wallpaper) string {
	if wp.Light == "" {
		return ""
	}
	out := appearance.WallpaperName(system, wp.Light)
	if wp.Dark != "" && wp.Dark != wp.Light {
		out += ", dark: " + appearance.WallpaperName(system, wp.Dark)
	}
	if wp.Fit != "" {
		out += ", " + appearance.ValueLabel(appearance.KeyWallpaperFit, wp.Fit)
	}
	return out
}

// useWallpaper sets the wallpaper: a picture, with another for the dark style or not, and its fit; or the fit
// alone.
func useWallpaper(ctx context.Context, w io.Writer, e *session.Session, rest []string, dark, fit string) error {
	if fit != "" {
		fit = appearance.ParseValue(appearance.KeyWallpaperFit, fit)
		if err := appearance.CheckValue(appearance.KeyWallpaperFit, fit); err != nil {
			return err
		}
	}
	switch {
	case len(rest) > 1:
		return errors.New("appearance use wallpaper: give one picture, and the dark one with -dark")
	case len(rest) == 0 && (fit == "" || dark != ""):
		return errors.New("appearance use wallpaper: give a picture, or -fit to change only how it fits")
	case len(rest) == 0:
		if err := e.Looks.Use(ctx, appearance.KeyWallpaperFit, fit); err != nil {
			return err
		}
	default:
		if err := e.Looks.UseWallpaper(ctx, appearance.Wallpaper{Light: rest[0], Dark: dark, Fit: fit}); err != nil {
			return err
		}
	}
	fmt.Fprintf(w, "Wallpaper: %s\n", settingValue(e, e.Looks.Current(ctx), appearance.KeyWallpaper))
	presetHint(ctx, w, e)
	return nil
}

func listThemes(ctx context.Context, w io.Writer, e *session.Session) error {
	themes, err := e.Looks.Themes()
	if err != nil {
		return err
	}
	gtk, err := e.Looks.GTKThemes()
	if err != nil {
		return err
	}
	cur := e.Looks.Current(ctx)
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "NAME\tKIND\tWHERE\tIN USE")
	row := func(t appearance.Theme, kinds, inUse []string) {
		where := "system"
		switch {
		case t.By != "":
			where = "uas (" + t.By + ")"
		case t.User:
			where = "you"
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\n", t.Name, strings.Join(kinds, ", "), where, strings.Join(inUse, ", "))
	}
	for _, t := range themes {
		var kinds, inUse []string
		if t.Icons {
			kinds = append(kinds, "icons")
		}
		if t.Cursors {
			kinds = append(kinds, "cursors")
		}
		if cur[appearance.KeyIconTheme] == t.Name {
			inUse = append(inUse, "icons")
		}
		if cur[appearance.KeyCursorTheme] == t.Name {
			inUse = append(inUse, "cursor")
		}
		row(t, kinds, inUse)
	}
	for _, t := range gtk {
		kind := "gtk"
		if !t.GTK4 {
			kind = "gtk (no GTK 4 files)"
		}
		var inUse []string
		if cur[appearance.KeyGTKTheme] == t.Name {
			inUse = append(inUse, "gtk")
		}
		row(t, []string{kind}, inUse)
	}
	desk := e.Looks.Desktop
	if desk == nil || !desk.Supports(appearance.KeyShellTheme) {
		return tw.Flush()
	}
	shell, err := e.Looks.ShellThemes()
	if err != nil {
		return err
	}
	u := e.Looks.UserThemes(ctx)
	shellInUse := func(name string) []string {
		if v, ok := cur[appearance.KeyShellTheme]; ok && v == name && (u.Ready() || name == "") {
			return []string{"shell"}
		}
		if _, ok := cur[appearance.KeyShellTheme]; (!ok || !u.Ready()) && name == "" {
			return []string{"shell"} // without User Themes running, the Shell has its own look
		}
		return nil
	}
	fmt.Fprintf(tw, "%s\t%s\t%s\t%s\n", "Default", "shell", "GNOME", strings.Join(shellInUse(""), ", "))
	for _, t := range shell {
		row(t, []string{"shell"}, shellInUse(t.Name))
	}
	if err := tw.Flush(); err != nil {
		return err
	}
	if why := u.Why(); why != "" {
		fmt.Fprintf(w, "\n%s Using one offers to fix that.\n", why)
	}
	return nil
}

// presetHint says, after a font or theme was used, when the look no longer matches the preset applied last,
// and how to save it into it. The preset itself is left as it is.
func presetHint(ctx context.Context, w io.Writer, e *session.Session) {
	if p, ok := e.Looks.ChangedPreset(ctx, e.Presets); ok {
		fmt.Fprintf(w, "Your look no longer matches %s; to save it into the preset: uas appearance preset update %q\n",
			p.Name, p.Name)
	}
}

// offerUserThemes asks to get the User Themes extension running for a Shell theme, and does it when the
// user says yes. It never does anything without asking.
func offerUserThemes(ctx context.Context, w io.Writer, e *session.Session, u appearance.UserThemes) error {
	fix := u.Fix()
	if fix == "" {
		return errors.New(u.Why())
	}
	fmt.Fprintln(w, u.Why())
	if !u.Installed {
		fmt.Fprintln(w, "GNOME asks you before it installs it from extensions.gnome.org.")
	}
	ok, err := confirm(fix + "?")
	if err != nil {
		return fmt.Errorf("%s %s first, or run this on a terminal to be asked", u.Why(), userThemesHint(u))
	}
	if !ok {
		return errors.New("nothing was changed")
	}
	if err := e.Looks.EnableUserThemes(ctx); err != nil {
		return err
	}
	fmt.Fprintln(w, "User Themes is on.")
	return nil
}

// userThemesHint says how to get User Themes running from the command line.
func userThemesHint(u appearance.UserThemes) string {
	if !u.Installed {
		return "Install it with: uas extensions install " + appearance.UserThemesUUID + ","
	}
	if !u.ExtensionsOn {
		return "Turn extensions on in GNOME's Extensions app, and User Themes with: uas extensions enable " +
			appearance.UserThemesUUID + ","
	}
	return "Turn it on with: uas extensions enable " + appearance.UserThemesUUID + ","
}

// printGTK4 says what is in ~/.config/gtk-4.0 after a GTK theme was used.
func printGTK4(w io.Writer, e *session.Session) {
	g, err := e.Looks.GTK4()
	if err != nil {
		return
	}
	if g != nil && len(g.Written) > 0 {
		fmt.Fprintf(w, "Copied its GTK 4 files into %s: %s.\n", g.Dir, strings.Join(g.Written, ", "))
		if len(g.BackedUp) > 0 {
			verb, them := "are", "them"
			if len(g.BackedUp) == 1 {
				verb, them = "is", "it"
			}
			fmt.Fprintf(w, "Your %s %s kept in %s; revert puts %s back.\n", strings.Join(g.BackedUp, ", "), verb,
				g.Backup, them)
		}
	}
	fmt.Fprintln(w, appearance.GTKNote)
}

func orDefault(s string) string {
	if s == "" {
		return "the default"
	}
	return s
}
