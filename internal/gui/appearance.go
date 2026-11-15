package gui

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/tyler-builds/jamb/access"
	"github.com/tyler-builds/jamb/icons"
	"github.com/tyler-builds/jamb/ui"

	"github.com/tyler-builds/uas/internal/catalog"
	"github.com/tyler-builds/uas/internal/client/appearance"
	"github.com/tyler-builds/uas/internal/client/backend"
	"github.com/tyler-builds/uas/internal/client/state"
)

// The Appearance page's tabs.
const (
	lookFonts     = "fonts"
	lookIcons     = "icons"
	lookCursors   = "cursors"
	lookInstalled = "installed"
)

// lookTabKind is the catalog kind a tab browses.
func lookTabKind(tab string) catalog.Kind {
	switch tab {
	case lookIcons:
		return catalog.KindIconTheme
	case lookCursors:
		return catalog.KindCursorTheme
	}
	return catalog.KindFont
}

// looksKey is the query of what uas installed, what the desktop uses, and the themes on the system.
const looksKey = "looks"

// looks is what the Appearance pages know about this system.
type looks struct {
	recs    []state.AppearanceInstall
	saved   []state.SavedSetting
	current map[string]string // the desktop's settings, by key
	themes  []appearance.Theme
}

// rec returns uas's install of a catalog ID.
func (l looks) rec(id string) (state.AppearanceInstall, bool) {
	for _, r := range l.recs {
		if r.ID == id {
			return r, true
		}
	}
	return state.AppearanceInstall{}, false
}

// savedFor returns the earlier value of a setting uas changed.
func (l looks) savedFor(key string) (state.SavedSetting, bool) {
	for _, s := range l.saved {
		if s.Key == key {
			return s, true
		}
	}
	return state.SavedSetting{}, false
}

// using returns the settings an install is in use for.
func (l looks) using(r state.AppearanceInstall) []string {
	var keys []string
	for key, v := range l.current {
		switch {
		case slices.Contains(appearance.FontKeys, key) && slices.Contains(r.Families, appearance.FontFamily(v)):
		case !slices.Contains(appearance.FontKeys, key) && slices.Contains(r.Themes, v) &&
			((key == appearance.KeyIconTheme && r.Kind == string(catalog.KindIconTheme)) ||
				(key == appearance.KeyCursorTheme && r.Kind == string(catalog.KindCursorTheme))):
		default:
			continue
		}
		keys = append(keys, key)
	}
	slices.Sort(keys)
	return keys
}

// looks asks for what uas installed, the desktop's settings and the themes on the system. Other apps
// change the settings too, so the answer is kept only for a few seconds.
func (a *App) looks(ctx *ui.Context) ui.QueryResult[looks] {
	return ui.Query(ctx, looksKey, func(c context.Context) (looks, error) {
		in := a.s.Looks
		if in == nil {
			return looks{}, errors.New("uas couldn't find your home folder")
		}
		var l looks
		var err error
		if l.recs, err = in.Records(); err != nil {
			return l, err
		}
		l.saved, _ = in.Saved()
		l.current = in.Current(c)
		l.themes, err = in.Themes()
		return l, err
	}, ui.QueryStaleTime(5*time.Second))
}

// refreshLooks asks again.
func refreshLooks(ctx *ui.Context) {
	ui.InvalidateQueries(ctx, func(key string) bool { return key == looksKey || strings.HasPrefix(key, "look-ups") })
}

// openLook shows a font's or theme's page.
func (a *App) openLook(id string) { a.open(view{page: pageLook, arg: id}) }

