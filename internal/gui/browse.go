package gui

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/tyler-builds/jamb/ui"

	"github.com/tyler-builds/uas/internal/catalog"
)

// listMaxWidth is how wide list pages grow.
const listMaxWidth = 1280

// searchPage shows results for the top bar's search, with source filters and a sort.
func (a *App) searchPage(ctx *ui.Context) ui.Element {
	q := strings.TrimSpace(a.search.Get())
	src := a.searchSource.Get()
	order := a.searchOrder.Get()

	// A chip for each source this catalog has apps from (catalogs differ by system), and the selected one even
	// if it has none; the sort moves to the next line when they don't fit beside it.
	counts := a.sourceCounts(ctx)
	filters := segmented(ctx, filterChip(ctx, "All", src == "", func() { a.searchSource.Set("") }))
	for _, s := range catalog.SourcePrecedence {
		if counts.Data[s] == 0 && src != s {
			continue
		}
		s := s
		filters = filters.Child(filterChip(ctx, s.Name(), src == s, func() { a.searchSource.Set(s) }))
	}
	controls := ui.Div().Row().Wrap().Align(ui.Center).Justify(ui.SpaceBetween).Gap(space3).
		Child(filters).
		Child(orderPicker(a.searchOrder, true))
	page := pageBody(listMaxWidth)

	if q == "" {
		return scroll(page.
			Child(pageHeader(ctx, "Search", "Type in the box above to search "+a.appCount(ctx)+
				" apps by name or what they do.", nil)).
			Child(controls).
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
	var busy ui.Element
	if res.Fetching && !res.Loading {
		busy = ui.ProgressRing().Size(ui.RingSmall).Label("Searching")
	}
	return scroll(page.
		Child(pageHeader(ctx, "Search results", summary, busy)).
		Child(controls).
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
	return scroll(pageBody(listMaxWidth).
		Child(pageHeader(ctx, categoryLabel(key), categoryTip(key), orderPicker(a.browseOrder, false))).
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
		Child(pageHeader(ctx, src.Name(), sub, orderPicker(a.browseOrder, false))).
		Child(a.resultList(ctx, res, "This source has no apps in the catalog.")))
}

// pageHeader is the top of every page: its title, a line about it, and controls on the right.
func pageHeader(ctx *ui.Context, titleText, sub string, trailing ui.Element) ui.Element {
	return ui.Div().Row().Align(ui.End).Gap(space4).
		Child(ui.Div().Column().Gap(space1).Grow(1).Basis(ui.Px(0)).MinWidth(ui.Px(0)).
			Child(title(titleText)).
			Child(ui.If(sub != "", muted(ctx, sub)))).
		Child(ui.If(trailing != nil, ui.Div().Row().Align(ui.Center).Shrink(0).Child(trailing)))
}

// sourcesPage explains each source and opens its list.
func (a *App) sourcesPage(ctx *ui.Context) ui.Element {
	t := ctx.Theme()
	notes := map[catalog.SourceID]string{
		catalog.SourceFlathub:  "uas's first choice for desktop apps: sandboxed, no restart, and many publishers are verified.",
		catalog.SourceHomebrew: "The usual way to add command-line tools without touching the system.",
		catalog.SourceGitHub:   "Apps released only on GitHub, each with a trust score. uas checks every download's checksum.",
		catalog.SourceGitLab:   "Apps released on GitLab, each with a trust score. uas checks every download against the catalog.",
		catalog.SourceCodeberg: "Apps released on Codeberg, each with a trust score. uas checks every download against the catalog.",
		catalog.SourceDebian:   "Debian's own packages, installed with apt on Debian.",
		catalog.SourceUbuntu:   "Ubuntu's own packages, installed with apt on Ubuntu and systems built on it.",
		catalog.SourceArch:     "Arch Linux's own packages, installed with pacman on Arch and systems built on it.",
		catalog.SourceSnap:     "Snaps from the Snap Store, with the store's verified publishers marked.",
		catalog.SourceFedora:   "Fedora's own packages. On image-based Fedora, layering changes the system image, so uas uses it when nothing else fits.",
		catalog.SourceCOPR:     "Community builds from Fedora COPR, each with a trust score from its build history.",
	}
	page := pageBody(listMaxWidth).
		Child(pageHeader(ctx, "Sources", "Where uas finds apps, and how it treats each one. uas recommends the best fit for this system on every app's page.", nil)).
		Child(a.sourceTiles(ctx))
	list := ui.Div().Column().Gap(space3)
	counts := a.sourceCounts(ctx)
	for _, s := range catalog.SourcePrecedence {
		// The catalog for this system has only the sources it can use; the rest aren't worth explaining.
		if a.s.Backends[s] == nil && counts.Data[s] == 0 {
			continue
		}
		status := statusPill(ctx, a.s.Backends[s] != nil, "Ready")
		if a.s.Backends[s] == nil {
			status = statusPill(ctx, false, "Not available here")
		}
		list = list.Child(ui.Card().Gap(space2).
			Child(ui.Div().Row().Align(ui.Center).Gap(space3).
				Child(ui.Icon(sourceIcons[s]).Size(20).Color(sourceColor(ctx, s))).
				Child(ui.Div().Grow(1).Child(subhead(s.Name()))).
				Child(status)).
			Child(ui.Text(notes[s]).FontSize(14)).
			Child(ui.Text(sourceTips[s] + ".").FontSize(13).TextColor(t.TextMuted)))
	}
	return scroll(page.Child(sectionHeader(ctx, "How uas treats each source", "")).Child(list))
}
