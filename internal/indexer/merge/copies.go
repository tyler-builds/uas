package merge

import (
	"net/url"
	"regexp"
	"strings"
	"unicode"

	"github.com/tyler-builds/uas/internal/catalog"
)

// GitHub and COPR are where anyone can publish anything. When an app is also offered by a reviewed source
// (Flathub, Fedora, Homebrew), a GitHub or COPR copy of it is worth showing only if it comes from the app's
// own developer; a repackage by someone else looks like the app but isn't from its makers, which is how
// scams look too. So:
//
//   - A GitHub-only app with the same name as a reviewed app joins it as another source when its repo owner
//     is the app's developer (see official). If the app already has a GitHub source, it is hidden as a
//     duplicate.
//   - Otherwise it is hidden when it looks like a repackage (its repo or description says AppImage of,
//     unofficial, portable and the like), or when it is small (under minStarsBesideReviewed) next to an
//     established app (at least establishedInstalls installs a year).
//   - Otherwise it is a different app that happens to share the name, and stays.
//   - COPR listings of an app that a reviewed source offers are hidden unless the COPR owner is the
//     developer.
//
// This hides some genuine third-party builds (a COPR with a newer version, say). That is the intended trade.

// Hidden is a listing left out of the catalog as an unofficial copy.
type Hidden struct {
	Source catalog.SourceID
	Ref    string
	Name   string
	Of     string // the app it copies
	Reason string
}

// trustedCOPROwners publish COPR builds that are kept even when they aren't the developer's: ublue-os is
// the vendor of Bazzite and the other Universal Blue images uas runs on, and builds their own packages there.
var trustedCOPROwners = map[string]bool{"ublue-os": true}

const (
	minStarsBesideReviewed = 1000
	establishedInstalls    = 10_000
)

// looksRepackaged matches repo names and descriptions of builds of someone else's app.
var (
	repackagedRepo = regexp.MustCompile(`(?i)appimage|flatpak|[-_]snap\b|portable|unofficial|repack|packag|[-_]bin$|[-_]builds?$`)
	repackagedText = regexp.MustCompile(`(?i)\bunofficial\b|\bappimages?\s+(of|for)\b|\bportable (version|build)\b|` +
		`\brepack|\bpackaged (as|for|into)\b|\bnot affiliated\b|\bcommunity build\b`)
)

func isReviewed(s catalog.SourceID) bool {
	return s == catalog.SourceFlathub || s == catalog.SourceFedora || s == catalog.SourceHomebrew
}

func hasReviewed(ls []catalog.Listing) bool {
	for _, l := range ls {
		if isReviewed(l.Source) {
			return true
		}
	}
	return false
}

// sortCopies merges, hides or keeps GitHub apps that share a reviewed app's name, and drops unofficial COPR
// listings from reviewed apps. It returns the new group order and what it hid.
func sortCopies(groups map[string][]catalog.Listing, order []string) ([]string, []Hidden) {
	// Reviewed apps by name.
	byName := map[string][]string{}
	for _, g := range order {
		if !hasReviewed(groups[g]) {
			continue
		}
		names := map[string]bool{}
		for _, l := range groups[g] {
			if n := squashName(l.Name); n != "" && isReviewed(l.Source) {
				names[n] = true
			}
		}
		for n := range names {
			byName[n] = append(byName[n], g)
		}
	}

	var hidden []Hidden
	for _, g := range order {
		ls := groups[g]
		if len(ls) != 1 || !ls[0].Source.Releases() {
			continue
		}
		l := ls[0]
		target := mostInstalled(groups, byName[squashName(l.Name)])
		if target == "" {
			continue
		}
		app := groups[target]
		repo := l.Source.Repo(l.Ref)
		owner, _, _ := strings.Cut(repo, "/")
		isOfficial := official(owner, l.Source.Host(), l, app)
		switch {
		case isOfficial && !hasSource(app, l.Source):
			groups[target] = append(app, l)
			delete(groups, g)
		case isOfficial:
			hidden = append(hidden, hide(l, app, "another "+l.Source.Name()+" repo of the developer, who already has one listed"))
			delete(groups, g)
		case repackagedRepo.MatchString(repo[strings.LastIndex(repo, "/")+1:]) || repackagedText.MatchString(l.Summary):
			hidden = append(hidden, hide(l, app, "a repackage by someone other than the developer"))
			delete(groups, g)
		case l.Stars < minStarsBesideReviewed && installs(app) >= establishedInstalls:
			hidden = append(hidden, hide(l, app, "a small repo with the name of an established app"))
			delete(groups, g)
		}
	}

	for _, g := range order {
		ls, ok := groups[g]
		if !ok || !hasReviewed(ls) {
			continue
		}
		kept := ls[:0:0]
		for _, l := range ls {
			if l.Source == catalog.SourceCOPR {
				owner, _, _ := strings.Cut(l.Project, "/")
				owner = strings.TrimPrefix(owner, "@")
				if !trustedCOPROwners[owner] && !official(owner, "", l, ls) {
					hidden = append(hidden, hide(l, ls, "a COPR build by someone other than the developer"))
					continue
				}
			}
			kept = append(kept, l)
		}
		groups[g] = kept
	}

	var out []string
	for _, g := range order {
		if _, ok := groups[g]; ok {
			out = append(out, g)
		}
	}
	return out, hidden
}

