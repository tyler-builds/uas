package gui

import (
	"context"
	"errors"
	"fmt"
	"os"
	"slices"
	"strings"
	"time"

	"github.com/tyler-builds/jamb/access"
	"github.com/tyler-builds/jamb/icons"
	"github.com/tyler-builds/jamb/ui"

	"github.com/tyler-builds/uas/internal/client/appearance"
	"github.com/tyler-builds/uas/internal/client/backend"
)

// Presets: looks saved under a name, on the Appearance page's Presets tab, each with a page of its own to
// change it (pageLookPreset). See appearance.Preset.

// presetsKey is the query of the presets and the look Undo goes back to.
const presetsKey = "presets"

// presetsData is what the Presets tab shows.
type presetsData struct {
	list []appearance.Preset
	undo *appearance.Undo
	// bad says which files couldn't be read, if any.
	bad error
}

// find returns the preset of a name, whatever its case.
func (d presetsData) find(name string) (appearance.Preset, bool) {
	for _, p := range d.list {
		if strings.EqualFold(p.Name, name) {
			return p, true
		}
	}
	return appearance.Preset{}, false
}

func (a *App) presets(ctx *ui.Context) ui.QueryResult[presetsData] {
	return ui.Query(ctx, presetsKey, func(c context.Context) (presetsData, error) {
		if a.s.Presets == nil {
			return presetsData{}, errors.New("uas couldn't find your home folder")
		}
		var d presetsData
		d.list, d.bad = a.s.Presets.List()
		if u, ok, _ := a.s.Presets.Undo(); ok {
			d.undo = &u
		}
		return d, nil
	}, ui.QueryStaleTime(5*time.Second))
}

func refreshPresets(ctx *ui.Context) {
	ui.InvalidateQueries(ctx, func(key string) bool { return key == presetsKey || key == looksKey })
}

// lookTabNow is the Appearance page's tab: the one picked, or until one is, Presets when there are any and
// Fonts when there are none.
func (a *App) lookTabNow(ctx *ui.Context) string {
	if t := a.lookTab.Get(); t != "" {
		return t
	}
	if a.s.Presets != nil {
		if res := a.presets(ctx); !res.Loading && len(res.Data.list) > 0 {
			return lookPresets
		}
	}
	return lookFonts
}

// canApply reports whether this desktop's settings can be changed from uas, so presets can be applied.
func (a *App) canApply() bool { return a.s.Looks != nil && a.s.Looks.Desktop != nil }

// presetHeaderButtons are Save current look and New preset, at the top of the Presets tab.
func (a *App) presetHeaderButtons(ctx *ui.Context) ui.Element {
	if !a.canApply() {
		return nil
	}
	return ui.Div().Row().Gap(space2).
		Child(ui.Button("New preset").Icon(icons.Plus).Secondary().OnClick(func() { a.openNaming("", "") })).
		Child(ui.Button("Save current look").Icon(icons.Save).Primary().OnClick(func() { a.openSave() }))
}

// presetsTab is the Presets tab: the presets as cards, with Apply and their menu.
func (a *App) presetsTab(ctx *ui.Context, q string) []ui.Element {
	if !a.canApply() {
		return []ui.Element{ui.Empty("Presets need GNOME or KDE Plasma").Icon(icons.SwatchBook).
			Description("uas can't change this desktop's settings, so it can't apply a look. Fonts and themes " +
				"still install; choose them in the desktop's own settings.")}
	}
	res := a.presets(ctx)
	lk := a.looks(ctx)
	switch {
	case res.Loading:
		return []ui.Element{ui.Skeleton().Height(ui.Px(220))}
	case res.Err != nil:
		return []ui.Element{ui.Empty("Could not read your presets").Icon(icons.TriangleAlert).
			Description(errText(res.Err))}
	}
	d := res.Data
	var out []ui.Element
	if el := a.appliedAlert(ctx); el != nil {
		out = append(out, el)
	}
	if d.bad != nil {
		out = append(out, ui.Alert("Some presets couldn't be read").Warning().Description(errText(d.bad)))
	}
	if len(d.list) == 0 {
		return append(out, ui.Empty("No presets yet").Icon(icons.SwatchBook).
			Description("Save how your desktop looks now under a name, and switch back to it in one step.").
			Action(ui.Button("Save current look").Icon(icons.Save).Primary().OnClick(func() { a.openSave() })))
	}
	words := strings.Fields(strings.ToLower(q))
	cols := gridColumns(ctx, listMaxWidth, 250, 5)
	grid := ui.Div().Grid(repeatFr(cols)...).Gap(space3)
	n := 0
	for _, p := range d.list {
		if !matchWords(words, p.Name) {
			continue
		}
		n++
		grid = grid.Child(a.presetCard(ctx, p, lk.Data))
	}
	if n == 0 {
		return append(out, ui.Empty("No preset matches").Icon(icons.ListFilter).
			Description("No preset's name has “"+q+"”."))
	}
	what := "Applying one sets each part it has, and leaves the rest as it is."
	return append(out, muted(ctx, what), grid)
}

func matchWords(words []string, s string) bool {
	s = strings.ToLower(s)
	for _, w := range words {
		if !strings.Contains(s, w) {
			return false
		}
	}
	return true
}

