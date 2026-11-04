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

	page := pageBody(homeMaxWidth).
		Child(a.featured(ctx, info)).
		Child(sectionHeader(ctx, "Popular Apps", "", linkButton(ctx, "See all", icons.ArrowRight, func() {
			a.browseOrder.Set(catalog.OrderPopular)
			a.open(view{page: pageCategory})
		}))).
		Child(a.cardRow(ctx, popular, cols)).
		Child(sectionHeader(ctx, "New & Updated", "", linkButton(ctx, "See all", icons.ArrowRight, func() {
			a.browseOrder.Set(catalog.OrderUpdated)
			a.open(view{page: pageCategory})
		}))).
		Child(a.cardRow(ctx, fresh, cols)).
		Child(sectionHeader(ctx, "Browse by Source", "")).
		Child(a.sourceTiles(ctx, 3))
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
	grid := ui.Div().Grid(repeatFr(cols)...).Gap(space4)
	if res.Loading {
		for range cols {
			grid = grid.Child(ui.Skeleton().Height(ui.Px(128)))
		}
		return grid
	}
	for _, r := range res.Data {
		grid = grid.Child(a.appCard(ctx, r))
	}
	return grid
}

// featured is the banner at the top of the home page, with geometric art drawn on a canvas.
func (a *App) featured(ctx *ui.Context, info catalog.BuildInfo) ui.Element {
	t := ctx.Theme()
	art := artColors(t)
	surface := t.Surface
	system := a.s.Platform.PrettyName
	if system == "" {
		system = "your system"
	}
	art2 := ui.Canvas(func(p *ui.Painter, w, h float32) {
		p.FillRect(0, 0, w, h, ui.LinearPaint(0, 0, w, h,
			ui.ColorStop{At: 0, Color: surface}, ui.ColorStop{At: 1, Color: art[0].Alpha(0.6)}))
		shape := func(c ui.Color, alpha float32, pts ...[2]float32) {
			path := ui.NewPath().MoveTo(pts[0][0]*w, pts[0][1]*h)
			for _, pt := range pts[1:] {
				path.LineTo(pt[0]*w, pt[1]*h)
			}
			path.Close()
			p.Fill(path, ui.LinearPaint(pts[0][0]*w, h, w, 0,
				ui.ColorStop{At: 0, Color: c.Alpha(alpha * 0.55)}, ui.ColorStop{At: 1, Color: c.Alpha(alpha)}))
		}
		shape(art[0], 0.95, [2]float32{0.52, 1}, [2]float32{0.70, 0.18}, [2]float32{0.86, 0}, [2]float32{1, 0}, [2]float32{1, 1})
		shape(art[1], 0.55, [2]float32{0.60, 0}, [2]float32{0.76, 0}, [2]float32{0.58, 1}, [2]float32{0.44, 1})
		shape(art[1], 0.85, [2]float32{0.66, 1}, [2]float32{0.80, 0.42}, [2]float32{1, 0.26}, [2]float32{1, 1})
		shape(art[2], 0.80, [2]float32{0.84, 1}, [2]float32{0.92, 0.58}, [2]float32{1, 0.48}, [2]float32{1, 1})
		shape(art[2], 0.35, [2]float32{0.88, 0}, [2]float32{1, 0}, [2]float32{1, 0.30})
	}).Absolute().Inset(0).Size(ui.Full, ui.Full).AccessHidden() // a canvas is 300x150 unless sized
	// The art is a background layer; the words and the button sit on top as real elements, so assistive
	// technology reads them (a canvas is exposed as one image).
	return ui.Div().Height(ui.Px(220)).Radius(radiusXL).ClipContent().
		Child(art2).
		Child(ui.Div().Column().Justify(ui.Center).Gap(space3).PaddingX(space8 + 8).Height(ui.Full).
			Child(ui.Text("FEATURED").FontSize(12).Semibold().TextColor(t.TextMuted)).
			Child(display("Every app for " + system + ". One store.").FontSize(32)).
			Child(ui.Text(compactThousands(info.Apps) + " apps from Flathub, Fedora, Homebrew, COPR and GitHub, " +
				"checked and ranked in one place.").FontSize(15).TextColor(t.TextMuted)).
			Child(ui.Div().Row().MarginTop(space1).Child(a.featuredButton(ctx))))
}

// featuredButton is the banner's light call to action.
func (a *App) featuredButton(ctx *ui.Context) ui.Element {
	ink := ui.Hex("#111418")
	return ui.Div().Row().Align(ui.Center).Gap(space2).PaddingX(space4).Height(ui.Px(40)).Radius(radiusS).
		Background(ui.Hex("#f4f6f8")).Hover(ui.Style{Background: ui.Hex("#ffffff")}).Shadow(ui.ShadowSm).
		Cursor(ui.CursorPointer).Role(access.RoleButton).Label("Browse popular apps").TabIndex(0).
		OnClick(func() {
			a.browseOrder.Set(catalog.OrderPopular)
			a.open(view{page: pageCategory})
		}).
		Child(ui.Text("Browse popular").FontSize(15).Semibold().TextColor(ink)).
		Child(ui.Icon(icons.ArrowRight).Size(16).Color(ink))
}

// sourceTiles are the sources with their app counts, each opening its list.
func (a *App) sourceTiles(ctx *ui.Context, cols int) ui.Element {
	t := ctx.Theme()
	counts := ui.Query(ctx, a.key("source-counts"), func(c context.Context) (map[catalog.SourceID]int, error) {
		db, err := a.catalog(c)
		if err != nil {
			return nil, err
		}
		return db.SourceCounts(c)
	}, ui.QueryStaleTime(time.Hour))
	cols = min(cols, gridColumns(ctx, homeMaxWidth, 300, cols))
	grid := ui.Div().Grid(repeatFr(cols)...).Gap(space4)
	for _, s := range catalog.SourcePrecedence {
		s := s
		c := sourceColor(ctx, s)
		grid = grid.Child(clickable(ctx, ui.Div().Row().Align(ui.Center).Gap(space4).Padding(space5).
			Radius(radiusL).Background(t.Surface), s.Name(),
			func() { a.open(view{page: pageSource, arg: string(s)}) }).
			Tooltip(sourceTips[s]).
			Child(ui.Div().Size(ui.Px(48), ui.Px(48)).Radius(radiusM).Background(c.Alpha(0.15)).Center().
				Child(sourceMark(ctx, s, 26))).
			Child(ui.Div().Column().Gap(2).Grow(1).Basis(ui.Px(0)).MinWidth(ui.Px(0)).
				Child(subhead(s.Name())).
				Child(muted(ctx, plural(counts.Data[s], "app", "apps")))).
			Child(ui.Icon(icons.ChevronRight).Size(20).Color(t.TextMuted)))
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
