package catalog

import (
	"context"
	"errors"
	"path/filepath"
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
		rs, err := db.Search(ctx, q, 10)
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

	rs, err := db.Search(ctx, "browser", 10)
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
	rs, err := db.Browse(context.Background(), "Game", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(rs) != 2 || rs[0].ID != "b.Big" || rs[1].ID != "a.Small" {
		t.Errorf("Game = %+v", rs)
	}
	if rs, _ := db.Browse(context.Background(), "", 10); len(rs) != 3 {
		t.Errorf("all = %+v", rs)
	}
}
