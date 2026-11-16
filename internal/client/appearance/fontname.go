package appearance

import (
	"strconv"
	"strings"
)

// A font setting is a Pango font description: the family, then style options, then the size, then font
// variations and OpenType features ("Fira Code weight=450 10", "Cantarell Italic 11 @wght=300 #tnum=1").
// Pango reads it from the end, taking words off for as long as they are something other than the family;
// fontParts does the same.

// pangoStyles are the style words Pango knows, lower-cased: styles, variants, weights, stretches and
// gravities.
var pangoStyles = map[string]bool{}

func init() {
	for _, w := range strings.Fields(`normal roman oblique italic
		small-caps all-small-caps petite-caps all-petite-caps unicase title-caps
		thin ultra-light extra-light light semi-light demi-light book regular medium semi-bold demi-bold bold
		ultra-bold extra-bold heavy black ultra-black extra-black ultraheavy ultra-heavy extra-heavy
		ultra-condensed extra-condensed condensed semi-condensed semi-expanded expanded extra-expanded
		ultra-expanded not-rotated south upside-down north rotated-left east rotated-right west`) {
		pangoStyles[w] = true
	}
}

// pangoFields are the style options Pango writes as "name=value" when the value has no word, such as
// "weight=450".
var pangoFields = []string{"style=", "variant=", "weight=", "stretch=", "gravity="}

// fontParts splits a font setting into its family and its size ("" when it has none). Style options,
// variations and features are dropped. At least one word is always kept for the family.
func fontParts(value string) (family, size string) {
	fields := strings.Fields(value)
	n := len(fields)
	// Features and variations come last.
	for n > 1 && (strings.HasPrefix(fields[n-1], "#") || strings.HasPrefix(fields[n-1], "@")) {
		n--
	}
	if n > 1 && isFontSize(fields[n-1]) {
		size = fields[n-1]
		n--
	}
	for n > 1 && isPangoStyle(fields[n-1]) {
		n--
	}
	return strings.TrimSuffix(strings.Join(fields[:n], " "), ","), size
}

// isFontSize reports whether a word is a Pango size: points ("10", "10.5") or pixels ("14px").
func isFontSize(w string) bool {
	w = strings.TrimSuffix(w, "px")
	f, err := strconv.ParseFloat(w, 64)
	return err == nil && f > 0
}

func isPangoStyle(w string) bool {
	l := strings.ToLower(w)
	if pangoStyles[l] {
		return true
	}
	for _, f := range pangoFields {
		if strings.HasPrefix(l, f) && len(l) > len(f) {
			return true
		}
	}
	return false
}

// FontValue is a font setting with its family replaced and its size kept: ("Cantarell 11", "Inter") is
// "Inter 11". The old font's style options are dropped: they belong to it, not to the new family.
func FontValue(current, family string) string {
	if _, size := fontParts(current); size != "" {
		return family + " " + size
	}
	return family + " 11"
}

// FontFamily is the family of a font setting: "Inter" of "Inter 11", "Fira Code" of
// "Fira Code weight=450 10".
func FontFamily(value string) string {
	family, _ := fontParts(value)
	return strings.TrimSpace(family)
}

// FontSize is the size of a font setting, such as "11", or "" when it gives none.
func FontSize(value string) string {
	_, size := fontParts(value)
	return size
}
