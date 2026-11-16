package appearance

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

// A preset is a look saved under a name: a GTK theme, a GNOME Shell theme, a theme for icons and cursors,
// fonts, the light or dark style, the accent color and the wallpaper, each optional. Applying it sets every part it has and leaves the rest alone. Presets are
// the user's own, so they live with uas's settings, one JSON file each in ~/.config/uas/presets; the look
// from before the last apply, for Undo, is uas's, and lives with its state.

// Part names: the keys of Preset.Parts, which are also the setting names the command line takes.
const (
	PartScheme = "scheme"
	PartAccent = "accent"
	PartGTK    = "gtk" // a GTK theme, by name
	// PartShell is a GNOME Shell theme, by name; an empty value is the Shell's own look. It stores only the
	// theme (and where it came from), never the User Themes extension or its other settings.
	PartShell     = "shell"
	PartIcons     = "icons"
	PartCursor    = "cursor"
	PartInterface = "interface"
	PartDocument  = "document"
	PartMonospace = "monospace"
	PartWallpaper = "wallpaper" // a wallpaper: Part.Light, Part.Dark and Part.Fit, rather than Value
)

// PartOrder is the order parts are listed and applied in: the style and accent first, as everything else
// is seen in them, and the wallpaper last.
var PartOrder = []string{PartScheme, PartAccent, PartGTK, PartShell, PartIcons, PartCursor, PartInterface,
	PartDocument, PartMonospace, PartWallpaper}

// partKeys are the parts that are one desktop setting, by part, and the wallpaper, which is
// KeyWallpaper and the other WallpaperKeys. Parts not here are ones this version of uas keeps in a preset's
// file but can't apply.
var partKeys = map[string]string{
	PartScheme:    KeyColorScheme,
	PartAccent:    KeyAccentColor,
	PartGTK:       KeyGTKTheme,
	PartShell:     KeyShellTheme,
	PartIcons:     KeyIconTheme,
	PartCursor:    KeyCursorTheme,
	PartInterface: KeyFont,
	PartDocument:  KeyDocumentFont,
	PartMonospace: KeyMonospaceFont,
	PartWallpaper: KeyWallpaper,
}

// PartKey returns the setting a part sets, and false for a part this version of uas can't apply.
func PartKey(part string) (string, bool) {
	k, ok := partKeys[part]
	return k, ok
}

// KeyPart returns the part that sets a setting, or "".
func KeyPart(key string) string {
	key = GroupKey(key)
	for p, k := range partKeys {
		if k == key {
			return p
		}
	}
	return ""
}

// PartLabel names a part for people.
func PartLabel(part string) string {
	if k, ok := partKeys[part]; ok {
		return KeyLabel(k)
	}
	switch part {
	case PartWallpaper:
		return "Wallpaper"
	}
	return part
}

// PartWord is PartLabel in a sentence: lower case, but for a name such as GTK.
func PartWord(part string) string { return lowerWords(PartLabel(part)) }

// PresetFormat is the version of the preset file format this uas writes.
const PresetFormat = 1

