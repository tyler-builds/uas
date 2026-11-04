package gui

import (
	"context"
	"fmt"
	"os/exec"
	"strings"
	"time"

	"github.com/tyler-builds/jamb/icons"
	"github.com/tyler-builds/jamb/markdown"
	"github.com/tyler-builds/jamb/ui"

	"github.com/tyler-builds/uas/internal/catalog"
	"github.com/tyler-builds/uas/internal/client/backend"
	"github.com/tyler-builds/uas/internal/client/recommend"
)

// pending is an install waiting for the user's OK.
type pending struct {
	app     *catalog.App
	listing catalog.Listing
}

// removal is a removal waiting for the user's OK.
type removal struct {
	app  backend.InstalledApp
	name string // the name to show
}

// listingKey identifies a listing among an app's listings.
func listingKey(l catalog.Listing) string { return string(l.Source) + "|" + l.Ref }

// detailPage shows one app.
func (a *App) detailPage(ctx *ui.Context, id string) ui.Element {
	return ui.Component(func(ctx *ui.Context) ui.Element {
		t := ctx.Theme()
		res := ui.Query(ctx, a.key("app", id), func(c context.Context) (*catalog.App, error) {
			db, err := a.catalog(c)
			if err != nil {
				return nil, err
			}
			return db.Resolve(c, id)
		})
		back := ui.Button("Back").Icon(icons.ArrowLeft).Ghost().OnClick(func() { a.selected.Set("") })
		page := ui.Div().Column().Gap(20).Padding(24).MaxWidth(ui.Px(960)).Child(back)
		switch {
		case res.Loading:
			return scroll(page.Child(ui.Skeleton().Height(ui.Px(96))).Child(ui.Skeleton().Height(ui.Px(240))))
		case res.Err != nil:
			return scroll(page.Child(ui.Empty("Could not open this app").Icon(icons.TriangleAlert).
				Description(errText(res.Err))))
		}
		app := res.Data

		var available []catalog.Listing
		for _, l := range app.Listings {
			if a.s.Available(l) {
				available = append(available, l)
			}
		}
		chosen := ui.Local(ctx, "source", "")
		choice, chooseErr := a.s.Choose(app, "")
		if chosen.Peek() == "" && chooseErr == nil {
			chosen.Set(listingKey(choice.Listing))
		}
		var listing catalog.Listing
		for _, l := range available {
			if listingKey(l) == chosen.Get() {
				listing = l
			}
		}

		header := ui.Div().Row().Gap(20).Align(ui.Start).
			Child(a.appIcon(ctx, app.IconURL, 96, app.Name)).
			Child(ui.Div().Column().Gap(6).Grow(1).Basis(ui.Px(0)).MinWidth(ui.Px(0)).
				Child(ui.Text(app.Name).FontSize(26).Semibold()).
				Child(ui.If(app.Developer != "", ui.Text(app.Developer).TextColor(t.TextMuted))).
				Child(ui.Text(app.Summary).FontSize(15)))
		page = page.Child(header)

		if len(available) == 0 {
			msg := "None of this app's sources can be installed on this system."
			if chooseErr != nil {
				msg = chooseErr.Error()
			}
			page = page.Child(ui.Alert("Not available here").Warning().Description(msg))
		} else {
			page = page.Child(a.installBox(ctx, app, available, chosen, listing, choice.Reason,
				listingKey(choice.Listing)))
		}

		if listing.Trust != nil {
			page = page.Child(trustPanel(ctx, listing))
		}
		if len(app.Screenshots) > 0 {
			shots := ui.Div().Row().Gap(16).ScrollX().PaddingBottom(8)
			for i, s := range app.Screenshots {
				if i == 3 {
					break
				}
				url := s.ThumbURL
				if url == "" {
					url = s.URL
				}
				shots = shots.Child(a.screenshot(url, 420, s.Caption))
			}
			page = page.Child(shots)
		}
		if app.Description != "" {
			page = page.Child(markdown.View(app.Description).Key("desc:" + app.ID).Label("Description").
				OnLink(openURL))
		}
		page = page.Child(a.details(ctx, app))
		return scroll(page)
	}).Key("detail:" + id)
}