// accentColors are GNOME's accent colors, as libadwaita draws them.
var accentColors = map[string]string{"blue": "#3584e4", "teal": "#2190a4", "green": "#3a944a",
	"yellow": "#c88800", "orange": "#ed5b00", "red": "#e62d42", "pink": "#d56199", "purple": "#9141ac",
	"slate": "#6f8396"}

func accentColor(name string) (ui.Color, bool) {
	h, ok := accentColors[name]
	if !ok {
		return ui.Color{}, false
	}
	c, err := ui.ParseHex(h)
	return c, err == nil
}

// presetCard is a preset in the grid: a preview of its look, its name and parts, and Apply.
func (a *App) presetCard(ctx *ui.Context, p appearance.Preset, lk looks) ui.Element {
	return ui.Component(func(ctx *ui.Context) ui.Element {
		t := ctx.Theme()
		id := "preset:" + strings.ToLower(p.Name)
		job, busy := a.running(id)
		inUse := appearance.InUse(p, lk.current)
		name := ui.Div().Row().Align(ui.Center).Gap(space2).MinWidth(ui.Px(0)).Height(ui.Px(24)).
			Child(ui.Text(p.Name).FontSize(14).Medium().Truncate().Grow(1).Basis(ui.Px(0)).MinWidth(ui.Px(0)))
		if inUse {
			name = name.Child(statusChip(ctx, "In use", t.Success))
		}
		open := ui.Div().Column().Gap(space2).Cursor(ui.CursorPointer).Role(access.RoleLink).
			Label(p.Name + ", change it").TabIndex(0).OnClick(func() { a.openPreset(p.Name) }).
			Child(a.presetPreview(ctx, p, 116)).
			Child(name).
			Child(ui.Text(presetSummary(p)).FontSize(12).TextColor(t.TextSubtle).Truncate())
		apply := ui.Button("Apply").Icon(icons.Check).Secondary().Small().Loading(busy).
			Disabled(len(p.Parts) == 0).OnClick(func() { a.applyPreset(ctx, p) })
		if inUse {
			apply = ui.Button("Apply again").Icon(icons.Check).Ghost().Small().Loading(busy).
				Tooltip("It is in use; applying it again sets anything that changed since").
				OnClick(func() { a.applyPreset(ctx, p) })
		}
		actions := ui.Div().Row().Align(ui.Center).Gap(space2).
			Child(apply).
			Child(ui.Div().Grow(1).MinWidth(ui.Px(0)).Child(ui.If(busy, caption(ctx, job.status).Truncate()))).
			Child(a.presetMenu(ctx, p))
		return ui.Card().Gap(space3).Padding(space3).MinWidth(ui.Px(0)).Child(open).Child(actions)
	}).Key("preset-card:" + p.Name)
}

// presetMenu is a preset's ⋯ menu: Change, Rename, Duplicate and Delete.
func (a *App) presetMenu(ctx *ui.Context, p appearance.Preset) ui.Element {
	return ui.DropdownMenu(ui.IconButton(icons.Ellipsis).Ghost().Small().Label("More for "+p.Name).
		Tooltip("More"), func() []ui.MenuItem {
		return []ui.MenuItem{
			{Label: "Change", Icon: icons.Pencil, OnClick: func() { a.openPreset(p.Name) }},
			{Label: "Rename", Icon: icons.Type, OnClick: func() { a.openNaming("rename", p.Name) }},
			{Label: "Duplicate", Icon: icons.Copy, OnClick: func() { a.duplicatePreset(ctx, p.Name) }},
			ui.MenuSeparator(),
			{Label: "Delete", Icon: icons.Trash, OnClick: func() { pp := p; a.presetDeleting.Set(&pp) }},
		}
	}).Key("preset-menu:" + p.Name)
}

// presetSummary lists a preset's parts as quiet words: the themes and fonts it sets, or what it leaves.
func presetSummary(p appearance.Preset) string {
	var words []string
	for _, n := range p.PartNames() {
		part := p.Parts[n]
		switch n {
		case appearance.PartScheme, appearance.PartAccent:
			continue // the preview shows them
		case appearance.PartInterface, appearance.PartDocument, appearance.PartMonospace:
			fam := appearance.FontFamily(part.Value)
			if !slices.Contains(words, fam) {
				words = append(words, fam)
			}
		default:
			if part.Value != "" {
				words = append(words, part.Value)
			} else {
				words = append(words, appearance.PartLabel(n))
			}
		}
	}
	if len(words) == 0 {
		if len(p.Parts) == 0 {
			return "No parts yet"
		}
		return "Style and accent color"
	}
	return strings.Join(words, " · ")
}

