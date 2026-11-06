package gui

import (
	"context"
	"fmt"
	"os"
	"time"

	"github.com/tyler-builds/jamb/icons"
	"github.com/tyler-builds/jamb/ui"

	"github.com/tyler-builds/uas/internal/catalog"
	"github.com/tyler-builds/uas/internal/client/backend"
	"github.com/tyler-builds/uas/internal/client/backend/rpmostree"
	"github.com/tyler-builds/uas/internal/client/selfupdate"
	"github.com/tyler-builds/uas/internal/client/settings"
	"github.com/tyler-builds/uas/internal/client/updatecheck"
	"github.com/tyler-builds/uas/internal/version"
)

// settingsPage holds the settings, the catalog's state and what uas found about the system. Each section is
// a heading over a grouped list of rows, like a system settings app.
func (a *App) settingsPage(ctx *ui.Context, info catalog.BuildInfo) ui.Element {
	return ui.Component(func(ctx *ui.Context) ui.Element {
		t := ctx.Theme()
		scope := ui.Local(ctx, "scope", a.s.Settings.FlatpakScope)

		install := settingsList(ctx, settingsRow(ctx, ui.Icon(icons.Box).Size(18).Color(t.TextMuted),
			"Flatpak apps", "Just you needs no password; everyone on this computer asks for it.",
			ui.ToggleGroup(scope,
				ui.Option[settings.FlatpakScope]{Value: settings.ScopeUser, Label: "Just me"},
				ui.Option[settings.FlatpakScope]{Value: settings.ScopeSystem, Label: "Everyone"},
			).Label("Install Flatpak apps for").Small().OnChange(func(v settings.FlatpakScope) {
				st := a.s.Settings
				st.FlatpakScope = v
				if err := a.s.SaveSettings(st); err != nil {
					ctx.Toast(ui.ToastError, errText(err)).Title("Could not save settings")
					return
				}
				ctx.Toast(ui.ToastSuccess, "Settings saved")
			})))

		checks := a.updateChecksRow(ctx)

		_, refreshing := a.running("catalog")
		var catRows []ui.Element
		for _, src := range info.Sources {
			right := ui.Element(muted(ctx, compactThousands(src.Listings)+" listings"))
			if src.Error != "" {
				right = ui.Div().Tooltip(src.Error).Child(statusPill(ctx, false, "Unavailable at build time"))
			}
			catRows = append(catRows, settingsRow(ctx, sourceIcon(ctx, src.Source), src.Source.Name(), "", right))
		}
		cat := section(sectionHeader(ctx, "App catalog", fmt.Sprintf("%s apps, built %s",
			compactThousands(info.Apps), ui.FormatRelative(info.BuiltAt, time.Now())),
			ui.Button("Check for updates").Icon(icons.RefreshCw).Small().Secondary().Loading(refreshing).
				OnClick(func() { a.checkCatalog(ctx, true) })),
			settingsList(ctx, catRows...))

		p := a.s.Platform
		var sysRows []ui.Element
		for _, src := range catalog.SourcePrecedence {
			b := a.s.Backends[src]
			var note string
			switch bb := b.(type) {
			case nil:
				note = "Not installed on this system"
			case *rpmostree.Backend:
				note = fmt.Sprintf("Layers onto Fedora %d; needs a restart", bb.Release())
			default:
				if rs, ok := b.(backend.Restarter); ok && rs.NeedsRestart() {
					note = "Needs a restart"
				} else {
					note = "No restart needed"
				}
			}
			label := "Ready"
			if b == nil {
				label = "Not available"
			}
			sysRows = append(sysRows, settingsRow(ctx, sourceIcon(ctx, src), src.Name(), note,
				statusPill(ctx, b != nil, label)))
		}
		system := section(sectionHeader(ctx, "This system", fmt.Sprintf("%s on %s", p.PrettyName, p.Arch)),
			settingsList(ctx, sysRows...))

		privacy := settingsList(ctx, settingsRow(ctx, ui.Icon(icons.Lock).Size(18).Color(t.TextMuted),
			"Usage data", "uas collects none. If that ever changes, it will be a setting that is off until you "+
				"turn it on.", nil))

		page := pageBody(listMaxWidth).Gap(space8).
			Child(pageHeader(ctx, "Settings", "How uas installs apps, and what it found on this system.", nil)).
			Child(section(sectionHeader(ctx, "Installing", ""), install)).
			Child(section(sectionHeader(ctx, "Updates", ""), checks)).
			Child(cat).
			Child(system).
			Child(section(sectionHeader(ctx, "Privacy", ""), privacy)).
			Child(section(sectionHeader(ctx, "About", ""), a.aboutRow(ctx)))
		return scroll(page)
	}).Key("settings")
}

