package gui

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/tyler-builds/jamb/access"
	"github.com/tyler-builds/jamb/icons"
	"github.com/tyler-builds/jamb/ui"

	"github.com/tyler-builds/uas/internal/catalog"
	"github.com/tyler-builds/uas/internal/client/backend"
	"github.com/tyler-builds/uas/internal/client/shellext"
)

// The Extensions page's tabs.
const (
	extInstalled = "installed"
	extBrowse    = "browse"
)

// extensionsKey is the query of what GNOME Shell reports about its extensions.
const extensionsKey = "extensions"

// shellState is what GNOME Shell reports: every extension it knows, whoever installed it, and itself.
type shellState struct {
	list []shellext.Extension
	// allowed is GNOME's main switch for extensions. With it off none run.
	allowed bool
	version string // "51.1"
	major   string // "51", which is how extensions name the releases they support
}

// find returns the installed extension with a UUID.
func (s shellState) find(uuid string) (shellext.Extension, bool) {
	for _, e := range s.list {
		if e.UUID == uuid {
			return e, true
		}
	}
	return shellext.Extension{}, false
}

// shell asks GNOME Shell for its extensions. Other apps change them too, so the answer is kept only for a
// few seconds, and the pages that show it ask again while they are on screen (see keepFresh).
func (a *App) shell(ctx *ui.Context) ui.QueryResult[shellState] {
	return ui.Query(ctx, extensionsKey, func(c context.Context) (shellState, error) {
		m := a.s.Extensions
		if m == nil {
			return shellState{}, shellext.ErrUnavailable
		}
		list, err := m.List(c)
		if err != nil {
			return shellState{}, err
		}
		st := shellState{list: list, allowed: true}
		if on, err := m.UserExtensionsEnabled(c); err == nil {
			st.allowed = on
		}
		if v, err := m.ShellVersion(c); err == nil {
			st.version, st.major = v, shellext.MajorOf(v)
		}
		return st, nil
	}, ui.QueryStaleTime(5*time.Second))
}

// refreshExtensions asks GNOME Shell again.
func refreshExtensions(ctx *ui.Context) {
	ui.InvalidateQueries(ctx, func(key string) bool { return key == extensionsKey })
}

// keepFresh asks GNOME Shell again every few seconds while the calling component is on screen, so a change
// made in another app (GNOME's Extensions app, a browser) shows up.
func keepFresh(ctx *ui.Context) {
	tick := ui.Local(ctx, "ext-tick", 0)
	_ = tick.Get() // re-render when it ticks, which sets the next timer
	timer := ui.Local(ctx, "ext-timer", &pendingTimer{})
	if p := timer.Peek(); !p.on {
		p.on = true
		ctx.After(5*time.Second, func() {
			p.on = false
			tick.Set(tick.Peek() + 1)
			refreshExtensions(ctx)
		})
	}
}

// extensionsPage lists GNOME Shell extensions: the ones on this system, with their switches, and the ones
// extensions.gnome.org has for this version of GNOME. The top bar's field filters the first and searches the
// second.
func (a *App) extensionsPage(ctx *ui.Context, info catalog.BuildInfo) ui.Element {
	return ui.Component(func(ctx *ui.Context) ui.Element {
		sh := a.shell(ctx)
		keepFresh(ctx)
		tab := a.extTab.Get()
		q := strings.TrimSpace(a.listFilter.Get())

		sub := "Add-ons for the GNOME desktop, from extensions.gnome.org. GNOME installs and runs them; " +
			"uas finds them and switches them on and off."
		_, checking := a.running("ext-updates")
		check := ui.Button("Check for updates").Icon(icons.RefreshCw).Secondary().Small().Loading(checking).
			Tooltip("GNOME downloads newer versions of your extensions and installs them the next time you log in").
			OnClick(func() {
				a.startJob(ctx, "ext-updates", "Checking extensions for updates", false,
					"GNOME is checking. It installs what it finds the next time you log in.",
					func(c context.Context, _ func(backend.Progress)) error { return a.s.Extensions.CheckUpdates(c) })
			})

		installedLabel := "Installed"
		if !sh.Loading && sh.Err == nil {
			installedLabel = fmt.Sprintf("Installed %d", len(sh.Data.list))
		}
		controls := ui.Div().Row().Wrap().Align(ui.Center).Justify(ui.SpaceBetween).Gap(space3).Shrink(0).
			Child(segmented(ctx,
				filterChip(ctx, installedLabel, tab == extInstalled, func() { a.extTab.Set(extInstalled) }),
				filterChip(ctx, "Browse", tab == extBrowse, func() { a.extTab.Set(extBrowse) })))
		page := pageBody(listMaxWidth).Child(pageHeader(ctx, "Extensions", sub, check))

		if tab == extBrowse {
			// A search sorts by relevance unless told otherwise; the list without one, by downloads.
			sig := a.extOrder
			opts := []ui.Option[catalog.Order]{
				{Value: catalog.OrderPopular, Label: "Most downloaded"},
				{Value: catalog.OrderName, Label: "Name"},
			}
			if q != "" {
				sig = a.extSearchOrder
				opts = append([]ui.Option[catalog.Order]{{Value: catalog.OrderRelevance, Label: "Most relevant"}}, opts...)
			}
			controls = controls.Child(ui.Select(sig, opts...).Label("Sort by").Small().Width(ui.Px(170)).
				Key("ext-order:" + boolKey(q != "")))
			return fixed(page.Child(controls).Child(a.extBrowseList(ctx, info, sh, q, sig.Get())))
		}
		return scroll(page.Child(controls).Children(a.extInstalledSections(ctx, sh, q)...))
	}).Key("extensions")
}

