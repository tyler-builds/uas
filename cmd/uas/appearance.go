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

Fonts, icon themes and cursor themes, installed for your account: fonts from Google Fonts and Nerd Fonts,
themes from Pling (gnome-look.org).

commands:
  list                          what uas installed, and what the desktop uses (the default)
  search -kind K <words>        find fonts, icons or cursors (K)
  browse -kind K                the most popular fonts, icons or cursors
  info <id>                     show one, with its variants
  install [-variant V] [-use S] <id>
                                install one; -use also uses it for S (see use)
  remove <id>                   remove one uas installed
  use <S> <value>               set S: icons or cursor (a theme), interface, document or monospace (a font
                                family, or a family and size such as "Inter 11"), scheme (default, light
                                or dark) or accent (blue, teal, green, yellow, orange, red, pink, purple,
                                slate)
  revert <S>|all                put S, or every setting uas changed, back to what it was before uas
  themes                        the icon and cursor themes on this system
  updates                       fonts and themes uas installed that have a newer version
  update <id>                   install one's newer version
  presets                       your presets: looks saved under a name (see "uas appearance preset")
  preset <command>              save, apply and manage presets
`

// settingKeys are the names "use" and "revert" take for each setting, which are also the names of a
// preset's parts.
var settingKeys = map[string]string{"icons": appearance.KeyIconTheme, "cursor": appearance.KeyCursorTheme,
	"interface": appearance.KeyFont, "document": appearance.KeyDocumentFont, "monospace": appearance.KeyMonospaceFont,
	"scheme": appearance.KeyColorScheme, "style": appearance.KeyColorScheme, "accent": appearance.KeyAccentColor}

// settingNames lists the names of settingKeys for messages.
const settingNames = "icons, cursor, interface, document, monospace, scheme or accent"

// lookKinds are the names -kind takes.
var lookKinds = map[string]catalog.Kind{"fonts": catalog.KindFont, "font": catalog.KindFont,
	"icons": catalog.KindIconTheme, "icon": catalog.KindIconTheme, "cursors": catalog.KindCursorTheme,
	"cursor": catalog.KindCursorTheme}

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
	kind := fs.String("kind", "", "fonts, icons or cursors")
	variant := fs.String("variant", "", "the theme variant to install (see info)")
	use := fs.String("use", "", "also use it: icons, cursor (themes), interface, document or monospace (fonts)")
	limit := fs.Int("limit", 20, "maximum number of results")
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
			return fmt.Errorf("appearance %s: give -kind fonts, icons or cursors", cmd)
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
		if len(rest) != 2 {
			return fmt.Errorf("appearance use: give a setting (%s) and a value", settingNames)
		}
		key, err := setting(rest[0])
		if err != nil {
			return err
		}
		if err := e.Looks.Use(ctx, key, appearance.ParseValue(key, rest[1])); err != nil {
			return err
		}
		fmt.Fprintf(w, "%s: %s\n", appearance.KeyLabel(key), appearance.ValueLabel(key, e.Looks.Current(ctx)[key]))
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
			for _, s := range saved {
				fmt.Fprintf(w, "%s: %s\n", appearance.KeyLabel(s.Key), appearance.ValueLabel(s.Key, cur[s.Key]))
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
		fmt.Fprintf(w, "%s: %s\n", appearance.KeyLabel(key), appearance.ValueLabel(key, e.Looks.Current(ctx)[key]))
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
			v, ok := cur[key]
			if !ok {
				continue
			}
			line := fmt.Sprintf("%-15s %s", appearance.KeyLabel(key)+":", appearance.ValueLabel(key, v))
			for _, s := range saved {
				if s.Key == key {
					line += " (set by uas; revert goes back to " + orDefault(appearance.ValueLabel(key, s.Previous)) + ")"
				}
			}
			fmt.Fprintln(w, line)
		}
	}
	return nil
}

func listThemes(ctx context.Context, w io.Writer, e *session.Session) error {
	themes, err := e.Looks.Themes()
	if err != nil {
		return err
	}
	cur := e.Looks.Current(ctx)
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "NAME\tKIND\tWHERE\tIN USE")
	for _, t := range themes {
		var kinds []string
		if t.Icons {
			kinds = append(kinds, "icons")
		}
		if t.Cursors {
			kinds = append(kinds, "cursors")
		}
		where := "system"
		switch {
		case t.By != "":
			where = "uas (" + t.By + ")"
		case t.User:
			where = "you"
		}
		var inUse []string
		if cur[appearance.KeyIconTheme] == t.Name {
			inUse = append(inUse, "icons")
		}
		if cur[appearance.KeyCursorTheme] == t.Name {
			inUse = append(inUse, "cursor")
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\n", t.Name, strings.Join(kinds, ", "), where, strings.Join(inUse, ", "))
	}
	return tw.Flush()
}

func orDefault(s string) string {
	if s == "" {
		return "the default"
	}
	return s
}
