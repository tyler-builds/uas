package gui

// The uas design system: the palette as jamb themes, a type and spacing scale, and the components every page
// is built from. Pages use these rather than raw sizes and colors, so the app stays consistent. See
// docs/design.md.

import (
	"fmt"
	"strings"

	"github.com/tyler-builds/jamb/access"
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
	radiusS  = 8  // buttons, inputs, chips
	radiusM  = 12 // rows, tiles
	radiusL  = 14 // cards
	radiusXL = 20 // the featured banner
)

// palette is the colors for one mode: charcoal surfaces with a teal accent.
type palette struct {
	dark bool

	background, panel, surface, surfaceHover, surfaceActive string
	border, borderStrong                                    string
	text, textMuted, textSubtle                             string
	accent, accentHover, accentActive, accentText, link     string
	success, warning, danger                                string
	scrollbar, scrim                                        string
	// art is the colors of the featured banner's shapes, darkest first.
	art [3]string
}

var darkPalette = palette{
	dark:       true,
	background: "#16181d", panel: "#121418", surface: "#1e2128", surfaceHover: "#252932", surfaceActive: "#2c313b",
	border: "#2a2e37", borderStrong: "#3a3f4a",
	text: "#eef0f3", textMuted: "#a2a8b4", textSubtle: "#6e7480",
	accent: "#2dd4bf", accentHover: "#5eead4", accentActive: "#14b8a6", accentText: "#042f2e", link: "#5eead4",
	success: "#34d399", warning: "#fbbf24", danger: "#f87171",
	scrollbar: "#3a3f4a", scrim: "#05060acc",
	art: [3]string{"#0f3b3f", "#14706a", "#2dd4bf"},
}

var lightPalette = palette{
	background: "#eceff3", panel: "#e4e8ed", surface: "#ffffff", surfaceHover: "#f6f8fa", surfaceActive: "#d9dee5",
	border: "#dfe3e8", borderStrong: "#c8ced6",
	text: "#111418", textMuted: "#5b6472", textSubtle: "#8a919c",
	accent: "#0d9488", accentHover: "#0f766e", accentActive: "#115e59", accentText: "#ffffff", link: "#0f766e",
	success: "#047857", warning: "#b45309", danger: "#dc2626",
	scrollbar: "#c3c8d4", scrim: "#0f121c59",
	art: [3]string{"#b8ebe4", "#5fd1c3", "#14b8a6"},
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
	t.AccentText, t.Link, t.FocusRing = h(p.accentText), h(p.link), h(p.link)
	t.AccentSoft = h(p.accent).Alpha(0.14)
	t.Success, t.Warning, t.Danger = h(p.success), h(p.warning), h(p.danger)
	t.Scrollbar, t.Scrim = h(p.scrollbar), h(p.scrim)
	t.Radius, t.FontSize = radiusS, 14

	t.Sidebar = ui.SidebarStyle{
		Pane:         ui.Style{Background: h(p.panel)},
		Item:         ui.Style{Radius: radiusS, FontSize: 14, MinHeight: ui.Px(40)},
		ItemSelected: ui.Style{Background: h(p.surfaceActive), TextColor: h(p.text), FontWeight: ui.FontSemibold},
		Heading:      ui.Style{TextColor: h(p.textSubtle), FontSize: 11},
	}
	t.Buttons.Primary = ui.ButtonStyle{Root: ui.Style{FontWeight: ui.FontSemibold}}
	// Cards are one flat color, lighter than the page; a clickable one lightens under the pointer.
	t.Card = ui.CardStyle{
		Card:      ui.Style{Radius: radiusL, Padding: ui.All(space5), Gap: space3, Background: h(p.surface)},
		CardHover: ui.Style{Background: h(p.surfaceHover), Cursor: ui.CursorPointer},
	}
	return t
}

// artColors returns the featured banner's shape colors for the theme in use.
func artColors(t *ui.Theme) [3]ui.Color {
	p := lightPalette
	if t.Dark {
		p = darkPalette
	}
	return [3]ui.Color{ui.Hex(p.art[0]), ui.Hex(p.art[1]), ui.Hex(p.art[2])}
}

// Type scale.
func display(s string) *ui.TextElement { return ui.Text(s).FontSize(38).Bold() }
func title(s string) *ui.TextElement   { return ui.Text(s).FontSize(26).Bold() }
func heading(s string) *ui.TextElement { return ui.Text(s).FontSize(20).Semibold() }
func subhead(s string) *ui.TextElement { return ui.Text(s).FontSize(16).Semibold() }

func muted(ctx *ui.Context, s string) *ui.TextElement {
	return ui.Text(s).FontSize(14).TextColor(ctx.Theme().TextMuted)
}

func caption(ctx *ui.Context, s string) *ui.TextElement {
	return ui.Text(s).FontSize(12).TextColor(ctx.Theme().TextMuted)
}