// extInstalledSections are the extensions on this system: the user's, then the system's.
func (a *App) extInstalledSections(ctx *ui.Context, sh ui.QueryResult[shellState], q string) []ui.Element {
	switch {
	case sh.Loading:
		return []ui.Element{ui.Skeleton().Height(ui.Px(64)), ui.Skeleton().Height(ui.Px(200))}
	case sh.Err != nil:
		return []ui.Element{ui.Empty("Could not ask GNOME Shell for its extensions").Icon(icons.TriangleAlert).
			Description(errText(sh.Err)).
			Action(ui.Button("Try again").Secondary().OnClick(func() { refreshExtensions(ctx) }))}
	}
	var out []ui.Element
	if !sh.Data.allowed {
		out = append(out, a.extensionsOffAlert(ctx))
	}
	var user, system []ui.Element
	for _, e := range sh.Data.list {
		if !extMatches(e, q) {
			continue
		}
		if e.System {
			system = append(system, a.extRow(ctx, e))
		} else {
			user = append(user, a.extRow(ctx, e))
		}
	}
	switch {
	case len(sh.Data.list) == 0:
		return append(out, ui.Empty("No extensions yet").Icon(icons.Puzzle).
			Description("Extensions you add show up here, whichever app installs them.").
			Action(ui.Button("Browse extensions").Secondary().OnClick(func() { a.extTab.Set(extBrowse) })))
	case len(user)+len(system) == 0:
		return append(out, ui.Empty("No installed extension matches").Icon(icons.ListFilter).
			Description("Nothing on this system matches “"+q+"”.").
			Action(ui.Button("Search all extensions").Secondary().OnClick(func() { a.extTab.Set(extBrowse) })))
	}
	if len(user) > 0 {
		out = append(out, section(sectionHeader(ctx, "Added by you",
			"Installed for your account, by uas or any other app."), settingsList(ctx, user...)))
	}
	if len(system) > 0 {
		out = append(out, section(sectionHeader(ctx, a.s.SystemAppLabel(),
			"Part of the system: you can switch them on and off, but not remove them."),
			settingsList(ctx, system...)))
	}
	return out
}

// extensionsOffAlert says that GNOME's main switch is off, with a way to turn it on.
func (a *App) extensionsOffAlert(ctx *ui.Context) ui.Element {
	return ui.Alert("Extensions are turned off").Warning().
		Description("GNOME's main switch for extensions is off, so none of them run, whatever their own switch says.").
		Action(ui.Button("Turn on").Small().Secondary().OnClick(func() {
			go func() {
				err := a.s.Extensions.SetUserExtensionsEnabled(context.Background(), true)
				ctx.Post(func() {
					if err != nil {
						ctx.Toast(ui.ToastError, errText(err)).Title("Could not turn extensions on")
					}
					refreshExtensions(ctx)
				})
			}()
		}))
}

// extMatches reports whether every word of q is in the extension's name, UUID or description.
func extMatches(e shellext.Extension, q string) bool {
	hay := strings.ToLower(e.Name + " " + e.UUID + " " + e.Description)
	for _, w := range strings.Fields(strings.ToLower(q)) {
		if !strings.Contains(hay, w) {
			return false
		}
	}
	return true
}

