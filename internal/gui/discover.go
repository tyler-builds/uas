package gui

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/tyler-builds/jamb/access"
	"github.com/tyler-builds/jamb/icons"
	"github.com/tyler-builds/jamb/ui"

	"github.com/tyler-builds/uas/internal/catalog"
)

// categories are the main AppStream categories, shown as chips when nothing is searched.
var categories = []struct{ key, label, tip string }{
	{"", "Popular", "The most installed desktop apps from every source"},
	{"AudioVideo", "Audio & Video", "Music and video players, editors, recorders and streaming apps"},
	{"Development", "Development", "Code editors, IDEs, debuggers and other tools for programmers"},
	{"Education", "Education", "Learning apps, language tools, and apps for schools and teaching"},
	{"Game", "Games", "Games, game launchers and emulators"},
	{"Graphics", "Graphics", "Image editors, drawing and painting, photo tools, 3D and design apps"},
	{"Network", "Internet", "Browsers, chat, email, file sharing and remote desktop apps"},
	{"Office", "Office", "Documents, spreadsheets, notes, calendars and other productivity apps"},
	{"Science", "Science", "Maths, astronomy, chemistry, data analysis and other science apps"},
	{"System", "System", "System monitors, settings, terminals, disk and hardware tools"},
	{"Utility", "Utilities", "Small tools: calculators, archivers, clipboard managers and the like"},
}

// The sort choices; browsing has no query, so no relevance.
var (
	searchOrders = []ui.Option[catalog.Order]{
		{Value: catalog.OrderRelevance, Label: "Best match"},
		{Value: catalog.OrderPopular, Label: "Most popular"},
		{Value: catalog.OrderUpdated, Label: "Recently updated"},
		{Value: catalog.OrderName, Label: "Name"},
	}
	browseOrders = searchOrders[1:]
)

const listLimit = 200

// discoverPage is the search field, the category chips and the matching apps.
func (a *App) discoverPage(ctx *ui.Context) ui.Element {
	t := ctx.Theme()
	q := strings.TrimSpace(a.search.Get())
	cat := a.category.Get()

	var key string
	var fetch func(context.Context) ([]catalog.SearchResult, error)
	var sortPicker ui.Element
	if q != "" {
		order := a.searchOrder.Get()
		key = a.key("search", q, string(order))
		fetch = func(c context.Context) ([]catalog.SearchResult, error) {
			db, err := a.catalog(c)
			if err != nil {
				return nil, err
			}
			return db.Search(c, q, listLimit, order)
		}
		sortPicker = ui.Select(a.searchOrder, searchOrders...).Label("Sort by").Width(ui.Px(190)).Small().
			Tooltip("Best match weighs how well the words match with how popular the app is")
	} else {
		order := a.browseOrder.Get()
		key = a.key("browse", cat, string(order))
		fetch = func(c context.Context) ([]catalog.SearchResult, error) {
			db, err := a.catalog(c)
			if err != nil {
				return nil, err
			}
			return db.Browse(c, cat, listLimit, order)
		}
		sortPicker = ui.Select(a.browseOrder, browseOrders...).Label("Sort by").Width(ui.Px(190)).Small()
	}
	res := ui.Query(ctx, key, fetch, ui.QueryKeepPrevious(), ui.QueryStaleTime(time.Hour))

	chips := ui.Div().Row().Wrap().Gap(8)
	for _, c := range categories {
		c := c
		b := ui.Button(c.label).Small().Tooltip(c.tip).OnClick(func() { a.category.Set(c.key) }).Key("cat:" + c.key)
		if c.key == cat {
			b = b.Primary()
		} else {
			b = b.Secondary()
		}
		chips = chips.Child(b)
	}

	heading := "Popular apps"
	for _, c := range categories {
		if c.key == cat && c.key != "" {
			heading = c.label
		}
	}
	if q != "" {
		heading = "Results for “" + q + "”"
	}

	var body ui.Element
	switch {
	case res.Loading:
		body = ui.Div().Column().Gap(8).Padding(4).
			Child(ui.Skeleton().Height(ui.Px(64))).Child(ui.Skeleton().Height(ui.Px(64))).
			Child(ui.Skeleton().Height(ui.Px(64)))
	case res.Err != nil:
		body = ui.Empty("Search failed").Icon(icons.TriangleAlert).Description(errText(res.Err))
	case len(res.Data) == 0:
		body = ui.Empty("No apps found").Icon(icons.Search).
			Description("Try other words, or check the spelling.")
	default:
		items := res.Data
		body = ui.UniformList(len(items), func(i int) ui.Element { return a.resultRow(ctx, items[i]) }).
			Style(ui.Style{Grow: 1})
	}

	return ui.Div().Column().Grow(1).Gap(16).Padding(24).MinHeight(ui.Px(0)).
		Child(ui.TextInput(a.query).Label("Search apps").Placeholder("Search apps").Icon(icons.Search).Large().
			OnChange(func(s string) { a.scheduleSearch(ctx, s) }).
			OnSubmit(func(s string) { a.search.Set(s) })).
		Child(ui.If(q == "", chips)).
		Child(ui.Div().Row().Align(ui.Center).Gap(8).
			Child(ui.Text(heading).FontSize(18).Semibold()).
			Child(ui.If(res.Fetching && !res.Loading, ui.ProgressRing().Size(ui.RingSmall).Label("Searching"))).
			Child(ui.Spacer()).
			Child(ui.Text("Sort by").FontSize(13).TextColor(t.TextMuted)).
			Child(sortPicker)).
		Child(ui.Div().Column().Grow(1).Basis(ui.Px(0)).MinHeight(ui.Px(0)).Child(body)).
		Child(ui.If(len(res.Data) == listLimit, ui.Text("Showing the first 200. Search to narrow it down.").
			FontSize(12).TextColor(t.TextMuted)))
}

