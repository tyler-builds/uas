package gui

import (
	"context"
	"time"

	"github.com/tyler-builds/jamb/icons"
	"github.com/tyler-builds/jamb/ui"

	"github.com/tyler-builds/uas/internal/client/backend"
)

// permissionIcons give each Flatpak permission a picture.
var permissionIcons = map[string]*icons.Icon{
	"network":   icons.Globe,
	"host":      icons.HardDrive,
	"home":      icons.House,
	"downloads": icons.Download,
	"devices":   icons.Usb,
	"audio":     icons.Volume2,
	"bluetooth": icons.Bluetooth,
	"x11":       icons.AppWindow,
}

// permissionsPanel lists what an installed Flatpak app may reach outside its sandbox, with a switch for each.
// Changes are the user's own overrides, as Flatseal makes; Reset removes them.
func (a *App) permissionsPanel(ctx *ui.Context, appID string) ui.Element {
	return ui.Component(func(ctx *ui.Context) ui.Element {
		t := ctx.Theme()
		key := "perms:" + appID
		res := ui.Query(ctx, key, func(c context.Context) ([]backend.Permission, error) {
			return a.s.Permissions(c, appID)
		}, ui.QueryStaleTime(10*time.Second))
		busy := ui.Local(ctx, "busy", false)
		refetch := func() { ui.InvalidateQueries(ctx, func(k string) bool { return k == key }) }

		// run changes permissions off the UI thread, then reads them again.
		run := func(title string, fn func(context.Context) error) {
			busy.Set(true)
			go func() {
				err := fn(context.Background())
				ctx.Post(func() {
					busy.Set(false)
					if err != nil {
						ctx.Toast(ui.ToastError, errText(err)).Title(title)
					}
					refetch()
				})
			}()
		}

		changed := false
		var rows []ui.Element
		switch {
		case res.Loading:
			rows = append(rows, ui.Div().Padding(space4).Child(ui.Skeleton().Height(ui.Px(120))))
		case res.Err != nil:
			rows = append(rows, settingsRow(ctx, ui.Icon(icons.TriangleAlert).Size(18).Color(t.TextMuted),
				"Could not read the app's permissions", errText(res.Err), nil))
		}
		for _, p := range res.Data {
			changed = changed || p.Changed
			on := ui.Local(ctx, "perm:"+p.Key, p.Allowed)
			if !busy.Peek() && on.Peek() != p.Allowed {
				on.Set(p.Allowed) // follow what flatpak says, after a change or a reset
			}
			sub := p.Detail
			if p.Note != "" {
				sub = p.Note
			}
			if p.Changed {
				sub += " · changed by you"
			}
			// Access that comes with a broader one (Downloads with the home folder) can't be turned off alone.
			included := p.Note != "" && p.Allowed
			sw := ui.Switch(p.Label, on).Disabled(busy.Get() || included).OnChange(func(v bool) {
				run("Could not change "+p.Label, func(c context.Context) error {
					return a.s.SetPermission(c, appID, p.Key, v)
				})
			})
			if included {
				sw = sw.Tooltip(p.Note + "; turn that off to change this")
			}
			rows = append(rows, settingsRow(ctx, ui.Icon(permissionIcons[p.Key]).Size(18).Color(t.TextMuted),
				p.Label, sub, sw))
		}
		reset := ui.Button("Reset").Icon(icons.RotateCcw).Small().Secondary().Disabled(!changed || busy.Get()).
			Tooltip("Go back to the permissions the app asks for").
			OnClick(func() {
				run("Could not reset permissions", func(c context.Context) error {
					return a.s.ResetPermissions(c, appID)
				})
			})
		return section(sectionHeader(ctx, "Permissions",
			"What this app may reach outside its sandbox. Changes apply the next time it starts.", reset),
			settingsList(ctx, rows...))
	}).Key("perms:" + appID)
}
