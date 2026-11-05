// Package gui is the uas desktop app, built with jamb.
package gui

import (
	"context"
	"errors"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/tyler-builds/jamb/icons"
	"github.com/tyler-builds/jamb/ui"

	"github.com/tyler-builds/uas/internal/catalog"
	"github.com/tyler-builds/uas/internal/client/backend"
	"github.com/tyler-builds/uas/internal/client/refresh"
	"github.com/tyler-builds/uas/internal/client/session"
)

// Pages. A view is a page and its argument: the category key, the source, or the app ID.
const (
	pageHome      = "home"
	pageSearch    = "search"
	pageSources   = "sources"
	pageCategory  = "category"
	pageSource    = "source"
	pageApp       = "app"
	pageInstalled = "installed"
	pageUpdates   = "updates"
	pageSettings  = "settings"
)

// view is one place in the app.
type view struct {
	page string
	arg  string
}

// isList reports whether the view is Installed or Updates, whose lists the top bar filters.
func (v view) isList() bool { return v.page == pageInstalled || v.page == pageUpdates }

// navKey is the sidebar key of a view, such as "category:Game".
func (v view) navKey() string {
	if v.arg != "" && v.page != pageApp {
		return v.page + ":" + v.arg
	}
	return v.page
}

// App is the window's state.
type App struct {
	s *session.Session

	// history is the stack of views; the last one is shown. The sidebar starts a new stack.
	history *ui.Signal[[]view]
	navSel  *ui.Signal[string] // the sidebar's selected key

	query  *ui.Signal[string] // the search field's text
	search *ui.Signal[string] // the query being searched, set after a pause in typing
	// listFilter and listSource narrow the Installed and Updates lists.
	listFilter   *ui.Signal[string]
	listSource   *ui.Signal[catalog.SourceID]
	searchSource *ui.Signal[catalog.SourceID]
	searchOrder  *ui.Signal[catalog.Order]
	browseOrder  *ui.Signal[catalog.Order]

	jobs     *ui.Signal[map[string]job]
	confirm  *ui.Signal[*pending] // an install waiting for the user's OK
	removing *ui.Signal[*removal] // a removal waiting for the user's OK

	// catalogGen changes when a refreshed catalog replaces the open one, so queries keyed by it reload.
	catalogGen *ui.Signal[int]
	mu         sync.Mutex
	db         *catalog.DB
	icons      *iconLoader

	startup sync.Once // the background catalog check at start
}

// New creates the app for a session.
func New(s *session.Session) *App {
	return &App{
		s:            s,
		history:      ui.NewSignal([]view{{page: pageHome}}),
		navSel:       ui.NewSignal(pageHome),
		query:        ui.NewSignal(""),
		search:       ui.NewSignal(""),
		listFilter:   ui.NewSignal(""),
		listSource:   ui.NewSignal[catalog.SourceID](""),
		searchSource: ui.NewSignal[catalog.SourceID](""),
		searchOrder:  ui.NewSignal(catalog.OrderRelevance),
		browseOrder:  ui.NewSignal(catalog.OrderPopular),
		jobs:         ui.NewSignal(map[string]job{}),
		confirm:      ui.NewSignal[*pending](nil),
		removing:     ui.NewSignal[*removal](nil),
		catalogGen:   ui.NewSignal(0),
		icons:        newIconLoader(),
	}
}

// current is the view on screen.
func (a *App) current() view {
	h := a.history.Get()
	return h[len(h)-1]
}

// open goes to v, keeping the way back.
func (a *App) open(v view) {
	h := a.history.Peek()
	if h[len(h)-1] == v {
		return
	}
	a.history.Set(append(slices.Clone(h), v))
	if v.page != pageApp {
		a.navSel.Set(v.navKey())
	}
}

// openApp shows an app's page.
func (a *App) openApp(id string) { a.open(view{page: pageApp, arg: id}) }

