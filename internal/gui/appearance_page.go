package gui

import (
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/tyler-builds/jamb/access"
	"github.com/tyler-builds/jamb/icons"
	"github.com/tyler-builds/jamb/ui"

	"github.com/tyler-builds/uas/internal/catalog"
	"github.com/tyler-builds/uas/internal/client/appearance"
)

// fontSamples are the lines a font's page draws in it.
var fontSamples = []struct {
	text string
	size float64
}{
	{"The quick brown fox jumps over the lazy dog", 34},
	{"ABCDEFGHIJKLMNOPQRSTUVWXYZ", 22},
	{"abcdefghijklmnopqrstuvwxyz 0123456789 &?!", 22},
	{"Sphinx of black quartz, judge my vow. The five boxing wizards jump quickly.", 15},
}

// lookPage shows one font or theme from the catalog: previews, how popular it is, its variants, and
// Install, Install and use, Use, Remove and Revert.
func (a *App) lookPage(ctx *ui.Context, id string) ui.Element {
	return ui.Component(func(ctx *ui.Context) ui.Element {
		t := ctx.Theme()
		res := a.appQuery(ctx, id)
		lk := a.looks(ctx)
		page := pageBody(detailMaxWidth).Gap(space6)
		switch {
		case res.Loading:
			return scroll(page.Child(ui.Skeleton().Height(ui.Px(150))).Child(ui.Skeleton().Height(ui.Px(90))).
				Child(ui.Skeleton().Height(ui.Px(320))))
		case res.Err != nil:
			return scroll(page.Child(ui.Empty("Could not open this").Icon(icons.TriangleAlert).
				Description(errText(res.Err))))
		case !res.Data.Kind.Appearance() || len(res.Data.Listings) == 0:
			return scroll(page.Child(ui.Empty("Not a font or theme").Icon(icons.SwatchBook)))
		}
		app := res.Data
		l := app.Listings[0]
		variants := appearance.Variants(l)
		variant := ui.Local(ctx, "variant", "")
		if v := variant.Peek(); v == "" || !slices.Contains(variants, v) {
			pick := ""
			if rec, ok := lk.Data.rec(id); ok && len(rec.Themes) > 0 {
				pick = rec.Themes[0]
			} else if len(variants) > 0 {
				pick = variants[0]
			}
			variant.Set(pick)
		}
		useAs := ui.Local(ctx, "use-as", defaultFontKey(app))

		tags := ui.Div().Row().Wrap().Gap(space2).Child(chip(ctx, kindLabel(app.Kind)))
		if app.Developer != "" {
			tags = tags.Child(chip(ctx, app.Developer))
		}
		if app.License != "" {
			tags = tags.Child(chip(ctx, licenseName(app.License)))
		}
		var mark ui.Element
		if app.Kind == catalog.KindFont {
			mark = a.fontSample(ctx, id, l, "Aa", 40, 80, 88)
		} else {
			mark = ui.Div().Size(ui.Px(88), ui.Px(88)).ClipContent().Child(a.previewImage(ctx, l, 88))
		}
		header := ui.Div().Row().Gap(space5).Align(ui.Center).
			Child(ui.Div().Size(ui.Px(88), ui.Px(88)).Radius(20).Background(t.Surface).Border(1, t.Border).Center().
				ClipContent().Shrink(0).Child(mark)).
			Child(ui.Div().Column().Gap(space1).Grow(1).Basis(ui.Px(0)).MinWidth(ui.Px(0)).
				Child(ui.Text(app.Name).FontSize(24).Semibold()).
				Child(ui.Text(app.Summary).FontSize(14).TextColor(t.TextMuted).MaxLines(2)).
				Child(ui.Div().MarginTop(space2).Child(tags))).
			Child(a.lookAction(ctx, app, l, lk, variants, variant, useAs))
		page = page.Child(header).Child(statRow(ctx, lookStats(ctx, l)))

		w, _ := ctx.WindowSize()
		wide := w-sidebarWidth > 1000
		mainW := min(w-sidebarWidth-space2-2, detailMaxWidth) - 2*pagePad
		if wide {
			mainW -= 290 + space6
		}
		main := ui.Div().Column().Gap(space6).MinWidth(ui.Px(0))
		if app.Kind == catalog.KindFont {
			col := ui.Div().Column().Gap(space4).Padding(space5).Radius(radiusL).Border(1, t.Border).
				Background(t.Surface).ClipContent()
			for _, s := range fontSamples {
				col = col.Child(a.fontSample(ctx, id, l, s.text, s.size, mainW-2*space5, float32(s.size)*1.6))
			}
			main = main.Child(ui.Div().Column().Gap(space2).Child(heading("Preview")).Child(col).
				Child(caption(ctx, fontPreviewNote(l))))
			if styles := fontStyles(l); styles != "" {
				main = main.Child(ui.Div().Column().Gap(space2).Child(heading("Styles")).
					Child(ui.Text(styles).FontSize(14).LineHeight(1.5)))
			}
		} else if len(app.Screenshots) > 0 {
			cols := 2
			if mainW < 600 {
				cols = 1
			}
			shotW := (mainW - float32(cols-1)*space3) / float32(cols)
			grid := ui.Div().Grid(repeatFr(cols)...).Gap(space3)
			for _, s := range app.Screenshots[:min(len(app.Screenshots), 6)] {
				grid = grid.Child(a.screenshot(s.URL, shotW, s.Caption))
			}
			main = main.Child(ui.Div().Column().Gap(space2).Child(heading("Previews")).Child(grid))
		}
		if app.Description != "" {
			main = main.Child(ui.Div().Column().Gap(space2).Child(heading("About")).Child(plainText(app.Description)))
		}
		if l.Trust != nil {
			main = main.Child(trustPanel(ctx, l))
		}
		body := ui.Div().Column().Gap(space6)
		side := a.lookSide(ctx, app, l)
		if wide {
			body = ui.Div().Row().Gap(space6).Align(ui.Start)
			side = ui.Div().Width(ui.Px(290)).Shrink(0).Child(side)
		}
		return scroll(page.Child(body.Child(main.Grow(1).Basis(ui.Px(0))).Child(side)))
	}).Key("look:" + id)
}

