package gui

import (
	"context"
	"strings"

	"github.com/tyler-builds/jamb/icons"
	"github.com/tyler-builds/jamb/ui"

	"github.com/tyler-builds/uas/internal/catalog"
	"github.com/tyler-builds/uas/internal/client/appearance"
	"github.com/tyler-builds/uas/internal/client/backend"
)

// GNOME Shell themes: the Appearance page's Shell themes tab (on GNOME only), the Shell theme a GTK theme's
// page offers when the theme has one, and the User Themes extension they need. uas never turns the extension
// on without the user's click.

// canShell reports whether this desktop takes a Shell theme from uas: GNOME.
func (a *App) canShell() bool {
	return a.s.Looks != nil && a.s.Looks.Desktop != nil && a.s.Looks.Desktop.Supports(appearance.KeyShellTheme)
}

// shellInUse is the Shell theme the Shell shows: the setting's while User Themes runs, else its own look
// ("").
func (l looks) shellInUse() string {
	if !l.userThemes.Ready() {
		return ""
	}
	return l.current[appearance.KeyShellTheme]
}

// shellThemesTab lists the Shell themes on the system, the Shell's own look first, with Use and the one in
// use marked, and says when User Themes isn't running.
func (a *App) shellThemesTab(ctx *ui.Context, lk ui.QueryResult[looks], q string) []ui.Element {
	switch {
	case lk.Loading:
		return []ui.Element{ui.Skeleton().Height(ui.Px(200))}
	case lk.Err != nil:
		return []ui.Element{ui.Empty("Could not read the Shell themes").Icon(icons.TriangleAlert).
			Description(errText(lk.Err))}
	}
	d := lk.Data
	words := strings.Fields(strings.ToLower(q))
	var out []ui.Element
	if el := a.userThemesAlert(ctx, d.userThemes); el != nil {
		out = append(out, el)
	}
	var rows []ui.Element
	if matchWords(words, "Default GNOME Adwaita") {
		rows = append(rows, a.shellRow(ctx, appearance.Theme{}, d))
	}
	for _, th := range d.shellThemes {
		if matchWords(words, th.Name) {
			rows = append(rows, a.shellRow(ctx, th, d))
		}
	}
	if len(rows) == 0 {
		return append(out, ui.Empty("No Shell theme matches").Icon(icons.ListFilter).
			Description("No Shell theme on this system matches “"+q+"”."))
	}
	out = append(out, section(sectionHeader(ctx, "GNOME Shell themes",
		"The top bar, the overview and the Shell's menus. Many GTK themes come with one; these are the ones on "+
			"this system."), settingsList(ctx, rows...)), caption(ctx, appearance.ShellNote))
	if len(d.shellThemes) == 0 && len(words) == 0 {
		out = append(out, ui.Empty("No Shell themes on this system yet").Icon(icons.PanelTop).
			Description("Many GTK themes come with a Shell theme. Install one from GTK themes and it is listed here.").
			Action(ui.Button("Browse GTK themes").Secondary().OnClick(func() { a.lookTab.Set(lookGTK) })))
	}
	return out
}

// shellRow is a Shell theme on the system, or the Shell's own look for the zero Theme, with Use or its In
// use mark.
func (a *App) shellRow(ctx *ui.Context, th appearance.Theme, d looks) ui.Element {
	t := ctx.Theme()
	title, where := th.Name, a.s.SystemAppLabel()
	switch {
	case th.Name == "":
		title, where = "Default", "GNOME's own look"
	case th.By != "":
		where = "Installed by uas"
		if r, ok := d.rec(th.By); ok {
			where = "Comes with " + r.Name + ", installed by uas"
		}
	case th.User:
		where = "Added by you"
	}
	row := ui.Div().Row().Align(ui.Center).Gap(space3).MinHeight(ui.Px(52)).PaddingX(space4).PaddingY(space2).
		Key("shell-row:" + th.Name).
		Child(ui.Div().Column().Gap(3).Grow(1).Basis(ui.Px(0)).MinWidth(ui.Px(0)).
			Child(ui.Text(title).FontSize(14).Medium().Truncate()).
			Child(ui.Text(where).FontSize(12).TextColor(t.TextSubtle).Truncate()))
	item := systemItem(appearance.PartShell, th.Name, d)
	if th.Name == "" {
		item.name = "GNOME's own look"
	}
	switch {
	case d.shellInUse() == th.Name:
		row = row.Child(statusChip(ctx, "In use", t.Success))
	case a.canShell():
		_, busy := a.running("use:" + appearance.KeyShellTheme)
		name := th.Name
		row = row.Child(ui.Button("Use").Secondary().Small().Loading(busy).
			OnClick(func() { a.useShell(ctx, name, d.userThemes) }))
	}
	return row.Child(a.addToPresetMenu(ctx, item, true))
}

// useShell uses a Shell theme, or the Shell's own look, after asking to turn User Themes on when it isn't
// running.
func (a *App) useShell(ctx *ui.Context, name string, u appearance.UserThemes) {
	switch {
	case name == "" || u.Ready():
		a.useLook(ctx, appearance.KeyShellTheme, name)
	case u.Fix() != "":
		a.userThemesAsk.Set(&name)
	default:
		ctx.Toast(ui.ToastError, u.Why()).Title("Could not use " + name)
	}
}

