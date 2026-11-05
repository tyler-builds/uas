package catalog

import (
	"context"
	"errors"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

func testApps() []App {
	browser := Listing{Source: SourceFlathub, Ref: "app/org.example.Browser/x86_64/stable",
		AppStreamID: "org.example.Browser", Kind: KindDesktopApp, Name: "Example Browser",
		Summary: "Browse the web", Version: "2.0", Released: time.Unix(1760000000, 0).UTC(), Verified: true,
		VerifiedAs: "example.org"}
	return []App{
		{ID: "org.example.Browser", Kind: KindDesktopApp, Name: "Example Browser", Summary: "Browse the web",
			Description: "A fast browser for the internet.", Categories: []string{"Network"},
			Keywords: []string{"internet"}, Screenshots: []Screenshot{{URL: "https://example.org/s.png"}},
			Listings: []Listing{browser}},
		{ID: "org.example.Editor", Kind: KindDesktopApp, Name: "Editor", Summary: "Edit text files",
			Developer: "Example Project",
			Listings: []Listing{{Source: SourceFlathub, Ref: "app/org.example.Editor/x86_64/stable",
				AppStreamID: "org.example.Editor", Kind: KindDesktopApp, Name: "Editor"}}},
		{ID: "org.example.EditorPlus", Kind: KindDesktopApp, Name: "Editor Plus",
			Summary: "A text editor with plugins",
			Listings: []Listing{{Source: SourceFlathub, Ref: "app/org.example.EditorPlus/x86_64/stable",
				AppStreamID: "org.example.EditorPlus", Kind: KindDesktopApp, Name: "Editor Plus"}}},
	}
}

func writeTestCatalog(t *testing.T) *DB {
	t.Helper()
	path := filepath.Join(t.TempDir(), "catalog.db")
	info := BuildInfo{BuiltAt: time.Unix(1770000000, 0).UTC(), Apps: 3,
		Sources: []SourceStatus{{Source: SourceFlathub, Listings: 3}}}
	if err := Write(context.Background(), path, testApps(), info); err != nil {
		t.Fatal(err)
	}
	db, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

func TestInfo(t *testing.T) {
	db := writeTestCatalog(t)
	info := db.Info()
	if info.SchemaVersion != SchemaVersion || info.Apps != 3 || len(info.Sources) != 1 {
		t.Errorf("Info = %+v", info)
	}
}

func TestSearch(t *testing.T) {
	db := writeTestCatalog(t)
	ctx := context.Background()
	ids := func(q string) []string {
		t.Helper()
		rs, err := db.Search(ctx, q, SearchOptions{Order: OrderRelevance, Limit: 10})
		if err != nil {
			t.Fatal(err)
		}
		var out []string
		for _, r := range rs {
			out = append(out, r.ID)
		}
		return out
	}

	if got := ids("brow"); len(got) != 1 || got[0] != "org.example.Browser" {
		t.Errorf("prefix search = %q", got)
	}
	// An exact name match comes first.
	if got := ids("editor"); len(got) != 2 || got[0] != "org.example.Editor" {
		t.Errorf("exact name first = %q", got)
	}
	// Stemming: "editing" matches "edit".
	if got := ids("editing"); len(got) == 0 {
		t.Error("stemmed search found nothing")
	}
	// No app matches every word, so any word may match.
	if got := ids("browser plugins"); len(got) != 2 {
		t.Errorf("OR fallback = %q", got)
	}
	// FTS5 syntax in the query is treated as plain words.
	if got := ids(`"web" AND NEAR(`); len(got) != 1 {
		t.Errorf("query with FTS syntax = %q", got)
	}
	if got := ids("   "); got != nil {
		t.Errorf("empty query = %q", got)
	}

	rs, err := db.Search(ctx, "browser", SearchOptions{Order: OrderRelevance, Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if !rs[0].Verified || len(rs[0].Sources) != 1 || rs[0].Sources[0] != SourceFlathub {
		t.Errorf("result = %+v", rs[0])
	}
}

func TestApp(t *testing.T) {
	db := writeTestCatalog(t)
	a, err := db.App(context.Background(), "org.example.Browser")
	if err != nil {
		t.Fatal(err)
	}
	if a.Name != "Example Browser" || len(a.Screenshots) != 1 || len(a.Categories) != 1 {
		t.Errorf("App = %+v", a)
	}
	if len(a.Listings) != 1 || a.Listings[0].Version != "2.0" || !a.Listings[0].Verified ||
		!a.Listings[0].Released.Equal(time.Unix(1760000000, 0)) {
		t.Errorf("Listings = %+v", a.Listings)
	}

	if _, err := db.App(context.Background(), "org.example.Missing"); !errors.Is(err, ErrNotFound) {
		t.Errorf("missing app err = %v", err)
	}
}

func TestResolve(t *testing.T) {
	path := filepath.Join(t.TempDir(), "catalog.db")
	apps := []App{
		{ID: "homebrew:tree", Kind: KindConsoleApp, Name: "tree", Listings: []Listing{
			{Source: SourceHomebrew, Ref: "tree", Package: "tree", Name: "tree"}}},
		{ID: "org.example.Editor", Kind: KindDesktopApp, Name: "Editor", Listings: []Listing{
			{Source: SourceFedora, Ref: "fedora-45/editor", Package: "editor", AppStreamID: "editor.desktop"}}},
		{ID: "org.example.Other", Kind: KindDesktopApp, Name: "Other", Listings: []Listing{
			{Source: SourceFedora, Ref: "fedora-45/shared", Package: "shared"}}},
		{ID: "homebrew:shared", Kind: KindConsoleApp, Name: "shared", Listings: []Listing{
			{Source: SourceHomebrew, Ref: "shared", Package: "shared"}}},
	}
	if err := Write(context.Background(), path, apps, BuildInfo{BuiltAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	db, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ctx := context.Background()
	for name, want := range map[string]string{"tree": "homebrew:tree", "homebrew:tree": "homebrew:tree",
		"editor": "org.example.Editor", "editor.desktop": "org.example.Editor",
		"fedora-45/editor": "org.example.Editor"} {
		if a, err := db.Resolve(ctx, name); err != nil || a.ID != want {
			t.Errorf("Resolve(%q) = %v, %v; want %s", name, a, err, want)
		}
	}
	var amb *AmbiguousError
	if _, err := db.Resolve(ctx, "shared"); !errors.As(err, &amb) || len(amb.IDs) != 2 {
		t.Errorf("Resolve(shared) err = %v", err)
	}
	if _, err := db.Resolve(ctx, "nope"); !errors.Is(err, ErrNotFound) {
		t.Errorf("Resolve(nope) err = %v", err)
	}
}

func TestOpenMissing(t *testing.T) {
	if _, err := Open(filepath.Join(t.TempDir(), "nope.db")); err == nil {
		t.Fatal("expected an error")
	}
}

func TestBrowse(t *testing.T) {
	path := filepath.Join(t.TempDir(), "catalog.db")
	app := func(id, cat string, pop int, icon string) App {
		return App{ID: id, Kind: KindDesktopApp, Name: id, IconURL: icon, Categories: []string{cat}, Popularity: pop,
			Listings: []Listing{{Source: SourceFlathub, Ref: "app/" + id + "/x86_64/stable", Name: id}}}
	}
	apps := []App{app("a.Small", "Game", 10, "i"), app("b.Big", "Game", 9000, "i"), app("c.Office", "Office", 500, "i"),
		app("d.NoIcon", "Game", 99999, "")}
	if err := Write(context.Background(), path, apps, BuildInfo{BuiltAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	db, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	rs, err := db.Browse(context.Background(), BrowseQuery{Category: "Game", Order: OrderPopular, Limit: 10, AppsOnly: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(rs) != 2 || rs[0].ID != "b.Big" || rs[1].ID != "a.Small" {
		t.Errorf("Game = %+v", rs)
	}
	if rs, _ := db.Browse(context.Background(), BrowseQuery{Category: "", Order: OrderRelevance, Limit: 10, AppsOnly: true}); len(rs) != 3 {
		t.Errorf("all = %+v", rs)
	}
}

func TestOrders(t *testing.T) {
	path := filepath.Join(t.TempDir(), "catalog.db")
	day := func(n int) time.Time { return time.Date(2026, 1, n, 0, 0, 0, 0, time.UTC) }
	mk := func(id, name string, pop, stars int, updated time.Time) App {
		return App{ID: id, Kind: KindDesktopApp, Name: name, Summary: "editor", IconURL: "i", Popularity: pop,
			Stars: stars, Updated: updated,
			Listings: []Listing{{Source: SourceFlathub, Ref: "app/" + id + "/x86_64/stable", Name: name}}}
	}
	apps := []App{mk("a", "Zed", 10, 0, day(3)), mk("b", "Atom", 500, 0, day(1)), mk("c", "Kate", 10, 900, day(2))}
	if err := Write(context.Background(), path, apps, BuildInfo{BuiltAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	db, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ids := func(rs []SearchResult) string {
		var s []string
		for _, r := range rs {
			s = append(s, r.ID)
		}
		return strings.Join(s, ",")
	}
	ctx := context.Background()
	for order, want := range map[Order]string{OrderPopular: "b,c,a", OrderUpdated: "a,c,b", OrderName: "b,c,a"} {
		rs, err := db.Search(ctx, "editor", SearchOptions{Order: order, Limit: 10})
		if err != nil || ids(rs) != want {
			t.Errorf("Search by %s = %s, %v; want %s", order, ids(rs), err, want)
		}
		rs, err = db.Browse(ctx, BrowseQuery{Category: "", Order: order, Limit: 10, AppsOnly: true})
		if err != nil || ids(rs) != want {
			t.Errorf("Browse by %s = %s, %v; want %s", order, ids(rs), err, want)
		}
	}
	rs, _ := db.Browse(ctx, BrowseQuery{Category: "", Order: OrderPopular, Limit: 10, AppsOnly: true})
	if rs[1].Stars != 900 || !rs[0].Updated.Equal(day(1)) || rs[0].Popularity != 500 {
		t.Errorf("result details = %+v", rs[:2])
	}
	if a, _ := db.App(ctx, "c"); a.Stars != 900 || !a.Updated.Equal(day(2)) {
		t.Errorf("App = %+v", a)
	}
}

func TestSourceFiltersAndCounts(t *testing.T) {
	path := filepath.Join(t.TempDir(), "catalog.db")
	apps := []App{
		{ID: "a", Kind: KindDesktopApp, Name: "Alpha editor", IconURL: "i", Popularity: 5000, Listings: []Listing{
			{Source: SourceFlathub, Ref: "app/a/x86_64/stable", Name: "Alpha"},
			{Source: SourceFedora, Ref: "fedora-45/alpha", Package: "alpha", Name: "Alpha"}}},
		{ID: "homebrew:beta", Kind: KindConsoleApp, Name: "beta editor", Popularity: 10, Listings: []Listing{
			{Source: SourceHomebrew, Ref: "beta", Package: "beta", Name: "beta"}}},
	}
	if err := Write(context.Background(), path, apps, BuildInfo{BuiltAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	db, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ctx := context.Background()

	if rs, _ := db.Search(ctx, "editor", SearchOptions{Source: SourceHomebrew}); len(rs) != 1 || rs[0].ID != "homebrew:beta" {
		t.Errorf("search by source = %+v", rs)
	}
	// Source pages list console tools too; browsing pages keep desktop apps with icons.
	if rs, _ := db.Browse(ctx, BrowseQuery{Source: SourceHomebrew}); len(rs) != 1 {
		t.Errorf("browse homebrew = %+v", rs)
	}
	if rs, _ := db.Browse(ctx, BrowseQuery{AppsOnly: true}); len(rs) != 1 || rs[0].ID != "a" {
		t.Errorf("browse apps only = %+v", rs)
	}
	if rs, _ := db.Browse(ctx, BrowseQuery{MinPopularity: 100}); len(rs) != 1 || rs[0].ID != "a" {
		t.Errorf("browse min popularity = %+v", rs)
	}
	rs, _ := db.Browse(ctx, BrowseQuery{Source: SourceFedora})
	if len(rs) != 1 || len(rs[0].Refs) != 2 || !slices.Contains(rs[0].Refs, ListingRef{Source: SourceFedora, Ref: "fedora-45/alpha", Package: "alpha"}) {
		t.Errorf("refs = %+v", rs)
	}
	counts, err := db.SourceCounts(ctx)
	if err != nil || counts[SourceFlathub] != 1 || counts[SourceFedora] != 1 || counts[SourceHomebrew] != 1 {
		t.Errorf("counts = %v, %v", counts, err)
	}
}

// TestSearchWholeWordPrefix ranks a name that starts with the query as a word above a name the stemmer
// matches, even a shorter one.
func TestSearchWholeWordPrefix(t *testing.T) {
	path := filepath.Join(t.TempDir(), "catalog.db")
	apps := []App{
		{ID: "io.example.Godots", Kind: KindDesktopApp, Name: "Godots", Summary: "Godot version manager",
			Listings: []Listing{{Source: SourceFlathub, Ref: "app/io.example.Godots/x86_64/stable", Name: "Godots"}}},
		{ID: "org.godotengine.Godot", Kind: KindDesktopApp, Name: "Godot Engine", Summary: "Make games",
			Listings: []Listing{{Source: SourceFlathub, Ref: "app/org.godotengine.Godot/x86_64/stable", Name: "Godot Engine"}}},
	}
	if err := Write(context.Background(), path, apps, BuildInfo{BuiltAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	db, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	res, err := db.Search(context.Background(), "godot", SearchOptions{})
	if err != nil || len(res) != 2 || res[0].ID != "org.godotengine.Godot" {
		t.Fatalf("results = %+v, %v; want Godot Engine first", res, err)
	}
}

// TestSearchShort ranks one- and two-letter searches by name prefix and popularity.
func TestSearchShort(t *testing.T) {
	path := filepath.Join(t.TempDir(), "catalog.db")
	app := func(id, name, summary string, pop int) App {
		return App{ID: id, Kind: KindDesktopApp, Name: name, Summary: summary, Popularity: pop,
			Listings: []Listing{{Source: SourceFlathub, Ref: "app/" + id + "/x86_64/stable", Name: name}}}
	}
	apps := []App{
		app("org.example.Gimp", "GIMP", "Edit images", 500),
		app("org.example.Go", "Go", "A language", 10),
		app("org.example.Godot", "Godot Engine", "Make games", 1000),
		app("org.example.Notes", "Notes", "Good notes", 5000), // "go" only in its summary
	}
	if err := Write(context.Background(), path, apps, BuildInfo{BuiltAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	db, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	res, err := db.Search(context.Background(), "go", SearchOptions{})
	if err != nil {
		t.Fatal(err)
	}
	var ids []string
	for _, r := range res {
		ids = append(ids, r.ID)
	}
	// The exact name first, then name prefixes by popularity; a summary match doesn't count for two letters.
	if strings.Join(ids, " ") != "org.example.Go org.example.Godot" {
		t.Errorf("results = %v", ids)
	}
}

func TestCategoryWords(t *testing.T) {
	got := categoryWords([]string{"WebBrowser", "IDE", "3DGraphics", "GUIDesigner", "P2P", "Office"})
	want := []string{"web browser", "ide", "3d graphics", "gui designer", "p2p", "office"}
	if !slices.Equal(got, want) {
		t.Errorf("categoryWords = %q, want %q", got, want)
	}
}

// TestSearchSynonyms finds apps by category words and by the names of well-known apps of their kind.
func TestSearchSynonyms(t *testing.T) {
	path := filepath.Join(t.TempDir(), "catalog.db")
	app := func(id, name, summary string, categories ...string) App {
		return App{ID: id, Kind: KindDesktopApp, Name: name, Summary: summary, Categories: categories,
			Listings: []Listing{{Source: SourceFlathub, Ref: "app/" + id + "/x86_64/stable", Name: name}}}
	}
	apps := []App{
		app("org.example.Calc", "Calc", "Work with numbers", "Office", "Spreadsheet"),
		app("org.example.Builder", "Builder", "Write software", "Development", "IDE"),
		app("org.example.Tagger", "Tagger", "Identify songs", "AudioVideo"),
		app("org.example.Surf", "Surf", "Go places", "Network", "WebBrowser"),
	}
	if err := Write(context.Background(), path, apps, BuildInfo{BuiltAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	db, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	for q, want := range map[string]string{
		"excel":       "org.example.Calc",    // a synonym of a category word
		"spreadsheet": "org.example.Calc",    // a category word
		"ide":         "org.example.Builder", // whole word: not "identify"
		"web browser": "org.example.Surf",
		"browser":     "org.example.Surf",
	} {
		res, err := db.Search(context.Background(), q, SearchOptions{})
		var ids []string
		for _, r := range res {
			ids = append(ids, r.ID)
		}
		if err != nil || len(ids) != 1 || ids[0] != want {
			t.Errorf("Search(%q) = %v, %v; want only %s", q, ids, err, want)
		}
	}
}

// TestSearchGenericByPopularity ranks a search for a kind of app by installs among apps that name the kind
// in their name, summary or categories; apps that only mention it in their description, or only start a
// word with it, come after.
func TestSearchGenericByPopularity(t *testing.T) {
	path := filepath.Join(t.TempDir(), "catalog.db")
	app := func(id, name, summary, description string, pop int) App {
		return App{ID: id, Kind: KindDesktopApp, Name: name, Summary: summary, Description: description,
			Popularity: pop, Listings: []Listing{{Source: SourceFlathub, Ref: "app/" + id + "/x86_64/stable", Name: name}}}
	}
	apps := []App{
		app("org.example.Small", "Vault", "A password manager", "", 100),
		app("org.example.Big", "Keys", "Password manager and secure wallet", "", 90000),
		app("org.example.Vpn", "Tunnel", "Private networking", "Has a password manager built in.", 900000),
		app("org.example.Managers", "Pass", "Managerless passwordless logins", "", 500000),
	}
	if err := Write(context.Background(), path, apps, BuildInfo{BuiltAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	db, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	res, err := db.Search(context.Background(), "password manager", SearchOptions{})
	var ids []string
	for _, r := range res {
		ids = append(ids, r.ID)
	}
	want := []string{"org.example.Big", "org.example.Small", "org.example.Managers", "org.example.Vpn"}
	if err != nil || !slices.Equal(ids, want) {
		t.Errorf("results = %v, %v; want %v", ids, err, want)
	}
}
