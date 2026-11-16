package appearance

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/tyler-builds/uas/internal/client/run"
)

// Setting keys: what "Use" changes. They are GNOME's names for its settings; other desktops map them to
// their own.
const (
	KeyIconTheme   = "icon-theme"
	KeyCursorTheme = "cursor-theme"
	// KeyGTKTheme is the GTK theme, by its folder's name: what GTK 3 apps, and GTK 4 apps that don't use
	// libadwaita, are drawn with. uas sets only this setting (see Installer.Use).
	KeyGTKTheme = "gtk-theme"
	// KeyShellTheme is the GNOME Shell theme, by its folder's name, as the User Themes extension takes it:
	// what draws the top bar, the overview and the Shell's menus. "" is the Shell's own look. GNOME applies
	// it only while User Themes runs (see UserThemes).
	KeyShellTheme    = "shell-theme"
	KeyFont          = "font-name"           // the interface font
	KeyDocumentFont  = "document-font-name"  // the font of documents
	KeyMonospaceFont = "monospace-font-name" // the fixed-width font, as terminals use
	// KeyColorScheme is the light or dark style: SchemeDefault, SchemeLight or SchemeDark.
	KeyColorScheme = "color-scheme"
	// KeyAccentColor is the accent color, one of AccentColors.
	KeyAccentColor = "accent-color"
	// KeyWallpaper is the wallpaper: a picture's path (or, for one that isn't a local file, its URI). On
	// GNOME, setting it sets the lock screen's picture too, as GNOME's settings do.
	KeyWallpaper = "wallpaper"
	// KeyWallpaperDark is the wallpaper with the dark style, as KeyWallpaper.
	KeyWallpaperDark = "wallpaper-dark"
	// KeyWallpaperFit is how the wallpaper fills the screen, one of WallpaperFits.
	KeyWallpaperFit = "wallpaper-fit"
)

// WallpaperKeys are the settings of the wallpaper. They are listed, saved and reverted as one: "Wallpaper".
var WallpaperKeys = []string{KeyWallpaper, KeyWallpaperDark, KeyWallpaperFit}

// IsWallpaperKey reports whether a setting is one of WallpaperKeys.
func IsWallpaperKey(key string) bool { return slices.Contains(WallpaperKeys, key) }

// GroupKey is the setting a setting is listed and reverted under: KeyWallpaper for the wallpaper's, the
// setting itself for any other.
func GroupKey(key string) string {
	if IsWallpaperKey(key) {
		return KeyWallpaper
	}
	return key
}

// The values of KeyWallpaperFit, as GNOME's picture-options has them.
const (
	FitZoom      = "zoom"
	FitCentered  = "centered"
	FitScaled    = "scaled"
	FitStretched = "stretched"
	FitSpanned   = "spanned"
	FitTiled     = "wallpaper"
	FitNone      = "none"
)

// WallpaperFits are the values of KeyWallpaperFit; FitZoom, the first, is the default.
var WallpaperFits = []string{FitZoom, FitCentered, FitScaled, FitStretched, FitSpanned, FitTiled, FitNone}

// FontKeys are the settings a font can be used for.
var FontKeys = []string{KeyFont, KeyDocumentFont, KeyMonospaceFont}

// SettingKeys are every setting uas can change, in the order they are listed and applied: the style and
// accent first, as everything else is seen in them.
var SettingKeys = []string{KeyColorScheme, KeyAccentColor, KeyGTKTheme, KeyShellTheme, KeyIconTheme, KeyCursorTheme,
	KeyFont, KeyDocumentFont, KeyMonospaceFont, KeyWallpaper, KeyWallpaperDark, KeyWallpaperFit}

// EmptyValue reports whether "" is a value of a setting rather than no value: the Shell theme's, where it is
// the Shell's own look.
func EmptyValue(key string) bool { return key == KeyShellTheme }

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
	case KeyWallpaperFit:
		return WallpaperFits
	}
	return nil
}

// CheckValue refuses a value a setting can't take.
func CheckValue(key, value string) error {
	if value == "" && EmptyValue(key) {
		return nil
	}
	if strings.TrimSpace(value) == "" {
		return fmt.Errorf("%s: give a value", KeyWord(key))
	}
	if c := Choices(key); c != nil && !slices.Contains(c, value) {
		names := make([]string, len(c))
		for i, v := range c {
			names[i] = ValueName(key, v)
		}
		return fmt.Errorf("%s: %q isn't %s", KeyWord(key), value, strings.Join(names, ", "))
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
	case KeyGTKTheme:
		return "GTK theme"
	case KeyShellTheme:
		return "Shell theme"
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
	case KeyWallpaper:
		return "Wallpaper"
	case KeyWallpaperDark:
		return "Dark wallpaper"
	case KeyWallpaperFit:
		return "Wallpaper fit"
	}
	return key
}

// KeyWord is KeyLabel in a sentence: lower case, but for a name such as GTK.
func KeyWord(key string) string { return lowerWords(KeyLabel(key)) }