// appearancePage lets the user browse fonts, icon themes and cursor themes, and see what is installed. The
// top bar's field searches the tab's kind.
func (a *App) appearancePage(ctx *ui.Context, info catalog.BuildInfo) ui.Element {
	return ui.Component(func(ctx *ui.Context) ui.Element {
		tab := a.lookTab.Get()
		q := strings.TrimSpace(a.listFilter.Get())
		lk := a.looks(ctx)
		sub := "Fonts from Google Fonts and Nerd Fonts, and icon and cursor themes from Pling (gnome-look.org), " +
			"installed for your account."
		if a.s.Looks != nil && a.s.Looks.Desktop == nil {
			sub += " This desktop's settings can't be changed from uas, so they are installed only."
		}
		installed := "Installed"
		if !lk.Loading && lk.Err == nil {
			installed = fmt.Sprintf("Installed %d", len(lk.Data.recs))
		}
		chips := segmented(ctx,
			filterChip(ctx, "Fonts", tab == lookFonts, func() { a.lookTab.Set(lookFonts) }),
			filterChip(ctx, "Icons", tab == lookIcons, func() { a.lookTab.Set(lookIcons) }),
			filterChip(ctx, "Cursors", tab == lookCursors, func() { a.lookTab.Set(lookCursors) }),
			filterChip(ctx, installed, tab == lookInstalled, func() { a.lookTab.Set(lookInstalled) }))
		controls := ui.Div().Row().Wrap().Align(ui.Center).Justify(ui.SpaceBetween).Gap(space3).Shrink(0).Child(chips)
		page := pageBody(listMaxWidth).Child(pageHeader(ctx, "Appearance", sub, nil))
		if tab == lookInstalled {
			return scroll(page.Child(controls).Children(a.lookInstalledSections(ctx, lk, q)...))
		}
		sig := a.lookOrder
		opts := []ui.Option[catalog.Order]{
			{Value: catalog.OrderPopular, Label: "Most popular"},
			{Value: catalog.OrderUpdated, Label: "Recently updated"},
			{Value: catalog.OrderName, Label: "Name"},
		}
		if q != "" {
			sig = a.lookSearchOrder
			opts = append([]ui.Option[catalog.Order]{{Value: catalog.OrderRelevance, Label: "Most relevant"}}, opts...)
		}
		controls = controls.Child(ui.Select(sig, opts...).Label("Sort by").Small().Width(ui.Px(170)).
			Key("look-order:" + boolKey(q != "")))
		return fixed(page.Child(controls).Child(a.lookGrid(ctx, info, lk.Data, lookTabKind(tab), q, sig.Get())))
	}).Key("appearance")
}

// lookSources are the sources of a kind.
func lookSources(k catalog.Kind) []catalog.SourceID {
	if k == catalog.KindFont {
		return []catalog.SourceID{catalog.SourceGoogleFonts, catalog.SourceNerdFonts}
	}
	return []catalog.SourceID{catalog.SourcePling}
}

