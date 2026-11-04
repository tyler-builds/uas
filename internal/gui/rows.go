package gui

import (
	"context"
	"time"

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

// appCard is an app in a home page row: icon, name, summary and where it comes from.
func (a *App) appCard(ctx *ui.Context, r catalog.SearchResult) ui.Element {
	t := ctx.Theme()
	return clickable(ctx, ui.Div().Row().Align(ui.Center).Gap(space4).Padding(space5).Height(ui.Px(128)).
		Radius(radiusL).Background(t.Surface).MinWidth(ui.Px(0)),
		r.Name, func() { a.openApp(r.ID) }).
		Child(a.appIcon(ctx, r.IconURL, 60, r.Name)).
		Child(ui.Div().Column().Gap(space1).Grow(1).Basis(ui.Px(0)).MinWidth(ui.Px(0)).
			Child(ui.Text(r.Name).FontSize(16).Semibold().Truncate()).
			Child(ui.Text(r.Summary).FontSize(13).TextColor(t.TextMuted).Truncate()).
			Child(ui.Div().MarginTop(space3).Child(sourceTags(ctx, r.Sources, false, 2)))).
		Key("row:" + r.ID)
}

// resultRow is an app in a list: icon, name, summary, sources, figures, and Install and Details.
func (a *App) resultRow(ctx *ui.Context, r catalog.SearchResult, installed installedIndex) ui.Element {
	t := ctx.Theme()
	reachIcon, reach, reachTip := reachText(r)
	updated := "Release date unknown"
	if !r.Updated.IsZero() {
		updated = "Updated " + r.Updated.Local().Format("Jan 2, 2006")
	}
	row := ui.Div().Row().Align(ui.Center).Gap(space5).PaddingX(space6).PaddingY(space4).
		Radius(radiusL).Background(t.Surface).Hover(ui.Style{Background: t.SurfaceHover}).
		Child(ui.Div().Cursor(ui.CursorPointer).OnClick(func() { a.openApp(r.ID) }).
			Child(a.appIcon(ctx, r.IconURL, 72, r.Name))).
		Child(ui.Div().Column().Gap(space1).Grow(1).Basis(ui.Px(0)).MinWidth(ui.Px(0)).
			Cursor(ui.CursorPointer).OnClick(func() { a.openApp(r.ID) }). // Details is the keyboard way in
			Child(ui.Div().Row().Align(ui.Center).Gap(space2).MinWidth(ui.Px(0)).
				Child(ui.Div().Shrink(0).MaxWidth(ui.Px(360)).Child(ui.Text(r.Name).FontSize(18).Semibold().Truncate())).
				Child(ui.If(r.Developer != "", ui.Div().MinWidth(ui.Px(0)).Tooltip("Developer: "+r.Developer).
					Child(ui.Text("by "+r.Developer).FontSize(13).TextColor(t.TextMuted).Truncate())))).
			Child(ui.Text(r.Summary).FontSize(15).TextColor(t.TextMuted).Truncate()).
			Child(ui.Div().MarginTop(space2).Child(sourceTags(ctx, r.Sources, r.Verified, 0)))).
		Child(ui.Div().Column().Gap(space2).Width(ui.Px(200)).Shrink(0).
			Child(statLine(ctx, reachIcon, reach, reachTip)).
			Child(statLine(ctx, icons.Clock, updated, "")))
	return row.Child(a.rowActions(ctx, r, installed)).Key("row:" + r.ID)
}

// rowActions is the Install (or Installed) button and the Details link of a result row.
func (a *App) rowActions(ctx *ui.Context, r catalog.SearchResult, installed installedIndex) ui.Element {
	_, busy := a.running(r.ID)
	var main ui.Element
	if in, ok := installed.find(r); ok {
		label := "Installed"
		if in.Pending {
			label = "After restart"
		}
		main = ui.Button(label).Icon(icons.Check).Secondary().Width(ui.Full).Tooltip("Open its page to update or remove it").
			OnClick(func() { a.openApp(r.ID) })
	} else {
		main = ui.Button("Install").Icon(icons.Download).Primary().Width(ui.Full).Loading(busy).
			OnClick(func() { a.quickInstall(ctx, r) })
	}
	return ui.Div().Column().Align(ui.Center).Gap(space2).Width(ui.Px(150)).Shrink(0).
		Child(main).
		Child(linkButton(ctx, "Details", icons.ChevronRight, func() { a.openApp(r.ID) }))
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
		col := ui.Div().Column().Gap(space3)
		for range 4 {
			col = col.Child(ui.Skeleton().Height(ui.Px(108)))
		}
		return col
	case res.Err != nil:
		return ui.Empty("Something went wrong").Icon(icons.TriangleAlert).Description(errText(res.Err))
	case len(res.Data) == 0:
		return ui.Empty("No apps found").Icon(icons.Search).Description(empty)
	}
	installed := a.installedApps(ctx)
	col := ui.Div().Column().Gap(space3)
	for _, r := range res.Data {
		col = col.Child(a.resultRow(ctx, r, installed))
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
	return ui.Select(sig, opts...).Label("Sort by").Width(ui.Px(190))
}

// pageBody is a scrolling page with the standard padding, centered up to maxWidth.
func pageBody(maxWidth float32) *ui.DivElement {
	return ui.Div().Column().Gap(space6).PaddingX(space6 + 4).PaddingTop(space4).PaddingBottom(space8).
		MaxWidth(ui.Px(maxWidth))
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

// gridColumns fits items of about minWidth into the content area, up to most.
func gridColumns(ctx *ui.Context, maxWidth, minWidth float32, most int) int {
	w, _ := ctx.WindowSize()
	content := min(w-sidebarWidth, maxWidth) - 2*(space6+4)
	return max(1, min(most, int(content/minWidth)))
}

func repeatFr(n int) []ui.Length {
	cols := make([]ui.Length, n)
	for i := range cols {
		cols[i] = ui.Fr(1)
	}
	return cols
}
