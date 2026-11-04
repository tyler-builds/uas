package gui

import (
	"context"
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
	var body ui.Element
	switch {
	case res.Loading:
		body = ui.Div().Column().Gap(8).Child(ui.Skeleton().Height(ui.Px(56))).Child(ui.Skeleton().Height(ui.Px(56)))
	case res.Err != nil:
		body = ui.Empty("Could not list installed apps").Icon(icons.TriangleAlert).Description(errText(res.Err))
	case len(res.Data) == 0:
		body = ui.Empty("No apps installed yet").Icon(icons.Package).
			Description("Apps you install from any source show up here.")
	default:
		items := res.Data
		body = ui.ListOf(items, func(in backend.InstalledApp) string { return string(in.Source) + "|" + in.Scope + "|" + in.Ref },
			func(_ int, in backend.InstalledApp) ui.Element { return a.installedRow(ctx, in) }).
			Style(ui.Style{Grow: 1})
	}
	return ui.Div().Column().Grow(1).Gap(16).Padding(24).MinHeight(ui.Px(0)).
		Child(ui.Div().Row().Align(ui.Center).Gap(12).
			Child(ui.Text("Installed").FontSize(22).Semibold()).
			Child(ui.If(len(res.Data) > 0, ui.Badge(len(res.Data)).Noun("apps"))).
			Child(ui.Spacer()).
			Child(ui.Button("Refresh").Icon(icons.RefreshCw).Secondary().Small().Loading(res.Fetching).
				OnClick(res.Refetch))).
		Child(ui.Text("Packages layered onto the system image update with the system.").FontSize(13).
			TextColor(t.TextMuted)).
		Child(ui.Div().Column().Grow(1).Basis(ui.Px(0)).MinHeight(ui.Px(0)).Child(body))
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
		meta := sourceLabel(in.Source)
		if in.Origin != "" && in.Origin != string(in.Source) {
			meta += " · " + in.Origin
		}
		if in.Scope != "" {
			meta += " · " + in.Scope
		}
		row := ui.Div().Row().Align(ui.Center).Gap(14).PaddingX(12).PaddingY(8).Radius(8).
			Hover(ui.Style{Background: t.Surface}).
			Child(a.appIcon(ctx, icon, 40, name)).
			Child(ui.Div().Column().Gap(2).Grow(1).Basis(ui.Px(0)).MinWidth(ui.Px(0)).
				Child(ui.Text(name).FontSize(15).Semibold().Truncate()).
				Child(ui.Text(meta).FontSize(12).TextColor(t.TextMuted).Truncate())).
			Child(ui.If(in.Version != "", ui.Text(in.Version).FontSize(13).TextColor(t.TextMuted))).
			Child(ui.If(in.Pending, ui.BadgeText("After restart").Variant(ui.BadgeWarning)))
		if match.Data != nil {
			row = row.Child(ui.Button("Details").Ghost().Small().OnClick(func() { a.selected.Set(appID) }))
		}
		if a.s.NeedsRestart(in.Source) {
			// Layered packages update with the system image; no per-app update.
		} else {
			row = row.Child(ui.Button("Update").Secondary().Small().Loading(busy).OnClick(func() {
				a.startJob(ctx, appID, "Updating "+name, false, name+" is up to date",
					func(c context.Context, p func(backend.Progress)) error { return a.s.Update(c, in, p) })
			}))
		}
		if in.Origin == "local file" {
			// Installed from a downloaded file outside uas; rpm-ostree removes it by package name.
			return row.Child(ui.Text("Installed from a file").FontSize(12).TextColor(t.TextMuted))
		}
		return row.Child(ui.Button("Remove").Danger().Small().Disabled(busy).OnClick(func() {
			a.removing.Set(&removal{app: in, name: name})
		}))
	}).Key("installed:" + string(in.Source) + "|" + in.Scope + "|" + in.Ref)
}