// lookGrid is a grid of cards of a kind: the most popular, or the ones the top bar's search matches.
func (a *App) lookGrid(ctx *ui.Context, info catalog.BuildInfo, lk looks, kind catalog.Kind, q string,
	order catalog.Order) ui.Element {
	var have, failed []string
	for _, src := range lookSources(kind) {
		if st, ok := info.AppearanceStatus(src); ok {
			if st.Listings > 0 {
				have = append(have, src.Name())
			} else if st.Error != "" {
				failed = append(failed, src.Name())
			}
		}
	}
	if len(have) == 0 {
		desc := "The app catalog on this computer was built before it listed fonts and themes."
		if len(failed) > 0 {
			desc = strings.Join(failed, " and ") + " could not be read when the catalog was built. The next catalog " +
				"should have them."
		}
		return ui.Empty("No " + kindPlural(kind) + " in the catalog yet").Icon(icons.SwatchBook).Description(desc).
			Action(ui.Button("Check for a newer catalog").Secondary().OnClick(func() { a.checkCatalog(ctx, true) }))
	}
	res := ui.Query(ctx, a.key("look-browse", string(kind), q, string(order)),
		func(c context.Context) ([]catalog.SearchResult, error) {
			db, err := a.catalog(c)
			if err != nil {
				return nil, err
			}
			if q != "" {
				return db.Search(c, q, catalog.SearchOptions{Order: order, Limit: 60, Kind: kind})
			}
			return db.Browse(c, catalog.BrowseQuery{Order: order, Limit: 60, Kind: kind})
		}, ui.QueryKeepPrevious(), ui.QueryStaleTime(time.Hour))
	cols := gridColumns(ctx, listMaxWidth, 250, 5)
	switch {
	case res.Loading:
		grid := ui.Div().Grid(repeatFr(cols)...).Gap(space3)
		for range cols * 2 {
			grid = grid.Child(ui.Skeleton().Height(ui.Px(220)))
		}
		return grid
	case res.Err != nil:
		return ui.Empty("Something went wrong").Icon(icons.TriangleAlert).Description(errText(res.Err))
	case len(res.Data) == 0:
		return ui.Empty("Nothing found").Icon(icons.Search).
			Description("No " + kindSingular(kind) + " matches “" + q + "”. Try other words.")
	}
	grid := ui.Div().Grid(repeatFr(cols)...).Gap(space3)
	for _, r := range res.Data {
		_, mine := lk.rec(r.ID)
		grid = grid.Child(a.lookCard(ctx, r, kind, mine))
	}
	what := "The most popular " + kindPlural(kind)
	switch {
	case q != "":
		what = plural(len(res.Data), kindSingular(kind), kindPlural(kind)) + " for “" + q + "”"
	case order == catalog.OrderUpdated:
		what = "The most recently updated " + kindPlural(kind)
	case order == catalog.OrderName:
		what = strings.ToUpper(kindPlural(kind)[:1]) + kindPlural(kind)[1:] + " by name"
	}
	what += ", from " + strings.Join(have, " and ") + "."
	if len(res.Data) == 60 {
		what += " Search to find others."
	}
	return ui.Div().Column().Gap(space3).MinHeight(ui.Px(0)).Child(muted(ctx, what)).
		Child(ui.Div().Column().ScrollY().MinHeight(ui.Px(0)).PaddingBottom(space2).Child(grid))
}

func kindSingular(k catalog.Kind) string {
	switch k {
	case catalog.KindIconTheme:
		return "icon theme"
	case catalog.KindCursorTheme:
		return "cursor theme"
	}
	return "font"
}

func kindPlural(k catalog.Kind) string { return kindSingular(k) + "s" }

// lookApp is a font's or theme's catalog entry, for a card or a page.
func (a *App) lookApp(ctx *ui.Context, id string) ui.QueryResult[*catalog.App] {
	return ui.Query(ctx, a.key("app", id), func(c context.Context) (*catalog.App, error) {
		db, err := a.catalog(c)
		if err != nil {
			return nil, err
		}
		return db.App(c, id)
	}, ui.QueryStaleTime(time.Hour))
}

// lookCard is a font or theme in a grid: a preview, its name and author, and how popular it is.
func (a *App) lookCard(ctx *ui.Context, r catalog.SearchResult, kind catalog.Kind, mine bool) ui.Element {
	return ui.Component(func(ctx *ui.Context) ui.Element {
		t := ctx.Theme()
		res := a.lookApp(ctx, r.ID)
		var l catalog.Listing
		if res.Data != nil && len(res.Data.Listings) > 0 {
			l = res.Data.Listings[0]
		}
		const previewH = 132
		var preview ui.Element
		if kind == catalog.KindFont {
			preview = a.fontSample(ctx, r.ID, l, r.Name, 30, 230, previewH)
		} else {
			preview = a.previewImage(ctx, l, previewH)
		}
		meta := []ui.Element{}
		if r.Developer != "" {
			meta = append(meta, ui.Text(r.Developer).FontSize(12).TextColor(t.TextSubtle).Truncate())
		}
		meta = append(meta, ui.Text(lookReach(l)).FontSize(12).TextColor(t.TextMuted).Shrink(0))
		name := ui.Div().Row().Align(ui.Center).Gap(space2).MinWidth(ui.Px(0)).
			Child(ui.Text(r.Name).FontSize(14).Medium().Truncate().Grow(1).Basis(ui.Px(0)).MinWidth(ui.Px(0)))
		if mine {
			name = name.Child(ui.Div().Tooltip("Installed").Child(ui.Icon(icons.CircleCheck).Size(15).Color(t.Success)))
		}
		return ui.Card().Gap(space2).Padding(space3).MinWidth(ui.Px(0)).Label(r.Name).
			OnClick(func() { a.openLook(r.ID) }).
			Child(ui.Div().Height(ui.Px(previewH)).Radius(radiusM).Background(t.SurfaceHover).ClipContent().Center().
				Child(preview)).
			Child(name).
			Child(metaLine(ctx, meta...))
	}).Key("look-card:" + r.ID)
}

