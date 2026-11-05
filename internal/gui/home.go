package gui

import (
	"context"
	"time"

	"github.com/tyler-builds/jamb/access"
	"github.com/tyler-builds/jamb/icons"
	"github.com/tyler-builds/jamb/ui"

	"github.com/tyler-builds/uas/internal/catalog"
)

// homeMaxWidth is how wide the home page and lists grow.
const homeMaxWidth = 1280

// newMinPopularity keeps "New & Updated" to apps people use: at least this many installs a year.
const newMinPopularity = 50_000

// homePage is the featured banner and rows of popular and fresh apps, then the sources.
func (a *App) homePage(ctx *ui.Context, info catalog.BuildInfo) ui.Element {
	cols := gridColumns(ctx, homeMaxWidth, 260, 4)
	popular := a.browse(ctx, catalog.BrowseQuery{AppsOnly: true, Order: catalog.OrderPopular, Limit: cols})
	fresh := a.browse(ctx, catalog.BrowseQuery{AppsOnly: true, Order: catalog.OrderUpdated,
		MinPopularity: newMinPopularity, Limit: cols})

	spot := a.browse(ctx, catalog.BrowseQuery{AppsOnly: true, Order: catalog.OrderPopular, Limit: 12})
	page := pageBody(homeMaxWidth).Gap(space8 + space1).
		Child(a.intro(ctx, info)).
		Child(a.spotlight(ctx, spot)).
		Child(section(sectionHeader(ctx, "Popular Apps", "", linkButton(ctx, "See all", icons.ArrowRight, func() {
			a.browseOrder.Set(catalog.OrderPopular)
			a.open(view{page: pageCategory})
		})), a.cardRow(ctx, popular, cols))).
		Child(section(sectionHeader(ctx, "New & Updated", "", linkButton(ctx, "See all", icons.ArrowRight, func() {
			a.browseOrder.Set(catalog.OrderUpdated)
			a.open(view{page: pageCategory})
		})), a.cardRow(ctx, fresh, cols))).
		Child(section(sectionHeader(ctx, "Categories", ""), a.categoryTiles(ctx))).
		Child(section(sectionHeader(ctx, "Sources", ""), a.sourceTiles(ctx)))
	return scroll(page)
}

// browse runs a catalog query for a list.
func (a *App) browse(ctx *ui.Context, q catalog.BrowseQuery) ui.QueryResult[[]catalog.SearchResult] {
	key := a.key("browse", q.Category, string(q.Source), string(q.Order), itoa(q.MinPopularity), itoa(q.Limit),
		boolKey(q.AppsOnly))
	return ui.Query(ctx, key, func(c context.Context) ([]catalog.SearchResult, error) {
		db, err := a.catalog(c)
		if err != nil {
			return nil, err
		}
		return db.Browse(c, q)
	}, ui.QueryKeepPrevious(), ui.QueryStaleTime(time.Hour))
}

// cardRow lays out a row of app cards.
func (a *App) cardRow(ctx *ui.Context, res ui.QueryResult[[]catalog.SearchResult], cols int) ui.Element {
	grid := ui.Div().Grid(repeatFr(cols)...).Gap(space3)
	if res.Loading {
		for range cols {
			grid = grid.Child(ui.Skeleton().Height(ui.Px(76)))
		}
		return grid
	}
	for _, r := range res.Data {
		grid = grid.Child(a.appCard(ctx, r))
	}
	return grid
}

// intro is the top of the home page: a title and a line about the catalog, with a way to browse it.
func (a *App) intro(ctx *ui.Context, info catalog.BuildInfo) ui.Element {
	system := a.s.Platform.PrettyName
	if system == "" {
		system = "your system"
	}
	return pageHeader(ctx, "Discover", compactThousands(info.Apps)+" apps for "+system+" from Flathub, Fedora, "+
		"Homebrew, COPR and GitHub, checked and ranked in one place.",
		ui.Button("Browse all").IconEnd(icons.ArrowRight).Secondary().Small().OnClick(func() {
			a.browseOrder.Set(catalog.OrderPopular)
			a.open(view{page: pageCategory})
		}))
}

// spotlightDay picks the Spotlight app; the screenshot test pins it.
var spotlightDay = func() int { return time.Now().YearDay() }

