package gui

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/tyler-builds/jamb/access"
	"github.com/tyler-builds/jamb/icons"
	"github.com/tyler-builds/jamb/ui"

	"github.com/tyler-builds/uas/internal/catalog"
	"github.com/tyler-builds/uas/internal/client/backend"
)

// installedIndex finds installed copies of catalog apps.
type installedIndex map[string]backend.InstalledApp

// installedApps returns what is installed, indexed by source and ref and by source and package name.
func (a *App) installedApps(ctx *ui.Context) installedIndex {
	res := ui.Query(ctx, "installed", a.s.Installed, ui.QueryStaleTime(30*time.Second))
	idx := installedIndex{}
	for _, in := range res.Data {
		idx[string(in.Source)+"|"+in.Ref] = in
		if in.Package != "" {
			idx[string(in.Source)+"|pkg:"+in.Package] = in
		}
	}
	return idx
}

// find returns the installed copy of a result, if any of its listings is installed.
func (idx installedIndex) find(r catalog.SearchResult) (backend.InstalledApp, bool) {
	for _, ref := range r.Refs {
		if in, ok := idx[string(ref.Source)+"|"+ref.Ref]; ok {
			return in, true
		}
		if ref.Package != "" {
			if in, ok := idx[string(ref.Source)+"|pkg:"+ref.Package]; ok {
				return in, true
			}
		}
	}
	return backend.InstalledApp{}, false
}

// reachText is the popularity figure lists sort by: installs a year, or GitHub stars.
func reachText(r catalog.SearchResult) (icon *icons.Icon, text, tip string) {
	switch {
	case r.Popularity > 0:
		return icons.Download, compact(r.Popularity) + " installs / yr",
			"Installs in a year: Flathub's installs last month times 12, or Homebrew's installs on request " +
				"in the past year, whichever is higher"
	case r.Stars > 0:
		return icons.Star, compact(r.Stars) + " GitHub stars",
			"GitHub doesn't publish install counts, so stars stand in for popularity"
	}
	return icons.Download, "No install data", "This app's sources don't publish install counts"
}

// appCard is an app in a home page row: icon, name, a line of summary and where it comes from.
func (a *App) appCard(ctx *ui.Context, r catalog.SearchResult) ui.Element {
	t := ctx.Theme()
	return ui.Card().Row().Align(ui.Center).Gap(space3).Padding(space3).MinWidth(ui.Px(0)).
		Label(r.Name).OnClick(func() { a.openApp(r.ID) }).
		Child(a.appIcon(ctx, r.IconURL, 44, r.Name)).
		Child(ui.Div().Column().Gap(2).Grow(1).Basis(ui.Px(0)).MinWidth(ui.Px(0)).
			Child(ui.Text(r.Name).FontSize(14).Medium().Truncate()).
			Child(ui.Text(r.Summary).FontSize(13).TextColor(t.TextMuted).Truncate()).
			Child(ui.Div().MarginTop(2).Child(metaLine(ctx, sourceWords(ctx, r.Sources, false, 2)...)))).
		Key("row:" + r.ID)
}

// metaLine is a row of quiet words split by middle dots, such as an app's sources and developer.
func metaLine(ctx *ui.Context, parts ...ui.Element) ui.Element {
	t := ctx.Theme()
	row := ui.Div().Row().Align(ui.Center).Gap(6).MinWidth(ui.Px(0))
	for i, p := range parts {
		if i > 0 {
			row = row.Child(ui.Text("·").FontSize(12).TextColor(t.TextSubtle))
		}
		row = row.Child(p)
	}
	return row
}