// goTo starts over at v, as the sidebar does.
func (a *App) goTo(v view) {
	if !v.isList() {
		a.listFilter.Set("")
		a.listSource.Set("")
	}
	a.history.Set([]view{v})
	a.navSel.Set(v.navKey())
}

// back returns to the previous view.
func (a *App) back() {
	h := a.history.Peek()
	if len(h) < 2 {
		return
	}
	h = h[:len(h)-1]
	a.history.Set(slices.Clone(h))
	for i := len(h) - 1; i >= 0; i-- {
		if h[i].page != pageApp {
			a.navSel.Set(h[i].navKey())
			break
		}
	}
}

// navView turns a sidebar key back into a view.
func navView(key string) view {
	page, arg, _ := strings.Cut(key, ":")
	return view{page: page, arg: arg}
}

// catalog returns the open catalog, opening (and if needed downloading) it on first use. It runs off the
// UI thread, inside queries.
func (a *App) catalog(ctx context.Context) (*catalog.DB, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.db != nil {
		return a.db, nil
	}
	db, err := a.s.OpenCatalog(ctx, nil)
	if err != nil {
		return nil, err
	}
	a.db = db
	return db, nil
}

// refreshCatalog downloads a newer catalog if there is one and switches to it. It runs off the UI thread.
func (a *App) refreshCatalog(ctx context.Context) (refresh.Result, error) {
	res, err := refresh.Refresh(ctx, session.RefreshOptions(a.s.CatalogPath))
	if err != nil || !res.Updated {
		return res, err
	}
	// The file was replaced; the open handle still reads the old one, so reopen.
	a.mu.Lock()
	old := a.db
	a.db = nil
	a.mu.Unlock()
	if old != nil {
		// Give queries that hold the old handle a moment to finish.
		time.AfterFunc(10*time.Second, func() { old.Close() })
	}
	return res, nil
}

// sidebarWidth is the width of the navigation pane.
const sidebarWidth = 224

// Root renders the window.
func (a *App) Root(ctx *ui.Context) ui.Element {
	t := ctx.Theme()
	// Open the catalog first; until it is ready, the window shows its progress.
	res := ui.Query(ctx, a.key("catalog"), func(c context.Context) (catalog.BuildInfo, error) {
		db, err := a.catalog(c)
		if err != nil {
			return catalog.BuildInfo{}, err
		}
		return db.Info(), nil
	}, ui.QueryStaleTime(time.Hour))

	if !res.Loading && res.Err == nil {
		// Look for a newer catalog in the background once the window is up.
		a.startup.Do(func() { ctx.Post(func() { a.checkCatalog(ctx, false) }) })
	}

	var content ui.Element
	switch {
	case res.Loading:
		content = ui.Div().Column().Center().Gap(space4).Grow(1).
			Child(brandMark(ctx, 64)).
			Child(subhead("Getting the app catalog ready")).
			Child(muted(ctx, "The first start downloads about 8 MB.")).
			Child(ui.ProgressRing().Size(ui.RingMedium).Label("Loading the app catalog"))
	case res.Err != nil:
		content = ui.Div().Column().Center().Grow(1).Child(
			ui.Empty("Could not load the app catalog").Icon(icons.CloudOff).Description(res.Err.Error()).
				Action(ui.Button("Try again").OnClick(res.Refetch)))
	default:
		content = a.page(ctx, res.Data)
	}

	// The pages sit on an inset panel, a step off the sidebar, with a hairline border.
	return ui.Div().Row().Size(ui.Full, ui.Full).Background(t.Panel).TextColor(t.Text).
		Child(a.sidebar(ctx, res.Data, !res.Loading && res.Err == nil)).
		Child(ui.Div().Column().Grow(1).Basis(ui.Px(0)).MinWidth(ui.Px(0)).
			MarginTop(space2).MarginRight(space2).MarginBottom(space2).
			Radius(radiusXL).Border(1, t.Border).Background(t.Background).ClipContent().
			Child(a.topBar(ctx)).
			Child(content)).
		Child(a.confirmDialog(ctx)).
		Child(a.removeDialog(ctx)).
		Child(ui.Toaster().Key("toaster"))
}

