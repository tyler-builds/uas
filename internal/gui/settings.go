package gui

import (
	"context"
	"fmt"
	"time"

	"github.com/tyler-builds/jamb/icons"
	"github.com/tyler-builds/jamb/ui"

	"github.com/tyler-builds/uas/internal/catalog"
	"github.com/tyler-builds/uas/internal/client/backend"
	"github.com/tyler-builds/uas/internal/client/backend/rpmostree"
	"github.com/tyler-builds/uas/internal/client/settings"
)

// settingsPage holds the settings, the catalog's state and what uas found about the system.
func (a *App) settingsPage(ctx *ui.Context, info catalog.BuildInfo) ui.Element {
	return ui.Component(func(ctx *ui.Context) ui.Element {
		t := ctx.Theme()
		scope := ui.Local(ctx, "scope", a.s.Settings.FlatpakScope)
		section := func(icon *icons.Icon, titleText, sub string) *ui.CardElement {
			return ui.Card().Padding(space6).Gap(space4).
				Child(ui.Div().Row().Gap(space3).Align(ui.Center).
					Child(ui.Div().Size(ui.Px(36), ui.Px(36)).Radius(radiusM).Background(t.AccentSoft).Center().
						Child(ui.Icon(icon).Size(18).Color(t.Accent))).
					Child(ui.Div().Column().Grow(1).Basis(ui.Px(0)).MinWidth(ui.Px(0)).
						Child(heading(titleText)).
						Child(caption(ctx, sub))))
		}

		flatpak := section(icons.Box, "Flatpak apps", "Where apps from Flathub are installed").
			Child(ui.RadioGroup(scope,
				ui.Option[settings.FlatpakScope]{Value: settings.ScopeUser, Label: "Just for me (no password)"},
				ui.Option[settings.FlatpakScope]{Value: settings.ScopeSystem, Label: "For everyone on this computer"},
			).Label("Install Flatpak apps").OnChange(func(v settings.FlatpakScope) {
				st := a.s.Settings
				st.FlatpakScope = v
				if err := a.s.SaveSettings(st); err != nil {
					ctx.Toast(ui.ToastError, errText(err)).Title("Could not save settings")
					return
				}
				ctx.Toast(ui.ToastSuccess, "Settings saved")
			}))

		_, refreshing := a.running("catalog")
		row := func(left, right ui.Element) ui.Element {
			return ui.Div().Row().Align(ui.Center).Gap(space3).MinHeight(ui.Px(36)).PaddingX(space1).
				Child(ui.Div().Row().Align(ui.Center).Grow(1).Basis(ui.Px(0)).MinWidth(ui.Px(0)).Child(left)).
				Child(right)
		}
		cat := section(icons.Layers, "App catalog", fmt.Sprintf("%s apps, built %s", compactThousands(info.Apps),
			ui.FormatRelative(info.BuiltAt, time.Now())))
		rows := ui.Div().Column()
		for _, src := range info.Sources {
			right := ui.Element(ui.Text(compactThousands(src.Listings) + " listings").FontSize(13).Semibold())
			if src.Error != "" {
				right = ui.Div().Tooltip(src.Error).Child(statusPill(ctx, false, "Unavailable at build time"))
			}
			rows = rows.Child(row(sourcePill(ctx, src.Source), right))
		}
		cat = cat.Child(rows).
			Child(ui.Div().Row().Child(ui.Button("Check for a newer catalog").Icon(icons.RefreshCw).Small().
				Secondary().Loading(refreshing).OnClick(func() { a.checkCatalog(ctx, true) })))

		p := a.s.Platform
		system := section(icons.Cpu, "This system", fmt.Sprintf("%s on %s", p.PrettyName, p.Arch))
		srcRows := ui.Div().Column()
		for _, src := range catalog.SourcePrecedence {
			b := a.s.Backends[src]
			var note string
			switch bb := b.(type) {
			case nil:
				note = "not installed on this system"
			case *rpmostree.Backend:
				note = fmt.Sprintf("layers onto Fedora %d; needs a restart", bb.Release())
			default:
				if rs, ok := b.(backend.Restarter); ok && rs.NeedsRestart() {
					note = "needs a restart"
				} else {
					note = "no restart needed"
				}
			}
			label := "Ready"
			if b == nil {
				label = "Not available"
			}
			srcRows = srcRows.Child(row(
				ui.Div().Row().Gap(space3).Align(ui.Center).
					Child(sourcePill(ctx, src)).
					Child(caption(ctx, note)),
				statusPill(ctx, b != nil, label)))
		}
		system = system.Child(srcRows)

		privacy := section(icons.Lock, "Privacy", "What uas sends and keeps").
			Child(muted(ctx, "uas collects no usage data. If that ever changes, it will be a setting that is off until you turn it on."))

		page := ui.Div().Column().Gap(space5).Padding(space8).MaxWidth(ui.Px(820)).
			Child(pageHeader(ctx, "Settings", "", nil)).
			Child(flatpak).Child(cat).Child(system).Child(privacy)
		return scroll(page)
	}).Key("settings")
}

// checkCatalog downloads a newer catalog if there is one. loud says to report "already up to date" too.
func (a *App) checkCatalog(ctx *ui.Context, loud bool) {
	if _, busy := a.running("catalog"); busy {
		return
	}
	a.setJob("catalog", job{title: "Checking for a newer catalog"})
	go func() {
		res, err := a.refreshCatalog(context.Background())
		ctx.Post(func() {
			a.setJob("catalog", job{done: true})
			switch {
			case err != nil:
				if loud {
					ctx.Toast(ui.ToastError, errText(err)).Title("Could not check the catalog")
				}
			case res.Updated:
				a.reloadCatalog(ctx)
				ctx.Toast(ui.ToastSuccess, fmt.Sprintf("%d apps", res.Manifest.Apps)).Title("App catalog updated")
			case loud:
				ctx.Toast(ui.ToastInfo, "The app catalog is up to date")
			}
		})
	}()
}