// presetPreview is a panel in the preset's style, with its icon theme's icons (or "Aa" in its interface
// font when it has no icon theme) and its style and accent color.
func (a *App) presetPreview(ctx *ui.Context, p appearance.Preset, height float32) ui.Element {
	t := ctx.Theme()
	bg, fg, muted := t.SurfaceHover, t.Text, t.TextMuted
	switch p.Parts[appearance.PartScheme].Value {
	case appearance.SchemeDark:
		bg, fg, muted = ui.RGB(0x1d, 0x1d, 0x20), ui.RGB(0xf4, 0xf4, 0xf5), ui.RGB(0xa1, 0xa1, 0xaa)
	case appearance.SchemeLight:
		bg, fg, muted = ui.RGB(0xf6, 0xf6, 0xf7), ui.RGB(0x18, 0x18, 0x1b), ui.RGB(0x63, 0x63, 0x6b)
	}
	var mark ui.Element = ui.Text("Aa").FontSize(30).Semibold().TextColor(fg)
	if theme := p.Parts[appearance.PartIcons].Value; theme != "" {
		mark = a.iconSamples(ctx, theme, 38, mark)
	}
	var words []string
	if s, ok := p.Parts[appearance.PartScheme]; ok {
		words = append(words, appearance.ValueLabel(appearance.KeyColorScheme, s.Value))
	}
	tag := ui.Div().Row().Align(ui.Center).Gap(6)
	if ac, ok := p.Parts[appearance.PartAccent]; ok {
		if c, ok := accentColor(ac.Value); ok {
			tag = tag.Child(ui.Div().Size(ui.Px(10), ui.Px(10)).Radius(5).Background(c))
		}
		words = append(words, appearance.ValueLabel(appearance.KeyAccentColor, ac.Value))
	}
	if len(words) > 0 {
		tag = tag.Child(ui.Text(strings.Join(words, " · ")).FontSize(12).TextColor(muted))
	}
	if wp, ok := a.presetWallpaper(p); ok {
		// The wallpaper fills the panel, with the style's colors on a card in front of it, as a window
		// on the desktop.
		front := ui.Div().Column().Align(ui.Center).Justify(ui.Center).Gap(space2).PaddingX(space4).
			PaddingY(space2 + 2).Radius(radiusM).Background(bg.Alpha(0.9)).
			Child(mark).Child(ui.If(len(words) > 0, tag))
		return ui.Div().Height(ui.Px(height)).Radius(radiusM).Border(1, t.Border).Background(bg).ClipContent().
			Child(ui.Div().Inset(0).Child(a.wallThumb(ctx, wp, ui.Full, height, 0))).
			Child(ui.Div().Inset(0).Center().Child(front))
	}
	return ui.Div().Column().Align(ui.Center).Justify(ui.Center).Gap(space3).Height(ui.Px(height)).
		Radius(radiusM).Background(bg).Border(1, t.Border).ClipContent().
		Child(mark).Child(ui.If(len(words) > 0, tag))
}

// iconSamples are a few icons of an installed icon theme, side by side, or fallback when it isn't
// installed.
func (a *App) iconSamples(ctx *ui.Context, theme string, size float32, fallback ui.Element) ui.Element {
	return ui.Component(func(ctx *ui.Context) ui.Element {
		res := ui.Query(ctx, "icon-samples:"+theme, func(c context.Context) ([]picture, error) {
			if a.s.Looks == nil {
				return nil, nil
			}
			themes, err := a.s.Looks.Themes()
			if err != nil {
				return nil, err
			}
			var out []picture
			for _, f := range appearance.IconFiles(themes, theme, appearance.SampleIcons) {
				b, err := os.ReadFile(f)
				if err != nil {
					continue
				}
				if pic, err := decodePicture(b); err == nil {
					out = append(out, pic)
				}
			}
			return out, nil
		}, ui.QueryStaleTime(time.Minute))
		if len(res.Data) == 0 {
			if res.Loading {
				return ui.Div().Height(ui.Px(size))
			}
			return fallback
		}
		row := ui.Div().Row().Align(ui.Center).Gap(space3)
		for _, pic := range res.Data {
			switch {
			case pic.svg != nil:
				row = row.Child(ui.SVG(pic.svg).Size(ui.Px(size), ui.Px(size)))
			case pic.img != nil:
				row = row.Child(ui.Image(pic.img).Size(ui.Px(size), ui.Px(size)).Fit(ui.FitContain))
			}
		}
		return row
	}).Key("icon-samples:" + theme)
}

// openPreset shows a preset's page.
func (a *App) openPreset(name string) { a.open(view{page: pageLookPreset, arg: name}) }

