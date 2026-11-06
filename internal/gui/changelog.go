package gui

import (
	"strings"

	"github.com/tyler-builds/jamb/icons"
	"github.com/tyler-builds/jamb/markdown"
	"github.com/tyler-builds/jamb/ui"

	"github.com/tyler-builds/uas/internal/catalog"
)

// whatsNew is an open "What's new" dialog: the notes of the releases an update brings.
type whatsNew struct {
	name      string
	changes   []catalog.Change
	from      catalog.SourceID // whose notes they are, when not the installed copy's source
	update    func()           // starts the update, or nil
	version   string           // the version the update brings
	installed string           // the installed version
}

// changelogFor returns the release notes to show for an app: those of the listing l, or else of the first of
// the app's listings that has any, with that listing's source.
func changelogFor(app *catalog.App, l catalog.Listing) ([]catalog.Change, catalog.SourceID) {
	if len(l.Changelog) > 0 {
		return l.Changelog, l.Source
	}
	for _, o := range app.Listings {
		if len(o.Changelog) > 0 {
			return o.Changelog, o.Source
		}
	}
	return nil, ""
}

// newerChanges returns the changes after the installed version, newest first: those above the entry for the
// installed version, or the newest one when the installed version isn't in the list.
func newerChanges(cs []catalog.Change, installed string) []catalog.Change {
	if iv := strings.TrimPrefix(installed, "v"); iv != "" {
		// A package's version carries the packaging's own parts: "1:1.2.3-1.fc45", "1.2.3-1ubuntu2".
		if _, after, ok := strings.Cut(iv, ":"); ok {
			iv = after
		}
		for i, c := range cs {
			v := strings.TrimPrefix(c.Version, "v")
			if iv == v || strings.HasPrefix(iv, v+"-") || strings.HasPrefix(iv, v+"+") || strings.HasPrefix(iv, v+"~") {
				return cs[:i]
			}
		}
	}
	if len(cs) > 0 {
		return cs[:1]
	}
	return nil
}

// changeView is one release's notes: its version and date, the notes, and a link to the full notes.
func changeView(ctx *ui.Context, c catalog.Change, key string) ui.Element {
	head := ui.Div().Row().Align(ui.Center).Gap(space2).
		Child(ui.Text("Version " + c.Version).FontSize(14).Semibold())
	if !c.Date.IsZero() {
		head = head.Child(caption(ctx, c.Date.Local().Format("Jan 2, 2006")))
	}
	col := ui.Div().Column().Gap(space2).Child(head).
		Child(markdown.View(c.Notes).Key("notes:" + key + ":" + c.Version).Label("Release notes").OnLink(openURL))
	if c.URL != "" {
		label := "Release page"
		if c.Cut {
			label = "Read the full notes"
		}
		url := c.URL
		col = col.Child(ui.Div().Row().Child(linkButton(ctx, label, icons.ExternalLink, func() { openURL(url) })))
	}
	return col
}

// changelogSection is the app page's "What's new": the newest release's notes, and the earlier ones folded.
func changelogSection(ctx *ui.Context, app *catalog.App, l catalog.Listing) ui.Element {
	cs, from := changelogFor(app, l)
	if len(cs) == 0 {
		return nil
	}
	head := ui.Div().Row().Align(ui.Center).Gap(space2).Child(heading("What's new"))
	if l.Source != "" && from != l.Source {
		head = head.Child(caption(ctx, "from "+from.Name()))
	}
	col := ui.Div().Column().Gap(space3).Child(head).Child(changeView(ctx, cs[0], app.ID))
	if len(cs) > 1 {
		earlier := ui.Div().Column().Gap(space5).PaddingTop(space2)
		for _, c := range cs[1:] {
			earlier = earlier.Child(changeView(ctx, c, app.ID))
		}
		open := ui.Local(ctx, "earlier-releases", false)
		col = col.Child(ui.Collapsible("Earlier versions", open, earlier).Key("earlier:" + app.ID))
	}
	return col
}

// whatsNewDialog shows the notes of the releases an update brings.
func (a *App) whatsNewDialog(ctx *ui.Context) ui.Element {
	w := a.whatsNew.Get()
	if w == nil {
		return ui.Div().Display(false)
	}
	body := ui.Div().Column().Gap(space5).MaxHeight(ui.Px(460)).ScrollY().PaddingRight(space2)
	var about []string
	if w.installed != "" {
		about = append(about, "You have version "+w.installed+".")
	}
	if w.from != "" {
		about = append(about, "Release notes from "+w.from.Name()+".")
	}
	if len(about) > 0 {
		body = body.Child(muted(ctx, strings.Join(about, " ")))
	}
	for _, c := range w.changes {
		body = body.Child(changeView(ctx, c, "dialog:"+w.name))
	}
	closeDialog := func() { a.whatsNew.Set(nil) }
	m := ui.Modal("What's new in "+w.name, body).Width(ui.Px(600)).OnClose(closeDialog).CloseOnBackdrop()
	if w.update == nil {
		done := ui.Button("Close").Primary().OnClick(closeDialog)
		return m.Footer(done).Default(done)
	}
	label := "Update"
	if w.version != "" && len(w.version) <= 12 {
		label = "Update to " + w.version
	}
	update := ui.Button(label).Icon(icons.Download).Primary().OnClick(func() { closeDialog(); w.update() })
	return m.Footer(ui.Button("Not now").Secondary().OnClick(closeDialog), update).Default(update)
}