// lookReach is the figure a card shows: downloads, Google Fonts' popularity rank, or the rating.
func lookReach(l catalog.Listing) string {
	switch {
	case l.Rank > 0:
		return fmt.Sprintf("#%d on Google Fonts", l.Rank)
	case l.Installs > 0:
		return compact(l.Installs) + " downloads"
	}
	return ""
}

// previewImage is a theme's first preview picture, cropped to fill height, or an icon when it has none.
func (a *App) previewImage(ctx *ui.Context, l catalog.Listing, height float32) ui.Element {
	t := ctx.Theme()
	if len(l.Screenshots) == 0 {
		icon := icons.Images
		if l.Kind == catalog.KindCursorTheme {
			icon = icons.MousePointer2
		}
		return ui.Icon(icon).Size(28).Color(t.TextSubtle)
	}
	url := l.Screenshots[0].URL
	if l.Screenshots[0].ThumbURL != "" {
		url = l.Screenshots[0].ThumbURL
	}
	return ui.Component(func(ctx *ui.Context) ui.Element {
		res := ui.Query(ctx, "image:"+url, func(c context.Context) (picture, error) {
			b, err := a.icons.load(c, url)
			if err != nil {
				return picture{}, err
			}
			return decodePicture(b)
		}, ui.QueryStaleTime(24*time.Hour), ui.QueryCacheTime(10*time.Minute))
		if res.Data.img == nil {
			if res.Err != nil {
				return ui.Icon(icons.ImageOff).Size(24).Color(t.TextSubtle)
			}
			return ui.Skeleton().Width(ui.Full).Height(ui.Px(height))
		}
		return ui.Image(res.Data.img).Size(ui.Full, ui.Px(height)).Fit(ui.FitCover).Alt(l.Name + " preview")
	}).Key("look-preview:" + url)
}

// maxPreviewFont is the largest font file a card downloads for its preview. Most Google fonts' regular file
// is far smaller; a font for Chinese or Japanese is several megabytes.
const maxPreviewFont = 1 << 20

// fontData returns a font file to preview a listing with: its first file from Google Fonts (from uas's
// cache, downloaded and checked against the catalog the first time), or a file uas installed. ok is false
// when there is none to use without a large download: a Nerd Font not yet installed, or a large file when
// big is false.
func (a *App) fontData(c context.Context, id string, l catalog.Listing, big bool) (data []byte, ok bool, err error) {
	if a.s.Looks == nil {
		return nil, false, nil
	}
	if recs, err := a.s.Looks.Records(); err == nil {
		for _, r := range recs {
			if r.ID != id || len(r.Paths) == 0 {
				continue
			}
			entries, err := os.ReadDir(r.Paths[0])
			if err != nil {
				break
			}
			for _, e := range entries {
				if n := strings.ToLower(e.Name()); strings.HasSuffix(n, ".ttf") || strings.HasSuffix(n, ".otf") {
					// Prefer the regular style, as the catalog's first file is.
					if strings.Contains(n, "regular") || len(entries) == 1 {
						b, err := os.ReadFile(filepath.Join(r.Paths[0], e.Name()))
						return b, err == nil, err
					}
				}
			}
			for _, e := range entries {
				if n := strings.ToLower(e.Name()); strings.HasSuffix(n, ".ttf") || strings.HasSuffix(n, ".otf") {
					b, err := os.ReadFile(filepath.Join(r.Paths[0], e.Name()))
					return b, err == nil, err
				}
			}
		}
	}
	if len(l.Assets) == 0 || l.Assets[0].Kind != catalog.AssetFont || (!big && l.Assets[0].Size > maxPreviewFont) {
		return nil, false, nil
	}
	p, err := a.s.Looks.FontFile(c, l.Assets[0])
	if err != nil {
		return nil, false, err
	}
	b, err := os.ReadFile(p)
	return b, err == nil, err
}