// presetPage is a preset's page, where each part is changed, or left as it is.
func (a *App) presetPage(ctx *ui.Context, name string) ui.Element {
	return ui.Component(func(ctx *ui.Context) ui.Element {
		t := ctx.Theme()
		res := a.presets(ctx)
		lk := a.looks(ctx)
		page := pageBody(detailMaxWidth).Gap(space6)
		switch {
		case res.Loading || lk.Loading:
			return scroll(page.Child(ui.Skeleton().Height(ui.Px(60))).Child(ui.Skeleton().Height(ui.Px(300))))
		case res.Err != nil:
			return scroll(page.Child(ui.Empty("Could not read your presets").Icon(icons.TriangleAlert).
				Description(errText(res.Err))))
		}
		p, ok := res.Data.find(name)
		if !ok {
			return scroll(page.Child(ui.Empty("This preset is gone").Icon(icons.SwatchBook).
				Description("It was renamed or deleted.")))
		}
		id := "preset:" + strings.ToLower(p.Name)
		_, busy := a.running(id)
		inUse := appearance.InUse(p, lk.Data.current)
		trailing := ui.Div().Row().Align(ui.Center).Gap(space2)
		if inUse {
			trailing = trailing.Child(statusChip(ctx, "In use", t.Success))
		}
		if a.canApply() {
			trailing = trailing.Child(ui.Button("Apply").Icon(icons.Check).Primary().Loading(busy).
				Disabled(len(p.Parts) == 0).OnClick(func() { a.applyPreset(ctx, p) }))
		}
		trailing = trailing.Child(a.presetMenu(ctx, p))
		sub := "Each part is set when you apply the preset. A part left as it is keeps whatever the desktop has then."
		page = page.Child(pageHeader(ctx, p.Name, sub, trailing))
		if el := a.appliedAlert(ctx); el != nil {
			page = page.Child(el)
		}
		desk := a.s.Looks.Desktop
		shown := func(part string) bool {
			key, _ := appearance.PartKey(part)
			return (desk != nil && desk.Supports(key)) || p.Sets(part)
		}
		var look, fonts []ui.Element
		for _, part := range []string{appearance.PartScheme, appearance.PartAccent, appearance.PartIcons,
			appearance.PartCursor} {
			if shown(part) {
				look = append(look, a.presetPartRow(ctx, p, part, lk.Data))
			}
		}
		if shown(appearance.PartWallpaper) {
			look = append(look, a.presetWallpaperRow(ctx, p))
		}
		for _, part := range []string{appearance.PartInterface, appearance.PartDocument, appearance.PartMonospace} {
			if shown(part) {
				fonts = append(fonts, a.presetPartRow(ctx, p, part, lk.Data))
			}
		}
		if len(look) > 0 {
			page = page.Child(section(sectionHeader(ctx, "Look", ""), settingsList(ctx, look...)))
		}
		if len(fonts) > 0 {
			page = page.Child(section(sectionHeader(ctx, "Fonts",
				"A font with a size is set at that size; a font without one keeps the size the desktop has."),
				settingsList(ctx, fonts...)))
		}
		var later []string
		for _, n := range p.PartNames() {
			if _, ok := appearance.PartKey(n); !ok {
				later = append(later, appearance.PartLabel(n))
			}
		}
		if len(later) > 0 {
			page = page.Child(caption(ctx, "It also has parts that a newer version of uas sets: "+
				strings.Join(later, ", ")+". They are kept as they are."))
		}
		return scroll(page)
	}).Key("preset-page:" + name)
}

// presetPartRow is one part of a preset on its page, with the control that changes it.
func (a *App) presetPartRow(ctx *ui.Context, p appearance.Preset, part string, lk looks) ui.Element {
	t := ctx.Theme()
	key, _ := appearance.PartKey(part)
	cur := p.Parts[part]
	desk := a.s.Looks.Desktop
	set := func(v string) { a.setPresetPart(ctx, p.Name, part, v) }
	var desc string
	var control ui.Element
	switch part {
	case appearance.PartScheme:
		desc = "Light or dark, for apps that follow the desktop's style"
		chip := func(label, v string) ui.Element {
			return filterChip(ctx, label, cur.Value == v && (v != "" || !p.Sets(part)), func() { set(v) })
		}
		control = segmented(ctx, chip("Leave as is", ""), chip("Default", appearance.SchemeDefault),
			chip("Light", appearance.SchemeLight), chip("Dark", appearance.SchemeDark))
	case appearance.PartAccent:
		desc = "The color of buttons, links and selections in apps that follow it"
		row := ui.Div().Row().Align(ui.Center).Gap(space2).
			Child(filterChip(ctx, "Leave as is", !p.Sets(part), func() { set("") }))
		for _, name := range appearance.AccentColors {
			c, _ := accentColor(name)
			sw := ui.Div().Size(ui.Px(22), ui.Px(22)).Radius(11).Background(c).Cursor(ui.CursorPointer).
				Role(access.RoleButton).Label(appearance.ValueLabel(key, name)).TabIndex(0).
				Tooltip(appearance.ValueLabel(key, name)).AccessSelected(cur.Value == name).
				OnClick(func() { set(name) })
			if cur.Value == name {
				sw = ui.Div().Size(ui.Px(28), ui.Px(28)).Radius(14).Border(2, t.Text).Center().Child(sw)
			} else {
				sw = ui.Div().Size(ui.Px(28), ui.Px(28)).Center().Child(sw)
			}
			row = row.Child(sw)
		}
		control = row
	case appearance.PartIcons, appearance.PartCursor:
		desc = "The icons apps and the desktop draw"
		if part == appearance.PartCursor {
			desc = "The mouse pointer"
		}
		opts := []ui.Option[string]{{Value: "", Label: "Leave as is"}}
		have := false
		for _, th := range lk.themes {
			if (part == appearance.PartIcons && th.Icons) || (part == appearance.PartCursor && th.Cursors) {
				label := th.Name
				if th.Title != "" && th.Title != th.Name {
					label += " · " + th.Title
				}
				opts = append(opts, ui.Option[string]{Value: th.Name, Label: label})
				have = have || th.Name == cur.Value
			}
		}
		if cur.Value != "" && !have {
			label := cur.Value + " (not on this system)"
			if cur.From != nil {
				label = cur.Value + " (installed when applied)"
			}
			opts = append(opts, ui.Option[string]{Value: cur.Value, Label: label})
		}
		sig := ui.Local(ctx, "part:"+p.Name+":"+part, cur.Value)
		if sig.Peek() != cur.Value {
			sig.Set(cur.Value)
		}
		control = ui.Select(sig, opts...).Small().Width(ui.Px(320)).Label(appearance.PartLabel(part)).
			Key("part-select:" + part).OnChange(set)
	default: // a font
		desc = map[string]string{appearance.PartInterface: "Menus, buttons and labels",
			appearance.PartDocument: "The text of documents", appearance.PartMonospace: "Terminals and code"}[part]
		control = a.presetFontControl(ctx, p, part, cur)
	}
	left := ui.Div().Column().Gap(3).Grow(1).Basis(ui.Px(0)).MinWidth(ui.Px(0)).
		Child(ui.Text(appearance.PartLabel(part)).FontSize(14).Medium()).
		Child(ui.Text(desc).FontSize(12).TextColor(t.TextSubtle).Truncate())
	if desk == nil || !desk.Supports(key) {
		left = left.Child(caption(ctx, "This desktop can't set it from uas; it is kept in the preset.").
			TextColor(t.Warning))
	}
	return ui.Div().Row().Align(ui.Center).Gap(space4).MinHeight(ui.Px(64)).PaddingX(space4).PaddingY(space2).
		Child(left).Child(ui.Div().Shrink(0).Child(control))
}

