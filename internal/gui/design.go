package gui

// The uas design system: the palette as jamb themes, a type and spacing scale, and the components every page
// is built from. Pages use these rather than raw sizes and colors, so the app stays consistent. See
// docs/design.md.

import (
	"bytes"
	_ "embed"
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
	radiusS  = 6  // buttons, inputs, chips
	radiusM  = 8  // rows, tiles
	radiusL  = 10 // cards and lists
	radiusXL = 12 // the featured banner and the content panel
)

// palette is the colors for one mode: neutral near-black (or white) surfaces with hairline borders, and
// teal kept for the brand mark and small status marks.
type palette struct {
	dark bool

	background, panel, surface, surfaceHover, surfaceActive string
	border, borderStrong                                    string
	text, textMuted, textSubtle                             string
	accent, accentHover, accentActive, accentText, link     string
	brand                                                   string
	success, warning, danger                                string
	scrollbar, scrim                                        string
}

var darkPalette = palette{
	dark:       true,
	background: "#0e0e10", panel: "#08080a", surface: "#19191d", surfaceHover: "#202025", surfaceActive: "#2a2a30",
	border: "#222227", borderStrong: "#313137",
	text: "#f2f2f4", textMuted: "#a9a9b3", textSubtle: "#75757f",
	accent: "#14897e", accentHover: "#18978b", accentActive: "#117469", accentText: "#ffffff", link: "#7cc8bf",
	brand:   "#2dd4bf",
	success: "#3ecf8e", warning: "#f5b544", danger: "#f87171",
	scrollbar: "#2e2e34", scrim: "#000000b3",
}

var lightPalette = palette{
	background: "#ffffff", panel: "#f5f5f6", surface: "#ffffff", surfaceHover: "#f6f6f8", surfaceActive: "#ececef",
	border: "#e8e8ec", borderStrong: "#d8d8de",
	text: "#0c0c0e", textMuted: "#5d5d66", textSubtle: "#9a9aa3",
	accent: "#0f766e", accentHover: "#115e59", accentActive: "#134e4a", accentText: "#ffffff", link: "#0f766e",
	brand:   "#0d9488",
	success: "#15803d", warning: "#b45309", danger: "#dc2626",
	scrollbar: "#d0d0d6", scrim: "#0c0c0e59",
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
	t.AccentSoft = h(p.text).Alpha(0.08)
	t.FocusRing = h(p.link)
	t.Success, t.Warning, t.Danger = h(p.success), h(p.warning), h(p.danger)
	t.Scrollbar, t.Scrim = h(p.scrollbar), h(p.scrim)
	t.Radius, t.FontSize = radiusS, 13

	t.Sidebar = ui.SidebarStyle{
		Pane: ui.Style{Background: h(p.panel), BorderColor: h(p.panel), Padding: ui.All(space2)},
		Item: ui.Style{Radius: radiusS, FontSize: 13, MinHeight: ui.Px(30), TextColor: h(p.textMuted),
			Padding: ui.XY(space2+2, 0), Gap: space2 + 2},
		ItemHover:    ui.Style{Background: h(p.surfaceHover), TextColor: h(p.text)},
		ItemSelected: ui.Style{Background: h(p.surfaceActive), TextColor: h(p.text), FontWeight: ui.FontMedium},
		Heading: ui.Style{TextColor: h(p.textSubtle), FontSize: 11, FontWeight: ui.FontMedium,
			Padding: ui.Edges{Top: space4, Right: 10, Bottom: space1, Left: 10}},
	}
	t.Button = ui.ButtonStyle{Root: ui.Style{FontWeight: ui.FontMedium}}
	// Teal marks what matters, sparingly: primary actions, links and focus.
	t.Buttons.Primary = ui.ButtonStyle{Root: ui.Style{FontWeight: ui.FontMedium}}
	t.Badge = ui.BadgeStyle{Badge: ui.Style{Background: h(p.surfaceActive)},
		Label: ui.Style{TextColor: h(p.textMuted), FontWeight: ui.FontMedium}}
	t.Buttons.Secondary = ui.ButtonStyle{
		Root:      ui.Style{Background: h(p.surface), BorderColor: h(p.borderStrong)},
		RootHover: ui.Style{Background: h(p.surfaceHover), BorderColor: h(p.borderStrong)},
	}
	t.Field = ui.FieldStyle{
		Frame:            ui.Style{Background: h(p.surface), BorderColor: h(p.border)},
		FrameHover:       ui.Style{BorderColor: h(p.borderStrong)},
		FrameFocusWithin: ui.Style{BorderColor: h(p.textSubtle)},
		Selection:        ui.Style{Background: h(p.link).Alpha(0.35)},
	}
	// Cards are a quiet fill a step off the page with a hairline border; a clickable one lightens under the
	// pointer.
	t.Card = ui.CardStyle{
		Card: ui.Style{Radius: radiusL, Padding: ui.All(space4), Gap: space3, Background: h(p.surface),
			BorderWidth: 1, BorderColor: h(p.border)},
		CardHover: ui.Style{Background: h(p.surfaceHover), BorderColor: h(p.borderStrong), Cursor: ui.CursorPointer},
	}
	return t
}

// brandColor is the teal of the uas mark, for the theme in use.
func brandColor(t *ui.Theme) ui.Color {
	if t.Dark {
		return ui.Hex(darkPalette.brand)
	}
	return ui.Hex(lightPalette.brand)
}

// Type scale.
func title(s string) *ui.TextElement   { return ui.Text(s).FontSize(24).Semibold() }
func heading(s string) *ui.TextElement { return ui.Text(s).FontSize(16).Semibold() }
func subhead(s string) *ui.TextElement { return ui.Text(s).FontSize(14).Medium() }