func kindLabel(k catalog.Kind) string {
	switch k {
	case catalog.KindIconTheme:
		return "Icon theme"
	case catalog.KindCursorTheme:
		return "Cursor theme"
	}
	return "Font"
}

// defaultFontKey is the setting a font is most likely wanted for: monospace for a fixed-width font.
func defaultFontKey(app *catalog.App) string {
	for _, c := range app.Categories {
		if strings.EqualFold(c, "Monospace") {
			return appearance.KeyMonospaceFont
		}
	}
	if len(app.Listings) > 0 && app.Listings[0].Source == catalog.SourceNerdFonts {
		return appearance.KeyMonospaceFont
	}
	return appearance.KeyFont
}

// fontPreviewNote says what the preview is drawn from.
func fontPreviewNote(l catalog.Listing) string {
	if l.Source == catalog.SourceNerdFonts {
		return "Nerd Fonts come in one archive per family, so uas draws the preview once the font is installed."
	}
	return "Drawn by uas from the font's regular file, which it downloads from Google Fonts' repository and " +
		"checks against the catalog. Apps may draw it a little differently."
}

// fontStyles lists a Google font's files' styles.
func fontStyles(l catalog.Listing) string {
	var styles []string
	for _, a := range l.Assets {
		if a.Kind == catalog.AssetFont && a.Style != "" && !slices.Contains(styles, a.Style) {
			styles = append(styles, a.Style)
		}
	}
	return strings.Join(styles, " · ")
}

// lookStats are the figures under a font's or theme's name.
func lookStats(ctx *ui.Context, l catalog.Listing) []stat {
	t := ctx.Theme()
	var stats []stat
	switch {
	case l.Rank > 0:
		stats = append(stats, stat{icons.TrendingUp, fmt.Sprintf("#%d", l.Rank), "On Google Fonts", t.TextMuted,
			"Its place among Google Fonts' families by how much they are used"})
	case l.Installs > 0:
		tip := "Downloads from " + l.Source.Name() + ", over all time"
		if l.Source == catalog.SourcePling {
			tip += ". Pling's counts are rough"
		}
		stats = append(stats, stat{icons.Download, compact(l.Installs), "Downloads", t.TextMuted, tip})
	}
	if l.Source == catalog.SourcePling {
		if l.Rated() {
			stats = append(stats, stat{icons.Star, fmt.Sprintf("%d of 100", l.Score), "Rating", t.TextMuted,
				"Pling users' rating"})
		} else {
			stats = append(stats, stat{icons.Star, "Not rated", "Rating", t.TextMuted, "Nobody has rated it on Pling yet"})
		}
	}
	if !l.Released.IsZero() {
		stats = append(stats, stat{icons.CalendarDays, ui.FormatRelative(l.Released, time.Now()), "Updated",
			t.TextMuted, l.Released.Format("2 January 2006")})
	}
	if l.Version != "" && l.Source != catalog.SourceGoogleFonts {
		stats = append(stats, stat{icons.Tag, shortVersion(l.Version), "Version", t.TextMuted, ""})
	}
	if l.Trust != nil {
		c := t.Success
		switch {
		case l.Trust.Score < 50:
			c = t.Danger
		case l.Trust.Score < 70:
			c = t.Warning
		}
		stats = append(stats, stat{icons.ShieldCheck, fmt.Sprintf("%d/100", l.Trust.Score), "Trust score", c,
			"How much evidence there is that it is used and kept up; see below"})
	} else {
		stats = append(stats, stat{icons.ShieldCheck, "Curated", "Source", t.Success, curatedTip(l.Source)})
	}
	return stats
}

