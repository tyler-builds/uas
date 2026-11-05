package catalog

import (
	"strings"
	"unicode"
)

// synonyms are other ways of saying what a search word asks for, mostly the names of well-known apps
// standing for their kind ("excel" for a spreadsheet). A word with synonyms matches itself or any of them.
// Category words come from the apps' freedesktop categories, which the catalog indexes as words (see
// categoryWords), so "ide" finds apps in the IDE category.
var synonyms = map[string][]string{
	"ide":         {"development environment", "code editor"},
	"excel":       {"spreadsheet"},
	"word":        {"word processor"},
	"powerpoint":  {"presentation"},
	"office":      {"office suite", "libreoffice"},
	"photoshop":   {"image editor", "raster graphics"},
	"illustrator": {"vector graphics"},
	"notepad":     {"text editor"},
	"itunes":      {"music player"},
	"explorer":    {"file manager"},
	"finder":      {"file manager"},
	"torrent":     {"bittorrent"},
	"messenger":   {"instant messaging", "chat"},
	"im":          {"instant messaging"},
	"daw":         {"digital audio workstation"},
	"cad":         {"computer aided design"},
	"browser":     {"web browser"},
}

// categoryWords spells freedesktop categories as words: "WebBrowser" is "web browser", "3DGraphics" is
// "3d graphics" and "IDE" stays "ide".
func categoryWords(categories []string) []string {
	var out []string
	for _, c := range categories {
		var words []string
		var cur []rune
		rs := []rune(c)
		for i, r := range rs {
			// A word starts at a capital that follows a small letter, or that starts a capitalized word after
			// capitals ("GUIDesigner": "GUI", "Designer").
			if unicode.IsUpper(r) && len(cur) > 0 && (unicode.IsLower(rs[i-1]) ||
				(i+1 < len(rs) && unicode.IsLower(rs[i+1]) && unicode.IsUpper(rs[i-1]))) {
				words = append(words, string(cur))
				cur = nil
			}
			cur = append(cur, r)
		}
		if len(cur) > 0 {
			words = append(words, string(cur))
		}
		out = append(out, strings.ToLower(strings.Join(words, " ")))
	}
	return out
}
