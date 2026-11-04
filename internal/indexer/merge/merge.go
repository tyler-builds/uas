// Package merge combines listings from different sources into apps.
//
// Listings merge when they share an AppStream ID. Groups that are still separate then merge when they point at
// the same code-forge repo (github.com/owner/repo), or have the same name and the same homepage or
// source-code URL, as long as they come from different sources and the match is unambiguous (exactly two
// groups share the key). That catches distros that use old-style IDs such as
// "rednotebook" for the app Flathub calls "app.rednotebook.RedNotebook".
package merge

import (
	"net/url"
	"slices"
	"sort"
	"strconv"
	"strings"

	"github.com/tyler-builds/uas/internal/catalog"
)

// Merge groups listings into apps, sorted by app ID.
func Merge(listings []catalog.Listing) []catalog.App {
	groups := map[string][]catalog.Listing{}
	var order []string
	refs := map[string]bool{}
	for _, l := range listings {
		// A source can list the same ref twice; keep the first.
		ref := string(l.Source) + "\x00" + l.Ref
		if refs[ref] {
			continue
		}
		refs[ref] = true
		key := groupKey(l)
		if _, ok := groups[key]; !ok {
			order = append(order, key)
		}
		groups[key] = append(groups[key], l)
	}

	// A repo URL on a code forge names one project, so it links without the name. Homepages and other
	// URLs also need the same name.
	order = linkGroups(groups, order, func(l catalog.Listing) string { return forgeRepo(l.VCSURL) })
	order = linkGroups(groups, order, nameAnd(func(l catalog.Listing) string { return l.Homepage }))
	order = linkGroups(groups, order, nameAnd(func(l catalog.Listing) string { return l.VCSURL }))

	apps := make([]catalog.App, 0, len(groups))
	seen := map[string]bool{}
	for _, key := range order {
		a := build(groups[key])
		// Group keys are unique, so app IDs should be too; guard anyway, since a duplicate primary key
		// would fail the whole build.
		for base, n := a.ID, 2; seen[a.ID]; n++ {
			a.ID = base + "~" + strconv.Itoa(n)
		}
		seen[a.ID] = true
		apps = append(apps, a)
	}
	sort.Slice(apps, func(i, j int) bool { return apps[i].ID < apps[j].ID })
	return apps
}

// nameAnd returns a key of a listing's name plus its normalized URL, or "" if either is missing.
func nameAnd(urlOf func(catalog.Listing) string) func(catalog.Listing) string {
	return func(l catalog.Listing) string {
		u := normalizeURL(urlOf(l))
		if u == "" || l.Name == "" {
			return ""
		}
		return strings.ToLower(strings.TrimSpace(l.Name)) + "\x00" + u
	}
}

// forgeRepo returns "host/owner/repo" for a repo URL on a known code forge, or "".
func forgeRepo(raw string) string {
	u := normalizeURL(raw)
	host, path, ok := strings.Cut(u, "/")
	if !ok || !genericHosts[host] || host == "sourceforge.net" {
		return ""
	}
	parts := strings.Split(path, "/")
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return ""
	}
	return u
}

// linkGroups merges pairs of groups that share a key. It returns the new group order.
func linkGroups(groups map[string][]catalog.Listing, order []string, keyOf func(catalog.Listing) string) []string {
	byKey := map[string][]string{}
	for _, g := range order {
		keys := map[string]bool{}
		for _, l := range groups[g] {
			if k := keyOf(l); k != "" {
				keys[k] = true
			}
		}
		for k := range keys {
			byKey[k] = append(byKey[k], g)
		}
	}
	merged := map[string]bool{}
	for _, g := range order {
		for _, l := range groups[g] {
			k := keyOf(l)
			if k == "" || merged[g] {
				continue
			}
			pair := byKey[k]
			if len(pair) != 2 || merged[pair[0]] || merged[pair[1]] {
				continue
			}
			a, b := pair[0], pair[1]
			if !disjointSources(groups[a], groups[b]) {
				continue
			}
			groups[a] = append(groups[a], groups[b]...)
			delete(groups, b)
			merged[a], merged[b] = true, true
		}
	}
	var out []string
	for _, g := range order {
		if _, ok := groups[g]; ok {
			out = append(out, g)
		}
	}
	return out
}

func disjointSources(a, b []catalog.Listing) bool {
	seen := map[catalog.SourceID]bool{}
	for _, l := range a {
		seen[l.Source] = true
	}
	for _, l := range b {
		if seen[l.Source] {
			return false
		}
	}
	return true
}

// normalizeURL reduces a URL to host and path, so "http://www.example.org/app/" and
// "https://example.org/app" compare equal. Bare hosts of big code forges are too generic to identify an app.
func normalizeURL(raw string) string {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || u.Host == "" {
		return ""
	}
	host := strings.TrimPrefix(strings.ToLower(u.Host), "www.")
	path := strings.TrimSuffix(strings.ToLower(u.Path), "/")
	path = strings.TrimSuffix(path, ".git")
	if path == "" && genericHosts[host] {
		return ""
	}
	return host + path
}

var genericHosts = map[string]bool{"github.com": true, "gitlab.com": true, "sourceforge.net": true,
	"codeberg.org": true, "invent.kde.org": true, "gitlab.gnome.org": true}

func groupKey(l catalog.Listing) string {
	if id := normalizeAppStreamID(l.AppStreamID); id != "" {
		return "appstream:" + id
	}
	return "listing:" + string(l.Source) + ":" + l.Ref
}

// normalizeAppStreamID lowercases an AppStream ID and drops the ".desktop" suffix that older catalogs use.
func normalizeAppStreamID(id string) string {
	return strings.TrimSuffix(strings.ToLower(strings.TrimSpace(id)), ".desktop")
}

func rank(s catalog.SourceID) int {
	if i := slices.Index(catalog.SourcePrecedence, s); i >= 0 {
		return i
	}
	return len(catalog.SourcePrecedence)
}

// build makes one app from listings that describe it. Each field comes from the highest-precedence listing
// that has it.
func build(ls []catalog.Listing) catalog.App {
	ls = slices.Clone(ls)
	sort.SliceStable(ls, func(i, j int) bool { return rank(ls[i].Source) < rank(ls[j].Source) })

	a := catalog.App{Listings: ls}
	first := ls[0]
	if first.AppStreamID != "" {
		a.ID = strings.TrimSuffix(first.AppStreamID, ".desktop")
	} else {
		a.ID = string(first.Source) + ":" + first.Ref
	}
	for _, l := range ls {
		fill(&a.Kind, l.Kind)
		fill(&a.Name, l.Name)
		fill(&a.Summary, l.Summary)
		fill(&a.Description, l.Description)
		fill(&a.Developer, l.Developer)
		fill(&a.License, l.License)
		fill(&a.Homepage, l.Homepage)
		fill(&a.VCSURL, l.VCSURL)
		fill(&a.IconURL, l.IconURL)
		if len(a.Categories) == 0 {
			a.Categories = l.Categories
		}
		if len(a.Keywords) == 0 {
			a.Keywords = l.Keywords
		}
		if len(a.Screenshots) == 0 {
			a.Screenshots = l.Screenshots
		}
		a.Popularity = max(a.Popularity, l.Installs)
		a.Stars = max(a.Stars, l.Stars)
		if l.Released.After(a.Updated) {
			a.Updated = l.Released
		}
	}
	return a
}

func fill[T comparable](dst *T, v T) {
	var zero T
	if *dst == zero {
		*dst = v
	}
}