// sectionHeader is a section's title with optional controls on the right, such as a "See all" link.
func sectionHeader(ctx *ui.Context, titleText, sub string, trailing ...ui.Element) ui.Element {
	left := ui.Div().Column().Gap(2).Grow(1).Basis(ui.Px(0)).MinWidth(ui.Px(0)).Child(heading(titleText))
	if sub != "" {
		left = left.Child(muted(ctx, sub))
	}
	row := ui.Div().Row().Align(ui.End).Gap(space3).Child(left)
	for _, el := range trailing {
		row = row.Child(el)
	}
	return row
}

// linkButton is a quiet button with an icon after its text, such as "See all →".
func linkButton(_ *ui.Context, label string, icon *icons.Icon, onClick func()) ui.Element {
	return ui.Button(label).IconEnd(icon).Ghost().Small().OnClick(onClick)
}

// chip is a rounded tag, such as a category on an app page.
func chip(ctx *ui.Context, label string) ui.Element {
	t := ctx.Theme()
	return ui.Div().PaddingX(space3 + 2).PaddingY(6).Radius(16).Background(t.SurfaceActive).Shrink(0).
		Child(ui.Text(label).FontSize(14))
}

// filterChip is one choice of a row of filters; the selected one is filled with the accent.
func filterChip(ctx *ui.Context, label string, selected bool, onClick func()) ui.Element {
	t := ctx.Theme()
	d := ui.Div().PaddingX(space4 + 2).Height(ui.Px(38)).Radius(19).Center().Shrink(0).
		Cursor(ui.CursorPointer).Role(access.RoleButton).Label(label).TabIndex(0).OnClick(onClick).
		AccessSelected(selected)
	if selected {
		return d.Background(t.Accent).Child(ui.Text(label).FontSize(14).Semibold().TextColor(t.AccentText))
	}
	return d.Background(t.Surface).Border(1, t.Border).Hover(ui.Style{Background: t.SurfaceHover}).
		Child(ui.Text(label).FontSize(14))
}

// tint is a source's color, by darkness.
type tint struct{ onDark, onLight string }

var sourceTints = map[catalog.SourceID]tint{
	catalog.SourceFlathub:  {"#7aa7ff", "#2459c7"},
	catalog.SourceFedora:   {"#a99bff", "#5b45d9"},
	catalog.SourceHomebrew: {"#fbbf4a", "#a15c00"},
	catalog.SourceCOPR:     {"#fb7185", "#be123c"},
	catalog.SourceGitHub:   {"#b4bac6", "#4b5262"},
	catalog.SourceAppImage: {"#5eead4", "#0f766e"},
}

// sourceTips explain each source in a sentence or two.
var sourceTips = map[catalog.SourceID]string{
	catalog.SourceFlathub: "On Flathub, the Flatpak app store. Flatpak apps run in a sandbox and install " +
		"without a restart",
	catalog.SourceFedora: "Packaged and signed by Fedora. Installing adds it to the system image " +
		"(layering), which takes effect after a restart",
	catalog.SourceHomebrew: "A Homebrew formula, mostly command-line tools. Installs for your user without " +
		"a restart",
	catalog.SourceGitHub: "Released on GitHub by its developer, as an AppImage or Flatpak bundle. No distribution reviews it; " +
		"its page shows a trust score",
	catalog.SourceCOPR: "Built in a Fedora COPR project by the project's owner. Fedora doesn't review it, " +
		"and installing layers it into the system image",
	catalog.SourceAppImage: "An AppImage you added from a file. uas updates it from where the AppImage says, " +
		"if it says, and checks each download's checksum",
}

func tintColor(ctx *ui.Context, t tint) ui.Color {
	if ctx.Theme().Dark {
		return ui.Hex(t.onDark)
	}
	return ui.Hex(t.onLight)
}

func sourceColor(ctx *ui.Context, s catalog.SourceID) ui.Color { return tintColor(ctx, sourceTints[s]) }

// verifiedTip explains the verified mark.
const verifiedTip = "Flathub has confirmed that the publisher is the app's own developer, through the " +
	"developer's website or code-hosting account"

// sourceTag is a source's icon in its color followed by its name, as the lists show where an app comes from.
func sourceTag(ctx *ui.Context, s catalog.SourceID) ui.Element {
	return ui.Div().Row().Align(ui.Center).Gap(6).Shrink(0).Tooltip(s.Name() + ": " + sourceTips[s]).
		Child(ui.Icon(sourceIcons[s]).Size(15).Color(sourceColor(ctx, s))).
		Child(ui.Text(s.Name()).FontSize(13).TextColor(ctx.Theme().TextMuted))
}

// verifiedTag is the shield shown for a Flathub-verified publisher.
func verifiedTag(ctx *ui.Context) ui.Element {
	t := ctx.Theme()
	return ui.Div().Row().Align(ui.Center).Gap(6).Shrink(0).Tooltip("Verified publisher: " + verifiedTip).
		Child(ui.Icon(icons.ShieldCheck).Size(15).Color(t.Success)).
		Child(ui.Text("Verified").FontSize(13).TextColor(t.TextMuted))
}