// fontSample draws text in a font listing's own font, at size points, within maxW by height; or says why it
// can't.
func (a *App) fontSample(ctx *ui.Context, id string, l catalog.Listing, text string, size float64, maxW,
	height float32) ui.Element {
	t := ctx.Theme()
	note := func(s string) ui.Element {
		return ui.Div().Column().Align(ui.Center).Gap(space1).Padding(space3).
			Child(ui.Text("Aa").FontSize(28).Semibold().TextColor(t.TextSubtle)).
			Child(caption(ctx, s).Align(ui.Center))
	}
	if l.Ref == "" {
		return ui.Skeleton().Width(ui.Full).Height(ui.Px(height))
	}
	ink := t.Text
	return ui.Component(func(ctx *ui.Context) ui.Element {
		res := ui.Query(ctx, a.key("font-sample", id, text, fmt.Sprint(size), fmt.Sprint(maxW), boolKey(t.Dark)),
			func(c context.Context) (*sample, error) {
				data, ok, err := a.fontData(c, id, l, size > 30)
				if err != nil || !ok {
					return nil, err
				}
				s, err := renderSample(data, text, size, ink, maxW)
				if err != nil {
					return nil, err
				}
				return &s, nil
			}, ui.QueryStaleTime(time.Hour), ui.QueryCacheTime(5*time.Minute))
		switch {
		case res.Loading:
			return ui.Skeleton().Width(ui.Px(maxW * 0.7)).Height(ui.Px(min(height, float32(size)*1.4)))
		case errors.Is(res.Err, errNoGlyphs):
			return note("A font for another script")
		case res.Err != nil:
			return note("No preview")
		case res.Data == nil:
			if l.Source == catalog.SourceNerdFonts {
				return note("Preview once installed")
			}
			return note("Preview on its page")
		}
		return ui.Image(res.Data.img).Size(ui.Px(res.Data.w), ui.Px(res.Data.h)).Alt(text + " in " + l.Name)
	}).Key("font-sample:" + id + ":" + text)
}

