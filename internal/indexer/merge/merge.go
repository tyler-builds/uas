// Package merge combines listings from different sources into apps.
//
// Today listings merge only when they share an AppStream ID. Matching on upstream repo and homepage URLs comes
// with the sources that need it (Homebrew, COPR, GitHub).
package merge

import (
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
	}
	return a
}

func fill[T comparable](dst *T, v T) {
	var zero T
	if *dst == zero {
		*dst = v
	}
}