// official reports whether owner (a GitHub, GitLab, Codeberg or COPR account) is the developer of the app
// that listings describe: it owns the app's code-forge URLs, it is named in the app's AppStream ID
// (io.github.<owner>.App, org.<owner>.App), or the app's homepage is the owner's site (lmms.io for the LMMS
// account). host, if set, is the forge the account is on, so a code-forge URL counts only on that forge.
func official(owner, host string, l catalog.Listing, app []catalog.Listing) bool {
	o := squashName(owner)
	if o == "" {
		return false
	}
	for _, a := range app {
		if a.Source == l.Source && a.Ref == l.Ref {
			continue
		}
		for _, u := range []string{a.Homepage, a.VCSURL} {
			if h, fo := forgeOwner(u); fo == o && (host == "" || h == host) {
				return true
			}
		}
		if idOwner(a.AppStreamID) == o {
			return true
		}
		// Not the copy's own homepage: anyone can point a repo's homepage at the real app's site.
		if d := siteName(a.Homepage); d != "" && d == o {
			return true
		}
	}
	return false
}

// forgeOwner is the host and owner in a code-forge URL (github.com/<owner>/...), the owner squashed, or "".
func forgeOwner(raw string) (host, owner string) {
	u := normalizeURL(raw)
	host, path, ok := strings.Cut(u, "/")
	if !ok || !genericHosts[host] {
		return "", ""
	}
	owner, _, _ = strings.Cut(path, "/")
	return host, squashName(owner)
}

// idOwner is the organization an AppStream ID names: "kde" in org.kde.App, "owner" in io.github.owner.App.
func idOwner(id string) string {
	parts := strings.Split(normalizeAppStreamID(id), ".")
	if len(parts) < 3 {
		return ""
	}
	switch parts[0] + "." + parts[1] {
	case "io.github", "com.github", "io.gitlab", "com.gitlab", "page.codeberg", "io.sourceforge":
		if len(parts) >= 4 {
			return squashName(parts[2])
		}
		return ""
	}
	return squashName(parts[1])
}

// siteName is the name of a homepage's site, without "www." and the top-level domain: "lmms" for
// https://lmms.io/. Code-forge and hosting sites, where anyone can have a page, give "".
func siteName(raw string) string {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || u.Host == "" {
		return ""
	}
	host := strings.TrimPrefix(strings.ToLower(u.Hostname()), "www.")
	if genericHosts[host] {
		return ""
	}
	labels := strings.Split(host, ".")
	if len(labels) < 2 {
		return ""
	}
	site := labels[len(labels)-2]
	switch site {
	case "github", "gitlab", "codeberg", "sourceforge", "netlify", "vercel", "pages", "blogspot", "wordpress":
		return ""
	}
	return squashName(site)
}

// squashName lowercases s and keeps only letters and digits, so names compare loosely.
func squashName(s string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(s) {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			b.WriteRune(r)
		}
	}
	return b.String()
}

func hasSource(ls []catalog.Listing, s catalog.SourceID) bool {
	for _, l := range ls {
		if l.Source == s {
			return true
		}
	}
	return false
}

func installs(ls []catalog.Listing) int {
	n := 0
	for _, l := range ls {
		n = max(n, l.Installs)
	}
	return n
}

// mostInstalled returns the group with the most installs, or "".
func mostInstalled(groups map[string][]catalog.Listing, gs []string) string {
	best, most := "", -1
	for _, g := range gs {
		ls, ok := groups[g]
		if !ok {
			continue
		}
		if n := installs(ls); n > most {
			best, most = g, n
		}
	}
	return best
}

func hide(l catalog.Listing, app []catalog.Listing, reason string) Hidden {
	of := ""
	for _, a := range app { // the listing whose name it shares, else the first reviewed one
		if isReviewed(a.Source) && (of == "" || squashName(a.Name) == squashName(l.Name)) {
			of = a.Name
		}
	}
	return Hidden{Source: l.Source, Ref: l.Ref, Name: l.Name, Of: of, Reason: reason}
}
