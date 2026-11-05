package gui

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"github.com/tyler-builds/jamb/dialog"
	"github.com/tyler-builds/jamb/icons"
	"github.com/tyler-builds/jamb/ui"

	"github.com/tyler-builds/uas/internal/catalog"
	"github.com/tyler-builds/uas/internal/client/backend"
)

// installedPage lists everything installed, from every source.
func (a *App) installedPage(ctx *ui.Context) ui.Element {
	res := ui.Query(ctx, "installed", a.s.Installed, ui.QueryStaleTime(30*time.Second))
	ups := a.updates(ctx)
	page := pageBody(listMaxWidth).
		Child(a.listHeader(ctx, icons.Download, ui.Color{}, "Installed",
			"Everything installed on this system, from every source.",
			ui.Div().Row().Gap(space2).
				Child(a.addAppImageButton(ctx)).
				Child(a.refreshButton(ctx, res, ups))))
	switch {
	case res.Loading:
		return scroll(page.Child(ui.Skeleton().Height(ui.Px(80))).Child(ui.Skeleton().Height(ui.Px(80))))
	case res.Err != nil:
		return scroll(page.Child(ui.Empty("Could not list installed apps").Icon(icons.TriangleAlert).
			Description(errText(res.Err))))
	case len(res.Data) == 0:
		return scroll(page.Child(ui.Empty("No apps installed yet").Icon(icons.Package).
			Description("Apps you install from any source show up here.")))
	}
	return scroll(page.Child(a.filteredList(ctx, res.Data, ups.Data, "installed")))
}

// updatesPage lists the installed apps that have updates, with Update all.
func (a *App) updatesPage(ctx *ui.Context) ui.Element {
	res := ui.Query(ctx, "installed", a.s.Installed, ui.QueryStaleTime(30*time.Second))
	ups := a.updates(ctx)
	var with []backend.InstalledApp
	for _, in := range res.Data {
		if _, ok := ups.Data[in.Key()]; ok {
			with = append(with, in)
		}
	}
	actions := ui.Div().Row().Gap(space2).
		Child(ui.If(len(with) > 0, ui.Button(updatesLabel(len(with))).Icon(icons.Download).Primary().
			OnClick(func() { a.updateAll(ctx, with) }))).
		Child(a.refreshButton(ctx, res, ups))
	page := pageBody(listMaxWidth).
		Child(a.listHeader(ctx, icons.RefreshCw, ui.Color{}, "Updates",
			"Newer versions from Flathub, Homebrew and GitHub. Layered packages update with the system.",
			actions))
	switch {
	case res.Loading || ups.Loading:
		return scroll(page.Child(ui.Skeleton().Height(ui.Px(80))))
	case len(with) == 0:
		return scroll(page.Child(ui.Empty("Everything is up to date").Icon(icons.CircleCheck).
			Description("uas checks for updates every ten minutes while it runs.")))
	}
	return scroll(page.Child(a.filteredList(ctx, with, ups.Data, "with updates")))
}