// scroll puts a page in a scrolling column that fills the content area, centered up to its MaxWidth.
//
// The centering is a row with Justify(Center) and a growing page, not Align(Center) on the column: under
// Align(Center), jamb sizes the page to its content, measures rows that use Basis(0) at no width, and the
// page ends up far taller than its content (see jamb-notes.md).
func scroll(page *ui.DivElement) ui.Element {
	return ui.Div().Column().Grow(1).Basis(ui.Px(0)).MinHeight(ui.Px(0)).ScrollY().
		Child(ui.Div().Row().Justify(ui.Center).Child(page.Grow(1).Basis(ui.Px(0)).MinWidth(ui.Px(0))))
}

// installBox is the source picker and the install, update and remove buttons.
func (a *App) installBox(ctx *ui.Context, app *catalog.App, available []catalog.Listing, chosen *ui.Signal[string],
	listing catalog.Listing, reason, recommended string) ui.Element {
	t := ctx.Theme()
	installed := ui.Query(ctx, "installed", a.s.Installed, ui.QueryStaleTime(30*time.Second))
	var copy *backend.InstalledApp
	for _, in := range installed.Data {
		if in.Matches(listing) {
			in := in
			copy = &in
		}
	}

	opts := make([]ui.Option[string], 0, len(available))
	for _, l := range available {
		opts = append(opts, ui.Option[string]{Value: listingKey(l), Label: listingLabel(l)})
	}
	picker := ui.Select(chosen, opts...).Label("Install from").Width(ui.Px(340))

	job, busy := a.running(app.ID)
	var action ui.Element
	switch {
	case installed.Loading:
		// Disabled as well as Loading: a loading button exposes no busy state to assistive technology.
		action = ui.Button("Install").Primary().Loading(true).Disabled(true)
	case copy != nil:
		in := *copy
		row := ui.Div().Row().Gap(8).Align(ui.Center).
			Child(ui.BadgeText(installedLabel(in)).Variant(ui.BadgeSuccess).StatusDot()).
			Child(a.updateControl(ctx, in, app.ID, app.Name, busy))
		action = row.Child(ui.Button("Remove").Danger().Disabled(busy).OnClick(func() {
			a.removing.Set(&removal{app: in, name: app.Name})
		}))
	default:
		action = ui.Button("Install").Primary().Icon(icons.Download).Loading(busy).OnClick(func() {
			a.requestInstall(ctx, app, listing)
		})
	}

	box := ui.Div().Column().Gap(10).Padding(16).Radius(10).Border(1, t.Border).Background(t.Surface).
		Child(ui.Div().Row().Gap(12).Align(ui.End).Wrap().Child(picker).Child(action))
	if chosen.Get() == recommended && reason != "" {
		box = box.Child(ui.Text("Recommended: " + reason + ".").FontSize(13).TextColor(t.TextMuted))
	} else if reason != "" {
		for _, l := range available {
			if listingKey(l) == recommended {
				box = box.Child(ui.Text("uas recommends " + listingLabel(l) + ": " + reason + ".").FontSize(13).
					TextColor(t.TextMuted))
			}
		}
	}
	for _, n := range a.s.InstallNotes(listing) {
		box = box.Child(ui.Text(n).FontSize(13).TextColor(t.TextMuted))
	}
	box = box.Child(a.whyPanel(ctx, app, listing))
	if busy {
		box = box.Child(ui.Div().Row().Gap(8).Align(ui.Center).
			Child(ui.ProgressRing().Size(ui.RingSmall).Label(job.title)).
			Child(ui.Text(job.status).FontSize(13).Truncate()))
	}
	return box
}

// updateControl is an Update button when the app has an update, "Up to date" when its source says it has
// none, and nothing for sources that can't tell (layered packages update with the system).
func (a *App) updateControl(ctx *ui.Context, in backend.InstalledApp, jobID, name string, busy bool) ui.Element {
	if !a.canCheck(in) {
		return ui.Div()
	}
	ups := a.updates(ctx)
	version, ok := ups.Data[in.Key()]
	switch {
	case ok:
		label := "Update"
		if version != "" {
			label = "Update to " + version
		}
		return ui.Button(label).Primary().Icon(icons.Download).Loading(busy).OnClick(func() {
			a.startJob(ctx, jobID, "Updating "+name, a.s.NeedsRestart(in.Source), name+" is up to date",
				func(c context.Context, p func(backend.Progress)) error { return a.s.Update(c, in, p) })
		})
	case ups.Loading:
		return ui.ProgressRing().Size(ui.RingSmall).Label("Checking for updates")
	case ups.Err != nil:
		return ui.Div()
	}
	return ui.BadgeText("Up to date").Variant(ui.BadgeNeutral)
}

