package appearance

import (
	"context"
	"errors"
	"os"
	"strconv"
	"strings"

	"github.com/tyler-builds/uas/internal/client/run"
)

// Setting keys: what "Use" changes. They are GNOME's names for its settings; other desktops map them to
// their own.
const (
	KeyIconTheme     = "icon-theme"
	KeyCursorTheme   = "cursor-theme"
	KeyFont          = "font-name"           // the interface font
	KeyDocumentFont  = "document-font-name"  // the font of documents
	KeyMonospaceFont = "monospace-font-name" // the fixed-width font, as terminals use
)

// FontKeys are the settings a font can be used for.
var FontKeys = []string{KeyFont, KeyDocumentFont, KeyMonospaceFont}

// KeyLabel names a setting for people.
func KeyLabel(key string) string {
	switch key {
	case KeyIconTheme:
		return "Icons"
	case KeyCursorTheme:
		return "Cursor"
	case KeyFont:
		return "Interface text"
	case KeyDocumentFont:
		return "Document text"
	case KeyMonospaceFont:
		return "Monospace text"
	}
	return key
}

// ErrUnsupported is returned for a setting the desktop can't change from uas.
var ErrUnsupported = errors.New("this desktop's setting can't be changed from uas")

// Desktop reads and changes the desktop's appearance settings.
type Desktop interface {
	// Name is "gnome" or "plasma".
	Name() string
	// Supports reports whether the desktop has the setting and uas can change it.
	Supports(key string) bool
	// Get returns the setting's value: a theme name, or for a font key, a family and size ("Cantarell 11").
	Get(ctx context.Context, key string) (string, error)
	// Set changes the setting.
	Set(ctx context.Context, key, value string) error
	// Reset puts the setting back to the desktop's default.
	Reset(ctx context.Context, key string) error
}

// DetectDesktop returns the desktop of $XDG_CURRENT_DESKTOP, or nil for one uas doesn't change settings
// of, where fonts and themes are installed only.
func DetectDesktop(current string, r run.Runner) Desktop {
	for _, d := range strings.Split(current, ":") {
		switch strings.ToUpper(strings.TrimSpace(d)) {
		case "GNOME":
			return &GNOME{Runner: r}
		case "KDE":
			return &Plasma{Runner: r}
		}
	}
	return nil
}

// GNOME changes org.gnome.desktop.interface with gsettings. The change applies at once, in Flatpak apps
// too (through the settings portal).
type GNOME struct{ Runner run.Runner }

const gnomeSchema = "org.gnome.desktop.interface"

func (*GNOME) Name() string { return "gnome" }

func (*GNOME) Supports(key string) bool {
	switch key {
	case KeyIconTheme, KeyCursorTheme, KeyFont, KeyDocumentFont, KeyMonospaceFont:
		return true
	}
	return false
}

func (g *GNOME) Get(ctx context.Context, key string) (string, error) {
	if !g.Supports(key) {
		return "", ErrUnsupported
	}
	out, err := g.Runner.Run(ctx, "gsettings", []string{"get", gnomeSchema, key}, nil)
	if err != nil {
		return "", err
	}
	return parseGVariantString(strings.TrimSpace(out)), nil
}

func (g *GNOME) Set(ctx context.Context, key, value string) error {
	if !g.Supports(key) {
		return ErrUnsupported
	}
	_, err := g.Runner.Run(ctx, "gsettings", []string{"set", gnomeSchema, key, quoteGVariant(value)}, nil)
	return err
}

func (g *GNOME) Reset(ctx context.Context, key string) error {
	if !g.Supports(key) {
		return ErrUnsupported
	}
	_, err := g.Runner.Run(ctx, "gsettings", []string{"reset", gnomeSchema, key}, nil)
	return err
}

// parseGVariantString reads a GVariant string as gsettings prints it: 'Adwaita', or "it's" when it holds a
// single quote.
func parseGVariantString(s string) string {
	if len(s) < 2 || (s[0] != '\'' && s[0] != '"') || s[len(s)-1] != s[0] {
		return s
	}
	var b strings.Builder
	body := s[1 : len(s)-1]
	for i := 0; i < len(body); i++ {
		if body[i] == '\\' && i+1 < len(body) {
			i++
		}
		b.WriteByte(body[i])
	}
	return b.String()
}

