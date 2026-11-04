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

// detailMaxWidth is how wide an app page grows.
const detailMaxWidth = 1240

// detailPage shows one app.
func (a *App) detailPage(ctx *ui.Context, id string) ui.Element {
	return ui.Component(func(ctx *ui.Context) ui.Element {
		res := ui.Query(ctx, a.key("app", id), func(c context.Context) (*catalog.App, error) {
			db, err := a.catalog(c)
			if err != nil {
				return nil, err
			}
			return db.Resolve(c, id)
		})
		page := pageBody(detailMaxWidth).Gap(space6)
		switch {
		case res.Loading:
			return scroll(page.Child(ui.Skeleton().Height(ui.Px(150))).Child(ui.Skeleton().Height(ui.Px(90))).
				Child(ui.Skeleton().Height(ui.Px(320))))
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

		page = page.
			Child(a.detailHeader(ctx, app, available, chosen, listing, chooseErr)).
			Child(ui.Div().Height(ui.Px(1)).Background(ctx.Theme().Border)).
			Child(statsStrip(ctx, app, listing)).
			Child(ui.Div().Height(ui.Px(1)).Background(ctx.Theme().Border))

		main := ui.Div().Column().Gap(space6).MinWidth(ui.Px(0))
		if len(app.Screenshots) > 0 {
			shots := ui.Div().Row().Gap(space4).ScrollX().PaddingBottom(space2)
			for i, s := range app.Screenshots {
				if i == 4 {
					break
				}
				url := s.ThumbURL
				if url == "" {
					url = s.URL
				}
				shots = shots.Child(a.screenshot(url, 440, s.Caption))
			}
			main = main.Child(shots)
		}
		if app.Description != "" {
			main = main.Child(heading("About This App")).
				Child(markdown.View(app.Description).Key("desc:" + app.ID).Label("Description").OnLink(openURL))
		}
		if listing.Trust != nil {
			main = main.Child(trustPanel(ctx, listing))
		}
		side := a.sidePanel(ctx, app, listing, choice, chosen.Get() == listingKey(choice.Listing))

		// The side panel sits beside the main column when there is room, and under it otherwise.
		body := ui.Div().Column().Gap(space6)
		if w, _ := ctx.WindowSize(); w-sidebarWidth > 1000 {
			body = ui.Div().Row().Gap(space8).Align(ui.Start)
			side = ui.Div().Width(ui.Px(280)).Shrink(0).Child(side)
		}
		return scroll(page.Child(body.Child(main.Grow(1).Basis(ui.Px(0))).Child(side)))
	}).Key("detail:" + id)
}

// detailHeader is the icon, name, summary and tags, with Install and the source picker on the right.
func (a *App) detailHeader(ctx *ui.Context, app *catalog.App, available []catalog.Listing,
	chosen *ui.Signal[string], listing catalog.Listing, chooseErr error) ui.Element {
	t := ctx.Theme()
	tags := ui.Div().Row().Wrap().Gap(space2)
	for i, c := range app.Categories {
		if i == 3 {
			break
		}
		tags = tags.Child(chip(ctx, categoryName(c)))
	}
	if app.Developer != "" {
		tags = tags.Child(chip(ctx, app.Developer))
	}
	var install ui.Element
	if len(available) == 0 {
		msg := "None of this app's sources can be installed on this system."
		if chooseErr != nil {
			msg = chooseErr.Error()
		}
		install = ui.Div().Width(ui.Px(220)).Child(ui.Alert("Not available here").Warning().Description(msg))
	} else {
		install = a.installArea(ctx, app, available, chosen, listing)
	}
	return ui.Div().Row().Gap(space6).Align(ui.Center).
		Child(ui.Div().Size(ui.Px(140), ui.Px(140)).Radius(32).Background(t.Surface).Center().
			Shrink(0).Child(a.appIcon(ctx, app.IconURL, 104, app.Name))).
		Child(ui.Div().Column().Gap(space2).Grow(1).Basis(ui.Px(0)).MinWidth(ui.Px(0)).
			Child(ui.Text(app.Name).FontSize(36).Bold()).
			Child(ui.Text(app.Summary).FontSize(18).TextColor(t.TextMuted)).
			Child(ui.Div().MarginTop(space2).Child(tags))).
		Child(install)
}

// installArea is the big Install button, or the installed state, over the source picker.
func (a *App) installArea(ctx *ui.Context, app *catalog.App, available []catalog.Listing, chosen *ui.Signal[string],
	listing catalog.Listing) ui.Element {
	installed := ui.Query(ctx, "installed", a.s.Installed, ui.QueryStaleTime(30*time.Second))
	var copy *backend.InstalledApp
	for _, in := range installed.Data {
		if in.Matches(listing) {
			in := in
			copy = &in
		}
	}
	job, busy := a.running(app.ID)
	big := ui.ButtonStyle{Root: ui.Style{Height: ui.Px(48), FontSize: 16, Radius: 10}}

	var action ui.Element
	switch {
	case installed.Loading:
		// Disabled as well as Loading: a loading button exposes no busy state to assistive technology.
		action = ui.Button("Install").Primary().Large().Width(ui.Full).Styles(big).Loading(true)
	case copy != nil:
		in := *copy
		action = ui.Div().Column().Gap(space2).
			Child(ui.Div().Row().Align(ui.Center).Justify(ui.Center).Gap(space2).Height(ui.Px(48)).Radius(10).
				Background(ctx.Theme().AccentSoft).
				Child(ui.Icon(icons.CircleCheck).Size(18).Color(ctx.Theme().Accent)).
				Child(ui.Text(installedLabel(in)).FontSize(15).Semibold())).
			Child(ui.Div().Row().Gap(space2).
				Child(ui.Div().Grow(1).Basis(ui.Px(0)).Child(a.updateControl(ctx, in, app.ID, app.Name, busy))).
				Child(ui.Button("Remove").Secondary().Disabled(busy).OnClick(func() {
					a.removing.Set(&removal{app: in, name: app.Name})
				})))
	default:
		action = ui.Button("Install").Icon(icons.Download).Primary().Large().Width(ui.Full).Styles(big).
			Loading(busy).OnClick(func() { a.requestInstall(ctx, app, listing) })
	}

	opts := make([]ui.Option[string], 0, len(available))
	for _, l := range available {
		opts = append(opts, ui.Option[string]{Value: listingKey(l), Label: listingLabel(l)})
	}
	// The source's logo before its label, in the closed picker and in its list.
	withMark := func(opt ui.Option[string], _ ui.PartState) ui.Element {
		for _, l := range available {
			if listingKey(l) == opt.Value {
				return ui.Div().Row().Align(ui.Center).Gap(space2).
					Child(ui.Icon(sourceIcons[l.Source]).Size(16).Color(sourceColor(ctx, l.Source))).
					Child(ui.Text(listingLabel(l)).FontSize(13).Truncate())
			}
		}
		return ui.Text(opt.Label)
	}
	picker := ui.Select(chosen, opts...).Label("Install from").Small().Width(ui.Full).
		RenderValue(withMark).RenderOption(withMark)
	col := ui.Div().Column().Gap(space3).Width(ui.Px(200)).Shrink(0).Child(action).Child(picker)
	if busy {
		col = col.Child(ui.Div().Row().Gap(space2).Align(ui.Center).
			Child(ui.ProgressRing().Size(ui.RingSmall).Label(job.title)).
			Child(ui.Div().Grow(1).Basis(ui.Px(0)).MinWidth(ui.Px(0)).Child(caption(ctx, job.status).Truncate())))
	}
	return col
}

// statsStrip is the row of key figures under the header, split by dividers.
func statsStrip(ctx *ui.Context, app *catalog.App, listing catalog.Listing) ui.Element {
	t := ctx.Theme()
	type stat struct {
		icon         *icons.Icon
		value, label string
		color        ui.Color
		tip          string
	}
	var stats []stat
	switch {
	case app.Popularity > 0:
		stats = append(stats, stat{icons.Download, compact(app.Popularity), "Installs a year", t.TextMuted,
			"Flathub's installs last month times 12, or Homebrew's installs on request in the past year"})
	case app.Stars > 0:
		stats = append(stats, stat{icons.Star, compact(app.Stars), "GitHub stars", t.TextMuted, ""})
	}
	if !app.Updated.IsZero() {
		stats = append(stats, stat{icons.Clock, app.Updated.Local().Format("Jan 2, 2006"), "Latest release", t.TextMuted, ""})
	}
	if listing.Version != "" {
		stats = append(stats, stat{icons.Tag, shortVersion(listing.Version), "Version", t.TextMuted, listing.Version})
	}
	switch {
	case listing.Verified:
		stats = append(stats, stat{icons.ShieldCheck, "Verified", "Publisher", t.Success, verifiedTip})
	case listing.Trust != nil:
		c := t.Success
		if listing.Trust.Score < 50 {
			c = t.Danger
		} else if listing.Trust.Score < 70 {
			c = t.Warning
		}
		stats = append(stats, stat{icons.ShieldCheck, fmt.Sprintf("%d / 100", listing.Trust.Score), "Trust score", c,
			"How well kept the project is; see the breakdown below"})
	case listing.Source != "":
		stats = append(stats, stat{sourceIcons[listing.Source], listing.Source.Name(), "Source",
			sourceColor(ctx, listing.Source), sourceTips[listing.Source]})
	}
	row := ui.Div().Row().Align(ui.Center).PaddingY(space2)
	for i, s := range stats {
		if i > 0 {
			row = row.Child(ui.Div().Width(ui.Px(1)).Height(ui.Px(44)).Background(t.Border))
		}
		cell := ui.Div().Row().Align(ui.Center).Justify(ui.Center).Gap(space3).Grow(1).Basis(ui.Px(0)).
			MinWidth(ui.Px(0)).
			Child(ui.Icon(s.icon).Size(22).Color(s.color)).
			Child(ui.Div().Column().Gap(2).
				Child(ui.Text(s.value).FontSize(17).Semibold()).
				Child(muted(ctx, s.label)))
		if s.tip != "" {
			cell = cell.Tooltip(s.tip)
		}
		row = row.Child(cell)
	}
	return row
}

// shortVersion trims distro release suffixes, so "5.6.1-3.fc45" shows as "5.6.1".
func shortVersion(v string) string {
	if i := strings.IndexByte(v, '-'); i > 0 && strings.ContainsAny(v[:i], ".") {
		return v[:i]
	}
	return v
}

// sidePanel lists the source, category, license and developer, the install notes, and Report an Issue.
func (a *App) sidePanel(ctx *ui.Context, app *catalog.App, listing catalog.Listing, choice recommend.Choice,
	recommended bool) ui.Element {
	t := ctx.Theme()
	field := func(label string, value ui.Element) ui.Element {
		return ui.Div().Column().Gap(space1).Child(muted(ctx, label)).Child(value)
	}
	panel := ui.Div().Column().Gap(space5)
	if listing.Source != "" {
		panel = panel.Child(field("Source", ui.Div().Row().Align(ui.Center).Gap(space2).
			Child(ui.Icon(sourceIcons[listing.Source]).Size(20).Color(sourceColor(ctx, listing.Source))).
			Child(ui.Text(listing.Source.Name()).FontSize(17).Semibold())))
	}
	if c := mainCategory(app.Categories); c != "" {
		panel = panel.Child(field("Category", ui.Text(c).FontSize(17).Semibold()))
	}
	if app.License != "" {
		panel = panel.Child(field("License", ui.Div().Tooltip(app.License).
			Child(ui.Text(licenseName(app.License)).FontSize(17).Semibold())))
	}
	if app.Developer != "" {
		dev := ui.Element(ui.Text(app.Developer).FontSize(17).Semibold())
		if web := app.Homepage; strings.HasPrefix(web, "http") {
			dev = linkButton(ctx, app.Developer, icons.ExternalLink, func() { openURL(web) })
		}
		panel = panel.Child(field("Developer", dev))
	}

	notes := ui.Div().Column().Gap(space2)
	note := func(icon *icons.Icon, color ui.Color, text string) ui.Element {
		return ui.Div().Row().Gap(space2).Align(ui.Start).
			Child(ui.Div().PaddingTop(1).Child(ui.Icon(icon).Size(15).Color(color))).
			Child(ui.Div().Grow(1).Basis(ui.Px(0)).MinWidth(ui.Px(0)).Child(ui.Text(text).FontSize(13)))
	}
	if choice.Reason != "" {
		if recommended {
			notes = notes.Child(note(icons.Sparkles, t.Accent, "Recommended: "+choice.Reason+"."))
		} else {
			notes = notes.Child(note(icons.Sparkles, t.Accent, "uas recommends "+listingLabel(choice.Listing)+": "+
				choice.Reason+"."))
		}
	}
	for _, n := range a.s.InstallNotes(listing) {
		notes = notes.Child(note(icons.Info, t.TextMuted, n))
	}
	panel = panel.Child(ui.Card().Gap(space3).Padding(space4).Child(notes).Child(a.whyPanel(ctx, app, listing)))

	bugs := ""
	for _, l := range app.Listings {
		if strings.HasPrefix(l.BugTracker, "http") {
			bugs = l.BugTracker
			break
		}
	}
	links := ui.Div().Column().Gap(space2)
	if bugs != "" {
		links = links.Child(ui.Button("Report an Issue").Icon(icons.Flag).Secondary().Width(ui.Full).
			Tooltip(bugs).OnClick(func() { openURL(bugs) }))
	}
	if strings.HasPrefix(app.VCSURL, "http") {
		vcs := app.VCSURL
		links = links.Child(ui.Button("Source Code").Icon(icons.Code).Ghost().Width(ui.Full).Tooltip(vcs).
			OnClick(func() { openURL(vcs) }))
	}
	return panel.Child(links)
}

// mainCategory is the first of an app's categories that the sidebar has, or its first category.
func mainCategory(cats []string) string {
	for _, c := range cats {
		for _, n := range navCategories {
			if n.key == c {
				return n.label
			}
		}
	}
	if len(cats) > 0 {
		return cats[0]
	}
	return ""
}

// licenseName makes an SPDX expression readable: "LicenseRef-proprietary=https://..." is "Proprietary".
func licenseName(l string) string {
	switch {
	case strings.HasPrefix(l, "LicenseRef-proprietary"):
		return "Proprietary"
	case strings.HasPrefix(l, "LicenseRef-"):
		return "Custom license"
	}
	return l
}

// categoryName is the sidebar's word for an AppStream category, or the category itself.
func categoryName(c string) string {
	for _, n := range navCategories {
		if n.key == c {
			return n.label
		}
	}
	return c
}

// statusChip is a small colored label, such as a trust verdict.
func statusChip(ctx *ui.Context, label string, c ui.Color) ui.Element {
	return ui.Div().Row().Align(ui.Center).Gap(6).PaddingX(10).Height(ui.Px(24)).Radius(12).Shrink(0).
		Background(c.Alpha(0.14)).
		Child(ui.Div().Size(ui.Px(6), ui.Px(6)).Radius(3).Background(c)).
		Child(ui.Text(label).FontSize(12).Semibold().TextColor(c))
}

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
				Child(ui.Text(s).FontSize(13).Grow(1).Basis(ui.Px(0)))
		}
		panel := ui.Div().Column().Gap(6).
			Child(ui.Div().Row().Child(toggle.Icon(icons.ChevronUp))).
			Child(ui.Text("Installing from " + listingLabel(listing) + ":").FontSize(13).Semibold())
		for _, tr := range recommend.Traits(listing) {
			panel = panel.Child(bullet(tr))
		}
		if alts := recommend.Alternatives(app, a.s.Available, listing); len(alts) > 0 {
			panel = panel.Child(ui.Text("Other sources:").FontSize(13).Semibold().MarginTop(6))
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
			if v := a.history.Peek()[len(a.history.Peek())-1]; v.page == pageApp {
				id = v.arg // the job shows on the app page it was started from
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
	color, verdict := t.Success, "Well kept"
	switch {
	case tr.Score < 50:
		color, verdict = t.Danger, "Little evidence"
	case tr.Score < 70:
		color, verdict = t.Warning, "Some evidence"
	}
	what := "How well kept this GitHub project is. It doesn't review the code."
	if l.Source == catalog.SourceCOPR {
		what = "How well kept this COPR project is, from its build history. Fedora doesn't review it."
	}
	score := ui.Div().Column().Align(ui.Center).Justify(ui.Center).Size(ui.Px(84), ui.Px(84)).Radius(42).
		Border(3, color).Background(color.Alpha(0.10)).Shrink(0).
		Child(ui.Text(fmt.Sprint(tr.Score)).FontSize(26).Bold().TextColor(color)).
		Child(caption(ctx, "of 100"))
	panel := ui.Card().Gap(space4).Padding(space6).
		Child(ui.Div().Row().Gap(space5).Align(ui.Center).
			Child(score).
			Child(ui.Div().Column().Gap(space1).Grow(1).Basis(ui.Px(0)).MinWidth(ui.Px(0)).
				Child(ui.Div().Row().Gap(space2).Align(ui.Center).
					Child(heading("Trust score")).
					Child(statusChip(ctx, verdict, color))).
				Child(muted(ctx, what))))
	grid := ui.Div().Grid(ui.Px(220), ui.Fr(1), ui.Px(52)).Gap(space4).Align(ui.Center)
	for _, sg := range tr.Signals {
		frac := float64(sg.Points) / float64(max(sg.Max, 1))
		grid = grid.
			Child(ui.Div().Column().Gap(2).
				Child(ui.Text(sg.Name).FontSize(13).Semibold()).
				Child(caption(ctx, sg.Detail))).
			Child(ui.ProgressBar(frac).HideValue().Color(color).Label(sg.Name)).
			Child(ui.Text(fmt.Sprintf("%d/%d", sg.Points, sg.Max)).FontSize(13).Semibold().Align(ui.End))
	}
	panel = panel.Child(grid)
	for _, f := range tr.Flags {
		panel = panel.Child(ui.Alert(f).Warning())
	}
	return panel
}

// details lists the app's license and links.
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
