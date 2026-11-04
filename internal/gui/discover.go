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
var categories = []struct {
	key, label, tip string
	icon            *icons.Icon
}{
	{"", "Popular", "The most installed desktop apps from every source", icons.Flame},
	{"AudioVideo", "Audio & Video", "Music and video players, editors, recorders and streaming apps", icons.Clapperboard},
	{"Development", "Development", "Code editors, IDEs, debuggers and other tools for programmers", icons.Code},
	{"Education", "Education", "Learning apps, language tools, and apps for schools and teaching", icons.GraduationCap},
	{"Game", "Games", "Games, game launchers and emulators", icons.Gamepad2},
	{"Graphics", "Graphics", "Image editors, drawing and painting, photo tools, 3D and design apps", icons.Palette},
	{"Network", "Internet", "Browsers, chat, email, file sharing and remote desktop apps", icons.Globe},
	{"Office", "Office", "Documents, spreadsheets, notes, calendars and other productivity apps", icons.Briefcase},
	{"Science", "Science", "Maths, astronomy, chemistry, data analysis and other science apps", icons.FlaskConical},
	{"System", "System", "System monitors, settings, terminals, disk and hardware tools", icons.Cpu},
	{"Utility", "Utilities", "Small tools: calculators, archivers, clipboard managers and the like", icons.Wrench},
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

// cardHeight fits a card's icon, name, two lines of summary and its footer.
const cardHeight = 208

// discoverPage is the hero with the search field, the category chips, the top picks and the list.
func (a *App) discoverPage(ctx *ui.Context, info catalog.BuildInfo) ui.Element {
	t := ctx.Theme()
	q := strings.TrimSpace(a.search.Get())
	cat := a.category.Get()

	var key string
	var fetch func(context.Context) ([]catalog.SearchResult, error)
	var order catalog.Order
	var sortPicker ui.Element
	if q != "" {
		order = a.searchOrder.Get()
		key = a.key("search", q, string(order))
		fetch = func(c context.Context) ([]catalog.SearchResult, error) {
			db, err := a.catalog(c)
			if err != nil {
				return nil, err
			}
			return db.Search(c, q, listLimit, order)
		}
		sortPicker = ui.Select(a.searchOrder, searchOrders...).Label("Sort by").Width(ui.Px(180)).Small().
			Tooltip("Best match weighs how well the words match with how popular the app is")
	} else {
		order = a.browseOrder.Get()
		key = a.key("browse", cat, string(order))
		fetch = func(c context.Context) ([]catalog.SearchResult, error) {
			db, err := a.catalog(c)
			if err != nil {
				return nil, err
			}
			return db.Browse(c, cat, listLimit, order)
		}
		sortPicker = ui.Select(a.browseOrder, browseOrders...).Label("Sort by").Width(ui.Px(180)).Small()
	}
	res := ui.Query(ctx, key, fetch, ui.QueryKeepPrevious(), ui.QueryStaleTime(time.Hour))

	page := ui.Div().Column().Gap(space6).Padding(space8).MaxWidth(ui.Px(1180)).
		Child(a.discoverHero(ctx, info)).
		Child(ui.If(q == "", a.categoryChips(ctx, cat)))

	label := "Popular"
	for _, c := range categories {
		if c.key == cat {
			label = c.label
		}
	}
	var sub string
	switch {
	case q != "":
		sub = fmt.Sprintf("%d apps", len(res.Data))
		if len(res.Data) == 1 {
			sub = "1 app"
		}
		if len(res.Data) == listLimit {
			sub = "The first 200 apps; add words to narrow it down"
		}
	case order == catalog.OrderPopular:
		sub = "Ranked by installs in the past year"
	case order == catalog.OrderUpdated:
		sub = "Newest releases first"
	default:
		sub = "In alphabetical order"
	}
	titleText := label
	if q != "" {
		titleText = "Results for “" + q + "”"
	}
	busy := ui.If(res.Fetching && !res.Loading, ui.ProgressRing().Size(ui.RingSmall).Label("Loading"))
	page = page.Child(sectionHeader(ctx, titleText, sub, busy, sortPicker))

	switch {
	case res.Loading:
		return scroll(page.Child(a.skeletonGrid(ctx)))
	case res.Err != nil:
		return scroll(page.Child(ui.Empty("Search failed").Icon(icons.TriangleAlert).Description(errText(res.Err))))
	case len(res.Data) == 0:
		return scroll(page.Child(ui.Empty("No apps found").Icon(icons.Search).
			Description("Try other words, or check the spelling.")))
	}

	items := res.Data
	// Browsing the most popular apps leads with a grid of cards; everything else is a list.
	if q == "" && order == catalog.OrderPopular {
		cols := a.gridColumns(ctx)
		n := min(len(items), cols*2)
		grid := ui.Div().Grid(repeatFr(cols)...).Gap(space4)
		for _, r := range items[:n] {
			grid = grid.Child(a.appCard(ctx, r))
		}
		page = page.Child(grid)
		items = items[n:]
		if len(items) > 0 {
			page = page.Child(sectionHeader(ctx, "More in "+label, fmt.Sprintf("%d more apps", len(items))))
		}
	}
	if len(items) > 0 {
		list := ui.Div().Column().Gap(2).Padding(space2).Radius(radiusL).Background(t.Surface).Border(1, t.Border)
		for _, r := range items {
			list = list.Child(a.appRow(ctx, r))
		}
		page = page.Child(list)
	}
	return scroll(page)
}

// discoverHero welcomes the user and holds the search field.
func (a *App) discoverHero(ctx *ui.Context, info catalog.BuildInfo) ui.Element {
	t := ctx.Theme()
	system := a.s.Platform.PrettyName
	if system == "" {
		system = "your system"
	}
	return hero(ctx).
		Child(ui.Div().Row().Align(ui.Center).Gap(space2).
			Child(ui.Icon(icons.Sparkles).Size(16).Color(t.Accent)).
			Child(ui.Text("EVERY SOURCE, ONE STORE").FontSize(11.5).Semibold().TextColor(t.Accent))).
		Child(display("Discover apps")).
		Child(ui.Text(fmt.Sprintf("%s apps from Flathub, Fedora, Homebrew, COPR and GitHub, ranked by popularity "+
			"and checked for %s.", compactThousands(info.Apps), system)).FontSize(15).TextColor(t.TextMuted)).
		Child(ui.Div().MaxWidth(ui.Px(640)).Child(
			ui.TextInput(a.query).Label("Search apps").Placeholder("Search apps by name or what they do").
				Icon(icons.Search).Large().Width(ui.Full).
				OnChange(func(s string) { a.scheduleSearch(ctx, s) }).
				OnSubmit(func(s string) { a.search.Set(s) })))
}

// categoryChips picks the category to browse.
func (a *App) categoryChips(ctx *ui.Context, cat string) ui.Element {
	chips := ui.Div().Row().Wrap().Gap(space2)
	for _, c := range categories {
		c := c
		b := ui.Button(c.label).Icon(c.icon).Small().Tooltip(c.tip).
			OnClick(func() { a.category.Set(c.key) }).Key("cat:" + c.key)
		if c.key == cat {
			b = b.Primary()
		} else {
			b = b.Secondary()
		}
		chips = chips.Child(b)
	}
	return chips
}

// gridColumns fits cards of about 280 px into the content area.
func (a *App) gridColumns(ctx *ui.Context) int {
	w, _ := ctx.WindowSize()
	content := min(w-sidebarWidth, 1180) - 2*space8
	return max(1, min(4, int(content/280)))
}

func repeatFr(n int) []ui.Length {
	cols := make([]ui.Length, n)
	for i := range cols {
		cols[i] = ui.Fr(1)
	}
	return cols
}

// skeletonGrid stands in for the cards while they load.
func (a *App) skeletonGrid(ctx *ui.Context) ui.Element {
	cols := a.gridColumns(ctx)
	grid := ui.Div().Grid(repeatFr(cols)...).Gap(space4)
	for range cols * 2 {
		grid = grid.Child(ui.Skeleton().Height(ui.Px(cardHeight)))
	}
	return grid
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

// appCard is one of the top picks: a large, clickable card.
func (a *App) appCard(ctx *ui.Context, r catalog.SearchResult) ui.Element {
	t := ctx.Theme()
	return card(ctx).Gap(space3).Height(ui.Px(cardHeight)).Cursor(ui.CursorPointer).
		Hover(ui.Style{Background: t.SurfaceHover, BorderColor: t.Accent.Alpha(0.45)}).
		Transition(120 * time.Millisecond).
		Role(access.RoleButton).Label(r.Name).TabIndex(0).
		OnClick(func() { a.selected.Set(r.ID) }).
		Child(ui.Div().Row().Align(ui.Start).Gap(space3).
			Child(a.iconTile(ctx, r.IconURL, 56, r.Name)).
			Child(ui.Spacer()).
			Child(ui.If(r.Verified, verifiedPill(ctx)))).
		Child(ui.Div().Column().Gap(space1).
			Child(ui.Text(r.Name).FontSize(16).Semibold().Truncate()).
			Child(ui.Text(r.Summary).FontSize(13).TextColor(t.TextMuted).MaxLines(2))).
		Child(ui.Spacer()).
		Child(ui.Div().Row().Align(ui.Center).Gap(space2).
			Child(reach(ctx, r)).
			Child(ui.Spacer()).
			Child(compactSourcePills(ctx, r.Sources))).
		Key("row:" + r.ID)
}

// compactSourcePills shows the first source and a count of the others, to fit a card.
func compactSourcePills(ctx *ui.Context, sources []catalog.SourceID) ui.Element {
	row := ui.Div().Row().Gap(space1 + 2).Shrink(0)
	if len(sources) == 0 {
		return row
	}
	row = row.Child(sourcePill(ctx, sources[0]))
	if more := sources[1:]; len(more) > 0 {
		names := make([]string, len(more))
		for i, s := range more {
			names[i] = s.Name()
		}
		row = row.Child(pill(ctx, fmt.Sprintf("+%d", len(more)), ctx.Theme().TextMuted,
			"Also on "+strings.Join(names, " and ")))
	}
	return row
}

// appRow is one app in a list.
func (a *App) appRow(ctx *ui.Context, r catalog.SearchResult) ui.Element {
	t := ctx.Theme()
	return ui.Div().Row().Align(ui.Center).Gap(space4).PaddingX(space3).PaddingY(space2 + 2).Radius(radiusM).
		Cursor(ui.CursorPointer).Hover(ui.Style{Background: t.SurfaceHover}).
		Role(access.RoleButton).Label(r.Name).TabIndex(0).
		OnClick(func() { a.selected.Set(r.ID) }). // Enter and Space run it too when the row has focus
		Child(a.iconTile(ctx, r.IconURL, 48, r.Name)).
		Child(ui.Div().Column().Gap(2).Grow(1).Basis(ui.Px(0)).MinWidth(ui.Px(0)).
			Child(ui.Text(r.Name).FontSize(15).Semibold().Truncate()).
			Child(ui.Text(r.Summary).FontSize(13).TextColor(t.TextMuted).Truncate())).
		Child(sourcePills(ctx, r.Sources, r.Verified)).
		Child(stats(ctx, r)).
		Key("row:" + r.ID)
}

// reach is the popularity figure the lists sort by: installs a year, or GitHub stars.
func reach(ctx *ui.Context, r catalog.SearchResult) ui.Element {
	t := ctx.Theme()
	var icon *icons.Icon
	var text, tip string
	switch {
	case r.Popularity > 0:
		icon, text = icons.TrendingUp, compact(r.Popularity)+" / yr"
		tip = "Installs in a year: Flathub's installs last month times 12, or Homebrew's installs on request " +
			"in the past year, whichever is higher"
	case r.Stars > 0:
		icon, text = icons.Star, compact(r.Stars)
		tip = "Stars on GitHub. GitHub doesn't publish install counts, so stars stand in for popularity"
	default:
		icon, text = icons.TrendingUp, "—"
		tip = "This app's sources don't publish install counts"
	}
	return ui.Div().Row().Align(ui.Center).Gap(5).Shrink(0).Tooltip(tip).
		Child(ui.Icon(icon).Size(14).Color(t.TextMuted)).
		Child(ui.Text(text).FontSize(13).Semibold())
}

// stats shows what the list's order is based on: installs (or GitHub stars) and the latest release.
func stats(ctx *ui.Context, r catalog.SearchResult) ui.Element {
	updated := "Release date unknown"
	if !r.Updated.IsZero() {
		updated = "Updated " + r.Updated.Local().Format("Jan 2, 2006")
	}
	return ui.Div().Column().Gap(3).Align(ui.End).Width(ui.Px(140)).Shrink(0).
		Child(reach(ctx, r)).
		Child(caption(ctx, updated))
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

// compactThousands formats a count like 7,732.
func compactThousands(n int) string {
	s := fmt.Sprint(n)
	for i := len(s) - 3; i > 0; i -= 3 {
		s = s[:i] + "," + s[i:]
	}
	return s
}

func trimZero(s string) string { return strings.TrimSuffix(s, ".0") }

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
