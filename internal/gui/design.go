package gui

// The uas design system: the brand palette as jamb themes, a type and spacing scale, and the small set of
// components every page is built from. Pages should use these rather than raw sizes and colors, so the app
// stays consistent.

import (
	"github.com/tyler-builds/jamb/icons"
	"github.com/tyler-builds/jamb/ui"

	"github.com/tyler-builds/uas/internal/catalog"
)

// Spacing scale, in logical pixels.
const (
	space1 = 4
	space2 = 8
	space3 = 12
	space4 = 16
	space5 = 20
	space6 = 24
	space8 = 32
)

// Corner radii.
const (
	radiusS  = 8  // buttons, inputs, pills
	radiusM  = 12 // rows, icon tiles
	radiusL  = 16 // cards
	radiusXL = 22 // hero panels
)

// palette is the brand's colors for one mode.
type palette struct {
	dark bool

	background, panel, surface, surfaceHover, surfaceActive string
	border, borderStrong                                    string
	text, textMuted, textSubtle                             string
	accent, accentHover, accentActive, link                 string
	success, warning, danger                                string
	scrollbar, scrim                                        string
	// brandFrom and brandTo are the brand gradient's ends: violet to cyan.
	brandFrom, brandTo string
}

var darkPalette = palette{
	dark:       true,
	background: "#0c0e13", panel: "#11141b", surface: "#171b24", surfaceHover: "#1e2330", surfaceActive: "#252b3a",
	border: "#242a36", borderStrong: "#333a4a",
	text: "#eceef4", textMuted: "#a3aaba", textSubtle: "#6d7487",
	accent: "#7c6cff", accentHover: "#8e80ff", accentActive: "#6b5af0", link: "#a49bff",
	success: "#34d399", warning: "#fbbf24", danger: "#f87171",
	scrollbar: "#3a4153", scrim: "#05060acc",
	brandFrom: "#7c6cff", brandTo: "#22d3ee",
}

var lightPalette = palette{
	background: "#f5f6fa", panel: "#eceef5", surface: "#ffffff", surfaceHover: "#f1f3f8", surfaceActive: "#e6e9f1",
	border: "#e0e3ec", borderStrong: "#c9cedb",
	text: "#12141a", textMuted: "#586173", textSubtle: "#8a91a1",
	accent: "#6650f5", accentHover: "#5843ea", accentActive: "#4b37d6", link: "#5843ea",
	success: "#047857", warning: "#b45309", danger: "#dc2626",
	scrollbar: "#c3c8d4", scrim: "#0f121c59",
	brandFrom: "#6650f5", brandTo: "#0891b2",
}

// Themes returns the uas light and dark themes, built over jamb's so every widget picks them up.
func Themes() (light, dark *ui.Theme) {
	return buildTheme(ui.LightTheme(), lightPalette), buildTheme(ui.DarkTheme(), darkPalette)
}

func buildTheme(t *ui.Theme, p palette) *ui.Theme {
	h := ui.Hex
	t.Name = "uas"
	t.Background, t.Panel, t.Surface = h(p.background), h(p.panel), h(p.surface)
	t.SurfaceHover, t.SurfaceActive = h(p.surfaceHover), h(p.surfaceActive)
	t.Border, t.BorderStrong = h(p.border), h(p.borderStrong)
	t.Text, t.TextMuted, t.TextSubtle, t.Disabled = h(p.text), h(p.textMuted), h(p.textSubtle), h(p.textSubtle)
	t.Accent, t.AccentHover, t.AccentActive = h(p.accent), h(p.accentHover), h(p.accentActive)
	t.AccentText, t.Link, t.FocusRing = h("#ffffff"), h(p.link), h(p.link)
	t.AccentSoft = h(p.accent).Alpha(0.16)
	if !p.dark {
		t.AccentSoft = h(p.accent).Alpha(0.10)
	}
	t.Success, t.Warning, t.Danger = h(p.success), h(p.warning), h(p.danger)
	t.Scrollbar, t.Scrim = h(p.scrollbar), h(p.scrim)
	t.Radius, t.FontSize = radiusS, 14

	t.Sidebar = ui.SidebarStyle{
		Pane:         ui.Style{Background: h(p.panel)},
		Item:         ui.Style{Radius: radiusS},
		ItemSelected: ui.Style{Background: t.AccentSoft, TextColor: h(p.text), FontWeight: ui.FontSemibold},
		Indicator:    ui.Style{Background: h(p.accent)},
	}
	return t
}