// page renders the current view.
func (a *App) page(ctx *ui.Context, info catalog.BuildInfo) ui.Element {
	v := a.current()
	switch v.page {
	case pageApp:
		return a.detailPage(ctx, v.arg)
	case pageSearch:
		return a.searchPage(ctx)
	case pageSources:
		return a.sourcesPage(ctx)
	case pageSource:
		return a.sourcePage(ctx, catalog.SourceID(v.arg))
	case pageCategory:
		return a.categoryPage(ctx, v.arg)
	case pageInstalled:
		return a.installedPage(ctx)
	case pageUpdates:
		return a.updatesPage(ctx)
	case pageSettings:
		return a.settingsPage(ctx, info)
	}
	return a.homePage(ctx, info)
}

// navCategories are the categories in the sidebar.
var navCategories = []struct {
	key, label, tip, blurb string
	icon                   *icons.Icon
}{
	{"Development", "Development", "Code editors, IDEs, debuggers and other tools for programmers", "Editors, IDEs, tools", icons.Code},
	{"Game", "Games", "Games, game launchers and emulators", "Games, launchers, emulators", icons.Gamepad2},
	{"Graphics", "Graphics", "Image editors, drawing and painting, photo tools, 3D and design apps", "Photo, drawing, 3D", icons.Palette},
	{"Network", "Internet", "Browsers, chat, email, file sharing and remote desktop apps", "Browsers, chat, email", icons.Globe},
	{"AudioVideo", "Multimedia", "Music and video players, editors, recorders and streaming apps", "Players, editors, streaming", icons.Play},
	{"Office", "Productivity", "Documents, spreadsheets, notes, calendars and other productivity apps", "Documents, notes, calendars", icons.CircleCheck},
	{"System", "System", "System monitors, settings, terminals, disk and hardware tools", "Monitors, terminals, disks", icons.Settings},
	{"Utility", "Utilities", "Small tools: calculators, archivers, clipboard managers and the like", "Calculators, archivers", icons.Wrench},
}

func categoryLabel(key string) string {
	for _, c := range navCategories {
		if c.key == key {
			return c.label
		}
	}
	if key == "" {
		return "Popular"
	}
	return key
}

func categoryTip(key string) string {
	for _, c := range navCategories {
		if c.key == key {
			return c.tip
		}
	}
	return "The most installed desktop apps from every source"
}

// sidebar is the navigation pane.
func (a *App) sidebar(ctx *ui.Context, info catalog.BuildInfo, ready bool) ui.Element {
	installed := ui.Query(ctx, "installed", a.s.Installed, ui.QueryStaleTime(30*time.Second))
	items := []ui.SidebarItem{
		{Key: pageHome, Label: "Home", Icon: icons.LayoutGrid},
		{Key: pageSearch, Label: "Search", Icon: icons.Search},
		{Key: pageSources, Label: "Sources", Icon: icons.Layers},
		ui.SidebarHeading("Categories"),
	}
	for _, c := range navCategories {
		items = append(items, ui.SidebarItem{Key: pageCategory + ":" + c.key, Label: c.label, Icon: c.icon})
	}
	items = append(items, ui.SidebarHeading("Library"),
		ui.SidebarItem{Key: pageInstalled, Label: "Installed", Icon: icons.Download, Badge: len(installed.Data)},
		ui.SidebarItem{Key: pageUpdates, Label: "Updates", Icon: icons.RefreshCw, Badge: len(a.updates(ctx).Data)},
	)
	// The selected item's fill marks it; no bar beside it.
	return ui.Sidebar(a.navSel, items...).Label("uas").Width(ui.Px(sidebarWidth)).NoIndicator().
		OnChange(func(key string) { a.goTo(navView(key)) }).
		Header(ui.Div().Row().Align(ui.Center).Gap(space2 + 2).PaddingX(space2).PaddingTop(space2).PaddingBottom(space3).
			Child(brandMark(ctx, 24)).
			Child(ui.Text("uas").FontSize(15).Semibold())).
		Footer(a.sidebarFooter(ctx, info, ready))
}