// lookInstalledSections are the Installed tab: what uas installed, the themes on the system with the ones
// in use marked, and the settings uas changed.
func (a *App) lookInstalledSections(ctx *ui.Context, lk ui.QueryResult[looks], q string) []ui.Element {
	switch {
	case lk.Loading:
		return []ui.Element{ui.Skeleton().Height(ui.Px(64)), ui.Skeleton().Height(ui.Px(200))}
	case lk.Err != nil:
		return []ui.Element{ui.Empty("Could not read what is installed").Icon(icons.TriangleAlert).
			Description(errText(lk.Err))}
	}
	d := lk.Data
	words := strings.Fields(strings.ToLower(q))
	match := func(s ...string) bool {
		hay := strings.ToLower(strings.Join(s, " "))
		for _, w := range words {
			if !strings.Contains(hay, w) {
				return false
			}
		}
		return true
	}
	var out []ui.Element
	if len(d.saved) > 0 {
		var rows []ui.Element
		for _, s := range d.saved {
			rows = append(rows, a.savedRow(ctx, s, d.current[s.Key]))
		}
		out = append(out, section(sectionHeader(ctx, "Changed by uas",
			"Revert puts a setting back to what it was before uas changed it."), settingsList(ctx, rows...)))
	}
	var mine []ui.Element
	for _, r := range d.recs {
		if match(r.Name, r.ID, strings.Join(r.Themes, " "), strings.Join(r.Families, " ")) {
			mine = append(mine, a.lookRecRow(ctx, r, d))
		}
	}
	if len(mine) > 0 {
		out = append(out, section(sectionHeader(ctx, "Installed by uas",
			"Fonts and themes uas installed for your account. It keeps them updated."), settingsList(ctx, mine...)))
	}
	for _, kind := range []catalog.Kind{catalog.KindIconTheme, catalog.KindCursorTheme} {
		var rows []ui.Element
		for _, th := range d.themes {
			if ((kind == catalog.KindIconTheme && th.Icons) || (kind == catalog.KindCursorTheme && th.Cursors)) &&
				match(th.Name, th.Title) {
				rows = append(rows, a.themeRow(ctx, th, kind, d))
			}
		}
		if len(rows) == 0 {
			continue
		}
		title, sub := "Icon themes", "Every icon theme on this system, whichever app installed it."
		if kind == catalog.KindCursorTheme {
			title, sub = "Cursor themes", "Every cursor theme on this system, whichever app installed it."
		}
		out = append(out, section(sectionHeader(ctx, title, sub), settingsList(ctx, rows...)))
	}
	if len(d.recs) == 0 && len(words) == 0 {
		out = append([]ui.Element{ui.Alert("uas hasn't installed any fonts or themes yet").
			Description("Browse Fonts, Icons and Cursors to add some. Themes already on this system are below.")}, out...)
	}
	if len(out) == 0 {
		out = append(out, ui.Empty("Nothing installed matches").Icon(icons.ListFilter).
			Description("Nothing installed matches “"+q+"”."))
	}
	return out
}

// savedRow is a setting uas changed, with Revert.
func (a *App) savedRow(ctx *ui.Context, s state.SavedSetting, now string) ui.Element {
	t := ctx.Theme()
	_, busy := a.running("revert:" + s.Key)
	was := s.Previous
	if was == "" {
		was = "the default"
	}
	return ui.Div().Row().Align(ui.Center).Gap(space3).MinHeight(ui.Px(56)).PaddingX(space4).PaddingY(space2).
		Child(ui.Div().Column().Gap(3).Grow(1).Basis(ui.Px(0)).MinWidth(ui.Px(0)).
			Child(ui.Text(appearance.KeyLabel(s.Key) + ": " + now).FontSize(14).Medium().Truncate()).
			Child(ui.Text("Before uas: " + was).FontSize(12).TextColor(t.TextSubtle).Truncate())).
		Child(ui.Button("Revert").Icon(icons.Undo2).Secondary().Small().Loading(busy).
			Tooltip("Go back to " + was).OnClick(func() { a.revertLook(ctx, s.Key) }))
}