// catalogExtension finds an extension in the catalog by its UUID, or nil when extensions.gnome.org doesn't
// have it (or this catalog has no extensions).
func (a *App) catalogExtension(ctx *ui.Context, uuid string) *catalog.App {
	res := ui.Query(ctx, a.key("ext", uuid), func(c context.Context) (*catalog.App, error) {
		db, err := a.catalog(c)
		if err != nil {
			return nil, err
		}
		app, err := db.App(c, catalog.ExtensionAppID(uuid))
		if err != nil {
			return nil, nil // not in the catalog; the row shows what the Shell reports
		}
		return app, nil
	}, ui.QueryStaleTime(time.Hour))
	return res.Data
}

// extIcon is an extension's icon, or a puzzle piece when it has none, as on extensions.gnome.org.
func (a *App) extIcon(ctx *ui.Context, url string, size float32, name string) ui.Element {
	if url != "" {
		return a.appIcon(ctx, url, size, name)
	}
	t := ctx.Theme()
	return ui.Div().Size(ui.Px(size), ui.Px(size)).Radius(size*0.24).Background(t.SurfaceHover).
		Border(1, t.Border).Center().Shrink(0).
		Child(ui.Icon(icons.Puzzle).Size(size * 0.5).Color(t.TextMuted))
}

// firstLine is the first line of a description, for a row.
func firstLine(s string) string {
	line, _, _ := strings.Cut(strings.TrimSpace(s), "\n")
	return strings.Join(strings.Fields(line), " ")
}

// extNotes are the short facts under an installed extension's name: its version, and why it isn't running
// or what waits for the next login.
func extNotes(e shellext.Extension) []string {
	var notes []string
	if e.Version != "" {
		notes = append(notes, "Version "+e.Version)
	}
	if e.HasUpdate {
		notes = append(notes, "update installs at next login")
	}
	return notes
}

// extRow is one installed extension: what it is, its settings and removal, and its switch.
func (a *App) extRow(ctx *ui.Context, e shellext.Extension) ui.Element {
	return ui.Component(func(ctx *ui.Context) ui.Element {
		t := ctx.Theme()
		app := a.catalogExtension(ctx, e.UUID)
		icon, summary := "", firstLine(e.Description)
		if app != nil {
			icon = app.IconURL
			if summary == "" {
				summary = app.Summary
			}
		}
		id := catalog.ExtensionAppID(e.UUID)
		_, busy := a.running(id)

		parts := []ui.Element{ui.Text(strings.Join(extNotes(e), " · ")).FontSize(12).TextColor(t.TextSubtle).Truncate()}
		if len(extNotes(e)) == 0 {
			parts = []ui.Element{ui.Text(e.UUID).FontSize(12).TextColor(t.TextSubtle).Truncate()}
		}
		text := ui.Div().Column().Gap(3).Grow(1).Basis(ui.Px(0)).MinWidth(ui.Px(0)).
			Child(ui.Div().Row().Align(ui.Center).Gap(space2).MinWidth(ui.Px(0)).
				Child(ui.Text(e.Name).FontSize(14).Medium().Truncate().Shrink(0)).
				Child(ui.If(summary != "", ui.Text(summary).FontSize(13).TextColor(t.TextMuted).Truncate()))).
			Child(metaLine(ctx, parts...))
		if app != nil {
			// The row opens the extension's page when extensions.gnome.org has it.
			text = text.Cursor(ui.CursorPointer).OnClick(func() { a.openExtension(app.ID) }).
				Role(access.RoleLink).Label(e.Name + " details").TabIndex(0)
		}

		row := ui.Div().Row().Align(ui.Center).Gap(space3).MinHeight(ui.Px(60)).PaddingX(space4).PaddingY(space2 + 2).
			Child(a.extIcon(ctx, icon, 36, e.Name)).
			Child(text)
		if p := e.Problem(); p != "" && e.Enabled {
			row = row.Child(ui.Div().Tooltip(p).Shrink(0).Child(statusChip(ctx, "Not running", t.Danger)))
		}
		actions := ui.Div().Row().Align(ui.Center).Gap(space1).Shrink(0)
		if e.HasPrefs {
			actions = actions.Child(ui.IconButton(icons.Settings).Ghost().Small().Label("Settings of " + e.Name).
				Tooltip("Open its settings").OnClick(func() { a.openExtensionPrefs(ctx, e) }))
		}
		if !e.System {
			actions = actions.Child(ui.IconButton(icons.Trash).Ghost().Small().Label("Remove " + e.Name).
				Tooltip("Remove").Disabled(busy).OnClick(func() { a.extRemoving.Set(&e) }))
		}
		return row.Child(actions).Child(ui.Div().Shrink(0).MarginLeft(space2).Child(a.extSwitch(ctx, e)))
	}).Key("ext:" + e.UUID)
}

