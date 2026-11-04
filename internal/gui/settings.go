package gui

import (
	"context"
	"fmt"

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
		section := func(title string) *ui.DivElement {
			return ui.Div().Column().Gap(10).Padding(16).Radius(10).Border(1, t.Border).
				Child(ui.Text(title).FontSize(16).Semibold())
		}
		muted := func(s string) ui.Element { return ui.Text(s).FontSize(13).TextColor(t.TextMuted) }

		flatpak := section("Flatpak apps").
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
		cat := section("App catalog").
			Child(muted(fmt.Sprintf("%d apps, built %s.", info.Apps, info.BuiltAt.Local().Format("Jan 2, 2006 15:04"))))
		for _, s := range info.Sources {
			line := fmt.Sprintf("%s: %d listings", sourceLabel(s.Source), s.Listings)
			if s.Error != "" {
				line = fmt.Sprintf("%s: unavailable at build time (%s)", sourceLabel(s.Source), s.Error)
			}
			cat = cat.Child(muted(line))
		}
		cat = cat.Child(ui.Div().Row().Child(ui.Button("Check for a newer catalog").Icon(icons.RefreshCw).
			Secondary().Loading(refreshing).OnClick(func() { a.checkCatalog(ctx, true) })))

		p := a.s.Platform
		system := section("This system").
			Child(muted(fmt.Sprintf("%s (%s)", p.PrettyName, p.Arch)))
		for _, src := range catalog.SourcePrecedence {
			b := a.s.Backends[src]
			line := sourceLabel(src) + ": not available"
			if b != nil {
				line = sourceLabel(src) + ": ready"
				if r, ok := b.(*rpmostree.Backend); ok {
					line += fmt.Sprintf(", layering onto Fedora %d", r.Release)
				}
				if rs, ok := b.(backend.Restarter); ok && rs.NeedsRestart() {
					line += " (needs a restart)"
				}
			}
			system = system.Child(muted(line))
		}

		privacy := section("Privacy").
			Child(muted("uas collects no usage data. If that ever changes, it will be a setting that is off until you turn it on."))

		page := ui.Div().Column().Gap(16).Padding(24).MaxWidth(ui.Px(760)).
			Child(ui.Text("Settings").FontSize(22).Semibold()).
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