// filteredList shows apps narrowed by the top bar's filter and the source chips, with a count. The chips
// appear when the apps come from more than one source.
func (a *App) filteredList(ctx *ui.Context, apps []backend.InstalledApp, ups map[string]string,
	what string) ui.Element {
	q := strings.ToLower(strings.TrimSpace(a.listFilter.Get()))
	src := a.listSource.Get()
	counts := map[catalog.SourceID]int{}
	for _, in := range apps {
		counts[in.Source]++
	}
	if counts[src] == 0 {
		src = "" // the chosen source has nothing here (any more)
	}
	var shown []backend.InstalledApp
	for _, in := range apps {
		if (src == "" || in.Source == src) && matchesFilter(in, q) {
			shown = append(shown, in)
		}
	}

	count := plural(len(apps), "app", "apps") + " " + what
	if len(shown) != len(apps) {
		count = fmt.Sprintf("%d of %s", len(shown), count)
	}
	bar := ui.Div().Row().Align(ui.Center).Gap(space4)
	if len(counts) > 1 {
		chips := ui.Div().Row().Gap(space2).Wrap().Grow(1).Basis(ui.Px(0)).MinWidth(ui.Px(0)).
			Child(filterChip(ctx, "All", src == "", func() { a.listSource.Set("") }))
		for _, s := range sources {
			if n := counts[s]; n > 0 {
				chips = chips.Child(filterChip(ctx, fmt.Sprintf("%s %d", s.Name(), n), src == s,
					func() { a.listSource.Set(s) }))
			}
		}
		bar = bar.Child(chips).Child(muted(ctx, count))
	} else {
		bar = bar.Child(muted(ctx, count))
	}

	if len(shown) == 0 {
		return ui.Div().Column().Gap(space5).Child(bar).
			Child(ui.Empty("No apps match").Icon(icons.ListFilter).
				Description("Nothing " + what + " matches the filter.").
				Action(ui.Button("Clear filter").Secondary().OnClick(func() {
					a.listFilter.Set("")
					a.listSource.Set("")
				})))
	}
	list := ui.Div().Column().Gap(space3)
	for _, in := range shown {
		list = list.Child(a.installedRow(ctx, in, ups))
	}
	return ui.Div().Column().Gap(space5).Child(bar).Child(list)
}

// sources are the sources in the order the chips show them: the catalog's, then added AppImages.
var sources = append(append([]catalog.SourceID{}, catalog.SourcePrecedence...), catalog.SourceAppImage)

// matchesFilter reports whether every word of q (lowercase) is in the app's name, ID, package or source.
func matchesFilter(in backend.InstalledApp, q string) bool {
	hay := strings.ToLower(strings.Join([]string{in.Name, in.AppID, in.Package, in.Source.Name(), in.Origin}, " "))
	for _, w := range strings.Fields(q) {
		if !strings.Contains(hay, w) {
			return false
		}
	}
	return true
}

// addAppImageButton asks for a downloaded AppImage and adds it, so uas keeps it up to date.
func (a *App) addAppImageButton(ctx *ui.Context) ui.Element {
	_, busy := a.running("add-appimage")
	return ui.Button("Add AppImage").Icon(icons.FilePlus).Secondary().Loading(busy).
		Tooltip("Add an AppImage you downloaded. uas moves it into its own folder, adds it to your app menu " +
			"and keeps it up to date when the AppImage says where its updates come from.").
		OnClick(func() {
			ctx.Window().OpenFiles(dialog.FileOptions{Title: "Add an AppImage",
				Filters: []dialog.Filter{{Name: "AppImages", Patterns: []string{"*.AppImage", "*.appimage"}},
					{Name: "All files", Patterns: []string{"*"}}}},
				func(r dialog.FileResult) {
					if r.Err != nil {
						ctx.Toast(ui.ToastError, errText(r.Err)).Title("Could not open the file picker")
						return
					}
					if r.Canceled || len(r.Paths) == 0 {
						return
					}
					a.addAppImage(ctx, r.Paths[0])
				})
		})
}

// addAppImage adds an AppImage file in the background.
func (a *App) addAppImage(ctx *ui.Context, path string) {
	a.startJob(ctx, "add-appimage", "Adding "+filepath.Base(path), false, "AppImage added",
		func(c context.Context, p func(backend.Progress)) error {
			_, err := a.s.AddAppImage(c, path, p)
			return err
		})
}

// refreshButton checks again for installed apps and updates.
func (a *App) refreshButton(ctx *ui.Context, res ui.QueryResult[[]backend.InstalledApp],
	ups ui.QueryResult[map[string]string]) ui.Element {
	return ui.Button("Refresh").Icon(icons.RefreshCw).Secondary().Loading(res.Fetching || ups.Fetching).
		OnClick(func() {
			res.Refetch()
			ups.Refetch()
		})
}

func updatesLabel(n int) string {
	if n == 1 {
		return "Update 1 app"
	}
	return fmt.Sprintf("Update %d apps", n)
}