// fontSizes are the sizes a preset's font may be set at.
var fontSizes = []string{"9", "10", "10.5", "11", "12", "13", "14", "16", "18"}

// presetFontControl picks a font family, from those on the system, and its size.
func (a *App) presetFontControl(ctx *ui.Context, p appearance.Preset, part string, cur appearance.Part) ui.Element {
	return ui.Component(func(ctx *ui.Context) ui.Element {
		fams := ui.Query(ctx, "font-families", func(c context.Context) ([]string, error) {
			if a.s.Looks == nil {
				return nil, nil
			}
			return a.s.Looks.Families(c), nil
		}, ui.QueryStaleTime(time.Minute))
		family, size := appearance.FontFamily(cur.Value), appearance.FontSize(cur.Value)
		if cur.Value == "" {
			family = ""
		}
		opts := []ui.Option[string]{{Value: "", Label: "Leave as is"}}
		have := false
		for _, f := range fams.Data {
			opts = append(opts, ui.Option[string]{Value: f, Label: f})
			have = have || strings.EqualFold(f, family)
		}
		if family != "" && !have {
			label := family + " (not on this system)"
			if cur.From != nil {
				label = family + " (installed when applied)"
			}
			opts = append(opts, ui.Option[string]{Value: family, Label: label})
		}
		famSig := ui.Local(ctx, "family", family)
		if famSig.Peek() != family {
			famSig.Set(family)
		}
		sizeOpts := []ui.Option[string]{{Value: "", Label: "Keep size"}}
		for _, s := range fontSizes {
			sizeOpts = append(sizeOpts, ui.Option[string]{Value: s, Label: s})
		}
		if size != "" && !slices.Contains(fontSizes, size) {
			sizeOpts = append(sizeOpts, ui.Option[string]{Value: size, Label: size})
		}
		sizeSig := ui.Local(ctx, "size", size)
		if sizeSig.Peek() != size {
			sizeSig.Set(size)
		}
		value := func(f, s string) string {
			if f == "" || s == "" {
				return f
			}
			return f + " " + s
		}
		row := ui.Div().Row().Align(ui.Center).Gap(space2).
			Child(ui.Select(famSig, opts...).Small().Width(ui.Px(300)).Label(appearance.PartLabel(part)).
				Key("family").OnChange(func(f string) { a.setPresetPart(ctx, p.Name, part, value(f, size)) }))
		if family != "" {
			row = row.Child(ui.Select(sizeSig, sizeOpts...).Small().Width(ui.Px(110)).
				Label(appearance.PartLabel(part) + " size").Key("size").
				OnChange(func(s string) { a.setPresetPart(ctx, p.Name, part, value(family, s)) }))
		}
		return row
	}).Key("font-control:" + p.Name + ":" + part)
}

// setPresetPart changes one part of a preset, or with an empty value leaves it out. A theme or font uas
// installed keeps where it came from, so the preset can install it elsewhere.
func (a *App) setPresetPart(ctx *ui.Context, name, part, value string) {
	if a.s.Presets == nil {
		return
	}
	var from *appearance.Origin
	if value != "" && a.s.Looks != nil {
		key, _ := appearance.PartKey(part)
		if recs, err := a.s.Looks.Records(); err == nil {
			from = appearance.OriginOf(recs, key, value)
		}
	}
	_, err := a.s.Presets.Update(name, func(p *appearance.Preset) error {
		if value == "" {
			delete(p.Parts, part)
			return nil
		}
		old := p.Parts[part]
		if from == nil && old.From != nil && appearance.FontFamily(old.Value) == appearance.FontFamily(value) {
			from = old.From // the same font or theme, at another size
		}
		p.Parts[part] = appearance.Part{Value: value, From: from}
		return nil
	})
	if err != nil {
		ctx.Toast(ui.ToastError, errText(err)).Title("Could not change " + name)
	}
	refreshPresets(ctx)
}

// openSave opens the Save current look dialog, which reads the desktop's settings afresh each time.
func (a *App) openSave() {
	a.presetName.Set("")
	a.presetErr.Set("")
	a.presetReplace.Set(false)
	n := a.presetSaving.Peek()
	if n < 0 {
		n = -n
	}
	a.presetSaving.Set(n + 1)
}

