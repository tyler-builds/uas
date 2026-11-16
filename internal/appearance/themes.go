package appearance

import (
	"bufio"
	"fmt"
	"io"
	"path"
	"regexp"
	"slices"
	"sort"
	"strings"

	"github.com/tyler-builds/uas/internal/catalog"
)

// Theme is one icon, cursor or GTK theme found in an archive.
type Theme struct {
	// Name is the theme's folder name, which is its name on the system: what GNOME's icon-theme setting
	// holds ("Papirus-Dark").
	Name string `json:"name"`
	// Path is the folder's path in the archive.
	Path string `json:"path"`
	// Title is the name its index.theme gives, for people ("Papirus Dark"), if any.
	Title string `json:"title,omitempty"`
	// Icons is true for an icon theme: an index.theme with an [Icon Theme] section that lists icon folders.
	Icons bool `json:"icons,omitempty"`
	// Cursors is true for a theme with a cursors folder. Some icon themes ship their own cursors.
	Cursors bool `json:"cursors,omitempty"`
	// Inherits are the themes its index.theme falls back on.
	Inherits []string `json:"inherits,omitempty"`
	// GTK is true for a GTK theme: a gtk-3.0 or gtk-4.0 folder with a gtk.css or gtk-dark.css in it. What
	// else the folder holds (gnome-shell, gtk-2.0, xfwm4, cinnamon and the like) is part of the theme and
	// is written with it, but uas uses only the GTK theme.
	GTK bool `json:"gtk,omitempty"`
}

// Is reports whether the theme is of kind: an icon theme (KindIconTheme), a cursor theme (KindCursorTheme)
// or a GTK theme (KindGTKTheme).
func (t Theme) Is(kind catalog.Kind) bool {
	switch kind {
	case catalog.KindIconTheme:
		return t.Icons
	case catalog.KindCursorTheme:
		return t.Cursors
	case catalog.KindGTKTheme:
		return t.GTK
	}
	return false
}

// SameFolders reports whether two kinds of theme are installed into the same folder, and so share their
// names: icon and cursor themes go to the icons folder, GTK themes to the themes folder.
func SameFolders(a, b catalog.Kind) bool {
	return (a == catalog.KindGTKTheme) == (b == catalog.KindGTKTheme)
}

// Contents is what Inspect found in an archive.
type Contents struct {
	// Themes are the archive's themes, by name.
	Themes  []Theme
	Entries int
	Bytes   int64
	// Skipped are the links that are left out, with why: links that lead nowhere in the archive, or to a file
	// in LeftOut.
	Skipped map[string]string
	// LeftOut are the files and links left out because they could be run, with why: scripts, launchers,
	// build scripts and archives inside the archive (see forbidden). None is ever written.
	LeftOut map[string]string

	index  map[string]entry // every entry by name
	sorted []string         // the entries' names, sorted; see within
	dirs   map[string]bool  // every folder, named or implied by an entry inside it
}

// Of returns the archive's themes of kind.
func (c *Contents) Of(kind catalog.Kind) []Theme {
	var out []Theme
	for _, t := range c.Themes {
		if t.Is(kind) {
			out = append(out, t)
		}
	}
	return out
}