// sidebarFooter shows how fresh the catalog is.
func (a *App) sidebarFooter(ctx *ui.Context, info catalog.BuildInfo, ready bool) ui.Element {
	t := ctx.Theme()
	if !ready {
		return ui.Div()
	}
	_, checking := a.running("catalog")
	status := "Updated " + ui.FormatRelative(info.BuiltAt, time.Now())
	if checking {
		status = "Checking for a newer catalog…"
	}
	return ui.Div().Column().Gap(2).PaddingX(space2 + 2).PaddingY(space3).
		Child(ui.Div().Row().Align(ui.Center).Gap(6).
			Child(ui.Div().Size(ui.Px(6), ui.Px(6)).Radius(3).Background(t.Success.Alpha(0.7))).
			Child(ui.Text(compactThousands(info.Apps) + " apps").FontSize(12).Medium())).
		Child(caption(ctx, status))
}

// topBar holds the back button and where you are, the search field and the settings button.
func (a *App) topBar(ctx *ui.Context) ui.Element {
	t := ctx.Theme()
	left := ui.Div().Row().Align(ui.Center).Gap(space2).Grow(1).Basis(ui.Px(0)).MinWidth(ui.Px(0))
	if len(a.history.Get()) > 1 {
		left = left.Child(ui.IconButton(icons.ArrowLeft).Ghost().Small().Label("Back").Tooltip("Back").
			OnClick(a.back))
	}
	left = left.Child(a.crumbs(ctx))
	var search ui.Element
	if v := a.current(); v.isList() {
		// Installed and Updates filter their own list instead of searching the catalog.
		placeholder := "Filter installed apps…"
		if v.page == pageUpdates {
			placeholder = "Filter updates…"
		}
		search = ui.TextInput(a.listFilter).Label("Filter apps").Placeholder(placeholder).Icon(icons.ListFilter).
			Small().Clearable().Width(ui.Full).Key("list-filter")
	} else {
		search = ui.TextInput(a.query).Label("Search apps").Placeholder("Search apps…").Icon(icons.Search).
			Small().Clearable().Width(ui.Full).Key("search").
			OnChange(a.typed).
			OnSubmit(func(s string) {
				a.search.Set(strings.TrimSpace(s))
				a.showSearch()
			})
	}
	settings := ui.IconButton(icons.Settings).Ghost().Label("Settings").Tooltip("Settings").
		OnClick(func() { a.goTo(view{page: pageSettings}) })
	return ui.Div().Row().Align(ui.Center).Gap(space3).PaddingX(space4).Height(ui.Px(52)).Shrink(0).
		BorderEdges(ui.Edges{Bottom: 1}, t.Border).
		Child(left).
		Child(ui.Div().Width(ui.Px(340)).Shrink(1).MinWidth(ui.Px(160)).Child(search)).
		Child(settings)
}

