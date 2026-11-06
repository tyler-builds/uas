package gui

import (
	"context"
	"errors"
	"fmt"
	"os"
	"time"

	"github.com/tyler-builds/jamb/dialog"
	"github.com/tyler-builds/jamb/icons"
	"github.com/tyler-builds/jamb/ui"

	"github.com/tyler-builds/uas/internal/client/backend"
	"github.com/tyler-builds/uas/internal/client/session"
)

// restorePlan is a restore waiting for the user's OK.
type restorePlan struct {
	file  string
	ex    session.Export
	items []session.RestoreItem
}

func (p *restorePlan) todo() []session.RestoreItem {
	var out []session.RestoreItem
	for _, it := range p.items {
		if it.Action == session.RestoreInstall {
			out = append(out, it)
		}
	}
	return out
}

// backupMenu saves the list of installed apps to a file, or installs the apps of such a file.
func (a *App) backupMenu(ctx *ui.Context) ui.Element {
	_, busy := a.running("restore")
	trigger := ui.Button("Back up").IconEnd(icons.ChevronDown).Secondary().Small().
		Loading(busy).Tooltip("Save the list of installed apps, or install the apps of a saved list")
	return ui.DropdownMenu(trigger, func() []ui.MenuItem {
		return []ui.MenuItem{
			{Label: "Export installed apps…", Icon: icons.FileDown, OnClick: func() { a.exportApps(ctx) }},
			{Label: "Restore from a file…", Icon: icons.FileUp, OnClick: func() { a.pickRestore(ctx) }},
		}
	}).Key("backup")
}

// exportApps asks where to save the list of installed apps, and saves it.
func (a *App) exportApps(ctx *ui.Context) {
	name := "uas-apps-" + time.Now().Format("2006-01-02") + ".json"
	ctx.Window().SaveFile(dialog.FileOptions{Title: "Export installed apps", Name: name, Extension: "json",
		Filters: []dialog.Filter{{Name: "uas exports", Patterns: []string{"*.json"}}}},
		func(r dialog.FileResult) {
			if r.Err != nil {
				ctx.Toast(ui.ToastError, errText(r.Err)).Title("Could not open the file picker")
				return
			}
			if r.Canceled || len(r.Paths) == 0 {
				return
			}
			path := r.Paths[0]
			go func() {
				ex, err := a.s.Export(context.Background())
				if err == nil {
					err = writeExport(path, ex)
				}
				ctx.Post(func() {
					if err != nil {
						ctx.Toast(ui.ToastError, errText(err)).Title("Could not export")
						return
					}
					ctx.Toast(ui.ToastSuccess, "Restore them on another computer with Back up → Restore.").
						Title(fmt.Sprintf("Exported %s", plural(len(ex.Apps), "app", "apps")))
				})
			}()
		})
}

func writeExport(path string, ex session.Export) error {
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	if err := session.WriteExport(f, ex); err != nil {
		f.Close()
		return err
	}
	return f.Close()
}

// pickRestore asks for an export file, and shows what restoring it would install.
func (a *App) pickRestore(ctx *ui.Context) {
	ctx.Window().OpenFiles(dialog.FileOptions{Title: "Restore apps from a file",
		Filters: []dialog.Filter{{Name: "uas exports", Patterns: []string{"*.json"}},
			{Name: "All files", Patterns: []string{"*"}}}},
		func(r dialog.FileResult) {
			if r.Err != nil {
				ctx.Toast(ui.ToastError, errText(r.Err)).Title("Could not open the file picker")
				return
			}
			if r.Canceled || len(r.Paths) == 0 {
				return
			}
			path := r.Paths[0]
			go func() {
				p, err := a.planRestore(context.Background(), path)
				ctx.Post(func() {
					if err != nil {
						ctx.Toast(ui.ToastError, errText(err)).Title("Could not read " + path)
						return
					}
					a.restore.Set(p)
				})
			}()
		})
}

func (a *App) planRestore(ctx context.Context, path string) (*restorePlan, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	ex, err := session.ReadExport(f)
	f.Close()
	if err != nil {
		return nil, err
	}
	items, err := a.s.PlanRestore(ctx, ex)
	if err != nil {
		return nil, err
	}
	return &restorePlan{file: path, ex: ex, items: items}, nil
}

// restoreDialog lists what restoring an export does with each app, and installs the missing ones.
func (a *App) restoreDialog(ctx *ui.Context) ui.Element {
	p := a.restore.Get()
	if p == nil {
		return ui.Div().Display(false)
	}
	t := ctx.Theme()
	todo := p.todo()
	list := ui.Div().Column().Gap(space1).MaxHeight(ui.Px(320)).ScrollY()
	counts := map[session.RestoreAction]int{}
	for _, it := range p.items {
		counts[it.Action]++
		var mark *icons.Icon
		color := t.TextMuted
		text := it.App.Name
		switch it.Action {
		case session.RestoreInstall:
			mark, color = icons.Download, brandColor(t)
			if it.Note == "" { // a note names the source it switches to
				text += " · " + it.Listing.Source.Name()
			}
		case session.RestoreInstalled, session.RestoreSkip:
			mark = icons.Check
		default:
			mark, color = icons.TriangleAlert, t.TextMuted
		}
		if it.Note != "" {
			text += " (" + it.Note + ")"
		}
		list = list.Child(infoRow(ctx, mark, color, text))
	}
	summary := fmt.Sprintf("%s to install, %d already here.", plural(len(todo), "app", "apps"),
		counts[session.RestoreInstalled]+counts[session.RestoreSkip])
	if n := counts[session.RestoreUnavailable] + counts[session.RestoreManual]; n > 0 {
		summary += fmt.Sprintf(" %d can't be installed here.", n)
	}
	if len(p.ex.FlatpakOverrides) > 0 {
		summary += " Your Flatpak permission changes are restored too."
	}
	title := "Restore apps"
	if p.ex.System != "" {
		title = "Restore apps from " + p.ex.System
	}
	d := ui.AlertDialog(title, summary).Body(list).Width(ui.Px(520)).
		Cancel("", func() { a.restore.Set(nil) })
	if len(todo) > 0 || len(p.ex.FlatpakOverrides) > 0 {
		label := "Restore"
		if len(todo) > 0 {
			label = "Install " + plural(len(todo), "app", "apps")
		}
		d = d.Confirm(label, func() {
			a.restore.Set(nil)
			a.runRestore(ctx, p)
		})
	}
	return d
}

// runRestore installs the apps of a plan one after another, then restores permission changes.
func (a *App) runRestore(ctx *ui.Context, p *restorePlan) {
	todo := p.todo()
	restart := false
	for _, it := range todo {
		restart = restart || a.s.NeedsRestart(it.Listing.Source)
	}
	a.startJob(ctx, "restore", "Restoring "+plural(len(todo), "app", "apps"), restart, "Apps restored",
		func(c context.Context, prog func(backend.Progress)) error {
			var errs []error
			for i, it := range todo {
				prog(backend.Progress{Message: fmt.Sprintf("Installing %s (%d of %d)", it.App.Name, i+1, len(todo))})
				if err := a.s.Install(c, it.Listing, prog); err != nil {
					errs = append(errs, fmt.Errorf("%s: %w", it.App.Name, err))
				}
			}
			if _, err := session.RestoreOverrides(p.ex); err != nil {
				errs = append(errs, err)
			}
			return errors.Join(errs...)
		})
}