// lookRecRow is a font or theme uas installed: what it is, what it's used for, and its page and removal.
func (a *App) lookRecRow(ctx *ui.Context, r state.AppearanceInstall, d looks) ui.Element {
	t := ctx.Theme()
	_, busy := a.running(r.ID)
	what := strings.Join(r.Themes, ", ")
	icon := icons.Images
	switch catalog.Kind(r.Kind) {
	case catalog.KindFont:
		what, icon = strings.Join(r.Families, ", "), icons.Type
	case catalog.KindCursorTheme:
		icon = icons.MousePointer2
	}
	parts := []ui.Element{ui.Text(what).FontSize(12).TextColor(t.TextSubtle).Truncate()}
	if r.Version != "" {
		parts = append([]ui.Element{ui.Text("Version " + r.Version).FontSize(12).TextColor(t.TextSubtle).Shrink(0)}, parts...)
	}
	row := ui.Div().Row().Align(ui.Center).Gap(space3).MinHeight(ui.Px(60)).PaddingX(space4).PaddingY(space2 + 2).
		Child(ui.Div().Size(ui.Px(36), ui.Px(36)).Radius(radiusM).Background(t.SurfaceHover).Center().Shrink(0).
			Child(ui.Icon(icon).Size(18).Color(t.TextMuted))).
		Child(ui.Div().Column().Gap(3).Grow(1).Basis(ui.Px(0)).MinWidth(ui.Px(0)).
			Cursor(ui.CursorPointer).OnClick(func() { a.openLook(r.ID) }).
			Role(access.RoleLink).Label(r.Name + " details").TabIndex(0).
			Child(ui.Text(r.Name).FontSize(14).Medium().Truncate()).
			Child(metaLine(ctx, parts...)))
	for _, key := range d.using(r) {
		row = row.Child(statusChip(ctx, "In use: "+strings.ToLower(appearance.KeyLabel(key)), t.Success))
	}
	return row.Child(ui.IconButton(icons.Trash).Ghost().Small().Label("Remove " + r.Name).Tooltip("Remove").
		Disabled(busy).OnClick(func() { rr := r; a.lookRemoving.Set(&rr) }))
}

// themeRow is a theme on the system, with Use, or its In use mark.
func (a *App) themeRow(ctx *ui.Context, th appearance.Theme, kind catalog.Kind, d looks) ui.Element {
	t := ctx.Theme()
	key := appearance.KeyIconTheme
	if kind == catalog.KindCursorTheme {
		key = appearance.KeyCursorTheme
	}
	where := a.s.SystemAppLabel()
	switch {
	case th.By != "":
		where = "Installed by uas"
	case th.User:
		where = "Added by you"
	}
	title := th.Name
	if th.Title != "" && th.Title != th.Name {
		title += " · " + th.Title
	}
	row := ui.Div().Row().Align(ui.Center).Gap(space3).MinHeight(ui.Px(52)).PaddingX(space4).PaddingY(space2).
		Child(ui.Div().Column().Gap(3).Grow(1).Basis(ui.Px(0)).MinWidth(ui.Px(0)).
			Child(ui.Text(title).FontSize(14).Medium().Truncate()).
			Child(ui.Text(where).FontSize(12).TextColor(t.TextSubtle).Truncate()))
	desk := a.s.Looks.Desktop
	switch {
	case d.current[key] == th.Name:
		row = row.Child(statusChip(ctx, "In use", t.Success))
	case desk != nil && desk.Supports(key):
		_, busy := a.running("use:" + key)
		row = row.Child(ui.Button("Use").Secondary().Small().Loading(busy).
			OnClick(func() { a.useLook(ctx, key, th.Name) }))
	}
	return row
}

// useLook sets a desktop setting to a theme or font family, saving the old value for Revert.
func (a *App) useLook(ctx *ui.Context, key, value string) {
	a.startJob(ctx, "use:"+key, "Using "+value, false,
		appearance.KeyLabel(key)+" set to "+value+". Revert is on the Installed tab.",
		func(c context.Context, _ func(backend.Progress)) error { return a.s.Looks.Use(c, key, value) })
}

// revertLook puts a setting back to what it was before uas changed it.
func (a *App) revertLook(ctx *ui.Context, key string) {
	a.startJob(ctx, "revert:"+key, "Reverting "+strings.ToLower(appearance.KeyLabel(key)), false,
		appearance.KeyLabel(key)+" is back to what it was",
		func(c context.Context, _ func(backend.Progress)) error { return a.s.Looks.Revert(c, key) })
}

