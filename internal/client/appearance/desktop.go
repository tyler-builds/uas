package appearance

import (
	"context"
	"errors"
	"fmt"
	"os"
	"slices"
	"strings"
	"sync"
	"time"

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
	// KeyColorScheme is the light or dark style: SchemeDefault, SchemeLight or SchemeDark.
	KeyColorScheme = "color-scheme"
	// KeyAccentColor is the accent color, one of AccentColors.
	KeyAccentColor = "accent-color"
)

// FontKeys are the settings a font can be used for.
var FontKeys = []string{KeyFont, KeyDocumentFont, KeyMonospaceFont}

// SettingKeys are every setting uas can change, in the order they are listed and applied: the style and
// accent first, as everything else is seen in them.
var SettingKeys = []string{KeyColorScheme, KeyAccentColor, KeyIconTheme, KeyCursorTheme, KeyFont,
	KeyDocumentFont, KeyMonospaceFont}

// The values of KeyColorScheme.
const (
	SchemeDefault = "default"
	SchemeLight   = "prefer-light"
	SchemeDark    = "prefer-dark"
)

// AccentColors are the values of KeyAccentColor, in the order GNOME's settings show them.
var AccentColors = []string{"blue", "teal", "green", "yellow", "orange", "red", "pink", "purple", "slate"}

// Choices are the values a setting takes, for one that has a fixed set; nil for one that takes a name.
func Choices(key string) []string {
	switch key {
	case KeyColorScheme:
		return []string{SchemeDefault, SchemeLight, SchemeDark}
	case KeyAccentColor:
		return AccentColors
	}
	return nil
}

// CheckValue refuses a value a setting can't take.
func CheckValue(key, value string) error {
	if strings.TrimSpace(value) == "" {
		return fmt.Errorf("%s: give a value", strings.ToLower(KeyLabel(key)))
	}
	if c := Choices(key); c != nil && !slices.Contains(c, value) {
		names := make([]string, len(c))
		for i, v := range c {
			names[i] = ValueName(key, v)
		}
		return fmt.Errorf("%s: %q isn't %s", strings.ToLower(KeyLabel(key)), value, strings.Join(names, ", "))
	}
	return nil
}

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
	case KeyColorScheme:
		return "Style"
	case KeyAccentColor:
		return "Accent color"
	}
	return key
}

// ValueLabel names a setting's value for people: "Dark" for prefer-dark, "Teal" for teal. Names of themes
// and fonts are as they are.
func ValueLabel(key, value string) string {
	switch key {
	case KeyColorScheme:
		switch value {
		case SchemeDefault:
			return "Default"
		case SchemeLight:
			return "Light"
		case SchemeDark:
			return "Dark"
		}
	case KeyAccentColor:
		if value != "" {
			return strings.ToUpper(value[:1]) + value[1:]
		}
	}
	return value
}

// ValueName is the word the command line takes for a value: "dark" for prefer-dark.
func ValueName(key, value string) string {
	if key == KeyColorScheme {
		return strings.ToLower(ValueLabel(key, value))
	}
	return value
}

// ParseValue reads a value as the command line gives it: "dark", "light" or "default" for the style, the
// value itself for anything else.
func ParseValue(key, s string) string {
	if key == KeyColorScheme {
		for _, v := range Choices(key) {
			if strings.EqualFold(s, ValueName(key, v)) {
				return v
			}
		}
	}
	if Choices(key) != nil {
		return strings.ToLower(s)
	}
	return s
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

// GNOME changes its settings with gsettings. A change applies at once, in Flatpak apps too (through the
// settings portal).
type GNOME struct {
	Runner run.Runner

	once sync.Once
	keys map[string]bool // the keys of gnomeSchema, or nil when gsettings couldn't list them
}

const gnomeSchema = "org.gnome.desktop.interface"

// gnomeKeys are the settings uas changes, by key. Newer ones are only there from a GNOME release on, and
// are checked for: color-scheme from GNOME 42, accent-color from GNOME 47.
var gnomeKeys = map[string]struct {
	schema, key string
	newer       bool
}{
	KeyIconTheme:     {gnomeSchema, "icon-theme", false},
	KeyCursorTheme:   {gnomeSchema, "cursor-theme", false},
	KeyFont:          {gnomeSchema, "font-name", false},
	KeyDocumentFont:  {gnomeSchema, "document-font-name", false},
	KeyMonospaceFont: {gnomeSchema, "monospace-font-name", false},
	KeyColorScheme:   {gnomeSchema, "color-scheme", true},
	KeyAccentColor:   {gnomeSchema, "accent-color", true},
}

func (*GNOME) Name() string { return "gnome" }

func (g *GNOME) Supports(key string) bool {
	k, ok := gnomeKeys[key]
	if !ok {
		return false
	}
	if !k.newer {
		return true
	}
	g.once.Do(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		out, err := g.Runner.Run(ctx, "gsettings", []string{"list-keys", gnomeSchema}, nil)
		if err != nil {
			return
		}
		g.keys = map[string]bool{}
		for _, l := range strings.Fields(out) {
			g.keys[l] = true
		}
	})
	// When gsettings can't say, the setting is tried, and fails as a change of it would anyway.
	return g.keys == nil || g.keys[k.key]
}

func (g *GNOME) Get(ctx context.Context, key string) (string, error) {
	if !g.Supports(key) {
		return "", ErrUnsupported
	}
	k := gnomeKeys[key]
	out, err := g.Runner.Run(ctx, "gsettings", []string{"get", k.schema, k.key}, nil)
	if err != nil {
		return "", err
	}
	return parseGVariantString(strings.TrimSpace(out)), nil
}

func (g *GNOME) Set(ctx context.Context, key, value string) error {
	if !g.Supports(key) {
		return ErrUnsupported
	}
	k := gnomeKeys[key]
	_, err := g.Runner.Run(ctx, "gsettings", []string{"set", k.schema, k.key, quoteGVariant(value)}, nil)
	return err
}

func (g *GNOME) Reset(ctx context.Context, key string) error {
	if !g.Supports(key) {
		return ErrUnsupported
	}
	k := gnomeKeys[key]
	_, err := g.Runner.Run(ctx, "gsettings", []string{"reset", k.schema, k.key}, nil)
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
