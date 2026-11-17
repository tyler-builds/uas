package gui

import (
	"context"
	"strings"

	"github.com/tyler-builds/jamb/icons"
	"github.com/tyler-builds/jamb/ui"

	"github.com/tyler-builds/uas/internal/client/appearance"
	"github.com/tyler-builds/uas/internal/client/backend"
)

// "Add to preset" and keeping a preset up to date. A font's or theme's page, and the rows of the system's
// themes, have an "Add to preset" menu of the user's presets: picking one sets that part of that preset and
// nothing else. Nothing is applied or installed; the part keeps where it can be installed from, so applying
// the preset installs it. The preset last applied (or saved from the current look) shows "Changed" once the
// look no longer matches it, with Update, which saves the current look into it; and after Use, the
// confirmation offers that Update too. uas never updates a preset by itself.

// presetItem is a font or theme to add to a preset: the part it sets, the part's value, and its name for
// people.
type presetItem struct {
	part  string
	value appearance.Part
	name  string
}

// addToPresetMenu is the "Add to preset" menu of an item: the presets, those that have it for its part
// checked, then New preset…. small makes its button a row's icon button.
func (a *App) addToPresetMenu(ctx *ui.Context, it presetItem, small bool) ui.Element {
	if a.s.Presets == nil || !a.canApply() || it.value.Value == "" && it.part != appearance.PartShell {
		return nil
	}
	return ui.Component(func(ctx *ui.Context) ui.Element {
		var trigger ui.Element = ui.Button("Add to preset").Icon(icons.ListPlus).IconEnd(icons.ChevronDown).
			Ghost().Width(ui.Full)
		if small {
			trigger = ui.IconButton(icons.ListPlus).Ghost().Small().Label("Add " + it.name + " to a preset").
				Tooltip("Add to preset")
		}
		return ui.DropdownMenu(trigger, func() []ui.MenuItem {
			// Read when the menu opens, so it lists the presets as they are now.
			list, _ := a.s.Presets.List()
			items := []ui.MenuItem{ui.MenuSection("As the " + appearance.PartWord(it.part) + ", add to")}
			for _, p := range list {
				name := p.Name
				items = append(items, ui.MenuItem{Label: name, Check: ui.MenuCheckBox,
					Checked: p.Has(it.part, it.value.Value), OnClick: func() { a.addToPreset(ctx, name, it) }})
			}
			if len(list) > 0 {
				items = append(items, ui.MenuSeparator())
			}
			return append(items, ui.MenuItem{Label: "New preset…", Icon: icons.Plus, OnClick: func() {
				a.presetName.Set("")
				a.presetErr.Set("")
				pending := it
				a.presetNaming.Set(&presetNaming{add: &pending})
			}})
		}).Key("add-to-preset:" + it.part + ":" + it.value.Value)
	}).Key("add-to-preset:" + it.part + ":" + it.name)
}

// addToPreset sets an item's part of a preset, and says so, with a way to open the preset.
func (a *App) addToPreset(ctx *ui.Context, name string, it presetItem) {
	p, err := a.s.Presets.AddPart(name, it.part, it.value)
	if err != nil {
		ctx.Toast(ui.ToastError, errText(err)).Title("Could not add " + it.name + " to " + name)
		return
	}
	a.addedToPreset(ctx, p, it)
}

// addedToPreset says that an item was added to a preset, with Open.
func (a *App) addedToPreset(ctx *ui.Context, p appearance.Preset, it presetItem) {
	refreshPresets(ctx)
	what := it.name + " is its " + appearance.PartWord(it.part) + "."
	if it.value.From != nil {
		what += " Applying the preset installs it where it is missing."
	}
	name := p.Name
	ctx.Toast(ui.ToastSuccess, what).Title("Added to "+name).Action("Open", func() { a.openPreset(name) })
}

// offerUpdate adds "Update <preset>" to a job's success toast when the look no longer matches the preset last
// applied. It runs off the UI thread, after the job's work.
func (a *App) offerUpdate(ctx *ui.Context, c context.Context, res *jobResult) {
	if a.s.Looks == nil {
		return
	}
	p, ok := a.s.Looks.ChangedPreset(c, a.s.Presets)
	if !ok {
		return
	}
	name := p.Name
	res.action, res.do = "Update "+name, func() { a.updatePreset(ctx, name) }
}

// updatePreset saves the current look into a preset (see appearance.Installer.UpdatePreset).
func (a *App) updatePreset(ctx *ui.Context, name string) {
	a.startJobWith(ctx, "preset:"+strings.ToLower(name), "Updating "+name, false, name+" has your current look",
		func(c context.Context, _ func(backend.Progress), res *jobResult) error {
			p, changed, err := a.s.Looks.UpdatePreset(c, a.s.Presets, name)
			if err != nil {
				return err
			}
			words := make([]string, len(changed))
			for i, part := range changed {
				words[i] = appearance.PartWord(part)
			}
			if len(words) > 0 {
				res.text = "Saved your " + joinAnd(words) + " into " + p.Name + "."
			}
			res.action, res.do = "Open", func() { a.openPreset(p.Name) }
			ctx.Post(func() { refreshPresets(ctx) })
			return nil
		})
}

// presetStatusChip is a preset's In use or Changed mark; nil for neither.
func presetStatusChip(ctx *ui.Context, st appearance.PresetStatus) ui.Element {
	t := ctx.Theme()
	switch st {
	case appearance.StatusInUse:
		return statusChip(ctx, "In use", t.Success)
	case appearance.StatusChanged:
		return ui.Div().Tooltip("Your look changed since you applied it").Child(statusChip(ctx, "Changed", t.Warning))
	}
	return nil
}