// searchDebounce is how long typing must pause before a search runs.
const searchDebounce = 250 * time.Millisecond

// scheduleSearch searches for s once typing pauses.
func (a *App) scheduleSearch(ctx *ui.Context, s string) {
	if a.cancelSearch != nil {
		a.cancelSearch()
	}
	a.cancelSearch = ctx.After(searchDebounce, func() { a.search.Set(s) })
}

// resultRow is one app in a list: icon, name, summary and where it comes from.
func (a *App) resultRow(ctx *ui.Context, r catalog.SearchResult) ui.Element {
	t := ctx.Theme()
	return ui.Div().Row().Align(ui.Center).Gap(14).PaddingX(12).Height(ui.Px(72)).Radius(8).
		Cursor(ui.CursorPointer).Hover(ui.Style{Background: t.Surface}).
		Role(access.RoleButton).Label(r.Name).TabIndex(0).
		OnClick(func() { a.selected.Set(r.ID) }). // Enter and Space run it too when the row has focus

		Child(a.appIcon(ctx, r.IconURL, 48, r.Name)).
		Child(ui.Div().Column().Gap(2).Grow(1).Basis(ui.Px(0)).MinWidth(ui.Px(0)).
			Child(ui.Text(r.Name).FontSize(15).Semibold().Truncate()).
			Child(ui.Text(r.Summary).FontSize(13).TextColor(t.TextMuted).Truncate())).
		Child(sourceBadges(r.Sources, r.Verified)).
		Child(stats(ctx, r)).
		Key("row:" + r.ID)
}

// stats shows what the list's order is based on: installs (or GitHub stars) and the latest release.
func stats(ctx *ui.Context, r catalog.SearchResult) ui.Element {
	t := ctx.Theme()
	var reach, tip string
	switch {
	case r.Popularity > 0:
		reach = compact(r.Popularity) + " installs/yr"
		tip = "Installs in a year: Flathub's installs last month times 12, or Homebrew's installs on request " +
			"in the past year, whichever is higher"
	case r.Stars > 0:
		reach = "★ " + compact(r.Stars)
		tip = "Stars on GitHub. GitHub doesn't publish install counts, so stars stand in for popularity"
	default:
		reach = "No install data"
		tip = "This app's sources don't publish install counts"
	}
	updated := "Release date unknown"
	if !r.Updated.IsZero() {
		updated = "Updated " + r.Updated.Local().Format("Jan 2, 2006")
	}
	return ui.Div().Column().Gap(2).Align(ui.End).Width(ui.Px(140)).Shrink(0).Tooltip(tip).
		Child(ui.Text(reach).FontSize(12).Semibold()).
		Child(ui.Text(updated).FontSize(12).TextColor(t.TextMuted))
}

// compact formats a count like 1.2M or 34k.
func compact(n int) string {
	switch {
	case n >= 1_000_000:
		return trimZero(fmt.Sprintf("%.1f", float64(n)/1e6)) + "M"
	case n >= 10_000:
		return fmt.Sprintf("%dk", n/1000)
	case n >= 1000:
		return trimZero(fmt.Sprintf("%.1f", float64(n)/1e3)) + "k"
	}
	return fmt.Sprint(n)
}

func trimZero(s string) string { return strings.TrimSuffix(s, ".0") }

// sourceBadges shows where an app is available. Each badge explains its source on hover.
func sourceBadges(sources []catalog.SourceID, verified bool) ui.Element {
	row := ui.Div().Row().Gap(4).Shrink(0)
	for _, s := range sources {
		row = row.Child(ui.Div().Tooltip(sourceTips[s]).
			Child(ui.BadgeText(sourceLabel(s)).Variant(ui.BadgeNeutral)))
	}
	if verified {
		row = row.Child(ui.Div().Tooltip("Flathub has confirmed that the publisher is the app's own developer, " +
			"through the developer's website or code-hosting account").
			Child(ui.BadgeText("Verified").Variant(ui.BadgeSuccess).StatusDot()))
	}
	return row
}

// sourceTips explain each source in a sentence or two.
var sourceTips = map[catalog.SourceID]string{
	catalog.SourceFlathub: "On Flathub, the Flatpak app store. Flatpak apps run in a sandbox and install " +
		"without a restart",
	catalog.SourceFedora: "Packaged and signed by Fedora. Installing adds it to the system image " +
		"(layering), which takes effect after a restart",
	catalog.SourceHomebrew: "A Homebrew formula, mostly command-line tools. Installs for your user without " +
		"a restart",
	catalog.SourceGitHub: "Released on GitHub by its developer, as an AppImage. No distribution reviews it; " +
		"its page shows a trust score",
	catalog.SourceCOPR: "Built in a Fedora COPR project by the project's owner. Fedora doesn't review it, " +
		"and installing layers it into the system image",
}

func sourceLabel(s catalog.SourceID) string { return s.Name() }