// openNaming opens the New preset dialog (mode "") or the Rename dialog of a preset.
func (a *App) openNaming(mode, from string) {
	a.presetName.Set(from)
	a.presetErr.Set("")
	a.presetNaming.Set(&presetNaming{mode: mode, from: from})
}

// presetNaming is the New preset or Rename dialog.
type presetNaming struct {
	mode string // "" for New preset, "rename"
	from string // the preset renamed
}

// presetApply is an apply waiting for the user's OK, with its plan.
type presetApply struct {
	p    appearance.Preset
	plan []appearance.PlanItem
}

// presetApplied is what the last apply did, for the note with Undo.
type presetApplied struct {
	name    string
	skipped []string
	failed  error
}

// presetSaveDialog captures the current look and saves it under a name, with the parts the user keeps.
func (a *App) presetSaveDialog(ctx *ui.Context) ui.Element {
	n := a.presetSaving.Get()
	if n <= 0 {
		return ui.Div().Display(false)
	}
	return ui.Component(func(ctx *ui.Context) ui.Element {
		t := ctx.Theme()
		capture := ui.Query(ctx, fmt.Sprint("preset-capture:", n), func(c context.Context) (map[string]appearance.Part, error) {
			return a.s.Looks.Capture(c)
		}, ui.QueryCacheTime(0))
		close := func() { a.presetSaving.Set(-n) }
		body := ui.Div().Column().Gap(space4).
			Child(ui.TextInput(a.presetName).Label("Name").Placeholder("Evening").AutoFocus().Width(ui.Full).
				OnChange(func(string) { a.presetErr.Set(""); a.presetReplace.Set(false) }))
		if e := a.presetErr.Get(); e != "" {
			body = body.Child(caption(ctx, e).TextColor(t.Danger))
		}
		var keep map[string]*ui.Signal[bool]
		var names []string
		switch {
		case capture.Loading:
			body = body.Child(ui.Skeleton().Height(ui.Px(180)))
		case capture.Err != nil:
			body = body.Child(ui.Alert("Could not read the desktop's settings").Danger().
				Description(errText(capture.Err)))
		default:
			keep = map[string]*ui.Signal[bool]{}
			list := ui.Div().Column().Gap(space2)
			var system []string
			for _, n := range appearance.PartOrder {
				part, ok := capture.Data[n]
				if !ok {
					continue
				}
				names = append(names, n)
				key, _ := appearance.PartKey(n)
				keep[n] = ui.Local(ctx, "keep:"+n, true)
				if n == appearance.PartWallpaper {
					list = list.Child(a.savedWallpaperRow(ctx, part, keep[n]))
					continue
				}
				label := appearance.PartLabel(n) + ": " + appearance.ValueLabel(key, part.Value)
				row := ui.Div().Row().Align(ui.Center).Gap(space2).
					Child(ui.Checkbox(label, keep[n]).Key("keep:" + n))
				if c, ok := accentColor(part.Value); ok && n == appearance.PartAccent {
					row = row.Child(ui.Div().Size(ui.Px(10), ui.Px(10)).Radius(5).Background(c))
				}
				list = list.Child(row)
				if part.From == nil && key != appearance.KeyColorScheme && key != appearance.KeyAccentColor {
					v := appearance.FontFamily(part.Value)
					if !slices.Contains(system, v) {
						system = append(system, v)
					}
				}
			}
			body = body.Child(ui.Div().Column().Gap(space2).
				Child(ui.Text("Parts to save").FontSize(13).Medium()).Child(list))
			if len(system) > 0 {
				body = body.Child(caption(ctx, "uas didn't install "+joinOr(system)+", so it can't install "+
					"them elsewhere: on a computer that doesn't have them, those parts are skipped."))
			}
		}
		label := "Save"
		if a.presetReplace.Get() {
			label = "Replace"
		}
		save := ui.Button(label).Primary().Disabled(capture.Loading || capture.Err != nil).OnClick(func() {
			parts := map[string]appearance.Part{}
			for _, n := range names {
				if keep[n].Peek() {
					parts[n] = capture.Data[n]
				}
			}
			p, err := a.s.Presets.Save(appearance.Preset{Name: a.presetName.Peek(), Parts: parts},
				a.presetReplace.Peek())
			var exists *appearance.ExistsError
			switch {
			case errors.As(err, &exists):
				a.presetErr.Set("A preset called “" + exists.Name + "” exists. Replace it, or choose another name.")
				a.presetReplace.Set(true)
				return
			case err != nil:
				a.presetErr.Set(errText(err))
				return
			}
			close()
			a.lookTab.Set(lookPresets)
			refreshPresets(ctx)
			ctx.Toast(ui.ToastSuccess, "Apply it from Appearance to come back to this look.").Title("Saved " + p.Name)
		})
		return ui.Modal("Save current look", body).Width(ui.Px(480)).OnClose(close).
			Footer(ui.Button("Cancel").Ghost().OnClick(close), save).Default(save)
	}).Key(fmt.Sprint("preset-save:", n))
}

func joinOr(words []string) string {
	if len(words) < 2 {
		return joinAnd(words)
	}
	return strings.Join(words[:len(words)-1], ", ") + " or " + words[len(words)-1]
}