// quoteGVariant writes s as a GVariant string, so gsettings never reads it as anything else.
func quoteGVariant(s string) string {
	return "'" + strings.NewReplacer(`\`, `\\`, `'`, `\'`).Replace(s) + "'"
}

// Plasma changes KDE Plasma's icon and cursor themes with the commands its settings use, which tell running
// apps. It has no command for fonts, so fonts are installed only.
type Plasma struct {
	Runner run.Runner
	// ChangeIcons is plasma-changeicons, which lives outside $PATH; empty finds it.
	ChangeIcons string
}

func (*Plasma) Name() string { return "plasma" }

func (p *Plasma) Supports(key string) bool {
	switch key {
	case KeyIconTheme:
		return p.changeIcons() != ""
	case KeyCursorTheme:
		return true
	}
	return false
}

// changeIconsPaths are where distributions put plasma-changeicons.
var changeIconsPaths = []string{"/usr/libexec/plasma-changeicons", "/usr/lib/libexec/plasma-changeicons",
	"/usr/lib64/libexec/plasma-changeicons", "/usr/lib/x86_64-linux-gnu/libexec/plasma-changeicons",
	"/usr/lib/aarch64-linux-gnu/libexec/plasma-changeicons"}

func (p *Plasma) changeIcons() string {
	if p.ChangeIcons != "" {
		return p.ChangeIcons
	}
	for _, c := range changeIconsPaths {
		if fi, err := os.Stat(c); err == nil && fi.Mode().IsRegular() {
			p.ChangeIcons = c
			return c
		}
	}
	return ""
}

// plasmaDefaults are Plasma's own values, which its config files leave out until they're changed.
var plasmaDefaults = map[string]string{KeyIconTheme: "breeze", KeyCursorTheme: "breeze_cursors"}

func (p *Plasma) Get(ctx context.Context, key string) (string, error) {
	var args []string
	switch key {
	case KeyIconTheme:
		args = []string{"--file", "kdeglobals", "--group", "Icons", "--key", "Theme"}
	case KeyCursorTheme:
		args = []string{"--file", "kcminputrc", "--group", "Mouse", "--key", "cursorTheme"}
	default:
		return "", ErrUnsupported
	}
	out, err := p.Runner.Run(ctx, "kreadconfig6", append(args, "--default", plasmaDefaults[key]), nil)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(out), nil
}

func (p *Plasma) Set(ctx context.Context, key, value string) error {
	var err error
	switch key {
	case KeyIconTheme:
		if p.changeIcons() == "" {
			return ErrUnsupported
		}
		_, err = p.Runner.Run(ctx, p.changeIcons(), []string{value}, nil)
	case KeyCursorTheme:
		_, err = p.Runner.Run(ctx, "plasma-apply-cursortheme", []string{value}, nil)
	default:
		return ErrUnsupported
	}
	return err
}

func (p *Plasma) Reset(ctx context.Context, key string) error {
	if d, ok := plasmaDefaults[key]; ok {
		return p.Set(ctx, key, d)
	}
	return ErrUnsupported
}

// FontValue is a font setting with its family replaced and its size kept: ("Cantarell 11", "Inter") is
// "Inter 11".
func FontValue(current, family string) string {
	fields := strings.Fields(current)
	if n := len(fields); n > 1 {
		if _, err := strconv.ParseFloat(fields[n-1], 64); err == nil {
			return family + " " + fields[n-1]
		}
	}
	return family + " 11"
}

// FontFamily is the family of a font setting: "Inter" of "Inter 11".
func FontFamily(value string) string {
	fields := strings.Fields(value)
	if n := len(fields); n > 1 {
		if _, err := strconv.ParseFloat(fields[n-1], 64); err == nil {
			return strings.Join(fields[:n-1], " ")
		}
	}
	return strings.TrimSpace(value)
}
