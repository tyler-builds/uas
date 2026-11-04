// Package gui is the uas desktop app, built with jamb.
package gui

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/tyler-builds/jamb/icons"
	"github.com/tyler-builds/jamb/ui"

	"github.com/tyler-builds/uas/internal/catalog"
	"github.com/tyler-builds/uas/internal/client/refresh"
	"github.com/tyler-builds/uas/internal/client/session"
)

// Pages of the sidebar.
const (
	pageDiscover  = "discover"
	pageInstalled = "installed"
	pageSettings  = "settings"
)

// App is the window's state.
type App struct {
	s *session.Session

	page     *ui.Signal[string]
	query    *ui.Signal[string] // the search field's text
	search   *ui.Signal[string] // the query being searched, set after a pause in typing
	category *ui.Signal[string] // the category browsed when there is no search; "" is Popular
	selected *ui.Signal[string] // the app whose page is open; "" shows the page list
	jobs     *ui.Signal[map[string]job]
	confirm  *ui.Signal[*pending] // an install waiting for the user's OK
	removing *ui.Signal[*removal] // a removal waiting for the user's OK

	// catalogGen changes when a refreshed catalog replaces the open one, so queries keyed by it reload.
	catalogGen *ui.Signal[int]
	mu         sync.Mutex
	db         *catalog.DB
	icons      *iconLoader

	cancelSearch func()    // stops a search waiting for typing to pause
	startup      sync.Once // the background catalog check at start
}

// New creates the app for a session.
func New(s *session.Session) *App {
	return &App{
		s:          s,
		page:       ui.NewSignal(pageDiscover),
		query:      ui.NewSignal(""),
		search:     ui.NewSignal(""),
		category:   ui.NewSignal(""),
		selected:   ui.NewSignal(""),
		jobs:       ui.NewSignal(map[string]job{}),
		confirm:    ui.NewSignal[*pending](nil),
		removing:   ui.NewSignal[*removal](nil),
		catalogGen: ui.NewSignal(0),
		icons:      newIconLoader(),
	}
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
		content = ui.Div().Column().Center().Gap(16).Grow(1).
			Child(ui.ProgressRing().Size(ui.RingLarge).Label("Loading the app catalog")).
			Child(ui.Text("Loading the app catalog…").TextColor(t.TextMuted))
	case res.Err != nil:
		content = ui.Div().Column().Center().Grow(1).Child(
			ui.Empty("Could not load the app catalog").Icon(icons.CloudOff).Description(res.Err.Error()).
				Action(ui.Button("Try again").OnClick(res.Refetch)))
	case a.selected.Get() != "":
		content = a.detailPage(ctx, a.selected.Get())
	default:
		switch a.page.Get() {
		case pageInstalled:
			content = a.installedPage(ctx)
		case pageSettings:
			content = a.settingsPage(ctx, res.Data)
		default:
			content = a.discoverPage(ctx)
		}
	}

	running := 0
	for id, j := range a.jobs.Get() {
		if !j.done && id != "catalog" {
			running++
		}
	}
	return ui.Div().Row().Size(ui.Full, ui.Full).Background(t.Background).TextColor(t.Text).
		Child(ui.Sidebar(a.page,
			ui.SidebarItem{Key: pageDiscover, Label: "Discover", Icon: icons.Compass},
			ui.SidebarItem{Key: pageInstalled, Label: "Installed", Icon: icons.Package, Badge: running},
			ui.SidebarItem{Key: pageSettings, Label: "Settings", Icon: icons.Settings},
		).Label("uas").Width(ui.Px(200)).OnChange(func(string) { a.selected.Set("") }).
			Header(ui.Div().Row().Align(ui.Center).Gap(10).PaddingX(16).PaddingY(14).
				Child(ui.Icon(icons.Store).Size(22).Color(t.Accent)).
				Child(ui.Text("uas").FontSize(18).Semibold()))).
		Child(ui.Div().Column().Grow(1).Basis(ui.Px(0)).MinWidth(ui.Px(0)).Child(content)).
		Child(a.confirmDialog(ctx)).
		Child(a.removeDialog(ctx)).
		Child(ui.Toaster().Key("toaster"))
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