// sourceWords are an app's sources as words for a metaLine, each explained on hover, with Verified last.
// With max above zero, sources past max collapse into a "+n".
func sourceWords(ctx *ui.Context, sources []catalog.SourceID, verified bool, max int) []ui.Element {
	t := ctx.Theme()
	var out []ui.Element
	for i, s := range sources {
		if max > 0 && i == max {
			rest := make([]string, 0, len(sources)-max)
			for _, r := range sources[max:] {
				rest = append(rest, r.Name())
			}
			out = append(out, ui.Div().Tooltip("Also on "+strings.Join(rest, " and ")).Shrink(0).
				Child(ui.Text(fmt.Sprintf("+%d", len(sources)-max)).FontSize(12).TextColor(t.TextSubtle)))
			break
		}
		out = append(out, ui.Div().Tooltip(s.Name()+": "+sourceTips[s]).Shrink(0).
			Child(ui.Text(s.Name()).FontSize(12).TextColor(t.TextSubtle)))
	}
	if verified {
		out = append(out, ui.Div().Row().Align(ui.Center).Gap(4).Shrink(0).Tooltip("Verified publisher: "+verifiedTip).
			Child(ui.Icon(icons.ShieldCheck).Size(12).Color(t.Success.Alpha(0.8))).
			Child(ui.Text("Verified").FontSize(12).TextColor(t.TextSubtle)))
	}
	return out
}

// resultRow is an app in a list: icon, name and summary, its sources and developer, figures, and Install.
// Rows sit in a bordered list (see listBox), split by hairlines; the row opens the app's page.
func (a *App) resultRow(ctx *ui.Context, r catalog.SearchResult, installed installedIndex, first bool) ui.Element {
	return ui.Component(func(ctx *ui.Context) ui.Element {
		t := ctx.Theme()
		_, reach, reachTip := reachText(r)
		updated := "Release date unknown"
		if !r.Updated.IsZero() {
			updated = "Updated " + r.Updated.Local().Format("Jan 2, 2006")
		}
		meta := sourceWords(ctx, r.Sources, r.Verified, 0)
		if r.Developer != "" {
			meta = append(meta, ui.Div().MinWidth(ui.Px(0)).Tooltip("Developer: "+r.Developer).
				Child(ui.Text(r.Developer).FontSize(12).TextColor(t.TextSubtle).Truncate()))
		}
		row := listRow(ctx, first).
			Child(ui.Div().Cursor(ui.CursorPointer).OnClick(func() { a.openApp(r.ID) }).
				Child(a.appIcon(ctx, r.IconURL, 36, r.Name))).
			Child(ui.Div().Column().Gap(3).Grow(1).Basis(ui.Px(0)).MinWidth(ui.Px(0)).
				Cursor(ui.CursorPointer).OnClick(func() { a.openApp(r.ID) }).
				Role(access.RoleLink).Label(r.Name + " details").TabIndex(0).
				Child(ui.Div().Row().Align(ui.Center).Gap(space3).MinWidth(ui.Px(0)).
					Child(ui.Text(r.Name).FontSize(14).Medium().Truncate().Shrink(0)).
					Child(metaLine(ctx, meta...))).
				Child(ui.Text(r.Summary).FontSize(13).TextColor(t.TextMuted).Truncate())).
			Child(ui.Div().Column().Gap(3).Align(ui.End).Width(ui.Px(150)).Shrink(0).
				Child(ui.Div().Tooltip(reachTip).Child(ui.Text(reach).FontSize(12).TextColor(t.TextMuted).Medium())).
				Child(ui.Text(updated).FontSize(12).TextColor(t.TextSubtle)))
		return row.Child(a.rowActions(ctx, r, installed))
	}).Key("row:" + r.ID)
}

// listRow is one row of a listBox; rows after the first have a hairline above them.
func listRow(ctx *ui.Context, first bool) *ui.DivElement {
	t := ctx.Theme()
	row := ui.Div().Row().Align(ui.Center).Gap(space4).PaddingX(space4).PaddingY(10).
		Hover(ui.Style{Background: t.SurfaceHover})
	if !first {
		row = row.BorderEdges(ui.Edges{Top: 1}, t.Border)
	}
	return row
}

// listBox is the bordered frame around a list of rows.
func listBox(ctx *ui.Context) *ui.DivElement {
	t := ctx.Theme()
	return ui.Div().Column().Radius(radiusL).Border(1, t.Border).Background(t.Surface).ClipContent()
}

