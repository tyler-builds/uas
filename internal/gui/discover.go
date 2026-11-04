package gui

import (
	"context"
	"strings"
	"time"

	"github.com/tyler-builds/jamb/access"
	"github.com/tyler-builds/jamb/icons"
	"github.com/tyler-builds/jamb/ui"

	"github.com/tyler-builds/uas/internal/catalog"
)

// categories are the main AppStream categories, shown as chips when nothing is searched.
var categories = []struct{ key, label string }{
	{"", "Popular"},
	{"AudioVideo", "Audio & Video"},
	{"Development", "Development"},
	{"Education", "Education"},
	{"Game", "Games"},
	{"Graphics", "Graphics"},
	{"Network", "Internet"},
	{"Office", "Office"},
	{"Science", "Science"},
	{"System", "System"},
	{"Utility", "Utilities"},
}

const listLimit = 200

// discoverPage is the search field, the category chips and the matching apps.
func (a *App) discoverPage(ctx *ui.Context) ui.Element {
	t := ctx.Theme()
	q := strings.TrimSpace(a.search.Get())
	cat := a.category.Get()

	var key string
	var fetch func(context.Context) ([]catalog.SearchResult, error)
	if q != "" {
		key = a.key("search", q)
		fetch = func(c context.Context) ([]catalog.SearchResult, error) {
			db, err := a.catalog(c)
			if err != nil {
				return nil, err
			}
			return db.Search(c, q, listLimit)
		}
	} else {
		key = a.key("browse", cat)
		fetch = func(c context.Context) ([]catalog.SearchResult, error) {
			db, err := a.catalog(c)
			if err != nil {
				return nil, err
			}
			return db.Browse(c, cat, listLimit)
		}
	}
	res := ui.Query(ctx, key, fetch, ui.QueryKeepPrevious(), ui.QueryStaleTime(time.Hour))

	chips := ui.Div().Row().Wrap().Gap(8)
	for _, c := range categories {
		c := c
		b := ui.Button(c.label).Small().OnClick(func() { a.category.Set(c.key) }).Key("cat:" + c.key)
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
			Child(ui.If(res.Fetching && !res.Loading, ui.ProgressRing().Size(ui.RingSmall).Label("Searching")))).
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
		Key("row:" + r.ID)
}

// sourceBadges shows where an app is available.
func sourceBadges(sources []catalog.SourceID, verified bool) ui.Element {
	row := ui.Div().Row().Gap(4).Shrink(0)
	for _, s := range sources {
		row = row.Child(ui.BadgeText(sourceLabel(s)).Variant(ui.BadgeNeutral))
	}
	if verified {
		row = row.Child(ui.BadgeText("Verified").Variant(ui.BadgeSuccess).StatusDot())
	}
	return row
}

func sourceLabel(s catalog.SourceID) string { return s.Name() }