// lowerWords puts a label in lower case, but its words that are names in capitals ("GTK theme").
func lowerWords(s string) string {
	words := strings.Fields(s)
	for i, w := range words {
		if w != strings.ToUpper(w) || len(w) == 1 {
			words[i] = strings.ToLower(w)
		}
	}
	return strings.Join(words, " ")
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
	case KeyWallpaperFit:
		if value == FitTiled {
			return "Tiled"
		}
		if value != "" {
			return strings.ToUpper(value[:1]) + value[1:]
		}
	case KeyWallpaper, KeyWallpaperDark:
		return PictureName(value)
	case KeyShellTheme:
		if value == "" {
			return "Default"
		}
	}
	return value
}

// ValueName is the word the command line takes for a value: "dark" for prefer-dark.
func ValueName(key, value string) string {
	if key == KeyColorScheme || (key == KeyShellTheme && value == "") {
		return strings.ToLower(ValueLabel(key, value))
	}
	return value
}

// ParseValue reads a value as the command line gives it: "dark", "light" or "default" for the style, the
// value itself for anything else.
func ParseValue(key, s string) string {
	if key == KeyShellTheme && strings.EqualFold(s, "default") {
		return "" // the Shell's own look; a Shell theme called Default can't be named on the command line
	}
	if key == KeyWallpaperFit && strings.EqualFold(s, "tiled") {
		return FitTiled
	}
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
	// ExtensionDirs are where GNOME Shell extensions are installed: the user's
	// ($XDG_DATA_HOME/gnome-shell/extensions), then the system's under $XDG_DATA_DIRS. An extension installed
	// for the user keeps its settings' schema in its own schemas folder, which plain gsettings can't see, so
	// the Shell theme's setting is read there with --schemadir. Nil finds them from the environment.
	ExtensionDirs []string

	mu   sync.Mutex
	keys map[string]map[string]bool // the keys of each schema asked about, nil when gsettings couldn't list them
}

const (
	gnomeSchema      = "org.gnome.desktop.interface"
	gnomeBackground  = "org.gnome.desktop.background"
	gnomeScreensaver = "org.gnome.desktop.screensaver" // the lock screen
	gnomePictureURI  = "picture-uri"
	gnomePictureDark = "picture-uri-dark"
	gnomePictureFit  = "picture-options"
	// userThemesSchema is the User Themes extension's settings, whose name is the Shell theme.
	userThemesSchema = "org.gnome.shell.extensions.user-theme"
)

// gnomeKeys are the settings uas changes, by key. Newer ones are only there from a GNOME release on, and
// are checked for: color-scheme from GNOME 42, accent-color from GNOME 47.
var gnomeKeys = map[string]struct {
	schema, key string
	newer       bool
}{
	KeyIconTheme:     {gnomeSchema, "icon-theme", false},
	KeyCursorTheme:   {gnomeSchema, "cursor-theme", false},
	KeyGTKTheme:      {gnomeSchema, "gtk-theme", false},
	KeyShellTheme:    {userThemesSchema, "name", false},
	KeyFont:          {gnomeSchema, "font-name", false},
	KeyDocumentFont:  {gnomeSchema, "document-font-name", false},
	KeyMonospaceFont: {gnomeSchema, "monospace-font-name", false},
	KeyColorScheme:   {gnomeSchema, "color-scheme", true},
	KeyAccentColor:   {gnomeSchema, "accent-color", true},
	KeyWallpaper:     {gnomeBackground, gnomePictureURI, false},
	KeyWallpaperDark: {gnomeBackground, gnomePictureDark, true},
	KeyWallpaperFit:  {gnomeBackground, gnomePictureFit, false},
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
	keys := g.listKeys(k.schema)
	// When gsettings can't say, the setting is tried, and fails as a change of it would anyway.
	return keys == nil || keys[k.key]
}

// listKeys returns the keys gsettings lists for a schema, asking once; nil when it couldn't list them.
func (g *GNOME) listKeys(schema string) map[string]bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	keys, asked := g.keys[schema]
	if !asked {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if out, err := g.Runner.Run(ctx, "gsettings", []string{"list-keys", schema}, nil); err == nil {
			keys = map[string]bool{}
			for _, l := range strings.Fields(out) {
				keys[l] = true
			}
		}
		if g.keys == nil {
			g.keys = map[string]map[string]bool{}
		}
		g.keys[schema] = keys
	}
	return keys
}

// extensionDirs are ExtensionDirs, or the user's and the system's extension folders.
func (g *GNOME) extensionDirs() []string {
	if g.ExtensionDirs != nil {
		return g.ExtensionDirs
	}
	var out []string
	data := os.Getenv("XDG_DATA_HOME")
	if !filepath.IsAbs(data) {
		if home, err := os.UserHomeDir(); err == nil {
			data = filepath.Join(home, ".local", "share")
		}
	}
	if data != "" {
		out = append(out, filepath.Join(data, "gnome-shell", "extensions"))
	}
	sys := filepath.SplitList(os.Getenv("XDG_DATA_DIRS"))
	if len(sys) == 0 {
		sys = []string{"/usr/local/share", "/usr/share"}
	}
	for _, d := range sys {
		if filepath.IsAbs(d) {
			out = append(out, filepath.Join(d, "gnome-shell", "extensions"))
		}
	}
	return out
}