// extSwitch turns an extension on and off. It shows what was asked for until GNOME Shell reports it.
func (a *App) extSwitch(ctx *ui.Context, e shellext.Extension) ui.Element {
	return ui.Component(func(ctx *ui.Context) ui.Element {
		on := ui.Local(ctx, "on", e.Enabled)
		// want is the state the user asked for, until the Shell reports it (or a few seconds pass).
		want := ui.Local[*bool](ctx, "want", nil)
		if w := want.Peek(); w != nil && *w == e.Enabled {
			want.Set(nil)
		}
		if want.Peek() == nil && on.Peek() != e.Enabled {
			on.Set(e.Enabled) // follow the Shell, as after a change made elsewhere
		}
		sw := ui.Switch("Turn on "+e.Name, on).Disabled(want.Get() != nil || !e.CanChange).
			OnChange(func(v bool) {
				want.Set(&v)
				go func() {
					err := a.s.Extensions.SetEnabled(context.Background(), e.UUID, v)
					ctx.Post(func() {
						if err != nil {
							want.Set(nil)
							ctx.Toast(ui.ToastError, errText(err)).Title("Could not switch " + e.Name)
						} else {
							ctx.After(3*time.Second, func() { want.Set(nil) })
						}
						refreshExtensions(ctx)
					})
				}()
			})
		if !e.CanChange {
			sw = sw.Tooltip("This system's settings decide which extensions are on")
		}
		return sw
	}).Key("ext-switch:" + e.UUID)
}

// openExtension shows an extension's page.
func (a *App) openExtension(id string) { a.open(view{page: pageExtension, arg: id}) }

// openExtensionPrefs opens an extension's own settings window.
func (a *App) openExtensionPrefs(ctx *ui.Context, e shellext.Extension) {
	go func() {
		if err := a.s.Extensions.OpenPrefs(context.Background(), e.UUID); err != nil {
			ctx.Post(func() { ctx.Toast(ui.ToastError, errText(err)).Title("Could not open the settings of " + e.Name) })
		}
	}()
}

// installExtension asks GNOME Shell to install an extension. The Shell asks the user in its own dialog, so
// uas doesn't ask as well.
func (a *App) installExtension(ctx *ui.Context, id, name, uuid string) {
	a.startJob(ctx, id, "Installing "+name, false, name+" is installed and turned on",
		func(c context.Context, p func(backend.Progress)) error {
			p(backend.Progress{Message: "GNOME is asking whether to install it…"})
			return a.s.Extensions.Install(c, uuid)
		})
}

// extRemoveDialog asks before removing an extension.
func (a *App) extRemoveDialog(ctx *ui.Context) ui.Element {
	e := a.extRemoving.Get()
	if e == nil {
		return ui.Div().Display(false)
	}
	return ui.AlertDialog("Remove "+e.Name+"?", "GNOME removes the extension from your account. Its settings are kept.").
		Intent(ui.AlertDialogDestructive).
		Confirm("Remove", func() {
			a.extRemoving.Set(nil)
			a.startJob(ctx, catalog.ExtensionAppID(e.UUID), "Removing "+e.Name, false, e.Name+" is removed",
				func(c context.Context, _ func(backend.Progress)) error { return a.s.Extensions.Remove(c, e.UUID) })
		}).
		Cancel("", func() { a.extRemoving.Set(nil) })
}