func joinAnd(words []string) string {
	switch len(words) {
	case 0:
		return ""
	case 1:
		return words[0]
	}
	return strings.Join(words[:len(words)-1], ", ") + " and " + words[len(words)-1]
}

// presetNamingDialog names a new preset, or renames one.
func (a *App) presetNamingDialog(ctx *ui.Context) ui.Element {
	n := a.presetNaming.Get()
	if n == nil {
		return ui.Div().Display(false)
	}
	t := ctx.Theme()
	close := func() { a.presetNaming.Set(nil) }
	title, label, msg := "New preset", "Create", "Name it, then choose its parts. Parts you leave as they are "+
		"keep whatever the desktop has when you apply it."
	if n.mode == "rename" {
		title, label, msg = "Rename "+n.from, "Rename", ""
	}
	body := ui.Div().Column().Gap(space3)
	if msg != "" {
		body = body.Child(muted(ctx, msg))
	}
	body = body.Child(ui.TextInput(a.presetName).Label("Name").Placeholder("Evening").AutoFocus().Width(ui.Full).
		OnChange(func(string) { a.presetErr.Set("") }))
	if e := a.presetErr.Get(); e != "" {
		body = body.Child(caption(ctx, e).TextColor(t.Danger))
	}
	ok := ui.Button(label).Primary().OnClick(func() {
		var p appearance.Preset
		var err error
		if n.mode == "rename" {
			p, err = a.s.Presets.Rename(n.from, a.presetName.Peek())
		} else {
			p, err = a.s.Presets.Save(appearance.Preset{Name: a.presetName.Peek()}, false)
		}
		var exists *appearance.ExistsError
		switch {
		case errors.As(err, &exists):
			a.presetErr.Set("A preset called “" + exists.Name + "” exists. Choose another name.")
			return
		case err != nil:
			a.presetErr.Set(errText(err))
			return
		}
		close()
		refreshPresets(ctx)
		if n.mode == "rename" {
			a.renamedPreset(n.from, p.Name)
			return
		}
		a.lookTab.Set(lookPresets)
		a.openPreset(p.Name)
	})
	return ui.Modal(title, body).Width(ui.Px(440)).OnClose(close).
		Footer(ui.Button("Cancel").Ghost().OnClick(close), ok).Default(ok)
}

// renamedPreset points the views of a preset at its new name.
func (a *App) renamedPreset(from, to string) {
	h := slices.Clone(a.history.Peek())
	for i, v := range h {
		if v.page == pageLookPreset && strings.EqualFold(v.arg, from) {
			h[i].arg = to
		}
	}
	a.history.Set(h)
}

// duplicatePreset copies a preset as "<name> copy".
func (a *App) duplicatePreset(ctx *ui.Context, name string) {
	p, err := a.s.Presets.Copy(name, "")
	if err != nil {
		ctx.Toast(ui.ToastError, errText(err)).Title("Could not duplicate " + name)
		return
	}
	refreshPresets(ctx)
	ctx.Toast(ui.ToastSuccess, "Change it to make it your own.").Title("Copied as " + p.Name)
}

// applyPreset applies a preset: at once when everything it needs is here, else after the user's OK in the
// apply dialog, which says what is downloaded first and what is skipped.
func (a *App) applyPreset(ctx *ui.Context, p appearance.Preset) {
	if _, busy := a.running("preset:" + strings.ToLower(p.Name)); busy {
		return
	}
	go func() {
		plan, err := a.s.PlanPreset(context.Background(), p)
		ctx.Post(func() {
			if err != nil {
				ctx.Toast(ui.ToastError, errText(err)).Title("Could not apply " + p.Name)
				return
			}
			if len(appearance.Downloads(plan)) > 0 || slices.ContainsFunc(plan, func(it appearance.PlanItem) bool {
				return it.Step.Skipped()
			}) {
				a.presetApplying.Set(&presetApply{p: p, plan: plan})
				return
			}
			a.runPresetApply(ctx, p, plan)
		})
	}()
}

// runPresetApply applies a preset by its plan, as a job.
func (a *App) runPresetApply(ctx *ui.Context, p appearance.Preset, plan []appearance.PlanItem) {
	a.startJob(ctx, "preset:"+strings.ToLower(p.Name), "Applying "+p.Name, false, p.Name+" is applied",
		func(c context.Context, progress func(backend.Progress)) error {
			out, err := a.s.ApplyPreset(c, p, plan, false, progress)
			if err != nil {
				return err
			}
			note := &presetApplied{name: p.Name, failed: appearance.Failed(out)}
			for _, o := range out {
				if o.Step.Skipped() {
					note.skipped = append(note.skipped, appearance.PartLabel(o.Part))
				}
			}
			ctx.Post(func() { a.presetApplied.Set(note) })
			return note.failed
		})
}