// brandTo is the cyan end of the brand gradient; the violet end is the theme's accent.
func brandTo(t *ui.Theme) ui.Color {
	if t.Dark {
		return ui.Hex(darkPalette.brandTo)
	}
	return ui.Hex(lightPalette.brandTo)
}

// brandGradient is the full-strength brand gradient, for the logo and other small marks.
func brandGradient(t *ui.Theme) ui.Gradient { return ui.LinearGradient(135, t.Accent, brandTo(t)) }

// softGradient tints hero panels with the brand colors.
func softGradient(t *ui.Theme) ui.Gradient {
	return ui.LinearGradient(120, t.Accent.Alpha(0.20), brandTo(t).Alpha(0.06))
}

// Type scale.
func display(s string) *ui.TextElement { return ui.Text(s).FontSize(30).Bold() }
func title(s string) *ui.TextElement   { return ui.Text(s).FontSize(22).Semibold() }
func heading(s string) *ui.TextElement { return ui.Text(s).FontSize(16).Semibold() }
func body(s string) *ui.TextElement    { return ui.Text(s).FontSize(14) }

func muted(ctx *ui.Context, s string) *ui.TextElement {
	return ui.Text(s).FontSize(13).TextColor(ctx.Theme().TextMuted)
}

func caption(ctx *ui.Context, s string) *ui.TextElement {
	return ui.Text(s).FontSize(12).TextColor(ctx.Theme().TextMuted)
}

// card is the standard container: a raised surface with a hairline border.
func card(ctx *ui.Context) *ui.DivElement {
	t := ctx.Theme()
	return ui.Div().Column().Gap(space3).Padding(space5).Radius(radiusL).Background(t.Surface).Border(1, t.Border)
}

// hero is a panel tinted with the soft brand gradient, for the top of a page.
func hero(ctx *ui.Context) *ui.DivElement {
	t := ctx.Theme()
	return ui.Div().Column().Gap(space4).Padding(space8).Radius(radiusXL).BackgroundGradient(softGradient(t)).
		Border(1, t.Border)
}

// sectionHeader is a section's title, an optional line under it, and controls on the right.
func sectionHeader(ctx *ui.Context, titleText, sub string, trailing ...ui.Element) ui.Element {
	left := ui.Div().Column().Gap(2).Grow(1).Basis(ui.Px(0)).MinWidth(ui.Px(0)).Child(heading(titleText))
	if sub != "" {
		left = left.Child(caption(ctx, sub))
	}
	row := ui.Div().Row().Align(ui.End).Gap(space3).Child(left)
	for _, el := range trailing {
		row = row.Child(el)
	}
	return row
}

// brandMark is the uas logo: the store icon on the brand gradient.
func brandMark(ctx *ui.Context, size float32) ui.Element {
	return ui.Div().Size(ui.Px(size), ui.Px(size)).Radius(size * 0.28).BackgroundGradient(brandGradient(ctx.Theme())).
		Center().Shadow(ui.ShadowSm).
		Child(ui.Icon(icons.Store).Size(size * 0.55).Color(ui.Hex("#ffffff")))
}

// tint is a source's color: a strong one for text and dots, by darkness.
type tint struct{ onDark, onLight string }

var sourceTints = map[catalog.SourceID]tint{
	catalog.SourceFlathub:  {"#7aa7ff", "#2459c7"},
	catalog.SourceFedora:   {"#a99bff", "#5b45d9"},
	catalog.SourceHomebrew: {"#fbbf4a", "#a15c00"},
	catalog.SourceCOPR:     {"#3fd6b8", "#0b7a66"},
	catalog.SourceGitHub:   {"#b4bac6", "#4b5262"},
}