// updateAll updates the apps one after another in the background.
func (a *App) updateAll(ctx *ui.Context, apps []backend.InstalledApp) {
	a.startJob(ctx, "update-all", updatesLabel(len(apps)), false, "Updates installed",
		func(c context.Context, p func(backend.Progress)) error {
			var errs []error
			for _, in := range apps {
				p(backend.Progress{Message: "Updating " + in.Name})
				if err := a.s.Update(c, in, p); err != nil {
					errs = append(errs, fmt.Errorf("%s: %w", in.Name, err))
				}
			}
			return errors.Join(errs...)
		})
}

// installedRow is one installed app, matched to the catalog for its icon and page when possible.
func (a *App) installedRow(ctx *ui.Context, in backend.InstalledApp, ups map[string]string) ui.Element {
	return ui.Component(func(ctx *ui.Context) ui.Element {
		t := ctx.Theme()
		match := ui.Query(ctx, a.key("resolve", in.AppID), func(c context.Context) (*catalog.App, error) {
			db, err := a.catalog(c)
			if err != nil {
				return nil, err
			}
			app, err := db.Resolve(c, in.AppID)
			if err != nil {
				return nil, nil // not in the catalog; the row shows what the source reports
			}
			return app, nil
		}, ui.QueryStaleTime(time.Hour))
		name, icon, appID, summary := in.Name, in.Icon, in.AppID, ""
		if app := match.Data; app != nil {
			name, appID, summary = app.Name, app.ID, app.Summary
			if app.IconURL != "" {
				icon = app.IconURL
			}
		}
		_, busy := a.running(appID)

		var meta []string
		if in.Version != "" {
			meta = append(meta, "Version "+in.Version)
		}
		switch in.Scope {
		case "system":
			meta = append(meta, "for all users")
		case "user":
			meta = append(meta, "for you")
		}
		switch {
		case in.Source == catalog.SourceAppImage && in.Origin != "":
			meta = append(meta, "updates from "+in.Origin)
		case in.Source == catalog.SourceAppImage:
			meta = append(meta, "doesn't update itself")
		case in.Origin == "local file":
			meta = append(meta, "installed from a file")
		}

		row := ui.Div().Row().Align(ui.Center).Gap(space5).PaddingX(space6).PaddingY(space4).
			Radius(radiusL).Background(t.Surface).
			Child(a.appIcon(ctx, icon, 56, name)).
			Child(ui.Div().Column().Gap(space1).Grow(1).Basis(ui.Px(0)).MinWidth(ui.Px(0)).
				Child(ui.Text(name).FontSize(17).Semibold().Truncate()).
				Child(ui.If(summary != "", ui.Text(summary).FontSize(14).TextColor(t.TextMuted).Truncate())).
				Child(ui.Div().Row().Align(ui.Center).Gap(space4).MarginTop(space1).
					Child(sourceTag(ctx, in.Source)).
					Child(caption(ctx, strings.Join(meta, " · "))))).
			Child(ui.If(in.Pending, statusChip(ctx, "After restart", t.Warning)))

		actions := ui.Div().Row().Align(ui.Center).Gap(space2).Shrink(0)
		if version, ok := ups[in.Key()]; ok {
			label := "Update"
			if version != "" {
				label = "Update to " + version
			}
			actions = actions.Child(ui.Button(label).Icon(icons.Download).Primary().Loading(busy).OnClick(func() {
				a.startJob(ctx, appID, "Updating "+name, false, name+" is up to date",
					func(c context.Context, p func(backend.Progress)) error { return a.s.Update(c, in, p) })
			}))
		}
		if match.Data != nil {
			actions = actions.Child(linkButton(ctx, "Details", icons.ChevronRight, func() { a.openApp(appID) }))
		}
		if in.Origin != "local file" {
			actions = actions.Child(ui.Button("Remove").Secondary().Disabled(busy).OnClick(func() {
				a.removing.Set(&removal{app: in, name: name})
			}))
		}
		return row.Child(actions)
	}).Key("installed:" + string(in.Source) + "|" + in.Scope + "|" + in.Ref)
}
