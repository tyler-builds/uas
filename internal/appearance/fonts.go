package appearance

import (
	"bytes"
	"io"
	"os"
	"path"
	"regexp"
	"slices"
	"strings"

	"golang.org/x/image/font/sfnt"
)

// FontFileName is what an installed font file may be called. Google's variable fonts name their axes in
// brackets: "Roboto[wdth,wght].ttf".
var FontFileName = regexp.MustCompile(`(?i)^[A-Za-z0-9][A-Za-z0-9 ._+\-\[\],()]{0,200}\.(ttf|otf|ttc)$`)

// FontFamilies checks that data is a TrueType or OpenType font (or collection) and returns the family names
// it gives, the typographic family where it has one ("Inter", not "Inter Medium"). WOFF and WOFF2, which are
// for the web, are refused.
func FontFamilies(data []byte) ([]string, error) {
	var fonts []*sfnt.Font
	switch {
	case bytes.HasPrefix(data, []byte("ttcf")):
		col, err := sfnt.ParseCollection(data)
		if err != nil {
			return nil, refuse("it isn't a font uas can read: %v", err)
		}
		for i := range col.NumFonts() {
			f, err := col.Font(i)
			if err != nil {
				return nil, refuse("it isn't a font uas can read: %v", err)
			}
			fonts = append(fonts, f)
		}
	case bytes.HasPrefix(data, []byte{0, 1, 0, 0}) || bytes.HasPrefix(data, []byte("OTTO")) ||
		bytes.HasPrefix(data, []byte("true")):
		f, err := sfnt.Parse(data)
		if err != nil {
			return nil, refuse("it isn't a font uas can read: %v", err)
		}
		fonts = append(fonts, f)
	default:
		return nil, refuse("it isn't a TrueType or OpenType font")
	}
	var out []string
	var buf sfnt.Buffer
	for _, f := range fonts {
		name, err := f.Name(&buf, sfnt.NameIDTypographicFamily)
		if err != nil || strings.TrimSpace(name) == "" {
			name, err = f.Name(&buf, sfnt.NameIDFamily)
		}
		name = strings.TrimSpace(name)
		if err != nil || name == "" {
			return nil, refuse("the font has no family name")
		}
		if !slices.Contains(out, name) {
			out = append(out, name)
		}
	}
	return out, nil
}

// FontFile is a font file written by ExtractFonts.
type FontFile struct {
	Name     string   // the file's name in the folder
	Families []string // the families it holds
}

// fontDocs are the files besides fonts that font archives carry, which are skipped.
var fontDocs = regexp.MustCompile(`(?i)^(readme|license|licence|copying|ofl|authors|fontlog|changelog|notice)([._-].*)?$|\.(txt|md|html?|pdf)$`)

// ExtractFonts writes the TrueType and OpenType fonts of an archive into dir, a new, empty folder, all side
// by side, and returns them. Text files such as the licence are skipped. Anything else, a link included,
// refuses the archive, as does a file that isn't a font it can read.
func ExtractFonts(p, dir string, lim Limits) ([]FontFile, error) {
	root, err := os.OpenRoot(dir)
	if err != nil {
		return nil, err
	}
	defer root.Close()
	if err := emptyDir(root); err != nil {
		return nil, err
	}
	var out []FontFile
	var total int64
	entries := 0
	err = walk(p, func(e entry, r io.Reader) error {
		if entries++; entries > lim.MaxEntries {
			return refuse("it holds more than %d files", lim.MaxEntries)
		}
		base := path.Base(e.name)
		switch {
		case e.typ == typeDir:
			return nil
		case e.typ != typeFile:
			return refuse("it contains a link (%s); a font archive needs none", e.name)
		case fontDocs.MatchString(base) && !FontFileName.MatchString(base):
			return nil
		case !FontFileName.MatchString(base):
			return refuse("it contains %s, which isn't a font", e.name)
		case e.size > lim.MaxFileBytes:
			return refuse("%q is larger than %s", e.name, size(lim.MaxFileBytes))
		}
		if total += e.size; total > lim.MaxBytes {
			return refuse("it unpacks to more than %s", size(lim.MaxBytes))
		}
		data, err := io.ReadAll(io.LimitReader(r, e.size+1))
		if err != nil || int64(len(data)) != e.size {
			return refuse("the archive is damaged at %s", e.name)
		}
		fams, err := FontFamilies(data)
		if err != nil {
			return refuse("%s: %v", e.name, err)
		}
		if slices.ContainsFunc(out, func(f FontFile) bool { return strings.EqualFold(f.Name, base) }) {
			return refuse("it has two fonts called %s", base)
		}
		if err := root.WriteFile(base, data, 0o644); err != nil {
			return err
		}
		out = append(out, FontFile{Name: base, Families: fams})
		return nil
	})
	if err != nil {
		return nil, err
	}
	if len(out) == 0 {
		return nil, refuse("it holds no TrueType or OpenType font")
	}
	return out, nil
}