// extBrowseList lists the catalog's extensions for this version of GNOME: the most downloaded, or the ones
// that match the top bar's search.
func (a *App) extBrowseList(ctx *ui.Context, info catalog.BuildInfo, sh ui.QueryResult[shellState], q string,
	order catalog.Order) ui.Element {
	skeleton := func() ui.Element {
		col := ui.Div().Column().Gap(space2).MinHeight(ui.Px(0)).ClipContent()
		for range 6 {
			col = col.Child(ui.Skeleton().Height(ui.Px(60)))
		}
		return col
	}
	switch {
	case info.Extensions == nil:
		return ui.Empty("No extensions in the catalog yet").Icon(icons.Puzzle).
			Description("The app catalog on this computer was built before it listed extensions.").
			Action(ui.Button("Check for a newer catalog").Secondary().OnClick(func() { a.checkCatalog(ctx, true) }))
	case sh.Loading:
		// Which extensions to list depends on the version of GNOME, which the Shell says.
		return skeleton()
	case sh.Err != nil:
		return ui.Empty("Could not ask GNOME Shell which version it is").Icon(icons.TriangleAlert).
			Description(errText(sh.Err)).
			Action(ui.Button("Try again").Secondary().OnClick(func() { refreshExtensions(ctx) }))
	}
	st := sh.Data
	res := ui.Query(ctx, a.key("ext-browse", q, st.major, string(order)),
		func(c context.Context) ([]catalog.SearchResult, error) {
			db, err := a.catalog(c)
			if err != nil {
				return nil, err
			}
			if q != "" {
				return db.Search(c, q, catalog.SearchOptions{Order: order, Limit: 100, Extensions: true, Shell: st.major})
			}
			return db.Browse(c, catalog.BrowseQuery{Order: order, Limit: 150, Extensions: true, Shell: st.major})
		}, ui.QueryKeepPrevious(), ui.QueryStaleTime(time.Hour))
	switch {
	case res.Loading:
		return skeleton()
	case res.Err != nil:
		return ui.Empty("Something went wrong").Icon(icons.TriangleAlert).Description(errText(res.Err))
	case len(res.Data) == 0:
		desc := "extensions.gnome.org lists none for this version of GNOME."
		if q != "" {
			desc = "Nothing"
			if st.major != "" {
				desc += " for GNOME " + st.major
			}
			desc += " matches “" + q + "”. Try other words."
		}
		return ui.Empty("No extensions found").Icon(icons.Search).Description(desc)
	}
	list := scrollList(listBox(ctx))
	for i, r := range res.Data {
		list = list.Child(a.extResultRow(ctx, r, st, i == 0))
	}
	what := "The most downloaded extensions"
	if q != "" {
		what = plural(len(res.Data), "extension", "extensions") + " for “" + q + "”"
	}
	if st.major != "" {
		what += " that work with GNOME " + st.major
	}
	return ui.Div().Column().Gap(space3).MinHeight(ui.Px(0)).Child(muted(ctx, what+".")).Child(list)
}

// extUUID is the UUID of a catalog extension.
func extUUID(r catalog.SearchResult) string {
	for _, ref := range r.Refs {
		if ref.Source == catalog.SourceGNOMEExtensions {
			return ref.Ref
		}
	}
	return ""
}

// extResultRow is a catalog extension in a list: icon, name, author and summary, its downloads, and Install.
func (a *App) extResultRow(ctx *ui.Context, r catalog.SearchResult, st shellState, first bool) ui.Element {
	return ui.Component(func(ctx *ui.Context) ui.Element {
		t := ctx.Theme()
		uuid := extUUID(r)
		_, busy := a.running(r.ID)
		open := func() { a.openExtension(r.ID) }
		name := ui.Div().Row().Align(ui.Center).Gap(space3).MinWidth(ui.Px(0)).
			Child(ui.Text(r.Name).FontSize(14).Medium().Truncate().Shrink(0))
		if r.Developer != "" {
			name = name.Child(ui.Div().MinWidth(ui.Px(0)).Tooltip("Author: " + r.Developer).
				Child(ui.Text(r.Developer).FontSize(12).TextColor(t.TextSubtle).Truncate()))
		}
		var action ui.Element
		if _, ok := st.find(uuid); ok {
			action = ui.IconButton(icons.Check).Ghost().Small().Label("Installed").
				Tooltip("Installed: open its page to switch or remove it").OnClick(open)
		} else {
			action = ui.IconButton(icons.Download).Secondary().Small().Label("Install").Tooltip("Install " + r.Name).
				Loading(busy).Disabled(uuid == "").OnClick(func() { a.installExtension(ctx, r.ID, r.Name, uuid) })
		}
		return listRow(ctx, first).
			Child(ui.Div().Cursor(ui.CursorPointer).OnClick(open).Child(a.extIcon(ctx, r.IconURL, 36, r.Name))).
			Child(ui.Div().Column().Gap(3).Grow(1).Basis(ui.Px(0)).MinWidth(ui.Px(0)).
				Cursor(ui.CursorPointer).OnClick(open).
				Role(access.RoleLink).Label(r.Name + " details").TabIndex(0).
				Child(name).
				Child(ui.Text(r.Summary).FontSize(13).TextColor(t.TextMuted).Truncate())).
			Child(ui.Div().Width(ui.Px(130)).Shrink(0).Row().Justify(ui.End).
				Tooltip("Downloads from extensions.gnome.org, over all time").
				Child(ui.Text(compact(r.Popularity) + " downloads").FontSize(12).TextColor(t.TextMuted).Medium())).
			Child(ui.Div().Row().Justify(ui.End).Width(ui.Px(40)).Shrink(0).Child(action))
	}).Key("ext-row:" + r.ID)
}