// gsettings returns gsettings' arguments for a command on a setting: for the Shell theme, with --schemadir
// when the system's schemas don't have User Themes' and the extension's own folder does, as when it was
// installed for the user from extensions.gnome.org.
func (g *GNOME) gsettings(schema string, args ...string) []string {
	if schema != userThemesSchema {
		return args
	}
	if keys := g.listKeys(schema); keys != nil && keys["name"] {
		return args
	}
	for _, d := range g.extensionDirs() {
		dir := filepath.Join(d, UserThemesUUID, "schemas")
		if fi, err := os.Stat(filepath.Join(dir, "gschemas.compiled")); err == nil && fi.Mode().IsRegular() {
			return append([]string{"--schemadir", dir}, args...)
		}
	}
	return args
}

// shellErr explains a failure to read or change the Shell theme's setting, which is User Themes'.
func shellErr(key string, err error) error {
	if err != nil && key == KeyShellTheme {
		return fmt.Errorf("the User Themes extension's settings can't be read (is it installed?): %w", err)
	}
	return err
}

func (g *GNOME) Get(ctx context.Context, key string) (string, error) {
	if !g.Supports(key) {
		return "", ErrUnsupported
	}
	k := gnomeKeys[key]
	out, err := g.Runner.Run(ctx, "gsettings", g.gsettings(k.schema, "get", k.schema, k.key), nil)
	if err != nil {
		return "", shellErr(key, err)
	}
	v := parseGVariantString(strings.TrimSpace(out))
	if key == KeyWallpaper || key == KeyWallpaperDark {
		v = URIPath(v)
	}
	return v, nil
}

func (g *GNOME) Set(ctx context.Context, key, value string) error {
	if !g.Supports(key) {
		return ErrUnsupported
	}
	k := gnomeKeys[key]
	if key == KeyWallpaper || key == KeyWallpaperDark {
		value = PathURI(value)
	}
	if _, err := g.Runner.Run(ctx, "gsettings", g.gsettings(k.schema, "set", k.schema, k.key, quoteGVariant(value)),
		nil); err != nil {
		return shellErr(key, err)
	}
	if key == KeyWallpaper {
		// The lock screen shows the wallpaper too, as when it is changed in GNOME's settings. A system
		// without the lock screen's schema still has its wallpaper changed.
		_, _ = g.Runner.Run(ctx, "gsettings", []string{"set", gnomeScreensaver, gnomePictureURI, quoteGVariant(value)}, nil)
	}
	return nil
}

func (g *GNOME) Reset(ctx context.Context, key string) error {
	if !g.Supports(key) {
		return ErrUnsupported
	}
	k := gnomeKeys[key]
	if _, err := g.Runner.Run(ctx, "gsettings", g.gsettings(k.schema, "reset", k.schema, k.key), nil); err != nil {
		return shellErr(key, err)
	}
	if key == KeyWallpaper {
		_, _ = g.Runner.Run(ctx, "gsettings", []string{"reset", gnomeScreensaver, gnomePictureURI}, nil)
	}
	return nil
}

// PathURI is a picture's path as the file:// URI the desktop's settings take, with everything but the
// characters a URI's path may have escaped: spaces, "#", "%", "?" and every byte of a non-ASCII letter.
// A value that isn't an absolute path (a URI already, or "") is as it is.
func PathURI(p string) string {
	if !strings.HasPrefix(p, "/") {
		return p
	}
	const keep = "-._~!$&'()*+,;=:@/"
	var b strings.Builder
	b.WriteString("file://")
	for i := 0; i < len(p); i++ {
		c := p[i]
		if c < 0x80 && ('a' <= c && c <= 'z' || 'A' <= c && c <= 'Z' || '0' <= c && c <= '9' ||
			strings.IndexByte(keep, c) >= 0) {
			b.WriteByte(c)
			continue
		}
		fmt.Fprintf(&b, "%%%02X", c)
	}
	return b.String()
}

// URIPath is the path of a file:// URI, unescaped. Any other value (another kind of URI, or "") is as it
// is.
func URIPath(uri string) string {
	if !strings.HasPrefix(uri, "file://") {
		return uri
	}
	u, err := url.Parse(uri)
	if err != nil || (u.Host != "" && u.Host != "localhost") || !strings.HasPrefix(u.Path, "/") {
		return uri
	}
	return u.Path
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
// apps. It has no command for fonts or GTK themes, so those are installed only: Plasma sets the GTK theme
// through a module of its own (kde-gtk-config) that writes GTK's files itself, which uas leaves alone.
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