// sourceTags shows where an app is available, and whether its publisher is verified. With max above zero,
// sources past max collapse into a "+n".
func sourceTags(ctx *ui.Context, sources []catalog.SourceID, verified bool, max int) ui.Element {
	// The tags keep their width; whatever sits beside them (such as the developer) gives way instead.
	row := ui.Div().Row().Align(ui.Center).Gap(space4).Shrink(0)
	for i, s := range sources {
		if max > 0 && i == max {
			rest := make([]string, 0, len(sources)-max)
			for _, r := range sources[max:] {
				rest = append(rest, r.Name())
			}
			row = row.Child(ui.Div().Tooltip("Also on " + strings.Join(rest, " and ")).
				Child(caption(ctx, fmt.Sprintf("+%d", len(sources)-max))))
			break
		}
		row = row.Child(sourceTag(ctx, s))
	}
	if verified {
		row = row.Child(verifiedTag(ctx))
	}
	return row
}

// sourcePill is a source's name as a labeled pill, for places that spell sources out (Settings).
func sourcePill(ctx *ui.Context, s catalog.SourceID) ui.Element {
	c := sourceColor(ctx, s)
	return ui.Div().Row().Align(ui.Center).Gap(6).PaddingX(10).Height(ui.Px(26)).Radius(13).Shrink(0).
		Background(c.Alpha(0.14)).Tooltip(sourceTips[s]).
		Child(ui.Icon(sourceIcons[s]).Size(14).Color(c)).
		Child(ui.Text(s.Name()).FontSize(12.5).Semibold().TextColor(c))
}

// statusPill is a short "Ready" or "Not available" status.
func statusPill(ctx *ui.Context, ok bool, label string) ui.Element {
	t := ctx.Theme()
	c := t.TextMuted
	if ok {
		c = t.Success
	}
	return ui.Div().Row().Align(ui.Center).Gap(6).PaddingX(10).Height(ui.Px(24)).Radius(12).Shrink(0).
		Background(c.Alpha(0.14)).
		Child(ui.Div().Size(ui.Px(6), ui.Px(6)).Radius(3).Background(c)).
		Child(ui.Text(label).FontSize(12).Semibold().TextColor(c))
}

// statLine is an icon and a short figure, as the result rows show installs and release dates.
func statLine(ctx *ui.Context, icon *icons.Icon, text, tip string) ui.Element {
	t := ctx.Theme()
	d := ui.Div().Row().Align(ui.Center).Gap(space2).Shrink(0).
		Child(ui.Icon(icon).Size(16).Color(t.TextMuted)).
		Child(ui.Text(text).FontSize(14).TextColor(t.TextMuted))
	if tip != "" {
		d = d.Tooltip(tip)
	}
	return d
}

// brandMark is the uas logo: the store icon on a teal tile.
func brandMark(ctx *ui.Context, size float32) ui.Element {
	t := ctx.Theme()
	return ui.Div().Size(ui.Px(size), ui.Px(size)).Radius(size * 0.28).
		BackgroundGradient(ui.LinearGradient(135, t.Accent, t.AccentActive)).Center().
		Child(ui.Icon(icons.Store).Size(size * 0.55).Color(t.AccentText))
}

// iconTile frames an app icon on a surface tile, so icons of any shape sit on a consistent base.
func (a *App) iconTile(ctx *ui.Context, url string, size float32, name string) ui.Element {
	t := ctx.Theme()
	pad := size * 0.14
	return ui.Div().Size(ui.Px(size), ui.Px(size)).Radius(size * 0.24).Background(t.SurfaceActive).
		Center().Shrink(0).
		Child(a.appIcon(ctx, url, size-2*pad, name))
}

// compact formats a count like 1.2M or 34k.
func compact(n int) string {
	switch {
	case n >= 1_000_000:
		return trimZero(fmt.Sprintf("%.1f", float64(n)/1e6)) + "M"
	case n >= 10_000:
		return fmt.Sprintf("%dk", n/1000)
	case n >= 1000:
		return trimZero(fmt.Sprintf("%.1f", float64(n)/1e3)) + "k"
	}
	return fmt.Sprint(n)
}

// compactThousands formats a count like 7,732.
func compactThousands(n int) string {
	s := fmt.Sprint(n)
	for i := len(s) - 3; i > 0; i -= 3 {
		s = s[:i] + "," + s[i:]
	}
	return s
}

func trimZero(s string) string { return strings.TrimSuffix(s, ".0") }

func sourceLabel(s catalog.SourceID) string { return s.Name() }

func plural(n int, one, many string) string {
	if n == 1 {
		return "1 " + one
	}
	return compactThousands(n) + " " + many
}