// shellRange names the GNOME releases an extension supports: "GNOME 45 to 51".
func shellRange(shells []string) string {
	switch len(shells) {
	case 0:
		return "Unknown"
	case 1:
		return "GNOME " + shells[0]
	}
	return "GNOME " + shells[0] + " to " + shells[len(shells)-1]
}

// extensionPage shows one extension from the catalog.
func (a *App) extensionPage(ctx *ui.Context, id string) ui.Element {
	return ui.Component(func(ctx *ui.Context) ui.Element {
		t := ctx.Theme()
		res := ui.Query(ctx, a.key("app", id), func(c context.Context) (*catalog.App, error) {
			db, err := a.catalog(c)
			if err != nil {
				return nil, err
			}
			return db.Resolve(c, id)
		})
		sh := a.shell(ctx)
		keepFresh(ctx)
		page := pageBody(detailMaxWidth).Gap(space6)
		switch {
		case res.Loading:
			return scroll(page.Child(ui.Skeleton().Height(ui.Px(150))).Child(ui.Skeleton().Height(ui.Px(90))).
				Child(ui.Skeleton().Height(ui.Px(320))))
		case res.Err != nil:
			return scroll(page.Child(ui.Empty("Could not open this extension").Icon(icons.TriangleAlert).
				Description(errText(res.Err))))
		case res.Data.Kind != catalog.KindShellExtension || len(res.Data.Listings) == 0:
			return scroll(page.Child(ui.Empty("Not an extension").Icon(icons.Puzzle)))
		}
		app := res.Data
		l := app.Listings[0]

		tags := ui.Div().Row().Wrap().Gap(space2).Child(chip(ctx, "GNOME extension"))
		if app.Developer != "" {
			tags = tags.Child(chip(ctx, app.Developer))
		}
		header := ui.Div().Row().Gap(space5).Align(ui.Center).
			Child(ui.Div().Size(ui.Px(88), ui.Px(88)).Radius(20).Background(t.Surface).Border(1, t.Border).Center().
				Shrink(0).Child(a.extIcon(ctx, app.IconURL, 60, app.Name))).
			Child(ui.Div().Column().Gap(space1).Grow(1).Basis(ui.Px(0)).MinWidth(ui.Px(0)).
				Child(ui.Text(app.Name).FontSize(24).Semibold()).
				Child(ui.Text(app.Summary).FontSize(14).TextColor(t.TextMuted)).
				Child(ui.Div().MarginTop(space2).Child(tags))).
			Child(a.extensionAction(ctx, app, l, sh))

		stats := []stat{
			{icons.Download, compact(app.Popularity), "Downloads", t.TextMuted,
				"Downloads from extensions.gnome.org, over all time"},
			{icons.Puzzle, shellRange(l.Shells), "Works with", t.TextMuted,
				"The GNOME releases it has a version for"},
			{icons.ShieldCheck, "By GNOME", "Reviewed", t.Success,
				"GNOME's reviewers read every version before extensions.gnome.org lists it. Extensions still " +
					"run inside GNOME Shell with all of your account's access; nothing sandboxes them"},
		}
		if in, ok := sh.Data.find(l.Ref); ok && in.Version != "" {
			stats = append(stats, stat{icons.Tag, in.Version, "Installed version", t.TextMuted, ""})
		}
		page = page.Child(header).Child(statRow(ctx, stats))

		main := ui.Div().Column().Gap(space6).MinWidth(ui.Px(0))
		wide := false
		if w, _ := ctx.WindowSize(); w-sidebarWidth > 1000 {
			wide = true
		}
		if len(app.Screenshots) > 0 {
			w, _ := ctx.WindowSize()
			mainW := min(w-sidebarWidth-space2-2, detailMaxWidth) - 2*pagePad
			if wide {
				mainW -= 290 + space6
			}
			main = main.Child(ui.Div().Row().Child(a.fittedShot(app.Screenshots[0].URL, min(mainW, 640), 420)))
		}
		if app.Description != "" {
			main = main.Child(ui.Div().Column().Gap(space2).Child(heading("About")).Child(plainText(app.Description)))
		}

		body := ui.Div().Column().Gap(space6)
		side := a.extensionSide(ctx, app, l)
		if wide {
			body = ui.Div().Row().Gap(space6).Align(ui.Start)
			side = ui.Div().Width(ui.Px(290)).Shrink(0).Child(side)
		}
		return scroll(page.Child(body.Child(main.Grow(1).Basis(ui.Px(0))).Child(side)))
	}).Key("extension:" + id)
}