// crumbs names the page on screen, after the page it was opened from when that helps.
func (a *App) crumbs(ctx *ui.Context) ui.Element {
	t := ctx.Theme()
	h := a.history.Get()
	v := h[len(h)-1]
	name := func(v view) string {
		switch v.page {
		case pageHome:
			return "Home"
		case pageSearch:
			return "Search"
		case pageSources:
			return "Sources"
		case pageCategory:
			return categoryLabel(v.arg)
		case pageSource:
			return catalog.SourceID(v.arg).Name()
		case pageInstalled:
			return "Installed"
		case pageUpdates:
			return "Updates"
		case pageSettings:
			return "Settings"
		}
		return ""
	}
	row := ui.Div().Row().Align(ui.Center).Gap(6).MinWidth(ui.Px(0))
	if v.page == pageApp {
		// The app's name comes from the query its page runs, so it is usually there already.
		res := ui.Query(ctx, a.key("app", v.arg), func(c context.Context) (*catalog.App, error) {
			db, err := a.catalog(c)
			if err != nil {
				return nil, err
			}
			return db.Resolve(c, v.arg)
		})
		app := ""
		if res.Data != nil {
			app = res.Data.Name
		}
		for i := len(h) - 2; i >= 0; i-- {
			if n := name(h[i]); n != "" {
				row = row.Child(ui.Text(n).FontSize(13).TextColor(t.TextMuted).Shrink(0)).
					Child(ui.Text("/").FontSize(13).TextColor(t.TextSubtle))
				break
			}
		}
		return row.Child(ui.Text(app).FontSize(13).Medium().Truncate())
	}
	return row.Child(ui.Icon(crumbIcon(v)).Size(15).Color(t.TextMuted)).
		Child(ui.Text(name(v)).FontSize(13).Medium().Truncate())
}

// crumbIcon is the sidebar's icon for a view.
func crumbIcon(v view) *icons.Icon {
	switch v.page {
	case pageSearch:
		return icons.Search
	case pageSources:
		return icons.Layers
	case pageSource:
		return sourceIcons[catalog.SourceID(v.arg)]
	case pageCategory:
		for _, c := range navCategories {
			if c.key == v.arg {
				return c.icon
			}
		}
		return icons.Flame
	case pageInstalled:
		return icons.Download
	case pageUpdates:
		return icons.RefreshCw
	case pageSettings:
		return icons.Settings
	}
	return icons.LayoutGrid
}

// typed reacts to the search field: it opens the search page and searches what the field holds, on every
// keystroke. A search takes 1 to 10 ms and the page keeps showing the previous results until the new ones
// are in, so there is no debounce: a debounce timer once left the page searching an older prefix of what
// was typed ("Godo" for "Godot").
func (a *App) typed(s string) {
	q := strings.TrimSpace(s)
	if q != "" {
		a.showSearch()
	}
	a.search.Set(q)
}

// showSearch opens the search page, unless it is open.
func (a *App) showSearch() {
	if a.current().page != pageSearch {
		a.open(view{page: pageSearch})
	}
}

// updates returns the available updates by InstalledApp.Key. Checking asks the network, so it is kept for
// ten minutes; jobs and the Refresh button check again.
func (a *App) updates(ctx *ui.Context) ui.QueryResult[map[string]string] {
	return ui.Query(ctx, "updates", func(c context.Context) (map[string]string, error) {
		ups, err := a.s.Updates(c)
		if len(ups) > 0 || err == nil {
			// Partial results are still useful when one source can't be reached.
			return ups, nil
		}
		return ups, err
	}, ui.QueryStaleTime(10*time.Minute))
}

// canCheck reports whether the source of an installed app can tell when it has an update.
func (a *App) canCheck(in backend.InstalledApp) bool {
	_, ok := a.s.Backends[in.Source].(backend.UpdateChecker)
	return ok
}

// key makes a query key that changes when the catalog is replaced.
func (a *App) key(parts ...string) string {
	return strings.Join(append([]string{strconv.Itoa(a.catalogGen.Peek())}, parts...), ":")
}

// reloadCatalog makes every catalog query fetch again. It runs on the UI thread.
func (a *App) reloadCatalog(ctx *ui.Context) {
	a.catalogGen.Set(a.catalogGen.Peek() + 1)
	ui.InvalidateQueries(ctx, func(string) bool { return true })
}

// errText shortens an error for one line of the UI.
func errText(err error) string {
	if errors.Is(err, context.Canceled) {
		return "Cancelled"
	}
	msg := err.Error()
	if i := strings.IndexByte(msg, '\n'); i >= 0 {
		msg = msg[:i]
	}
	return msg
}
