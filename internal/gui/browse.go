package gui

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/tyler-builds/jamb/icons"
	"github.com/tyler-builds/jamb/ui"

	"github.com/tyler-builds/uas/internal/catalog"
)

// listMaxWidth is how wide list pages grow.
const listMaxWidth = 1280

// searchPage shows results for the top bar's search, with source filters and a sort.
func (a *App) searchPage(ctx *ui.Context) ui.Element {
	t := ctx.Theme()
	q := strings.TrimSpace(a.search.Get())
	src := a.searchSource.Get()
	order := a.searchOrder.Get()

	filters := ui.Div().Row().Gap(space2).Wrap().Grow(1).Basis(ui.Px(0)).MinWidth(ui.Px(0)).
		Child(filterChip(ctx, "All", src == "", func() { a.searchSource.Set("") }))
	for _, s := range catalog.SourcePrecedence {
		s := s
		filters = filters.Child(filterChip(ctx, s.Name(), src == s, func() { a.searchSource.Set(s) }))
	}
	page := pageBody(listMaxWidth).
		Child(ui.Div().Row().Align(ui.Center).Gap(space4).Child(filters).Child(orderPicker(a.searchOrder, true)))

	if q == "" {
		return scroll(page.
			Child(ui.Div().Column().Gap(space1).Child(title("Search")).
				Child(muted(ctx, "Type in the box above to search "+a.appCount(ctx)+" apps by name or what they do."))).
			Child(ui.Div().Row().Wrap().Gap(space2).Children(a.categoryChips(ctx)...)))
	}
	res := ui.Query(ctx, a.key("search", q, string(src), string(order)),
		func(c context.Context) ([]catalog.SearchResult, error) {
			db, err := a.catalog(c)
			if err != nil {
				return nil, err
			}
			return db.Search(c, q, catalog.SearchOptions{Order: order, Source: src, Limit: 100})
		}, ui.QueryKeepPrevious(), ui.QueryStaleTime(time.Hour))

	summary := fmt.Sprintf("Showing %s for “%s”", plural(len(res.Data), "result", "results"), q)
	if src != "" {
		summary += " on " + src.Name()
	}
	if len(res.Data) == 100 {
		summary = "Showing the first 100 results for “" + q + "”; add words to narrow it down"
	}
	return scroll(page.
		Child(ui.Div().Column().Gap(space1).
			Child(ui.Div().Row().Align(ui.Center).Gap(space3).
				Child(title("Search Results")).
				Child(ui.If(res.Fetching && !res.Loading, ui.ProgressRing().Size(ui.RingSmall).Label("Searching")))).
			Child(ui.Text(summary).FontSize(15).TextColor(t.TextMuted))).
		Child(a.resultList(ctx, res, "Try other words, another source, or check the spelling.")))
}

// categoryChips are links to the categories, for the empty search page.
func (a *App) categoryChips(ctx *ui.Context) []ui.Element {
	var out []ui.Element
	for _, c := range navCategories {
		c := c
		out = append(out, ui.Button(c.label).Icon(c.icon).Secondary().Tooltip(c.tip).
			OnClick(func() { a.goTo(view{page: pageCategory, arg: c.key}) }))
	}
	return out
}

// appCount is the catalog's size, for sentences such as "search 7,732 apps".
func (a *App) appCount(ctx *ui.Context) string {
	res := ui.Query(ctx, a.key("catalog"), func(c context.Context) (catalog.BuildInfo, error) {
		db, err := a.catalog(c)
		if err != nil {
			return catalog.BuildInfo{}, err
		}
		return db.Info(), nil
	}, ui.QueryStaleTime(time.Hour))
	return compactThousands(res.Data.Apps)
}