func curatedTip(s catalog.SourceID) string {
	if s == catalog.SourceNerdFonts {
		return "From the Nerd Fonts project's own releases on GitHub, checked against GitHub's checksum"
	}
	return "Google Fonts' families are chosen and checked by Google's fonts team, and openly licensed"
}

// lookAction is the install area of a font's or theme's page.
func (a *App) lookAction(ctx *ui.Context, app *catalog.App, l catalog.Listing, lk ui.QueryResult[looks],
	variants []string, variant, useAs *ui.Signal[string]) ui.Element {
	t := ctx.Theme()
	big := ui.ButtonStyle{Root: ui.Style{Height: ui.Px(34), FontSize: 14, Radius: radiusS}}
	job, busy := a.running(app.ID)
	col := ui.Div().Column().Gap(space2).Width(ui.Px(250)).Shrink(0)
	desk := (appearance.Desktop)(nil)
	if a.s.Looks != nil {
		desk = a.s.Looks.Desktop
	}
	key := appearance.KeyIconTheme
	switch app.Kind {
	case catalog.KindCursorTheme:
		key = appearance.KeyCursorTheme
	case catalog.KindFont:
		key = useAs.Get()
	}
	canUse := desk != nil && desk.Supports(key)

	if len(variants) > 1 {
		opts := make([]ui.Option[string], len(variants))
		for i, v := range variants {
			opts[i] = ui.Option[string]{Value: v, Label: v}
		}
		col = col.Child(ui.Select(variant, opts...).Label("Variant").Width(ui.Full).Key("variant"))
	}
	if app.Kind == catalog.KindFont && desk != nil {
		var opts []ui.Option[string]
		for _, k := range appearance.FontKeys {
			if desk.Supports(k) {
				opts = append(opts, ui.Option[string]{Value: k, Label: "Use for " + strings.ToLower(appearance.KeyLabel(k))})
			}
		}
		if len(opts) > 0 {
			col = col.Child(ui.Select(useAs, opts...).Label("Use for").Width(ui.Full).Key("use-as"))
		}
	}
	rec, installed := lk.Data.rec(app.ID)
	switch {
	case lk.Loading:
		col = col.Child(ui.Button("Install").Primary().Large().Width(ui.Full).Styles(big).Loading(true))
	case installed:
		v := variant.Get()
		inUse := false
		target := v
		if app.Kind == catalog.KindFont {
			if len(rec.Families) > 0 {
				target = rec.Families[0]
			}
			inUse = slices.Contains(rec.Families, appearance.FontFamily(lk.Data.current[key]))
		} else {
			inUse = lk.Data.current[key] == v
		}
		// A variant in another file of the item is a reinstall.
		otherFile := app.Kind != catalog.KindFont && v != "" && !slices.Contains(rec.Themes, v)
		state := "Installed"
		if inUse {
			state = "In use"
		}
		row := ui.Div().Row().Align(ui.Center).Gap(space2).Height(ui.Px(36)).PaddingX(space3).Radius(radiusS).
			Background(t.AccentSoft).
			Child(ui.Icon(icons.CircleCheck).Size(15).Color(t.Success)).
			Child(ui.Div().Grow(1).Child(ui.Text(state).FontSize(14).Medium()))
		col = col.Child(row)
		switch {
		case otherFile:
			col = col.Child(ui.Button("Install this variant").Icon(icons.Download).Primary().Width(ui.Full).
				Loading(busy).OnClick(func() { a.installLook(ctx, l, v, "") }))
		case canUse && !inUse:
			col = col.Child(ui.Button("Use").Icon(icons.Check).Primary().Width(ui.Full).Disabled(busy).
				OnClick(func() { a.useLook(ctx, key, target) }))
		}
		line := ui.Div().Row().Align(ui.Center).Gap(space1).
			Child(ui.Div().Grow(1).Basis(ui.Px(0)).MinWidth(ui.Px(0)).Child(caption(ctx, "Installed by uas").Truncate()))
		if s, ok := lk.Data.savedFor(key); ok && canUse {
			line = line.Child(ui.Button("Revert").Ghost().Small().Tooltip("Go back to " + orDefault(s.Previous)).
				OnClick(func() { a.revertLook(ctx, key) }))
		}
		line = line.Child(ui.Button("Remove").Ghost().Small().Disabled(busy).
			OnClick(func() { r := rec; a.lookRemoving.Set(&r) }))
		col = col.Child(line)
	default:
		v := variant.Get()
		col = col.Child(ui.Button("Install").Icon(icons.Download).Secondary().Large().Width(ui.Full).Styles(big).
			Loading(busy).OnClick(func() { a.installLook(ctx, l, v, "") }))
		if canUse {
			col = col.Child(ui.Button("Install and use").Icon(icons.Sparkles).Primary().Large().Width(ui.Full).
				Styles(big).Disabled(busy).OnClick(func() { a.installLook(ctx, l, v, key) }))
		}
		if a2, ok := appearance.AssetFor(l, v); ok && app.Kind != catalog.KindFont {
			also := slices.DeleteFunc(slices.Clone(a2.Themes), func(s string) bool { return s == v })
			text := "Downloads " + a2.Name + " (" + compactSize(a2.Size) + ")"
			if len(also) > 0 {
				text += ", which also has " + strings.Join(also, ", ") + "; they are installed together"
			}
			col = col.Child(caption(ctx, text+"."))
		}
		if s, ok := lk.Data.savedFor(key); ok && canUse {
			col = col.Child(ui.Button("Revert to " + orDefault(s.Previous)).Ghost().Small().
				OnClick(func() { a.revertLook(ctx, key) }))
		}
	}
	if desk == nil {
		col = col.Child(caption(ctx, "This desktop's settings can't be changed from uas: choose it in its own settings."))
	}
	if busy {
		col = col.Child(ui.Div().Row().Gap(space2).Align(ui.Center).
			Child(ui.ProgressRing().Size(ui.RingSmall).Label(job.title)).
			Child(ui.Div().Grow(1).Basis(ui.Px(0)).MinWidth(ui.Px(0)).Child(caption(ctx, job.status).Truncate())))
	}
	return col
}

