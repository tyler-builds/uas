package flathub

import (
	"context"
	"os"
	"reflect"
	"testing"
	"time"

	"github.com/tyler-builds/uas/internal/catalog"
)

func TestParse(t *testing.T) {
	f, err := os.Open("testdata/appstream.xml")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	listings, err := Parse(f, "x86_64")
	if err != nil {
		t.Fatal(err)
	}

	var ids []string
	for _, l := range listings {
		ids = append(ids, l.AppStreamID)
	}
	// The runtime and the add-on are skipped.
	if want := []string{"org.example.Browser", "org.example.Notes.desktop", "org.example.Fetch"}; !reflect.DeepEqual(ids, want) {
		t.Fatalf("ids = %q, want %q", ids, want)
	}

	b := listings[0]
	checks := []struct{ name, got, want string }{
		{"Ref", b.Ref, "app/org.example.Browser/x86_64/stable"},
		{"Kind", string(b.Kind), string(catalog.KindDesktopApp)},
		{"Name", b.Name, "Example Browser"},
		{"Summary", b.Summary, "Browse the web quickly"},
		{"Developer", b.Developer, "Example Project"},
		{"License", b.License, "MPL-2.0"},
		{"Homepage", b.Homepage, "https://example.org/"},
		{"VCSURL", b.VCSURL, "https://github.com/example/browser"},
		{"IconURL", b.IconURL, "https://example.org/icons/128@2.png"},
		{"Version", b.Version, "2.0.0"},
		{"VerifiedAs", b.VerifiedAs, "example.org"},
		{"Runtime", b.Runtime, "org.freedesktop.Platform/x86_64/25.08"},
		{"Description", b.Description, "A fast, private browser.\n\n" +
			"- Tabs\n- Run `example-browser --private` for private mode\n\n" +
			"1. Install\n2. Browse"},
	}
	for _, c := range checks {
		if c.got != c.want {
			t.Errorf("%s = %q, want %q", c.name, c.got, c.want)
		}
	}
	if !b.Verified {
		t.Error("Verified = false, want true")
	}
	if want := time.Unix(1760000000, 0).UTC(); !b.Released.Equal(want) {
		t.Errorf("Released = %v, want %v", b.Released, want)
	}
	if want := []string{"internet"}; !reflect.DeepEqual(b.Keywords, want) {
		t.Errorf("Keywords = %q, want %q", b.Keywords, want)
	}
	wantShots := []catalog.Screenshot{
		{URL: "https://example.org/shots/main.png", ThumbURL: "https://example.org/shots/main_624.png", Caption: "Main window"},
		{URL: "https://example.org/shots/settings.png", Caption: "Settings"},
	}
	if !reflect.DeepEqual(b.Screenshots, wantShots) {
		t.Errorf("Screenshots = %+v, want %+v", b.Screenshots, wantShots)
	}

	n := listings[1]
	if n.Kind != catalog.KindDesktopApp || n.Developer != "Sample Dev" || n.Verified {
		t.Errorf("legacy component = %+v", n)
	}
	if n.Ref != "app/org.example.Notes.desktop/x86_64/stable" {
		t.Errorf("Ref without bundle = %q", n.Ref)
	}
	if want := time.Date(2025, 3, 1, 0, 0, 0, 0, time.UTC); !n.Released.Equal(want) {
		t.Errorf("Released from date = %v, want %v", n.Released, want)
	}

	if listings[2].Kind != catalog.KindConsoleApp {
		t.Errorf("Kind = %q, want console-application", listings[2].Kind)
	}
}

func TestFetchFromFile(t *testing.T) {
	f := &Fetcher{Arch: "x86_64", File: "testdata/appstream.xml"}
	listings, err := f.Fetch(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(listings) != 3 {
		t.Fatalf("got %d listings, want 3", len(listings))
	}
}

func TestFetchRejectsUnknownArch(t *testing.T) {
	f := &Fetcher{Arch: "x86_64/../../evil", File: "testdata/appstream.xml"}
	if _, err := f.Fetch(context.Background()); err == nil {
		t.Fatal("expected an error")
	}
}