// whyPanel explains what installing from the picked source means and how the app's other sources compare.
// It opens from a "Why this source?" link, so the box stays short.
func (a *App) whyPanel(ctx *ui.Context, app *catalog.App, listing catalog.Listing) ui.Element {
	return ui.Component(func(ctx *ui.Context) ui.Element {
		t := ctx.Theme()
		open := ui.Local(ctx, "open", false)
		toggle := ui.Button("Why " + listing.Source.Name() + "?").Ghost().Small().Icon(icons.Info).
			OnClick(func() { open.Set(!open.Peek()) })
		if !open.Get() {
			return ui.Div().Row().Child(toggle)
		}
		bullet := func(s string) ui.Element {
			return ui.Div().Row().Gap(8).Align(ui.Start).
				Child(ui.Text("•").FontSize(13).TextColor(t.TextMuted)).
				Child(ui.Div().Grow(1).Basis(ui.Px(0)).MinWidth(ui.Px(0)).Child(ui.Text(s).FontSize(13)))
		}
		panel := ui.Div().Column().Gap(6).
			Child(ui.Div().Row().Child(toggle.Icon(icons.ChevronUp))).
			Child(ui.Text("Installing from " + listingLabel(listing) + ":").FontSize(13).Semibold())
		for _, tr := range recommend.Traits(listing) {
			panel = panel.Child(bullet(tr))
		}
		if alts := recommend.Alternatives(app, a.s.Available, listing); len(alts) > 0 {
			panel = panel.Child(ui.Div().MarginTop(6).Child(ui.Text("Other sources:").FontSize(13).Semibold()))
			for _, alt := range alts {
				panel = panel.Child(bullet(listingLabel(alt.Listing) + ": not picked because " + alt.Because + "."))
			}
		}
		return panel
	}).Key("why:" + listingKey(listing))
}

func installedLabel(in backend.InstalledApp) string {
	s := "Installed"
	if in.Version != "" {
		s += " " + in.Version
	}
	if in.Pending {
		s += " (after restart)"
	}
	return s
}

// listingLabel names a listing in the source picker.
func listingLabel(l catalog.Listing) string {
	parts := []string{sourceLabel(l.Source)}
	switch l.Source {
	case catalog.SourceFedora:
		parts[0] = strings.Replace(l.Distro, "fedora-", "Fedora ", 1)
	case catalog.SourceCOPR:
		parts = append(parts, l.Project)
	}
	if l.Version != "" {
		parts = append(parts, l.Version)
	}
	s := strings.Join(parts, " · ")
	switch {
	case l.Verified:
		s += " (verified)"
	case l.Trust != nil:
		s += fmt.Sprintf(" (trust %d)", l.Trust.Score)
	}
	return s
}

// requestInstall installs at once from reviewed sources, and asks first for the others.
func (a *App) requestInstall(ctx *ui.Context, app *catalog.App, l catalog.Listing) {
	switch l.Source {
	case catalog.SourceGitHub, catalog.SourceCOPR, catalog.SourceFedora:
		a.confirm.Set(&pending{app: app, listing: l})
	default:
		a.install(ctx, app, l)
	}
}

func (a *App) install(ctx *ui.Context, app *catalog.App, l catalog.Listing) {
	restart := a.s.NeedsRestart(l.Source)
	a.startJob(ctx, app.ID, "Installing "+app.Name, restart, app.Name+" is installed",
		func(c context.Context, p func(backend.Progress)) error { return a.s.Install(c, l, p) })
}

// confirmDialog asks before installing from a source that is unreviewed or changes the system image.
func (a *App) confirmDialog(ctx *ui.Context) ui.Element {
	p := a.confirm.Get()
	if p == nil {
		return ui.Div().Display(false)
	}
	l := p.listing
	title := fmt.Sprintf("Install %s from %s?", p.app.Name, listingLabel(l))
	body := ui.Div().Column().Gap(8)
	for _, n := range a.s.InstallNotes(l) {
		body = body.Child(ui.Text(n).FontSize(13))
	}
	if l.Trust != nil {
		body = body.Child(trustPanel(ctx, l))
	}
	intent := ui.AlertDialogConfirm
	if l.Trust != nil && (l.Trust.Score < 60 || len(l.Trust.Flags) > 0) {
		intent = ui.AlertDialogWarning
	}
	message := "This source is not reviewed by a distribution."
	if l.Source == catalog.SourceFedora {
		message = "This adds the package to your system image."
	}
	return ui.AlertDialog(title, message).Intent(intent).Body(body).
		Confirm("Install", func() {
			a.confirm.Set(nil)
			a.install(ctx, p.app, l)
		}).
		Cancel("", func() { a.confirm.Set(nil) })
}