// spotlight features one popular app, a different one each day: its name, summary and first screenshot,
// with Install and a way to its page.
func (a *App) spotlight(ctx *ui.Context, res ui.QueryResult[[]catalog.SearchResult]) ui.Element {
	t := ctx.Theme()
	const height = 232
	if len(res.Data) == 0 {
		return ui.Skeleton().Height(ui.Px(height))
	}
	r := res.Data[spotlightDay()%len(res.Data)]
	app := ui.Query(ctx, a.key("app", r.ID), func(c context.Context) (*catalog.App, error) {
		db, err := a.catalog(c)
		if err != nil {
			return nil, err
		}
		return db.Resolve(c, r.ID)
	})
	var shot ui.Element = ui.Div()
	if app.Data != nil && len(app.Data.Screenshots) > 0 {
		s := app.Data.Screenshots[0]
		url := s.ThumbURL
		if url == "" {
			url = s.URL
		}
		// The whole screenshot, framed and centered in the card's right side.
		shot = ui.Div().Width(ui.Px(340)).Height(ui.Full).Shrink(0).Center().PaddingX(space6).
			Child(a.screenshot(url, 280, ""))
	}
	_, busy := a.running(r.ID)
	installed := a.installedApps(ctx)
	var action ui.Element = ui.Button("Install").Icon(icons.Download).Primary().Small().Loading(busy).
		OnClick(func() { a.quickInstall(ctx, r) })
	if _, ok := installed.find(r); ok {
		action = ui.Button("Installed").Icon(icons.Check).Secondary().Small().OnClick(func() { a.openApp(r.ID) })
	}
	glow := brandColor(t)
	return ui.Div().Row().Height(ui.Px(height)).Radius(radiusXL).Border(1, t.Border).ClipContent().
		BackgroundGradient(ui.LinearGradient(120, t.Surface, t.Surface.Mix(glow, 0.06))).
		Child(ui.Div().Column().Justify(ui.Center).Gap(space2).PaddingX(space8).Grow(1).Basis(ui.Px(0)).
			MinWidth(ui.Px(0)).
			Child(ui.Div().Row().Align(ui.Center).Gap(6).
				Child(ui.Icon(icons.Sparkles).Size(13).Color(glow)).
				Child(ui.Text("Spotlight").FontSize(12).Medium().TextColor(t.TextMuted))).
			Child(ui.Div().Row().Align(ui.Center).Gap(space3).MarginTop(space1).
				Child(a.appIcon(ctx, r.IconURL, 44, r.Name)).
				Child(ui.Text(r.Name).FontSize(22).Medium().Truncate())).
			Child(ui.Text(r.Summary).FontSize(14).TextColor(t.TextMuted).MaxLines(2)).
			Child(metaLine(ctx, sourceWords(ctx, r.Sources, r.Verified, 0)...)).
			Child(ui.Div().Row().Gap(space2).MarginTop(space2).
				Child(action).
				Child(ui.Button("View details").Secondary().Small().OnClick(func() { a.openApp(r.ID) })))).
		Child(shot)
}

// sourceTiles are the sources with their app counts, each opening its list.
func (a *App) sourceTiles(ctx *ui.Context) ui.Element {
	t := ctx.Theme()
	counts := ui.Query(ctx, a.key("source-counts"), func(c context.Context) (map[catalog.SourceID]int, error) {
		db, err := a.catalog(c)
		if err != nil {
			return nil, err
		}
		return db.SourceCounts(c)
	}, ui.QueryStaleTime(time.Hour))
	// One strip, split by hairlines, with a cell per source.
	strip := ui.Div().Row().Align(ui.Stretch).Radius(radiusL).Border(1, t.Border).Background(t.Surface).ClipContent()
	for i, s := range catalog.SourcePrecedence {
		s := s
		c := sourceColor(ctx, s)
		cell := ui.Div().Row().Align(ui.Center).Gap(space3).Grow(1).Basis(ui.Px(0)).MinWidth(ui.Px(0)).
			PaddingX(space4).PaddingY(space3).Cursor(ui.CursorPointer).Hover(ui.Style{Background: t.SurfaceHover}).
			Role(access.RoleButton).Label(s.Name()).TabIndex(0).Tooltip(sourceTips[s]).
			OnClick(func() { a.open(view{page: pageSource, arg: string(s)}) }).
			Child(ui.Icon(sourceIcons[s]).Size(18).Color(c)).
			Child(ui.Div().Column().Gap(1).MinWidth(ui.Px(0)).
				Child(subhead(s.Name())).
				Child(caption(ctx, plural(counts.Data[s], "app", "apps"))))
		if i > 0 {
			cell = cell.BorderEdges(ui.Edges{Left: 1}, t.Border)
		}
		strip = strip.Child(cell)
	}
	grid := strip
	return grid
}

// categoryTiles are the sidebar's categories as tiles, each opening its list.
func (a *App) categoryTiles(ctx *ui.Context) ui.Element {
	t := ctx.Theme()
	grid := ui.Div().Grid(repeatFr(gridColumns(ctx, homeMaxWidth, 220, 4))...).Gap(space1)
	for _, c := range navCategories {
		c := c
		grid = grid.Child(ui.Div().Row().Align(ui.Center).Gap(space3).Padding(space2).Radius(radiusM).
			Cursor(ui.CursorPointer).Hover(ui.Style{Background: t.SurfaceHover}).
			Role(access.RoleButton).Label(c.label).TabIndex(0).
			Tooltip(c.tip).OnClick(func() { a.goTo(view{page: pageCategory, arg: c.key}) }).
			Child(ui.Div().Size(ui.Px(36), ui.Px(36)).Radius(radiusM).Background(t.Surface).Border(1, t.Border).
				Center().Shrink(0).Child(ui.Icon(c.icon).Size(17).Color(t.TextMuted))).
			Child(ui.Div().Column().Gap(1).Grow(1).Basis(ui.Px(0)).MinWidth(ui.Px(0)).
				Child(subhead(c.label)).
				Child(ui.Text(c.blurb).FontSize(12).TextColor(t.TextMuted).Truncate())))
	}
	return grid
}

func itoa(n int) string { return compactThousands(n) }

func boolKey(b bool) string {
	if b {
		return "1"
	}
	return "0"
}