var sourceIcons = map[catalog.SourceID]*icons.Icon{
	catalog.SourceFlathub:  icons.Box,
	catalog.SourceFedora:   icons.Layers,
	catalog.SourceHomebrew: icons.Terminal,
	catalog.SourceCOPR:     icons.Wrench,
	catalog.SourceGitHub:   icons.Code,
}

func tintColor(ctx *ui.Context, t tint) ui.Color {
	if ctx.Theme().Dark {
		return ui.Hex(t.onDark)
	}
	return ui.Hex(t.onLight)
}

// pill is a small tinted label with a dot, such as a source or a status.
func pill(ctx *ui.Context, label string, c ui.Color, tip string) ui.Element {
	p := ui.Div().Row().Align(ui.Center).Gap(5).PaddingX(9).Height(ui.Px(22)).Radius(11).Shrink(0).
		Background(c.Alpha(0.14)).
		Child(ui.Div().Size(ui.Px(6), ui.Px(6)).Radius(3).Background(c)).
		Child(ui.Text(label).FontSize(11.5).Semibold().TextColor(c))
	if tip != "" {
		p = p.Tooltip(tip)
	}
	return p
}

func sourcePill(ctx *ui.Context, s catalog.SourceID) ui.Element {
	return pill(ctx, s.Name(), tintColor(ctx, sourceTints[s]), sourceTips[s])
}

func verifiedPill(ctx *ui.Context) ui.Element {
	return pill(ctx, "Verified", ctx.Theme().Success, "Flathub has confirmed that the publisher is the app's own "+
		"developer, through the developer's website or code-hosting account")
}

// sourcePills shows where an app is available, each pill explaining its source on hover.
func sourcePills(ctx *ui.Context, sources []catalog.SourceID, verified bool) ui.Element {
	row := ui.Div().Row().Gap(space1 + 2).Shrink(0).Wrap()
	for _, s := range sources {
		row = row.Child(sourcePill(ctx, s))
	}
	if verified {
		row = row.Child(verifiedPill(ctx))
	}
	return row
}

// statChip is an icon, a value and a label, such as "2.9M installs a year".
func statChip(ctx *ui.Context, icon *icons.Icon, value, label, tip string) ui.Element {
	t := ctx.Theme()
	c := ui.Div().Row().Align(ui.Center).Gap(space2).PaddingX(space3).PaddingY(space2).Radius(radiusM).
		Background(t.Surface).Border(1, t.Border).
		Child(ui.Icon(icon).Size(16).Color(t.TextMuted)).
		Child(ui.Div().Column().
			Child(ui.Text(value).FontSize(14).Semibold()).
			Child(caption(ctx, label)))
	if tip != "" {
		c = c.Tooltip(tip)
	}
	return c
}

// meter is a thin bar filled to frac (0 to 1) in color.
func meter(ctx *ui.Context, frac float64, color ui.Color) ui.Element {
	frac = max(0, min(1, frac))
	return ui.Div().Height(ui.Px(6)).Radius(3).Background(ctx.Theme().Border).ClipContent().
		Child(ui.Div().Width(ui.Percent(float32(frac * 100))).Height(ui.Full).Radius(3).Background(color))
}

// statusPill is a short "Ready" or "Not available" style status.
func statusPill(ctx *ui.Context, ok bool, label string) ui.Element {
	if ok {
		return pill(ctx, label, ctx.Theme().Success, "")
	}
	return pill(ctx, label, ctx.Theme().TextMuted, "")
}

// iconTile frames an app icon on a surface tile, so icons of any shape sit on a consistent base.
func (a *App) iconTile(ctx *ui.Context, url string, size float32, name string) ui.Element {
	t := ctx.Theme()
	pad := size * 0.14
	return ui.Div().Size(ui.Px(size), ui.Px(size)).Radius(size*0.24).Background(t.Surface).Border(1, t.Border).
		Center().Shrink(0).
		Child(a.appIcon(ctx, url, size-2*pad, name))
}