// settingsList is a grouped list: rows in one bordered box, with a rule between them.
func settingsList(ctx *ui.Context, rows ...ui.Element) ui.Element {
	t := ctx.Theme()
	list := ui.Div().Column().Radius(radiusL).Border(1, t.Border).Background(t.Surface).ClipContent()
	for i, r := range rows {
		if i > 0 {
			list = list.Child(ui.Divider())
		}
		list = list.Child(r)
	}
	return list
}

// settingsRow is one row of a settings list: an icon, a title with an optional line under it, and a control
// or status on the right.
func settingsRow(ctx *ui.Context, icon ui.Element, titleText, sub string, right ui.Element) ui.Element {
	text := ui.Div().Column().Gap(2).Grow(1).Basis(ui.Px(0)).MinWidth(ui.Px(0)).
		Child(ui.Text(titleText).FontSize(14).Medium())
	if sub != "" {
		text = text.Child(caption(ctx, sub))
	}
	return ui.Div().Row().Align(ui.Center).Gap(space3).MinHeight(ui.Px(52)).PaddingX(space4).PaddingY(space2 + 2).
		Child(ui.Div().Size(ui.Px(20), ui.Px(20)).Center().Shrink(0).Child(icon)).
		Child(text).
		Child(ui.If(right != nil, ui.Div().Row().Align(ui.Center).Shrink(0).Child(right)))
}

// sourceIcon is a source's icon in its color, with what the source is as its tooltip.
func sourceIcon(ctx *ui.Context, s catalog.SourceID) ui.Element {
	return ui.Div().Tooltip(sourceTips[s]).Child(ui.Icon(sourceIcons[s]).Size(18).Color(sourceColor(ctx, s)))
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

// updateChecksRow turns the background update check on or off: a systemd user timer that runs this program
// once a day to notify about updates. It never installs anything.
func (a *App) updateChecksRow(ctx *ui.Context) ui.Element {
	t := ctx.Theme()
	timer, err := updatecheck.DefaultTimer()
	if err != nil {
		return settingsList(ctx, settingsRow(ctx, ui.Icon(icons.Bell).Size(18).Color(t.TextMuted),
			"Check for updates in the background", errText(err), nil))
	}
	on := ui.Local(ctx, "update-checks", timer.Enabled())
	busy := ui.Local(ctx, "update-checks-busy", false)
	sw := ui.Switch("Check for updates in the background", on).Disabled(busy.Get()).OnChange(func(v bool) {
		busy.Set(true)
		go func() {
			var err error
			if v {
				var exe string
				if exe, err = os.Executable(); err == nil {
					err = timer.Enable(context.Background(), []string{exe, "--check-updates"})
				}
			} else {
				err = timer.Disable(context.Background())
			}
			ctx.Post(func() {
				busy.Set(false)
				if err != nil {
					on.Set(timer.Enabled())
					ctx.Toast(ui.ToastError, errText(err)).Title("Could not change background checks")
				}
			})
		}()
	})
	return settingsList(ctx, settingsRow(ctx, ui.Icon(icons.Bell).Size(18).Color(t.TextMuted),
		"Check for updates in the background",
		"Once a day, with a notification when updates are ready. Nothing installs without you.", sw))
}

// aboutRow shows this uas's version, with an update to the latest release when there is one.
func (a *App) aboutRow(ctx *ui.Context) ui.Element {
	icon := ui.Div().Size(ui.Px(18), ui.Px(18)).Child(brandMark(ctx, 18))
	if !version.Release() {
		return settingsList(ctx, settingsRow(ctx, icon, "uas (built from source)",
			"Builds from source don't update themselves.", nil))
	}
	res := ui.Query(ctx, "self-update", func(c context.Context) (*selfupdate.Available, error) {
		return a.s.SelfUpdater().Check(c)
	}, ui.QueryStaleTime(time.Hour))
	job, busy := a.running("self-update")
	sub := "Up to date"
	var right ui.Element
	switch {
	case res.Loading:
		sub = "Checking for a newer version…"
	case res.Err != nil:
		sub = "Could not check for a newer version: " + errText(res.Err)
	case res.Data != nil:
		sub = "uas " + res.Data.Release.Version + " is available."
		if busy {
			sub = job.status
		}
		avail := res.Data
		right = ui.Button("Update to " + avail.Release.Version).Icon(icons.Download).Primary().Small().
			Loading(busy).OnClick(func() {
			a.startJob(ctx, "self-update", "Updating uas", false, "Restart uas to use the new version",
				func(c context.Context, p func(backend.Progress)) error {
					return a.s.SelfUpdater().Install(c, avail, func(msg string) { p(backend.Progress{Message: msg}) })
				})
		})
	}
	return settingsList(ctx, settingsRow(ctx, icon, "uas "+version.Version, sub, right))
}