// rowActions is the Install (or Installed) button of a result row. The row itself opens the app's page.
func (a *App) rowActions(ctx *ui.Context, r catalog.SearchResult, installed installedIndex) ui.Element {
	_, busy := a.running(r.ID)
	var main ui.Element
	if in, ok := installed.find(r); ok {
		label := "Installed"
		if in.Pending {
			label = "After restart"
		}
		main = ui.IconButton(icons.Check).Ghost().Small().Label(label).
			Tooltip(label + ": open its page to update or remove it").
			OnClick(func() { a.openApp(r.ID) })
	} else {
		// An outlined icon button, so a long list isn't a column of repeated words.
		main = ui.IconButton(icons.Download).Secondary().Small().Label("Install").Tooltip("Install " + r.Name).
			Loading(busy).OnClick(func() { a.quickInstall(ctx, r) })
	}
	return ui.Div().Row().Justify(ui.End).Width(ui.Px(40)).Shrink(0).Child(main)
}

// quickInstall installs an app from a list, from the source its page would recommend. Unreviewed and
// layered sources still ask first.
func (a *App) quickInstall(ctx *ui.Context, r catalog.SearchResult) {
	go func() {
		var app *catalog.App
		db, err := a.catalog(context.Background())
		if err == nil {
			app, err = db.App(context.Background(), r.ID)
		}
		ctx.Post(func() {
			if err != nil {
				ctx.Toast(ui.ToastError, errText(err)).Title("Could not install " + r.Name)
				return
			}
			choice, err := a.s.Choose(app, "")
			if err != nil {
				ctx.Toast(ui.ToastWarning, err.Error()).Title(r.Name + " can't be installed here")
				return
			}
			a.requestInstall(ctx, app, choice.Listing)
		})
	}()
}

// resultList shows results as rows, or a message when there are none.
func (a *App) resultList(ctx *ui.Context, res ui.QueryResult[[]catalog.SearchResult], empty string) ui.Element {
	switch {
	case res.Loading:
		col := ui.Div().Column().Gap(space2)
		for range 6 {
			col = col.Child(ui.Skeleton().Height(ui.Px(60)))
		}
		return col
	case res.Err != nil:
		return ui.Empty("Something went wrong").Icon(icons.TriangleAlert).Description(errText(res.Err))
	case len(res.Data) == 0:
		return ui.Empty("No apps found").Icon(icons.Search).Description(empty)
	}
	installed := a.installedApps(ctx)
	col := listBox(ctx)
	for i, r := range res.Data {
		col = col.Child(a.resultRow(ctx, r, installed, i == 0))
	}
	return col
}

// orderPicker sorts a list.
func orderPicker(sig *ui.Signal[catalog.Order], withRelevance bool) ui.Element {
	opts := []ui.Option[catalog.Order]{
		{Value: catalog.OrderPopular, Label: "Most popular"},
		{Value: catalog.OrderUpdated, Label: "Recently updated"},
		{Value: catalog.OrderName, Label: "Name"},
	}
	if withRelevance {
		opts = append([]ui.Option[catalog.Order]{{Value: catalog.OrderRelevance, Label: "Most relevant"}}, opts...)
	}
	return ui.Select(sig, opts...).Label("Sort by").Small().Width(ui.Px(170))
}

// pageBody is a scrolling page with the standard padding, centered up to maxWidth.
func pageBody(maxWidth float32) *ui.DivElement {
	return ui.Div().Column().Gap(space6).PaddingX(space8).PaddingTop(space6).PaddingBottom(space8).
		MaxWidth(ui.Px(maxWidth))
}

// scroll puts a page in a scrolling column that fills the content area, centered up to its MaxWidth.
func scroll(page *ui.DivElement) ui.Element {
	return ui.Div().Column().Align(ui.Center).Grow(1).Basis(ui.Px(0)).MinHeight(ui.Px(0)).ScrollY().
		Child(page.Width(ui.Full))
}

// gridColumns fits items of about minWidth into the content area, up to most.
func gridColumns(ctx *ui.Context, maxWidth, minWidth float32, most int) int {
	w, _ := ctx.WindowSize()
	content := min(w-sidebarWidth-space2, maxWidth) - 2*space8
	return max(1, min(most, int(content/minWidth)))
}

func repeatFr(n int) []ui.Length {
	cols := make([]ui.Length, n)
	for i := range cols {
		cols[i] = ui.Fr(1)
	}
	return cols
}