func muted(ctx *ui.Context, s string) *ui.TextElement {
	return ui.Text(s).FontSize(13).TextColor(ctx.Theme().TextMuted)
}

func caption(ctx *ui.Context, s string) *ui.TextElement {
	return ui.Text(s).FontSize(12).TextColor(ctx.Theme().TextMuted)
}

// section is a header and its content, kept closer together than sections are to each other.
func section(header, content ui.Element) ui.Element {
	return ui.Div().Column().Gap(space3).Child(header).Child(content)
}

// sectionHeader is a section's title with optional controls on the right, such as a "See all" link.
func sectionHeader(ctx *ui.Context, titleText, sub string, trailing ...ui.Element) ui.Element {
	left := ui.Div().Column().Gap(2).Grow(1).Basis(ui.Px(0)).MinWidth(ui.Px(0)).Child(heading(titleText))
	if sub != "" {
		left = left.Child(muted(ctx, sub))
	}
	row := ui.Div().Row().Align(ui.Center).Gap(space3).Child(left)
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
	return ui.Div().PaddingX(space2+2).Height(ui.Px(24)).Center().Radius(radiusS).Border(1, t.Border).
		Background(t.Surface).Shrink(0).
		Child(ui.Text(label).FontSize(12).TextColor(t.TextMuted))
}

// filterChip is one choice of a segmented row of filters (see segmented); the selected one is raised.
func filterChip(ctx *ui.Context, label string, selected bool, onClick func()) ui.Element {
	t := ctx.Theme()
	d := ui.Div().PaddingX(space3).Height(ui.Px(26)).Radius(radiusS - 1).Center().Shrink(0).
		Cursor(ui.CursorPointer).Role(access.RoleButton).Label(label).TabIndex(0).OnClick(onClick).
		AccessSelected(selected)
	if selected {
		return d.Background(t.SurfaceActive).Child(ui.Text(label).FontSize(13).Medium())
	}
	return d.Hover(ui.Style{Background: t.SurfaceHover}).
		Child(ui.Text(label).FontSize(13).TextColor(t.TextMuted))
}

// segmented holds filter chips in one bordered strip, like a segmented control.
func segmented(ctx *ui.Context, chips ...ui.Element) *ui.DivElement {
	t := ctx.Theme()
	d := ui.Div().Row().Align(ui.Center).Gap(2).Padding(2).Radius(radiusS+2).Border(1, t.Border).
		Background(t.Surface).Shrink(0)
	for _, c := range chips {
		d = d.Child(c)
	}
	return d
}

// tint is a source's color, by darkness.
type tint struct{ onDark, onLight string }

var sourceTints = map[catalog.SourceID]tint{
	catalog.SourceFlathub:  {"#7aa7ff", "#2459c7"},
	catalog.SourceFedora:   {"#a99bff", "#5b45d9"},
	catalog.SourceHomebrew: {"#fbbf4a", "#a15c00"},
	catalog.SourceCOPR:     {"#fb7185", "#be123c"},
	catalog.SourceGitHub:   {"#b4bac6", "#4b5262"},
	catalog.SourceGitLab:   {"#fc9867", "#c2410c"},
	catalog.SourceCodeberg: {"#60a5fa", "#1d4ed8"},
	catalog.SourceDebian:   {"#f472b6", "#be185d"},
	catalog.SourceUbuntu:   {"#fb923c", "#c2410c"},
	catalog.SourceArch:     {"#38bdf8", "#0369a1"},
	catalog.SourceSnap:     {"#a3e635", "#4d7c0f"},
	catalog.SourceAppImage: {"#5eead4", "#0f766e"},
}

// sourceTips explain each source in a sentence or two.
var sourceTips = map[catalog.SourceID]string{
	catalog.SourceFlathub: "On Flathub, the Flatpak app store. Flatpak apps run in a sandbox and install " +
		"without a restart",
	catalog.SourceFedora: "Packaged and signed by Fedora. On image-based Fedora (Bazzite, Silverblue) installing " +
		"adds it to the system image (layering), which takes effect after a restart",
	catalog.SourceHomebrew: "A Homebrew formula, mostly command-line tools. Installs for your user without " +
		"a restart",
	catalog.SourceGitHub: "Released on GitHub by its developer, as an AppImage or Flatpak bundle. No distribution reviews it; " +
		"its page shows a trust score",
	catalog.SourceGitLab: "Released on GitLab by its developer, as an AppImage or Flatpak bundle. No distribution reviews it; " +
		"its page shows a trust score",
	catalog.SourceCodeberg: "Released on Codeberg by its developer, as an AppImage or Flatpak bundle. No distribution " +
		"reviews it; its page shows a trust score",
	catalog.SourceDebian: "Packaged and signed by Debian. Installs system-wide with apt, which asks for your password",
	catalog.SourceUbuntu: "Packaged and signed by Ubuntu. Installs system-wide with apt, which asks for your password",
	catalog.SourceArch: "Packaged and signed by Arch Linux. Installs system-wide with pacman, which asks for your " +
		"password; updates come with the system's",
	catalog.SourceSnap: "From the Snap Store. Snaps mostly run in a sandbox and update themselves; anyone can " +
		"publish one, so check for a verified publisher",
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

// The uas logo, a package on a teal tile. brand/uas.svg is also the desktop icon, which keeps a margin
// around the tile; in the app the tile fills the box.
//
//go:embed brand/uas.svg
var logoSVG []byte

var logo = ui.MustSVG(bytes.Replace(logoSVG, []byte(`viewBox="0 0 128 128"`), []byte(`viewBox="8 8 112 112"`), 1))

// brandMark is the uas logo at size.
func brandMark(_ *ui.Context, size float32) ui.Element {
	return ui.SVG(logo).Size(ui.Px(size), ui.Px(size)).Alt("uas")
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