func orDefault(s string) string {
	if s == "" {
		return "the default"
	}
	return s
}

// compactSize is a file size for a caption.
func compactSize(n int64) string {
	switch {
	case n >= 1<<20:
		return fmt.Sprintf("%.1f MB", float64(n)/(1<<20))
	case n >= 1<<10:
		return fmt.Sprintf("%.0f KB", float64(n)/(1<<10))
	}
	return fmt.Sprintf("%d bytes", n)
}

// lookSide lists where a font or theme comes from, its files and links.
func (a *App) lookSide(ctx *ui.Context, app *catalog.App, l catalog.Listing) ui.Element {
	t := ctx.Theme()
	props := ui.Div().Column().Gap(space1)
	field := func(label string, value ui.Element) {
		props = props.Child(ui.Div().Row().Align(ui.Center).Gap(space3).MinHeight(ui.Px(30)).
			Child(ui.Div().Width(ui.Px(80)).Shrink(0).Child(muted(ctx, label))).
			Child(ui.Div().Row().Grow(1).Basis(ui.Px(0)).MinWidth(ui.Px(0)).Child(value)))
	}
	value := func(s string) *ui.TextElement { return ui.Text(s).FontSize(13).Medium().Truncate() }
	field("Source", value(l.Source.Name()))
	if app.Developer != "" {
		field("By", ui.Div().MinWidth(ui.Px(0)).Tooltip(app.Developer).Child(value(app.Developer)))
	}
	if app.License != "" {
		field("License", value(licenseName(app.License)))
	}
	var size int64
	files := 0
	for _, as := range l.Assets {
		size += as.Size
		files++
	}
	if app.Kind == catalog.KindFont && files > 0 {
		field("Files", value(fmt.Sprintf("%s, %s", plural(files, "file", "files"), compactSize(size))))
	}

	notes := ui.Div().Column().Gap(space1)
	for _, n := range a.s.LookNotes(l) {
		notes = notes.Child(infoRow(ctx, icons.Info, t.TextSubtle, n))
	}
	link := func(icon *icons.Icon, label, url string) ui.Element {
		return actionRow(ctx, icon, label, icons.ArrowUpRight).Role(access.RoleLink).Tooltip(url).
			OnClick(func() { openURL(url) })
	}
	links := ui.Div().Column().Gap(space1)
	if strings.HasPrefix(l.StoreURL, "https://") {
		links = links.Child(link(icons.Globe, "On "+l.Source.Name(), l.StoreURL))
	}
	if strings.HasPrefix(app.Homepage, "http") {
		links = links.Child(link(icons.House, "Project page", app.Homepage))
	}
	rule := func() ui.Element { return ui.Div().Height(ui.Px(1)).Background(t.Border) }
	return ui.Div().Column().Gap(space3).Child(props).Child(rule()).Child(notes).Child(rule()).Child(links)
}