// fittedShot shows an extension's screenshot within maxW by maxH, at its own size when that is smaller.
// Extension screenshots are often small crops of a panel or a menu, which a full-width frame would blow up.
func (a *App) fittedShot(url string, maxW, maxH float32) ui.Element {
	return ui.Component(func(ctx *ui.Context) ui.Element {
		t := ctx.Theme()
		res := ui.Query(ctx, "image:"+url, func(c context.Context) (picture, error) {
			b, err := a.icons.load(c, url)
			if err != nil {
				return picture{}, err
			}
			return decodePicture(b)
		}, ui.QueryStaleTime(24*time.Hour), ui.QueryCacheTime(10*time.Minute))
		if res.Data.img == nil {
			if res.Err != nil || (!res.Loading && !res.Fetching) {
				return ui.Div()
			}
			return ui.Skeleton().Width(ui.Px(maxW)).Height(ui.Px(min(maxH, maxW*9/16)))
		}
		w, h := res.Data.img.Size()
		if w <= 0 || h <= 0 {
			return ui.Div()
		}
		scale := min((maxW-2)/float32(w), (maxH-2)/float32(h), 1)
		return ui.Div().Radius(radiusM).Border(1, t.Border).ClipContent().
			Child(ui.Image(res.Data.img).Size(ui.Px(float32(w)*scale), ui.Px(float32(h)*scale)).Fit(ui.FitContain).
				Alt("Screenshot"))
	}).Key("ext-shot:" + url)
}

// plainText lays out text an author wrote with line breaks and no markup: a paragraph per line, a gap per
// blank line.
func plainText(s string) ui.Element {
	col := ui.Div().Column().Gap(space1)
	blank, lines := false, 0
	for _, line := range strings.Split(s, "\n") {
		line = strings.TrimRight(line, " \t\r")
		if strings.TrimSpace(line) == "" {
			blank = true
			continue
		}
		p := ui.Text(line).FontSize(14).LineHeight(1.5)
		if blank && lines > 0 {
			p = p.MarginTop(space2)
		}
		blank = false
		lines++
		col = col.Child(p)
	}
	return col
}