// Preset is a look saved under a name.
type Preset struct {
	Format    int       `json:"uas_preset"`
	Name      string    `json:"name"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
	// Parts are what it sets, by part name (PartIcons and the like). A part that is missing is left as it is.
	Parts map[string]Part `json:"parts"`

	file string // the file it was read from
}

// Part is one part of a preset.
type Part struct {
	// Value is what the setting is set to: a theme's name, a font ("Inter 11"), or one of Choices. For the
	// Shell theme, "" (a part with no value) is the Shell's own look.
	Value string `json:"value,omitempty"`
	// From is where it can be installed from when it isn't on the system; nil for one that came with the
	// system.
	From *Origin `json:"from,omitempty"`
	// Light, Dark and Fit are a wallpaper's, in place of Value: its picture, its picture with the dark style
	// ("" is Light) and how they fit the screen (one of WallpaperFits; "" is FitZoom). A picture is a
	// stored one's "sha256:<hex>" or a system one's path (see WallpaperRef).
	Light string `json:"light,omitempty"`
	Dark  string `json:"dark,omitempty"`
	Fit   string `json:"fit,omitempty"`
	// GTK4 is for a GTK theme part: false leaves the theme's files for GTK 4 apps out of ~/.config/gtk-4.0
	// and puts back what uas moved aside there, as a Previous look does when uas had put none there before
	// the apply. Missing (nil), the theme's GTK 4 files are copied in when it has them, as Use does.
	GTK4 *bool `json:"gtk4,omitempty"`

	// rest are the fields this version of uas doesn't know, kept as they are.
	rest map[string]json.RawMessage
}

// Wallpaper is a wallpaper part's pictures and fit, as its references.
func (p Part) Wallpaper() Wallpaper { return Wallpaper{Light: p.Light, Dark: p.Dark, Fit: p.Fit} }

// Origin is a font or theme of the catalog.
type Origin struct {
	ID string `json:"id"` // the catalog ID, such as "pling:1166289"
	// Variant is the theme that picks the file to install, for a theme.
	Variant string `json:"variant,omitempty"`
}

func (p *Part) UnmarshalJSON(b []byte) error {
	var all map[string]json.RawMessage
	if err := json.Unmarshal(b, &all); err != nil {
		return err
	}
	*p = Part{}
	if v, ok := all["value"]; ok {
		if err := json.Unmarshal(v, &p.Value); err != nil {
			return fmt.Errorf("value: %w", err)
		}
		delete(all, "value")
	}
	if v, ok := all["from"]; ok {
		if err := json.Unmarshal(v, &p.From); err != nil {
			return fmt.Errorf("from: %w", err)
		}
		delete(all, "from")
	}
	if v, ok := all["gtk4"]; ok {
		if err := json.Unmarshal(v, &p.GTK4); err != nil {
			return fmt.Errorf("gtk4: %w", err)
		}
		delete(all, "gtk4")
	}
	for name, dst := range map[string]*string{"light": &p.Light, "dark": &p.Dark, "fit": &p.Fit} {
		if v, ok := all[name]; ok {
			if err := json.Unmarshal(v, dst); err != nil {
				return fmt.Errorf("%s: %w", name, err)
			}
			delete(all, name)
		}
	}
	if len(all) > 0 {
		p.rest = all
	}
	return nil
}

func (p Part) MarshalJSON() ([]byte, error) {
	// value first, then from, a wallpaper's fields, then the rest by name, so the file reads in that order.
	var b bytes.Buffer
	b.WriteByte('{')
	field := func(name string, v any) error {
		raw, err := json.Marshal(v)
		if err != nil {
			return err
		}
		if b.Len() > 1 {
			b.WriteByte(',')
		}
		k, _ := json.Marshal(name)
		b.Write(k)
		b.WriteByte(':')
		b.Write(raw)
		return nil
	}
	if p.Value != "" {
		if err := field("value", p.Value); err != nil {
			return nil, err
		}
	}
	if p.From != nil {
		if err := field("from", p.From); err != nil {
			return nil, err
		}
	}
	for _, f := range []struct{ name, v string }{{"light", p.Light}, {"dark", p.Dark}, {"fit", p.Fit}} {
		if f.v != "" {
			if err := field(f.name, f.v); err != nil {
				return nil, err
			}
		}
	}
	if p.GTK4 != nil {
		if err := field("gtk4", *p.GTK4); err != nil {
			return nil, err
		}
	}
	names := make([]string, 0, len(p.rest))
	for k := range p.rest {
		names = append(names, k)
	}
	slices.Sort(names)
	for _, k := range names {
		if err := field(k, p.rest[k]); err != nil {
			return nil, err
		}
	}
	b.WriteByte('}')
	return b.Bytes(), nil
}

// Sets reports whether a preset sets a part.
func (p Preset) Sets(part string) bool {
	_, ok := p.Parts[part]
	return ok
}

// PartNames are the parts a preset sets, in PartOrder, then any this version of uas doesn't know.
func (p Preset) PartNames() []string {
	var out []string
	for _, n := range PartOrder {
		if p.Sets(n) {
			out = append(out, n)
		}
	}
	var unknown []string
	for n := range p.Parts {
		if !slices.Contains(PartOrder, n) {
			unknown = append(unknown, n)
		}
	}
	slices.Sort(unknown)
	return append(out, unknown...)
}

// Presets are the presets in a folder, one file each.
type Presets struct {
	Dir string
	// UndoFile holds the look from before the last apply.
	UndoFile string
	// Now is the clock; nil is time.Now.
	Now func() time.Time
	// Changed, when set, is called after a preset is saved, changed or deleted: the session removes the
	// stored wallpapers no preset uses any more (Installer.PruneWallpapers).
	Changed func()
}

// NewPresets returns the user's presets: $XDG_CONFIG_HOME/uas/presets, with the look to undo to in
// $XDG_DATA_HOME/uas/previous-look.json.
func NewPresets(configDir, dataDir string) *Presets {
	return &Presets{Dir: filepath.Join(configDir, "uas", "presets"),
		UndoFile: filepath.Join(dataDir, "uas", "previous-look.json")}
}

func (s *Presets) now() time.Time {
	if s.Now != nil {
		return s.Now().UTC().Truncate(time.Second)
	}
	return time.Now().UTC().Truncate(time.Second)
}

// ErrNoPreset is returned for a name no preset has.
var ErrNoPreset = errors.New("no preset is called that")

// ExistsError is returned when a name is taken.
type ExistsError struct{ Name string }

func (e *ExistsError) Error() string { return fmt.Sprintf("a preset called %q exists already", e.Name) }

// maxPresetName is the longest a name may be, in characters.
const maxPresetName = 64

// CheckPresetName refuses a name that can't be a preset's, and returns it trimmed.
func CheckPresetName(name string) (string, error) {
	name = strings.TrimSpace(name)
	switch {
	case name == "":
		return "", errors.New("give the preset a name")
	case utf8.RuneCountInString(name) > maxPresetName:
		return "", fmt.Errorf("a preset's name can be at most %d characters", maxPresetName)
	case strings.IndexFunc(name, unicode.IsControl) >= 0 || !utf8.ValidString(name):
		return "", errors.New("a preset's name can't have control characters")
	}
	return name, nil
}

// List returns the presets, by name. A file that can't be read is left out, and named in the error.
func (s *Presets) List() ([]Preset, error) {
	entries, err := os.ReadDir(s.Dir)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var out []Preset
	var errs []error
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") || strings.HasPrefix(e.Name(), ".") {
			continue
		}
		p, err := readPreset(filepath.Join(s.Dir, e.Name()))
		if err != nil {
			errs = append(errs, err)
			continue
		}
		out = append(out, p)
	}
	slices.SortFunc(out, func(a, b Preset) int {
		return strings.Compare(strings.ToLower(a.Name), strings.ToLower(b.Name))
	})
	return out, errors.Join(errs...)
}

func readPreset(path string) (Preset, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return Preset{}, err
	}
	var p Preset
	if err := json.Unmarshal(b, &p); err != nil {
		return Preset{}, fmt.Errorf("preset %s: %w", path, err)
	}
	if strings.TrimSpace(p.Name) == "" {
		return Preset{}, fmt.Errorf("preset %s has no name", path)
	}
	if p.Parts == nil {
		p.Parts = map[string]Part{}
	}
	p.file = path
	return p, nil
}

// Get returns the preset of a name, whatever its case.
func (s *Presets) Get(name string) (Preset, error) {
	list, _ := s.List()
	for _, p := range list {
		if strings.EqualFold(p.Name, strings.TrimSpace(name)) {
			return p, nil
		}
	}
	return Preset{}, fmt.Errorf("%w: %q", ErrNoPreset, strings.TrimSpace(name))
}

// Save writes a new preset. A preset of the same name, whatever its case, is replaced when replace is set;
// otherwise Save returns an *ExistsError.
func (s *Presets) Save(p Preset, replace bool) (Preset, error) {
	name, err := CheckPresetName(p.Name)
	if err != nil {
		return Preset{}, err
	}
	p.Name = name
	p.file = ""
	if old, err := s.Get(name); err == nil {
		if !replace {
			return Preset{}, &ExistsError{Name: old.Name}
		}
		p.file = old.file
	}
	now := s.now()
	p.CreatedAt, p.UpdatedAt = now, now
	if err := s.write(&p); err != nil {
		return Preset{}, err
	}
	s.changed()
	return p, nil
}

// Update changes a preset and writes it.
func (s *Presets) Update(name string, change func(*Preset) error) (Preset, error) {
	p, err := s.Get(name)
	if err != nil {
		return Preset{}, err
	}
	if err := change(&p); err != nil {
		return Preset{}, err
	}
	p.UpdatedAt = s.now()
	if err := s.write(&p); err != nil {
		return Preset{}, err
	}
	s.changed()
	return p, nil
}

func (s *Presets) changed() {
	if s.Changed != nil {
		s.Changed()
	}
}

// Rename gives a preset a new name, which no other preset may have; a change of case is allowed.
func (s *Presets) Rename(name, to string) (Preset, error) {
	p, err := s.Get(name)
	if err != nil {
		return Preset{}, err
	}
	to, err = CheckPresetName(to)
	if err != nil {
		return Preset{}, err
	}
	if other, err := s.Get(to); err == nil && other.file != p.file {
		return Preset{}, &ExistsError{Name: other.Name}
	}
	old := p.file
	p.Name, p.UpdatedAt, p.file = to, s.now(), ""
	if err := s.write(&p); err != nil {
		return Preset{}, err
	}
	if p.file != old {
		if err := os.Remove(old); err != nil && !errors.Is(err, os.ErrNotExist) {
			return p, err
		}
	}
	return p, nil
}

// Copy makes a copy of a preset under a new name, or when to is empty, under the first free name of
// "<name> copy", "<name> copy 2" and so on.
func (s *Presets) Copy(name, to string) (Preset, error) {
	p, err := s.Get(name)
	if err != nil {
		return Preset{}, err
	}
	if strings.TrimSpace(to) == "" {
		to = s.CopyName(p.Name)
	}
	c := Preset{Format: PresetFormat, Name: to, Parts: map[string]Part{}}
	for k, v := range p.Parts {
		c.Parts[k] = v
	}
	return s.Save(c, false)
}

// CopyName is the first name of "<name> copy", "<name> copy 2" and so on that no preset has.
func (s *Presets) CopyName(name string) string {
	base := name + " copy"
	if n := utf8.RuneCountInString(base); n > maxPresetName-3 {
		base = string([]rune(name)[:maxPresetName-8]) + " copy"
	}
	try := base
	for i := 2; ; i++ {
		if _, err := s.Get(try); err != nil {
			return try
		}
		try = fmt.Sprintf("%s %d", base, i)
	}
}

// Delete removes a preset.
func (s *Presets) Delete(name string) error {
	p, err := s.Get(name)
	if err != nil {
		return err
	}
	if err := os.Remove(p.file); err != nil {
		return err
	}
	s.changed()
	return nil
}

// write writes a preset to its file, or to a new file named for it.
func (s *Presets) write(p *Preset) error {
	if p.Format == 0 {
		p.Format = PresetFormat
	}
	if p.Parts == nil {
		p.Parts = map[string]Part{}
	}
	if err := os.MkdirAll(s.Dir, 0o755); err != nil {
		return err
	}
	if p.file == "" {
		p.file = s.freeFile(Slug(p.Name))
	}
	b, err := json.MarshalIndent(p, "", "  ")
	if err != nil {
		return err
	}
	return writeFile(p.file, append(b, '\n'))
}

// freeFile is the file for a new preset of a slug: <slug>.json, or <slug>-2.json and so on when that is
// taken.
func (s *Presets) freeFile(slug string) string {
	try := filepath.Join(s.Dir, slug+".json")
	for i := 2; ; i++ {
		if _, err := os.Lstat(try); errors.Is(err, os.ErrNotExist) {
			return try
		}
		try = filepath.Join(s.Dir, fmt.Sprintf("%s-%d.json", slug, i))
	}
}

// Slug is a preset's name as a file name: lower case letters and digits, with dashes between words.
func Slug(name string) string {
	var b strings.Builder
	dash := false
	for _, r := range strings.ToLower(name) {
		if b.Len() >= 48 {
			break
		}
		switch {
		case r < utf8.RuneSelf && (unicode.IsLetter(r) || unicode.IsDigit(r)):
			if dash && b.Len() > 0 {
				b.WriteByte('-')
			}
			dash = false
			b.WriteRune(r)
		default:
			dash = true
		}
	}
	if b.Len() == 0 {
		return "preset"
	}
	return b.String()
}

// writeFile writes a file atomically.
func writeFile(path string, b []byte) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), ".tmp-*")
	if err != nil {
		return err
	}
	if _, err := tmp.Write(b); err != nil {
		tmp.Close()
		os.Remove(tmp.Name())
		return err
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmp.Name())
		return err
	}
	if err := os.Chmod(tmp.Name(), 0o644); err != nil {
		os.Remove(tmp.Name())
		return err
	}
	return os.Rename(tmp.Name(), path)
}

// Undo is the look from before the last apply of a preset.
type Undo struct {
	// Applied is the name of the preset that was applied.
	Applied string    `json:"applied"`
	At      time.Time `json:"at"`
	// Look holds the values the apply changed, as they were before it.
	Look Preset `json:"look"`
}

// Undo returns the look from before the last apply, and false when there is none.
func (s *Presets) Undo() (Undo, bool, error) {
	b, err := os.ReadFile(s.UndoFile)
	if errors.Is(err, os.ErrNotExist) {
		return Undo{}, false, nil
	}
	if err != nil {
		return Undo{}, false, err
	}
	var u Undo
	if err := json.Unmarshal(b, &u); err != nil {
		return Undo{}, false, fmt.Errorf("%s: %w", s.UndoFile, err)
	}
	return u, true, nil
}

func (s *Presets) saveUndo(u Undo) error {
	if err := os.MkdirAll(filepath.Dir(s.UndoFile), 0o755); err != nil {
		return err
	}
	b, err := json.MarshalIndent(u, "", "  ")
	if err != nil {
		return err
	}
	return writeFile(s.UndoFile, append(b, '\n'))
}

func (s *Presets) clearUndo() error {
	if err := os.Remove(s.UndoFile); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	s.changed()
	return nil
}
