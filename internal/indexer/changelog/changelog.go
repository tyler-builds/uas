// Package changelog turns release notes into the short Markdown a listing's Changelog holds.
//
// Notes come as AppStream release descriptions (already Markdown, see appstream.DescriptionMarkdown) or as a
// code forge's release notes, which are Markdown written for a web page: headings, HTML, images, comment
// templates and "Full Changelog" links. Clean leaves what reads well in a short section of the app's page, and
// cuts it to a few paragraphs; the full notes stay a link away.
package changelog

import (
	"regexp"
	"strings"
	"time"

	"github.com/tyler-builds/uas/internal/catalog"
	"github.com/tyler-builds/uas/internal/indexer/appstream"
)

// Releases is how many of a listing's newest releases keep their notes.
const Releases = 5

// MaxChars is about how long one release's notes can be. Longer notes are cut at a line before it.
const MaxChars = 1500

var (
	comments  = regexp.MustCompile(`(?s)<!--.*?-->`)
	images    = regexp.MustCompile(`!\[[^\]]*\]\([^)]*\)`)
	htmlTags  = regexp.MustCompile(`</?[a-zA-Z][a-zA-Z0-9-]*(\s[^>\n]*)?/?>`) // not autolinks: <https://...>
	headings  = regexp.MustCompile(`(?m)^ {0,3}#{1,6}[ \t]+(.+?)[ \t#]*$`)
	blankRuns = regexp.MustCompile(`\n{3,}`)
	// fullChangelog is GitHub's generated "**Full Changelog**: <compare link>" line.
	fullChangelog = regexp.MustCompile(`(?mi)^[ \t]*\**full changelog\**:?[ \t]*https?://\S+[ \t]*$`)
)

// Clean tidies release notes and shortens them to about MaxChars. It reports whether it cut them.
//
// Headings become bold lines, so a release's notes don't outrank the page's own sections. Images and HTML
// tags are dropped (their text stays): images would be fetched from anywhere, and the page shows no HTML.
func Clean(md string) (string, bool) {
	md = strings.ReplaceAll(md, "\r\n", "\n")
	md = comments.ReplaceAllString(md, "")
	md = images.ReplaceAllString(md, "")
	md = htmlTags.ReplaceAllString(md, "")
	md = fullChangelog.ReplaceAllString(md, "")
	md = headings.ReplaceAllString(md, "**$1**")
	var lines []string
	for _, l := range strings.Split(md, "\n") {
		lines = append(lines, strings.TrimRight(l, " \t"))
	}
	md = strings.TrimSpace(blankRuns.ReplaceAllString(strings.Join(lines, "\n"), "\n\n"))
	if len(md) <= MaxChars {
		return md, false
	}
	cut := strings.LastIndex(md[:MaxChars], "\n")
	if cut < MaxChars/2 {
		// One long paragraph: cut at a space instead.
		if cut = strings.LastIndex(md[:MaxChars], " "); cut < 0 {
			cut = MaxChars
		}
	}
	md = strings.TrimSpace(md[:cut])
	// Close a code block the cut left open.
	if strings.Count(md, "```")%2 == 1 {
		md += "\n```"
	}
	return md + "\n\n…", true
}

// Add adds a release's notes to l's Changelog, cleaned, if it has fewer than Releases and the notes say
// something. Call it newest release first.
func Add(l *catalog.Listing, version string, date time.Time, notes, url string) {
	if len(l.Changelog) >= Releases {
		return
	}
	version = strings.TrimSpace(version)
	md, cut := Clean(notes)
	if version == "" || md == "" {
		return
	}
	l.Changelog = append(l.Changelog, catalog.Change{Version: version, Date: date, Notes: md, URL: url, Cut: cut})
}

// FromAppStream adds the notes of a component's newest stable releases (newest first, as appstream sorts
// them) to l. Only the Releases newest count, so an app whose recent releases have no notes doesn't show
// years-old ones as what's new.
func FromAppStream(l *catalog.Listing, rels []appstream.Release) {
	n := 0
	for _, r := range rels {
		if r.Type == "development" || r.Type == "snapshot" {
			continue
		}
		if n++; n > Releases {
			return
		}
		Add(l, r.Version, r.Time, r.Notes, r.URL)
	}
}