// userThemesAlert says, above the Shell themes, that User Themes isn't running, with the button that fixes
// it; nil when it runs.
func (a *App) userThemesAlert(ctx *ui.Context, u appearance.UserThemes) ui.Element {
	if u.Ready() {
		return nil
	}
	title := "User Themes is off"
	switch {
	case !u.Installed:
		title = "User Themes isn't installed"
	case !u.ExtensionsOn:
		title = "Extensions are turned off"
	case u.Enabled:
		title = "User Themes isn't working"
	}
	desc := u.Why() + " uas turns it on only when you ask."
	al := ui.Alert(title).Info().Description(desc)
	if fix := u.Fix(); fix != "" {
		_, busy := a.running("user-themes")
		al = al.Action(ui.Button(fix).Secondary().Small().Loading(busy).OnClick(func() { a.enableUserThemes(ctx, "") }))
	}
	return al
}

// enableUserThemes gets User Themes running, as the user asked, then uses a Shell theme when one is given.
func (a *App) enableUserThemes(ctx *ui.Context, theme string) {
	done := "User Themes is on"
	if theme != "" {
		done = "User Themes is on, and the Shell theme is " + theme
	}
	a.startJobWith(ctx, "user-themes", "Turning on User Themes", false, done,
		func(c context.Context, p func(backend.Progress), res *jobResult) error {
			if err := a.s.Looks.EnableUserThemes(c); err != nil {
				return err
			}
			if theme == "" {
				return nil
			}
			p(backend.Progress{Message: "Using " + theme + "…"})
			if err := a.s.Looks.Use(c, appearance.KeyShellTheme, theme); err != nil {
				return err
			}
			a.offerUpdate(ctx, c, res)
			return nil
		})
}

// userThemesDialog asks before turning User Themes on (or installing it) for a Shell theme the user chose.
func (a *App) userThemesDialog(ctx *ui.Context) ui.Element {
	theme := a.userThemesAsk.Get()
	if theme == nil {
		return ui.Div().Display(false)
	}
	close := func() { a.userThemesAsk.Set(nil) }
	u := a.looks(ctx).Data.userThemes
	title, msg := "Turn on User Themes?", u.Why()+" GNOME applies Shell themes only through it. You can turn it "+
		"off again on the Extensions page."
	switch {
	case !u.Installed:
		title = "Install User Themes?"
		msg = u.Why() + " GNOME applies Shell themes only through it, and asks you before it installs it from " +
			"extensions.gnome.org."
	case !u.ExtensionsOn:
		title = "Turn on extensions?"
		msg += " Turning extensions on also starts the other extensions you have switched on."
	}
	label := u.Fix()
	if label == "" {
		label = "Turn on User Themes"
	}
	name := *theme
	return ui.AlertDialog(title, msg).Icon(icons.Puzzle).
		Body(caption(ctx, "Then "+name+" is used for the Shell. "+appearance.ShellNote)).
		Confirm(label+" and use", func() {
			close()
			a.enableUserThemes(ctx, name)
		}).
		Cancel("", close)
}

// shellLine is the part of an installed GTK theme's page about the Shell theme it comes with: Use for the
// Shell, or its In use mark. Nil when the variant has none.
func (a *App) shellLine(ctx *ui.Context, app *catalog.App, variant string, d looks) ui.Element {
	if !a.canShell() {
		return nil
	}
	found := false
	for _, th := range d.shellThemes {
		found = found || (th.Name == variant && th.By == app.ID)
	}
	if !found {
		return nil
	}
	t := ctx.Theme()
	row := ui.Div().Row().Align(ui.Center).Gap(space3).PaddingX(space4).PaddingY(space3).
		Radius(radiusL).Border(1, t.Border).Background(t.Surface).
		Child(ui.Div().Size(ui.Px(36), ui.Px(36)).Radius(radiusM).Background(t.SurfaceHover).Center().Shrink(0).
			Child(ui.Icon(icons.PanelTop).Size(18).Color(t.TextMuted))).
		Child(ui.Div().Column().Gap(3).Grow(1).Basis(ui.Px(0)).MinWidth(ui.Px(0)).
			Child(ui.Text(variant + " has a GNOME Shell theme").FontSize(14).Medium().Truncate()).
			Child(ui.Text(appearance.ShellNote).FontSize(12).TextColor(t.TextSubtle)))
	if d.shellInUse() == variant {
		row = row.Child(statusChip(ctx, "In use for the Shell", t.Success))
	} else {
		_, busy := a.running("use:" + appearance.KeyShellTheme)
		row = row.Child(ui.Button("Use for the Shell").Secondary().Small().Loading(busy).
			OnClick(func() { a.useShell(ctx, variant, d.userThemes) }))
	}
	return row.Child(a.addToPresetMenu(ctx, systemItem(appearance.PartShell, variant, d), true))
}