// ThemeName is what a theme's folder may be called: it becomes a folder in ~/.local/share/icons (or themes)
// and a value of the desktop's settings.
var ThemeName = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9 ._+@,()-]{0,127}$`)

// reservedThemes are theme names that mean something to every desktop: "default" picks the cursor theme
// apps fall back on, and hicolor and locolor are where every app's own icons are. A theme of that name in the
// user's folder would take over.
var reservedThemes = map[string]bool{"default": true, "hicolor": true, "locolor": true}

// reservedGTKThemes are the GTK themes built into GTK itself, which GTK loads from its own resources
// whatever a folder of that name holds, and "Default", which GTK 2 falls back on. A GTK theme of one of
// these names would install and never show, or confuse which is which.
var reservedGTKThemes = map[string]bool{"default": true, "adwaita": true, "adwaita-dark": true,
	"highcontrast": true, "highcontrastinverse": true, "emacs": true}

// Inspect reads the archive at p, checks every entry (see the package comment), and finds the icon, cursor
// and GTK themes in it.
//
// A theme is a folder with an index.theme whose [Icon Theme] section lists icon folders (an icon theme), or
// with a cursors folder (a cursor theme), or both; or a folder with a gtk-3.0 or gtk-4.0 folder that has a
// gtk.css or gtk-dark.css (a GTK theme). Inspect refuses an archive in which no theme can be found or the
// themes can't be told apart cleanly: one inside another, two of the same name, a theme with no folder of
// its own, or a name no desktop would take. Icon and cursor themes are judged apart from GTK themes, as they
// install to other folders: a GTK theme that holds an icon theme is no theme inside another, and an archive
// whose icon or cursor themes are clean keeps them when its GTK themes aren't (they are left out of it).
func Inspect(p string, lim Limits) (*Contents, error) {
	c := &Contents{LeftOut: map[string]string{}, index: map[string]entry{}, dirs: map[string]bool{}}
	indexFiles := map[string][]byte{} // the index.theme files, by folder
	err := walk(p, func(e entry, r io.Reader) error {
		c.Entries++
		if c.Entries > lim.MaxEntries {
			return refuse("it holds more than %d files", lim.MaxEntries)
		}
		if prev, ok := c.index[e.name]; ok {
			if prev.typ == typeDir && e.typ == typeDir {
				return nil
			}
			return refuse("%q is in it twice", e.name)
		}
		if _, ok := c.LeftOut[e.name]; ok {
			return refuse("%q is in it twice", e.name)
		}
		var head []byte
		switch e.typ {
		case typeSymlink, typeHardlink:
			if e.link == "" {
				return refuse("%q is a link to nothing", e.name)
			}
		case typeFile:
			if e.size > lim.MaxFileBytes {
				return refuse("%q is larger than %s", e.name, size(lim.MaxFileBytes))
			}
			c.Bytes += e.size
			if c.Bytes > lim.MaxBytes {
				return refuse("it unpacks to more than %s", size(lim.MaxBytes))
			}
			head = make([]byte, 4)
			n, _ := io.ReadFull(r, head)
			head = head[:n:n]
		}
		if e.typ != typeDir {
			// A file left out is kept out of the index altogether, so nothing that follows (links that lead
			// to it, folders written whole, the themes found) can see it, let alone write it.
			if why, refuses := forbidden(e.name, head); refuses || (why != "" && required(e.name)) {
				return refuse("it contains %s", why)
			} else if why != "" {
				c.LeftOut[e.name] = why
				return nil
			}
		}
		c.index[e.name] = e
		for d := path.Dir(e.name); d != "."; d = path.Dir(d) {
			c.dirs[d] = true
		}
		if e.typ != typeFile {
			if e.typ == typeDir {
				c.dirs[e.name] = true
			}
			return nil
		}
		if path.Base(e.name) == "index.theme" && e.size <= 256<<10 {
			rest, err := io.ReadAll(io.LimitReader(r, 256<<10))
			if err != nil {
				return refuse("the archive is damaged: %v", err)
			}
			indexFiles[path.Dir(e.name)] = append(head, rest...)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	// A link that leads nowhere in the archive (to a missing file, to an absolute path, out of the archive,
	// round in a circle, or to a folder it is in) is left out: nothing is written for it. Themes often have a
	// stray one, a link to an icon of another theme, and refusing the whole theme for it would be no safer.
	c.Skipped = map[string]string{}
	for _, e := range c.index {
		if e.typ == typeSymlink || e.typ == typeHardlink {
			target, err := c.resolve(e.name, 0)
			switch {
			case err != nil:
				c.Skipped[e.name] = err.Error()
			case strings.HasPrefix(e.name, target+"/"):
				c.Skipped[e.name] = fmt.Sprintf("%q is a link to a folder it is in", e.name)
			}
		}
	}
	if err := c.findThemes(indexFiles); err != nil {
		return nil, err
	}
	return c, nil
}

// required reports whether an archive path is one a theme can't do without, which is never left out: an
// index.theme, or a file in a cursors folder. One that looks runnable refuses the archive instead, so a theme
// is never listed or installed without what makes it a theme.
func required(name string) bool {
	return path.Base(name) == "index.theme" || path.Base(path.Dir(name)) == "cursors"
}

// findThemes finds the archive's themes, refusing a layout that isn't clean.
func (c *Contents) findThemes(indexFiles map[string][]byte) error {
	candidates := map[string]*Theme{}
	theme := func(dir string) *Theme {
		if t := candidates[dir]; t != nil {
			return t
		}
		t := &Theme{Name: path.Base(dir), Path: dir}
		candidates[dir] = t
		return t
	}
	for dir, raw := range indexFiles {
		title, inherits, icons := parseIndex(raw)
		if !icons && !c.dirs[join(dir, "cursors")] {
			continue // not an icon theme's index: a GTK theme's, say
		}
		t := theme(dir)
		t.Title, t.Inherits, t.Icons = title, inherits, icons
	}
	for d := range c.dirs {
		if path.Base(d) != "cursors" || !c.hasFiles(d) {
			continue
		}
		parent := path.Dir(d)
		if parent == "." {
			parent = ""
		}
		theme(parent).Cursors = true
	}
	// A folder with an index.theme and a cursors folder with nothing written in it is no theme.
	for dir, t := range candidates {
		if !t.Icons && !t.Cursors {
			delete(candidates, dir)
		}
	}
	gtk := c.gtkThemes()
	if len(candidates) == 0 && len(gtk) == 0 {
		return refuse("it holds no theme: no folder has an index.theme for icons, a cursors folder, or a " +
			"gtk-3.0 or gtk-4.0 folder with a gtk.css")
	}
	// Icon and cursor themes first, judged as they were before GTK themes: an archive refused for them stays
	// refused, and one that was taken stays taken.
	themes, err := cleanThemes(candidates, reservedThemes)
	if len(candidates) > 0 && err != nil {
		return err
	}
	gtkThemes, gtkErr := cleanThemes(gtk, reservedGTKThemes)
	switch {
	case gtkErr != nil && len(candidates) == 0:
		return gtkErr
	case gtkErr != nil:
		gtkThemes = nil // the archive's icon or cursor themes are clean: they stand alone
	}
	byDir := map[string]int{}
	for _, t := range themes {
		byDir[t.Path] = len(c.Themes)
		c.Themes = append(c.Themes, t)
	}
	for _, t := range gtkThemes {
		if i, ok := byDir[t.Path]; ok {
			c.Themes[i].GTK = true // one folder that is both: it installs to each place for its kind
			continue
		}
		c.Themes = append(c.Themes, t)
	}
	sort.Slice(c.Themes, func(i, j int) bool { return c.Themes[i].Name < c.Themes[j].Name })
	return nil
}

// gtkThemes finds the archive's GTK themes: the folders with a gtk-3.0 or gtk-4.0 folder that has a gtk.css
// or gtk-dark.css (a file, or a link to one) written. GTK 3 and 4 load a theme from exactly those files, so
// a folder of style sources (gtk-3.0/*.scss, as in a theme's source code) is no theme.
func (c *Contents) gtkThemes() map[string]*Theme {
	out := map[string]*Theme{}
	var gtkDirs []string
	for d := range c.dirs {
		gtkDirs = append(gtkDirs, d)
	}
	for name, e := range c.index {
		if e.typ == typeSymlink || e.typ == typeHardlink {
			gtkDirs = append(gtkDirs, name) // a gtk-4.0 that is a link to another variant's
		}
	}
	for _, d := range gtkDirs {
		if b := path.Base(d); b != "gtk-3.0" && b != "gtk-4.0" {
			continue
		}
		parent := path.Dir(d)
		if parent == "." {
			parent = ""
		}
		if out[parent] != nil || !(c.writesFile(d+"/gtk.css") || c.writesFile(d+"/gtk-dark.css")) {
			continue
		}
		out[parent] = &Theme{Name: path.Base(parent), Path: parent, GTK: true}
	}
	return out
}

// writesFile reports whether an archive path is a file that is written: a file, or a link (or a path
// through a linked folder) that leads to one, and not left out.
func (c *Contents) writesFile(name string) bool {
	if _, skipped := c.Skipped[name]; skipped {
		return false
	}
	target, err := c.resolve(name, 0)
	if err != nil {
		return false
	}
	e, ok := c.index[target]
	return ok && e.typ == typeFile
}

// cleanThemes checks that themes of one kind can be told apart, and returns them; reserved are the names
// they can't have.
func cleanThemes(candidates map[string]*Theme, reserved map[string]bool) ([]Theme, error) {
	var out []Theme
	names := map[string]string{}
	for dir, t := range candidates {
		switch {
		case dir == "" || dir == ".":
			return nil, refuse("its theme is at the top of the archive, with no folder to name it")
		case !ThemeName.MatchString(t.Name):
			return nil, refuse("its theme folder %q has a name uas won't use", t.Name)
		case reserved[strings.ToLower(t.Name)]:
			return nil, refuse("its theme is called %q, which would take over the desktop's own %s", t.Name, t.Name)
		}
		if other, ok := names[strings.ToLower(t.Name)]; ok {
			return nil, refuse("two of its themes are called %q (%s and %s)", t.Name, other, dir)
		}
		names[strings.ToLower(t.Name)] = dir
		for other := range candidates {
			if other != dir && strings.HasPrefix(other, dir+"/") {
				return nil, refuse("its theme %q has another theme inside it (%s)", t.Name, other)
			}
		}
		out = append(out, *t)
	}
	return out, nil
}

// hasFiles reports whether a folder has a file or link in it that is written: a cursors folder of links
// that are all left out makes no cursor theme.
func (c *Contents) hasFiles(dir string) bool {
	return slices.ContainsFunc(c.within(dir), func(e entry) bool {
		_, skipped := c.Skipped[e.name]
		return e.typ != typeDir && !skipped
	})
}

// parseIndex reads an index.theme: the [Icon Theme] section's Name and Inherits, and whether it lists icon
// folders (Directories, or ScaledDirectories).
func parseIndex(raw []byte) (title string, inherits []string, icons bool) {
	sc := bufio.NewScanner(strings.NewReader(string(raw)))
	sc.Buffer(make([]byte, 64<<10), 1<<20)
	in := false
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if strings.HasPrefix(line, "[") {
			in = line == "[Icon Theme]"
			continue
		}
		key, value, ok := strings.Cut(line, "=")
		if !in || !ok {
			continue
		}
		key, value = strings.TrimSpace(key), strings.TrimSpace(value)
		switch key {
		case "Name":
			title = value
		case "Inherits":
			for _, v := range strings.Split(value, ",") {
				if v = strings.TrimSpace(v); v != "" {
					inherits = append(inherits, v)
				}
			}
		case "Directories", "ScaledDirectories":
			icons = icons || value != ""
		}
	}
	return title, inherits, icons
}

func join(dir, name string) string {
	if dir == "" || dir == "." {
		return name
	}
	return dir + "/" + name
}

// resolve follows a link (and links to links) to the file or folder it leads to, by archive path. It refuses
// a link that leads outside the archive, to nothing, to a file left out, or round in a circle.
func (c *Contents) resolve(name string, depth int) (string, error) {
	if depth > 40 {
		return "", refuse("%q is a link that goes round in a circle", name)
	}
	e, ok := c.index[name]
	if !ok {
		if _, out := c.LeftOut[name]; out {
			return "", refuse("a link in it leads to %q, which is left out", name)
		}
		if c.dirs[name] {
			return name, nil
		}
		// A path through a linked folder ("a/b/c" where "a/b" is a link).
		for d := path.Dir(name); d != "."; d = path.Dir(d) {
			if de, ok := c.index[d]; ok && de.typ == typeSymlink {
				target, err := c.resolve(d, depth+1)
				if err != nil {
					return "", err
				}
				return c.resolve(target+strings.TrimPrefix(name, d), depth+1)
			}
		}
		return "", refuse("a link in it leads to %q, which isn't in the archive", name)
	}
	switch e.typ {
	case typeFile, typeDir:
		return name, nil
	}
	var target string
	switch {
	case strings.HasPrefix(e.link, "/"):
		return "", refuse("%q is a link to %s, outside the archive", name, e.link)
	case e.typ == typeHardlink:
		// A hard link names its target from the top of the archive.
		target = path.Clean(strings.TrimPrefix(e.link, "./"))
	default:
		target = path.Join(path.Dir(name), e.link)
	}
	if target == name || !localPath(target) {
		return "", refuse("%q is a link to %s, outside the archive", name, e.link)
	}
	return c.resolve(target, depth+1)
}

func localPath(p string) bool {
	return p != "." && p != "" && !strings.HasPrefix(p, "../") && p != ".." && !strings.HasPrefix(p, "/")
}

// within returns the entries under a folder, by name.
func (c *Contents) within(dir string) []entry {
	if c.sorted == nil {
		c.sorted = make([]string, 0, len(c.index))
		for name := range c.index {
			c.sorted = append(c.sorted, name)
		}
		slices.Sort(c.sorted)
	}
	// Names under a folder sort together.
	prefix := dir + "/"
	var out []entry
	for i, _ := slices.BinarySearch(c.sorted, prefix); i < len(c.sorted) && strings.HasPrefix(c.sorted[i], prefix); i++ {
		out = append(out, c.index[c.sorted[i]])
	}
	return out
}
