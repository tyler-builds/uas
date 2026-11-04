package gui

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/tyler-builds/jamb/icons"
	"github.com/tyler-builds/jamb/ui"

	"github.com/tyler-builds/uas/internal/catalog"
	"github.com/tyler-builds/uas/internal/client/backend"
)

// installedPage lists what is installed, from every source.
func (a *App) installedPage(ctx *ui.Context) ui.Element {
	t := ctx.Theme()
	res := ui.Query(ctx, "installed", a.s.Installed, ui.QueryStaleTime(30*time.Second))
	ups := a.updates(ctx)
	var withUpdates, rest []backend.InstalledApp
	for _, in := range res.Data {
		if _, ok := ups.Data[in.Key()]; ok {
			withUpdates = append(withUpdates, in)
		} else {
			rest = append(rest, in)
		}
	}
	sub := "Everything installed from every source"
	switch {
	case ups.Loading:
		sub = "Checking for updates…"
	case len(res.Data) > 0 && len(withUpdates) == 0:
		sub = fmt.Sprintf("%d apps, all up to date", len(res.Data))
	case len(res.Data) > 0:
		sub = fmt.Sprintf("%d apps, %s", len(res.Data), updatesNoun(len(withUpdates)))
	}
	refresh := ui.Button("Refresh").Icon(icons.RefreshCw).Secondary().Small().
		Loading(res.Fetching || ups.Fetching).
		OnClick(func() {
			res.Refetch()
			ups.Refetch()
		})
	page := ui.Div().Column().Gap(space6).Padding(space8).MaxWidth(ui.Px(1180)).
		Child(ui.Div().Row().Align(ui.End).Gap(space3).
			Child(ui.Div().Column().Gap(space1).Grow(1).Basis(ui.Px(0)).MinWidth(ui.Px(0)).
				Child(title("Installed")).
				Child(muted(ctx, sub))).
			Child(refresh))

	switch {
	case res.Loading:
		return scroll(page.Child(ui.Skeleton().Height(ui.Px(64))).Child(ui.Skeleton().Height(ui.Px(64))))
	case res.Err != nil:
		return scroll(page.Child(ui.Empty("Could not list installed apps").Icon(icons.TriangleAlert).
			Description(errText(res.Err))))
	case len(res.Data) == 0:
		return scroll(page.Child(ui.Empty("No apps installed yet").Icon(icons.Package).
			Description("Apps you install from any source show up here.")))
	}

	if len(withUpdates) > 0 {
		list := ui.Div().Column().Gap(2)
		for _, in := range withUpdates {
			list = list.Child(a.installedRow(ctx, in))
		}
		page = page.Child(card(ctx).Padding(space4).Border(1, t.Accent.Alpha(0.45)).
			Child(ui.Div().Row().Align(ui.Center).Gap(space3).PaddingX(space2).
				Child(ui.Div().Size(ui.Px(36), ui.Px(36)).Radius(radiusM).Background(t.AccentSoft).Center().
					Child(ui.Icon(icons.Download).Size(18).Color(t.Accent))).
				Child(ui.Div().Column().Grow(1).Basis(ui.Px(0)).MinWidth(ui.Px(0)).
					Child(heading(capitalize(updatesNoun(len(withUpdates))))).
					Child(caption(ctx, "Newer versions from the apps' sources"))).
				Child(ui.Button(updatesLabel(len(withUpdates))).Icon(icons.Download).Primary().
					OnClick(func() { a.updateAll(ctx, withUpdates) }))).
			Child(list))
	}
	if len(rest) > 0 {
		list := ui.Div().Column().Gap(2).Padding(space2).Radius(radiusL).Background(t.Surface).Border(1, t.Border)
		for _, in := range rest {
			list = list.Child(a.installedRow(ctx, in))
		}
		page = page.Child(sectionHeader(ctx, "All apps",
			"Packages layered onto the system image update with the system, not here")).Child(list)
	}
	return scroll(page)
}

func updatesNoun(n int) string {
	if n == 1 {
		return "1 update available"
	}
	return fmt.Sprintf("%d updates available", n)
}

func capitalize(s string) string {
	if s == "" {
		return s
	}
	return strings.ToUpper(s[:1]) + s[1:]
}

func updatesLabel(n int) string {
	if n == 1 {
		return "Update 1 app"
	}
	return "Update " + strconv.Itoa(n) + " apps"
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
func (a *App) installedRow(ctx *ui.Context, in backend.InstalledApp) ui.Element {
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
		name, icon, appID := in.Name, "", in.AppID
		if app := match.Data; app != nil {
			name, icon, appID = app.Name, app.IconURL, app.ID
		}
		_, busy := a.running(appID)
		var meta []string
		if in.Origin != "" && in.Origin != string(in.Source) && in.Origin != "flathub" && in.Origin != "homebrew" {
			meta = append(meta, in.Origin)
		}
		if in.Scope == "system" {
			meta = append(meta, "for all users")
		} else if in.Scope == "user" {
			meta = append(meta, "for you")
		}
		if in.Version != "" {
			meta = append(meta, in.Version)
		}
		row := ui.Div().Row().Align(ui.Center).Gap(space4).PaddingX(space3).PaddingY(space2).Radius(radiusM).
			Hover(ui.Style{Background: t.SurfaceHover}).
			Child(a.iconTile(ctx, icon, 44, name)).
			Child(ui.Div().Column().Gap(3).Grow(1).Basis(ui.Px(0)).MinWidth(ui.Px(0)).
				Child(ui.Text(name).FontSize(15).Semibold().Truncate()).
				Child(ui.Div().Row().Gap(space2).Align(ui.Center).
					Child(sourcePill(ctx, in.Source)).
					Child(caption(ctx, strings.Join(meta, " · "))))).
			Child(ui.If(in.Pending, pill(ctx, "After restart", t.Warning,
				"Layered into the system image; it takes effect at the next restart")))
		if match.Data != nil {
			row = row.Child(ui.Button("Details").Ghost().Small().OnClick(func() { a.selected.Set(appID) }))
		}
		if version, ok := a.updates(ctx).Data[in.Key()]; ok {
			label := "Update"
			if version != "" {
				label = "Update to " + version
			}
			row = row.Child(ui.Button(label).Primary().Small().Loading(busy).OnClick(func() {
				a.startJob(ctx, appID, "Updating "+name, false, name+" is up to date",
					func(c context.Context, p func(backend.Progress)) error { return a.s.Update(c, in, p) })
			}))
		}
		if in.Origin == "local file" {
			// Installed from a downloaded file outside uas; rpm-ostree removes it by package name.
			return row.Child(ui.Text("Installed from a file").FontSize(12).TextColor(t.TextMuted))
		}
		return row.Child(ui.Button("Remove").Secondary().Small().Disabled(busy).OnClick(func() {
			a.removing.Set(&removal{app: in, name: name})
		}))
	}).Key("installed:" + string(in.Source) + "|" + in.Scope + "|" + in.Ref)
}
