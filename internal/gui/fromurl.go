package gui

import (
	"context"
	"strings"
	"time"

	"github.com/tyler-builds/jamb/icons"
	"github.com/tyler-builds/jamb/ui"

	"github.com/tyler-builds/uas/internal/catalog"
	"github.com/tyler-builds/uas/internal/client/fromurl"
)

// Install from URL: a link to an app's repo, pasted in the search box or the "Install from a link" dialog,
// opens the app's page. The catalog's page when it has the repo; otherwise the page of the app read from
// the repo just now (see session.AppForLink), with a note that it isn't in the catalog.

// openLink opens the page of the app a link points at.
func (a *App) openLink(link fromurl.Link) {
	a.openApp(link.AppID())
}

// liveNotice says, on the page of an app read from a link, that it isn't in the catalog, and why the
// system can't install it when it can't.
func (a *App) liveNotice(ctx *ui.Context, l catalog.Listing) ui.Element {
	where := l.Source.Host() + "/" + l.Source.Repo(l.Ref)
	// What installing it means is in the install notes beside it.
	msg := "uas read this app from " + where + " just now, because you gave its link. The catalog leaves it " +
		"out, often because the project is new or small. Check its trust score below, and install it only if " +
		"you trust the developer."
	alert := ui.Alert("Not in uas's catalog").Warning().Description(msg)
	if why := a.s.Unavailable(l); why != "" {
		return ui.Div().Column().Gap(space2).Child(alert).
			Child(ui.Alert("Nothing to install on this system").Description(why))
	}
	return alert
}

// linkSub is the subtitle of the search page for a link.
const linkSub = "Apps that aren't in uas's catalog can be installed from their GitHub releases."

// linkResults is the search page for a link: the catalog's app when it has the repo, else a way to look the
// app up from its repo. Looking it up asks GitHub, so it waits for a click rather than following each
// keystroke.
func (a *App) linkResults(ctx *ui.Context, page *ui.DivElement, q string) ui.Element {
	link, err := fromurl.Parse(q)
	if err != nil {
		return scroll(page.
			Child(pageHeader(ctx, "Install from a link", linkSub, nil)).
			Child(ui.Empty("Not a link uas can install from").Icon(icons.Link2Off).
				Description("Paste a link to an app's GitHub repo, such as https://github.com/owner/app, or to " +
					"one of its releases or release files.")))
	}
	res := ui.Query(ctx, a.key("by-repo", strings.ToLower(link.Ref())), func(c context.Context) (*catalog.App, error) {
		db, err := a.catalog(c)
		if err != nil {
			return nil, err
		}
		app, err := db.ByRepo(c, link.Source, link.Repo)
		if err != nil {
			return nil, nil // not in the catalog
		}
		return app, nil
	}, ui.QueryStaleTime(time.Hour))
	page = page.Child(pageHeader(ctx, "Install from a link", linkSub, nil))
	switch {
	case res.Loading:
		return fixed(page.Child(ui.Skeleton().Height(ui.Px(60))))
	case res.Data != nil:
		app := res.Data
		r := catalog.SearchResult{ID: app.ID, Name: app.Name, Summary: app.Summary, Developer: app.Developer,
			Kind: app.Kind, IconURL: app.IconURL, Verified: app.Verified(), Popularity: app.Popularity,
			Stars: app.Stars, Updated: app.Updated}
		for _, l := range app.Listings {
			r.Sources = append(r.Sources, l.Source)
			r.Refs = append(r.Refs, catalog.ListingRef{Source: l.Source, Ref: l.Ref, Package: l.Package})
		}
		return fixed(page.
			Child(muted(ctx, "uas's catalog has this app. Its page has the sources to install it from.")).
			Child(scrollList(listBox(ctx)).Child(a.resultRow(ctx, r, a.installedApps(ctx), true))))
	}
	t := ctx.Theme()
	card := ui.Card().Padding(space6).Gap(space4).
		Child(ui.Div().Row().Align(ui.Center).Gap(space4).
			Child(ui.Div().Size(ui.Px(44), ui.Px(44)).Radius(radiusS).Background(t.Surface).Border(1, t.Border).
				Center().Shrink(0).Child(ui.Icon(icons.Link).Size(20).Color(t.TextMuted))).
			Child(ui.Div().Column().Gap(space1).Grow(1).Basis(ui.Px(0)).MinWidth(ui.Px(0)).
				Child(subhead(strings.TrimPrefix(link.URL(), "https://")).Truncate()).
				Child(muted(ctx, "Not in uas's catalog. uas can read the app from its releases and show its "+
					"page, with a trust score, before you decide to install it."))).
			Child(ui.Button("Look it up").Icon(icons.ArrowRight).Primary().
				OnClick(func() { a.openLink(link) })))
	return fixed(page.Child(card))
}

// installLinkButton opens the "Install from a link" dialog.
func (a *App) installLinkButton() ui.Element {
	return ui.Button("Install from a link").Icon(icons.Link).Secondary().
		Tooltip("Install an app that isn't in uas's catalog, from a link to its GitHub repo").
		OnClick(func() { a.linkDialog.Set(true) })
}

// installLinkDialog asks for a link to an app's repo and opens the app's page.
func (a *App) installLinkDialog(ctx *ui.Context) ui.Element {
	if !a.linkDialog.Get() {
		return ui.Div().Display(false)
	}
	text := a.linkText.Get()
	_, err := fromurl.Parse(text)
	body := ui.Div().Column().Gap(space3).
		Child(ui.TextInput(a.linkText).Label("Link").Placeholder("https://github.com/owner/app").Icon(icons.Link)).
		Child(ui.If(strings.TrimSpace(text) != "" && err != nil,
			caption(ctx, "That isn't a link to a GitHub repo.").TextColor(ctx.Theme().Danger)))
	close := func() {
		a.linkDialog.Set(false)
		a.linkText.Set("")
	}
	return ui.AlertDialog("Install from a link",
		"Paste a link to an app's GitHub repo, a release, or a release file. uas shows the app's page "+
			"first, with its trust score, even if the app isn't in its catalog.").
		Icon(icons.Link).Body(body).
		Confirm("Open", func() {
			link, err := fromurl.Parse(a.linkText.Peek())
			if err != nil {
				return // the line under the field says why
			}
			close()
			a.openLink(link)
		}).
		Cancel("", close)
}
