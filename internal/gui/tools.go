package gui

import (
	"context"
	"strings"
	"time"
	"unicode"

	"github.com/tyler-builds/jamb/icons"
	"github.com/tyler-builds/jamb/ui"

	"github.com/tyler-builds/uas/internal/client/ujust"
)

// toolsTitle names the ujust page after the system: "Bazzite tools".
func (a *App) toolsTitle() string {
	name := a.s.Platform.PrettyName
	if i := strings.IndexAny(name, " ("); i > 0 {
		name = name[:i]
	}
	if name == "" {
		return "System tools"
	}
	return name + " tools"
}

// pendingTool is a ujust command waiting for the user's OK.
type pendingTool struct {
	recipe ujust.Recipe
	option string
}

// toolsPage lists the system's ujust commands by group. Each runs in a terminal window, after the user
// confirms, since many change the system or ask for a password.
func (a *App) toolsPage(ctx *ui.Context) ui.Element {
	return ui.Component(func(ctx *ui.Context) ui.Element {
		res := ui.Query(ctx, "ujust", func(c context.Context) ([]ujust.Recipe, error) {
			return a.s.Tools.Recipes(c)
		}, ui.QueryStaleTime(time.Hour))
		confirm := ui.Local[*pendingTool](ctx, "confirm", nil)
		page := pageBody(listMaxWidth).
			Child(pageHeader(ctx, a.toolsTitle(),
				"Commands your system provides for drivers, apps and tweaks (ujust). Each runs in a terminal "+
					"window, where it may ask questions or for your password.", nil))
		switch {
		case res.Loading:
			return scroll(page.Child(ui.Skeleton().Height(ui.Px(200))))
		case res.Err != nil:
			return scroll(page.Child(ui.Empty("Could not list the commands").Icon(icons.TriangleAlert).
				Description(errText(res.Err))))
		}
		q := strings.ToLower(strings.TrimSpace(a.listFilter.Get()))
		var groups []string
		byGroup := map[string][]ui.Element{}
		for _, r := range res.Data {
			if q != "" && !strings.Contains(strings.ToLower(r.Name+" "+r.Doc+" "+r.Group), q) {
				continue
			}
			if _, ok := byGroup[r.Group]; !ok {
				groups = append(groups, r.Group)
			}
			byGroup[r.Group] = append(byGroup[r.Group], a.toolRow(ctx, r, confirm))
		}
		if len(groups) == 0 {
			page = page.Child(ui.Empty("No commands match").Icon(icons.SquareTerminal))
		}
		for _, g := range groups {
			page = page.Child(section(sectionHeader(ctx, groupTitle(g), ""), settingsList(ctx, byGroup[g]...)))
		}
		return ui.Div().Size(ui.Full, ui.Full).Child(scroll(page)).Child(a.toolDialog(ctx, confirm))
	}).Key("tools")
}

// toolRow is one command: its name and description, and Run, with a choice of its options when it has
// them ("install", "uninstall", "status").
func (a *App) toolRow(ctx *ui.Context, r ujust.Recipe, confirm *ui.Signal[*pendingTool]) ui.Element {
	return ui.Component(func(ctx *ui.Context) ui.Element {
		t := ctx.Theme()
		right := ui.Div().Row().Align(ui.Center).Gap(space2)
		option := ui.Local(ctx, "option", "")
		if len(r.Options) > 0 {
			if option.Peek() == "" {
				option.Set(r.Options[0])
			}
			opts := make([]ui.Option[string], 0, len(r.Options))
			for _, o := range r.Options {
				opts = append(opts, ui.Option[string]{Value: o, Label: o})
			}
			right = right.Child(ui.Select(option, opts...).Label("Action for "+r.Name).Small().Width(ui.Px(130)))
		}
		right = right.Child(ui.Button("Run").Icon(icons.Play).Secondary().Small().
			Tooltip("ujust " + strings.TrimSpace(r.Name+" "+option.Get())).
			OnClick(func() {
				p := &pendingTool{recipe: r, option: option.Peek()}
				if p.option == "status" {
					a.runTool(ctx, p) // only reports; nothing to confirm
					return
				}
				confirm.Set(p)
			}))
		doc := r.Doc
		if doc == "" {
			doc = "No description"
		}
		return settingsRow(ctx, ui.Icon(icons.SquareTerminal).Size(18).Color(t.TextMuted), r.Name, doc, right)
	}).Key("tool:" + r.Name)
}

// toolDialog asks before running a command.
func (a *App) toolDialog(ctx *ui.Context, confirm *ui.Signal[*pendingTool]) ui.Element {
	p := confirm.Get()
	if p == nil {
		return ui.Div().Display(false)
	}
	cmd := strings.TrimSpace("ujust " + p.recipe.Name + " " + p.option)
	body := ui.Div().Column().Gap(space2).
		Child(ui.Text(cmd).FontSize(13).Mono().TextColor(ctx.Theme().Text))
	if p.recipe.Doc != "" {
		body = body.Child(ui.Text(p.recipe.Doc).FontSize(13))
	}
	return ui.AlertDialog("Run "+p.recipe.Name+"?",
		"It opens in a terminal window. It may change your system and ask for your password.").
		Body(body).
		Confirm("Run in terminal", func() {
			confirm.Set(nil)
			a.runTool(ctx, p)
		}).
		Cancel("", func() { confirm.Set(nil) })
}

func (a *App) runTool(ctx *ui.Context, p *pendingTool) {
	if err := ujust.Run(p.recipe.Name, p.option); err != nil {
		ctx.Toast(ui.ToastError, errText(err)).Title("Could not run " + p.recipe.Name)
	}
}

// groupTitle is a recipe group's heading: "apps" is "Apps"; recipes without one are "Other".
func groupTitle(g string) string {
	if g == "" {
		return "Other"
	}
	r := []rune(g)
	r[0] = unicode.ToUpper(r[0])
	return string(r)
}