// removeDialog asks before removing an app.
func (a *App) removeDialog(ctx *ui.Context) ui.Element {
	r := a.removing.Get()
	if r == nil {
		return ui.Div().Display(false)
	}
	msg := "Its settings in your home folder are kept."
	if a.s.NeedsRestart(r.app.Source) {
		msg = "It is removed from your system image at the next restart."
	}
	return ui.AlertDialog("Remove "+r.name+"?", msg).Intent(ui.AlertDialogDestructive).
		Confirm("Remove", func() {
			a.removing.Set(nil)
			in := r.app
			id := in.AppID
			if a.selected.Peek() != "" {
				id = a.selected.Peek()
			}
			a.startJob(ctx, id, "Removing "+r.name, a.s.NeedsRestart(in.Source), r.name+" is removed",
				func(c context.Context, p func(backend.Progress)) error { return a.s.Remove(c, in, p) })
		}).
		Cancel("", func() { a.removing.Set(nil) })
}

// trustPanel shows a trust score, its signals and its warnings.
func trustPanel(ctx *ui.Context, l catalog.Listing) ui.Element {
	t := ctx.Theme()
	tr := l.Trust
	variant := ui.BadgeSuccess
	switch {
	case tr.Score < 50:
		variant = ui.BadgeDanger
	case tr.Score < 70:
		variant = ui.BadgeWarning
	}
	panel := ui.Div().Column().Gap(8).Padding(16).Radius(10).Border(1, t.Border).
		Child(ui.Div().Row().Gap(10).Align(ui.Center).
			Child(ui.Text("Trust score").FontSize(15).Semibold()).
			Child(ui.BadgeText(fmt.Sprintf("%d / 100", tr.Score)).Variant(variant))).
		Child(ui.ProgressBar(float64(tr.Score) / 100).Label("Trust score"))
	for _, s := range tr.Signals {
		panel = panel.Child(ui.Div().Row().Gap(12).
			Child(ui.Text(s.Name).Width(ui.Px(170)).FontSize(13)).
			Child(ui.Text(fmt.Sprintf("%d/%d", s.Points, s.Max)).Width(ui.Px(48)).FontSize(13).Semibold()).
			Child(ui.Text(s.Detail).FontSize(13).TextColor(t.TextMuted).Grow(1)))
	}
	for _, f := range tr.Flags {
		panel = panel.Child(ui.Alert(f).Warning())
	}
	return panel
}

// details lists the app's links and license.
func (a *App) details(ctx *ui.Context, app *catalog.App) ui.Element {
	t := ctx.Theme()
	rows := ui.Div().Column().Gap(6)
	add := func(label string, value ui.Element) {
		rows = rows.Child(ui.Div().Row().Gap(12).Align(ui.Center).MinHeight(ui.Px(28)).
			Child(ui.Text(label).Width(ui.Px(120)).TextColor(t.TextMuted).FontSize(13)).Child(value))
	}
	if app.License != "" {
		add("License", ui.Text(app.License).FontSize(13))
	}
	for _, u := range []struct{ label, url string }{{"Website", app.Homepage}, {"Source code", app.VCSURL}} {
		if strings.HasPrefix(u.url, "https://") || strings.HasPrefix(u.url, "http://") {
			url := u.url
			add(u.label, ui.Button(url).Ghost().Small().Icon(icons.ExternalLink).OnClick(func() { openURL(url) }))
		}
	}
	return rows
}

// openURL opens a web link in the default browser.
func openURL(url string) {
	if !strings.HasPrefix(url, "https://") && !strings.HasPrefix(url, "http://") {
		return
	}
	cmd := exec.Command("xdg-open", url)
	if err := cmd.Start(); err == nil {
		go cmd.Wait()
	}
}