// appliedAlert is the note after an apply, with Undo; nil when there is none.
func (a *App) appliedAlert(ctx *ui.Context) ui.Element {
	n := a.presetApplied.Get()
	if n == nil {
		return nil
	}
	_, busy := a.running("preset-undo")
	desc := "Undo goes back to the look you had before."
	al := ui.Alert("Applied " + n.name).Success()
	if len(n.skipped) > 0 {
		desc = "Skipped " + strings.ToLower(joinAnd(n.skipped)) + ", which can't be set here. " + desc
		al = ui.Alert("Applied " + n.name).Info()
	}
	if n.failed != nil {
		al = ui.Alert("Applied part of " + n.name).Warning()
		desc = errText(n.failed) + ". " + desc
	}
	undo := ui.Button("Undo").Icon(icons.Undo2).Secondary().Small().Loading(busy).OnClick(func() {
		name := n.name
		a.startJob(ctx, "preset-undo", "Undoing "+name, false, "Back to the look from before "+name,
			func(c context.Context, _ func(backend.Progress)) error {
				_, out, err := a.s.UndoPreset(c)
				if err == nil {
					err = appearance.Failed(out)
				}
				if err == nil {
					ctx.Post(func() { a.presetApplied.Set(nil) })
				}
				return err
			})
	})
	return al.Description(desc).Action(undo).OnClose(func() { a.presetApplied.Set(nil) })
}

// presetApplyDialog asks before an apply that downloads fonts or themes first, or skips parts.
func (a *App) presetApplyDialog(ctx *ui.Context) ui.Element {
	ap := a.presetApplying.Get()
	if ap == nil {
		return ui.Div().Display(false)
	}
	t := ctx.Theme()
	close := func() { a.presetApplying.Set(nil) }
	downloads := appearance.Downloads(ap.plan)
	body := ui.Div().Column().Gap(space4)
	item := func(icon *icons.Icon, title, note string) ui.Element {
		return ui.Div().Row().Align(ui.Start).Gap(space3).
			Child(ui.Div().PaddingTop(2).Child(ui.Icon(icon).Size(15).Color(t.TextMuted))).
			Child(ui.Div().Column().Gap(2).Grow(1).Basis(ui.Px(0)).MinWidth(ui.Px(0)).
				Child(ui.Text(title).FontSize(13).Medium()).
				Child(ui.Text(note).FontSize(12).TextColor(t.TextSubtle)))
	}
	if len(downloads) > 0 {
		list := ui.Div().Column().Gap(space3)
		for _, d := range downloads {
			var parts []string
			for _, it := range ap.plan {
				if it.Step == appearance.StepInstall && it.Listing.Ref == d.Listing.Ref && it.Listing.Source == d.Listing.Source {
					parts = append(parts, appearance.PartWord(it.Part))
				}
			}
			title := d.Listing.Name
			if d.Variant != "" && d.Variant != d.Listing.Name {
				title += " (" + d.Variant + ")"
			}
			list = list.Child(item(icons.Download, title, "For "+joinAnd(parts)+", "+d.Note))
		}
		body = body.Child(ui.Div().Column().Gap(space2).
			Child(ui.Text("Installed first").FontSize(13).Semibold()).Child(list))
	}
	var skipped []ui.Element
	for _, it := range ap.plan {
		if it.Step.Skipped() {
			v := appearance.ValueLabel(it.Key, it.Value)
			title := appearance.PartLabel(it.Part)
			if v != "" {
				title += ": " + v
			}
			skipped = append(skipped, item(icons.CircleSlash, title, upperFirst(it.Note)))
		}
	}
	if len(skipped) > 0 {
		body = body.Child(ui.Div().Column().Gap(space2).
			Child(ui.Text("Skipped here").FontSize(13).Semibold()).
			Child(ui.Div().Column().Gap(space3).Children(skipped...)))
	}
	msg := "Every part it has that is on this system is set; the rest of your look stays as it is."
	label := "Apply"
	if len(downloads) > 0 {
		msg = "uas installs what it needs for your account first, from the same sources as the Appearance " +
			"page, then sets each part. Undo goes back to the look you have now."
		label = "Download and apply"
	}
	icon := icons.SwatchBook
	if len(downloads) > 0 {
		icon = icons.Download
	}
	return ui.AlertDialog("Apply "+ap.p.Name+"?", msg).Icon(icon).Body(body).Width(ui.Px(500)).
		Confirm(label, func() {
			close()
			a.runPresetApply(ctx, ap.p, ap.plan)
		}).
		Cancel("", close)
}

func upperFirst(s string) string {
	if s == "" {
		return s
	}
	return strings.ToUpper(s[:1]) + s[1:]
}

// presetDeleteDialog asks before deleting a preset.
func (a *App) presetDeleteDialog(ctx *ui.Context) ui.Element {
	p := a.presetDeleting.Get()
	if p == nil {
		return ui.Div().Display(false)
	}
	close := func() { a.presetDeleting.Set(nil) }
	return ui.AlertDialog("Delete "+p.Name+"?", "The preset is removed. The fonts and themes it uses stay "+
		"installed, and the desktop keeps the look it has now.").Intent(ui.AlertDialogDestructive).
		Confirm("Delete", func() {
			close()
			if err := a.s.Presets.Delete(p.Name); err != nil {
				ctx.Toast(ui.ToastError, errText(err)).Title("Could not delete " + p.Name)
				return
			}
			if v := a.current(); v.page == pageLookPreset && strings.EqualFold(v.arg, p.Name) {
				a.back()
			}
			refreshPresets(ctx)
			ctx.Toast(ui.ToastSuccess, "The fonts and themes it used stay installed.").Title("Deleted " + p.Name)
		}).
		Cancel("", close)
}