// categoryPage lists a category's apps ("" is every category: the popular list).
func (a *App) categoryPage(ctx *ui.Context, key string) ui.Element {
	order := a.browseOrder.Get()
	if order == catalog.OrderRelevance {
		order = catalog.OrderPopular
	}
	res := a.browse(ctx, catalog.BrowseQuery{Category: key, Order: order, AppsOnly: true, Limit: 120})
	var icon *icons.Icon = icons.Flame
	for _, c := range navCategories {
		if c.key == key {
			icon = c.icon
		}
	}
	return scroll(pageBody(listMaxWidth).
		Child(a.listHeader(ctx, icon, ui.Color{}, categoryLabel(key), categoryTip(key), orderPicker(a.browseOrder, false))).
		Child(a.resultList(ctx, res, "Nothing in this category yet.")))
}

// sourcePage lists everything one source offers.
func (a *App) sourcePage(ctx *ui.Context, src catalog.SourceID) ui.Element {
	order := a.browseOrder.Get()
	if order == catalog.OrderRelevance {
		order = catalog.OrderPopular
	}
	res := a.browse(ctx, catalog.BrowseQuery{Source: src, Order: order, Limit: 120})
	sub := sourceTips[src] + "."
	if a.s.Backends[src] == nil {
		sub += " This system can't install from it."
	}
	return scroll(pageBody(listMaxWidth).
		Child(a.listHeader(ctx, sourceIcons[src], sourceColor(ctx, src), src.Name(), sub, orderPicker(a.browseOrder, false))).
		Child(a.resultList(ctx, res, "This source has no apps in the catalog.")))
}

// listHeader is the top of a category or source page: an icon tile, the title, a line about it, and the sort.
func (a *App) listHeader(ctx *ui.Context, icon *icons.Icon, color ui.Color, titleText, sub string,
	trailing ui.Element) ui.Element {
	t := ctx.Theme()
	if color == (ui.Color{}) {
		color = t.Accent
	}
	return ui.Div().Row().Align(ui.Center).Gap(space4).
		Child(ui.Div().Size(ui.Px(56), ui.Px(56)).Radius(radiusM).Background(color.Alpha(0.15)).Center().Shrink(0).
			Child(ui.Icon(icon).Size(28).Color(color))).
		Child(ui.Div().Column().Gap(space1).Grow(1).Basis(ui.Px(0)).MinWidth(ui.Px(0)).
			Child(title(titleText)).
			Child(muted(ctx, sub))).
		Child(trailing)
}

// sourcesPage explains each source and opens its list.
func (a *App) sourcesPage(ctx *ui.Context) ui.Element {
	t := ctx.Theme()
	notes := map[catalog.SourceID]string{
		catalog.SourceFlathub:  "uas's first choice for desktop apps: sandboxed, no restart, and many publishers are verified.",
		catalog.SourceHomebrew: "The usual way to add command-line tools on Bazzite, without touching the system image.",
		catalog.SourceGitHub:   "Apps released only on GitHub, each with a trust score. uas checks every download's checksum.",
		catalog.SourceFedora:   "Fedora's own packages. Layering changes the system image, so uas uses it when nothing else fits.",
		catalog.SourceCOPR:     "Community builds from Fedora COPR, each with a trust score from its build history.",
	}
	page := pageBody(listMaxWidth).
		Child(ui.Div().Column().Gap(space1).
			Child(title("Sources")).
			Child(muted(ctx, "Where uas finds apps, and how it treats each one. uas recommends the best fit for this system on every app's page."))).
		Child(a.sourceTiles(ctx, 3))
	list := ui.Div().Column().Gap(space3)
	for _, s := range catalog.SourcePrecedence {
		status := statusPill(ctx, a.s.Backends[s] != nil, "Ready")
		if a.s.Backends[s] == nil {
			status = statusPill(ctx, false, "Not available here")
		}
		list = list.Child(card(ctx).Gap(space2).
			Child(ui.Div().Row().Align(ui.Center).Gap(space3).
				Child(ui.Icon(sourceIcons[s]).Size(20).Color(sourceColor(ctx, s))).
				Child(ui.Div().Grow(1).Child(subhead(s.Name()))).
				Child(status)).
			Child(ui.Text(notes[s]).FontSize(14)).
			Child(ui.Text(sourceTips[s] + ".").FontSize(13).TextColor(t.TextMuted)))
	}
	return scroll(page.Child(sectionHeader(ctx, "How uas treats each source", "")).Child(list))
}