// extensionAction is the install area of an extension's page: Install, or the switch with its settings and
// removal, or why it can't be installed here.
func (a *App) extensionAction(ctx *ui.Context, app *catalog.App, l catalog.Listing,
	sh ui.QueryResult[shellState]) ui.Element {
	t := ctx.Theme()
	big := ui.ButtonStyle{Root: ui.Style{Height: ui.Px(34), FontSize: 14, Radius: radiusS}}
	job, busy := a.running(app.ID)
	col := ui.Div().Column().Gap(space2).Width(ui.Px(230)).Shrink(0)
	in, installed := sh.Data.find(l.Ref)
	switch {
	case sh.Loading:
		col = col.Child(ui.Button("Install").Primary().Large().Width(ui.Full).Styles(big).Loading(true))
	case sh.Err != nil:
		col = col.Child(ui.Alert("GNOME Shell didn't answer").Warning().Description(errText(sh.Err)))
	case installed:
		state := "Turned off"
		if in.Enabled {
			state = "Turned on"
		}
		col = col.Child(ui.Div().Row().Align(ui.Center).Gap(space3).Height(ui.Px(36)).PaddingX(space3).
			Radius(radiusS).Background(t.AccentSoft).
			Child(ui.Div().Grow(1).Child(ui.Text(state).FontSize(14).Medium())).
			Child(a.extSwitch(ctx, in)))
		where := "Added by you"
		if in.System {
			where = a.s.SystemAppLabel()
		}
		line := ui.Div().Row().Align(ui.Center).Gap(space1).
			Child(ui.Div().Grow(1).Basis(ui.Px(0)).MinWidth(ui.Px(0)).Child(caption(ctx, where).Truncate()))
		if in.HasPrefs {
			line = line.Child(ui.Button("Settings").Ghost().Small().OnClick(func() { a.openExtensionPrefs(ctx, in) }))
		}
		if !in.System {
			line = line.Child(ui.Button("Remove").Ghost().Small().Disabled(busy).
				OnClick(func() { a.extRemoving.Set(&in) }))
		}
		col = col.Child(line)
		if p := in.Problem(); p != "" && in.Enabled {
			col = col.Child(ui.Alert("Not running").Danger().Description(p))
		}
		if in.HasUpdate {
			col = col.Child(caption(ctx, "GNOME has a newer version ready. It installs the next time you log in."))
		}
		if !sh.Data.allowed {
			col = col.Child(caption(ctx, "GNOME's main switch for extensions is off, so it isn't running."))
		}
	case sh.Data.major != "" && !slices.Contains(l.Shells, sh.Data.major):
		col = col.Child(ui.Alert("Not for GNOME " + sh.Data.major).Warning().
			Description("It has versions for " + shellRange(l.Shells) + ", and none for the one this system runs."))
	default:
		col = col.Child(ui.Button("Install").Icon(icons.Download).Primary().Large().Width(ui.Full).Styles(big).
			Loading(busy).OnClick(func() { a.installExtension(ctx, app.ID, app.Name, l.Ref) })).
			Child(caption(ctx, "GNOME asks you to confirm, then downloads it from extensions.gnome.org."))
	}
	if busy {
		col = col.Child(ui.Div().Row().Gap(space2).Align(ui.Center).
			Child(ui.ProgressRing().Size(ui.RingSmall).Label(job.title)).
			Child(ui.Div().Grow(1).Basis(ui.Px(0)).MinWidth(ui.Px(0)).Child(caption(ctx, job.status).Truncate())))
	}
	return col
}

// extensionSide lists where an extension comes from and its links.
func (a *App) extensionSide(ctx *ui.Context, app *catalog.App, l catalog.Listing) ui.Element {
	t := ctx.Theme()
	props := ui.Div().Column().Gap(space1)
	field := func(label string, value ui.Element) {
		props = props.Child(ui.Div().Row().Align(ui.Center).Gap(space3).MinHeight(ui.Px(30)).
			Child(ui.Div().Width(ui.Px(80)).Shrink(0).Child(muted(ctx, label))).
			Child(ui.Div().Row().Grow(1).Basis(ui.Px(0)).MinWidth(ui.Px(0)).Child(value)))
	}
	value := func(s string) *ui.TextElement { return ui.Text(s).FontSize(13).Medium().Truncate() }
	field("Source", ui.Div().Row().Align(ui.Center).Gap(6).
		Child(ui.Icon(icons.Puzzle).Size(14).Color(t.TextMuted)).
		Child(value("extensions.gnome.org")))
	if app.Developer != "" {
		field("Author", value(app.Developer))
	}
	field("UUID", ui.Div().MinWidth(ui.Px(0)).Tooltip(l.Ref).Child(value(l.Ref)))

	notes := ui.Div().Column().Gap(space1).
		Child(infoRow(ctx, icons.Info, t.TextSubtle, "GNOME Shell installs it for your account and keeps it updated.")).
		Child(infoRow(ctx, icons.Info, t.TextSubtle, "Extensions run inside GNOME Shell, not in a sandbox."))
	link := func(icon *icons.Icon, label, url string) ui.Element {
		return actionRow(ctx, icon, label, icons.ArrowUpRight).Role(access.RoleLink).Tooltip(url).
			OnClick(func() { openURL(url) })
	}
	links := ui.Div().Column().Gap(space1)
	if strings.HasPrefix(app.Homepage, "http") {
		links = links.Child(link(icons.Globe, "Project page", app.Homepage))
	}
	if strings.HasPrefix(l.StoreURL, "https://") {
		links = links.Child(link(icons.Puzzle, "On extensions.gnome.org", l.StoreURL))
	}
	rule := func() ui.Element { return ui.Div().Height(ui.Px(1)).Background(t.Border) }
	return ui.Div().Column().Gap(space3).Child(props).Child(rule()).Child(notes).Child(rule()).Child(links)
}