// installLook installs a font or theme, and uses it when use names a setting.
func (a *App) installLook(ctx *ui.Context, l catalog.Listing, variant, use string) {
	id := catalog.AppearanceAppID(l.Source, l.Ref)
	done := l.Name + " is installed"
	if use != "" {
		done += " and in use"
	}
	a.startJob(ctx, id, "Installing "+l.Name, false, done,
		func(c context.Context, p func(backend.Progress)) error {
			_, err := a.s.InstallLook(c, l, variant, use, p)
			return err
		})
}

// lookRemoveDialog asks before removing a font or theme.
func (a *App) lookRemoveDialog(ctx *ui.Context) ui.Element {
	r := a.lookRemoving.Get()
	if r == nil {
		return ui.Div().Display(false)
	}
	what := "uas removes its files from your account."
	if r.Kind == string(catalog.KindFont) {
		what = "uas removes its font files from your account. Apps that use it fall back on another font."
	} else if len(r.Themes) > 1 {
		what = "uas removes " + strings.Join(r.Themes, ", ") + " from your account."
	}
	what += " If it is in use, the setting goes back to what it was first."
	return ui.AlertDialog("Remove "+r.Name+"?", what).Intent(ui.AlertDialogDestructive).
		Confirm("Remove", func() {
			a.lookRemoving.Set(nil)
			a.startJob(ctx, r.ID, "Removing "+r.Name, false, r.Name+" is removed",
				func(c context.Context, p func(backend.Progress)) error { return a.s.RemoveLook(c, r.ID, p) })
		}).
		Cancel("", func() { a.lookRemoving.Set(nil) })
}

// lookUpdates are the fonts and themes uas installed that have a newer file in the catalog.
func (a *App) lookUpdates(ctx *ui.Context) ui.QueryResult[[]appearance.Update] {
	return ui.Query(ctx, a.key("look-ups"), a.s.LookUpdates, ui.QueryStaleTime(10*time.Minute))
}

// lookUpdatesSection lists, on the Updates page, the fonts and themes with an update, with Update.
func (a *App) lookUpdatesSection(ctx *ui.Context) ui.Element {
	return ui.Component(func(ctx *ui.Context) ui.Element {
		t := ctx.Theme()
		ups := a.lookUpdates(ctx)
		if len(ups.Data) == 0 {
			return ui.Div().Display(false)
		}
		var rows []ui.Element
		for _, u := range ups.Data {
			_, busy := a.running(u.Install.ID)
			v := "A new build of version " + u.Install.Version
			switch {
			case u.Version() != "" && u.Install.Version != "":
				v = "Version " + u.Install.Version + " to " + u.Version()
			case u.Version() != "":
				v = "Version " + u.Version()
			case u.Install.Version == "":
				v = "A newer file"
			}
			rows = append(rows, ui.Div().Row().Align(ui.Center).Gap(space3).MinHeight(ui.Px(60)).PaddingX(space4).
				PaddingY(space2+2).
				Child(ui.Div().Column().Gap(3).Grow(1).Basis(ui.Px(0)).MinWidth(ui.Px(0)).
					Cursor(ui.CursorPointer).OnClick(func() { a.openLook(u.Install.ID) }).
					Role(access.RoleLink).Label(u.Install.Name+" details").TabIndex(0).
					Child(ui.Text(u.Install.Name).FontSize(14).Medium().Truncate()).
					Child(ui.Text(v).FontSize(12).TextColor(t.TextSubtle).Truncate())).
				Child(ui.Button("Update").Secondary().Small().Loading(busy).OnClick(func() {
					a.startJob(ctx, u.Install.ID, "Updating "+u.Install.Name, false, u.Install.Name+" is updated",
						func(c context.Context, p func(backend.Progress)) error { return a.s.UpdateLook(c, u, p) })
				})))
		}
		return ui.Div().Shrink(0).Child(section(sectionHeader(ctx, "Fonts and themes",
			"Newer versions of fonts and themes uas installed."), settingsList(ctx, rows...)))
	}).Key("look-updates")
}
