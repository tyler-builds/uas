package gnome

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/tyler-builds/uas/internal/catalog"
)

func ext(uuid, name, desc string) extension {
	e := extension{UUID: uuid, Name: name, Description: desc, Downloads: 100}
	e.Creator.Username = "someone"
	return e
}

func TestFetch(t *testing.T) {
	dock := ext("dash-to-dock@micxgx.gmail.com", "Dash to Dock", "A dock for the Gnome Shell. This extension "+
		"moves the dash out of the overview.\nSide and bottom placement options are available.")
	dock.Creator.DisplayName = "michele_g"
	dock.Icon = Site + "/extension-data/icons/icon_307.png"
	dock.Screenshot = Site + "/extension-data/screenshots/screenshot_307.png"
	dock.URL = "https://micheleg.github.io/dash-to-dock/"
	dock.Link = "/extension/307/dash-to-dock/"
	themes := ext("user-theme@gnome-shell-extensions.gcampax.github.com", "User Themes", "Load shell themes")
	old := ext("old@example.com", "Old", "Only for GNOME 45")
	bad := ext("not a uuid", "Bad", "Dropped")

	// By Shell release, then page.
	pages := map[string][][]extension{
		"45": {{dock, old}, {themes}},
		"46": {{dock, bad, themes}},
	}
	var calls []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		calls = append(calls, q.Get("shell_version")+"/"+q.Get("page"))
		if q.Get("page_size") != "100" || q.Get("ordering") != "created" {
			http.Error(w, "query", http.StatusBadRequest)
			return
		}
		of := pages[q.Get("shell_version")]
		n := map[string]int{"1": 0, "2": 1}[q.Get("page")]
		var p page
		if n < len(of) {
			p.Results = of[n]
			if n+1 < len(of) {
				p.Next = "next"
			}
		}
		json.NewEncoder(w).Encode(p)
	}))
	defer srv.Close()

	f := &Fetcher{Client: srv.Client(), API: srv.URL, Every: time.Millisecond}
	ls, err := f.Fetch(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	// Both pages of 45, the one of 46, and 47, which is empty and ends the run.
	if want := []string{"45/1", "45/2", "46/1", "47/1"}; !reflect.DeepEqual(calls, want) {
		t.Errorf("calls = %v, want %v", calls, want)
	}
	if len(ls) != 3 {
		t.Fatalf("listings = %+v", ls)
	}
	d, o, th := ls[0], ls[1], ls[2]
	if d.Source != catalog.SourceGNOMEExtensions || d.Kind != catalog.KindShellExtension ||
		d.Ref != "dash-to-dock@micxgx.gmail.com" || d.Developer != "michele_g" || d.Installs != 100 ||
		d.IconURL != dock.Icon || len(d.Screenshots) != 1 || d.Homepage != dock.URL ||
		d.StoreURL != Site+"/extension/307/dash-to-dock/" || d.AppStreamID != "" {
		t.Errorf("dock = %+v", d)
	}
	if d.Summary != "A dock for the Gnome Shell." || !strings.Contains(d.Description, "\nSide and bottom") {
		t.Errorf("dock summary %q, description %q", d.Summary, d.Description)
	}
	if !reflect.DeepEqual(d.Shells, []string{"45", "46"}) || !reflect.DeepEqual(o.Shells, []string{"45"}) ||
		!reflect.DeepEqual(th.Shells, []string{"45", "46"}) {
		t.Errorf("shells: dock %v, old %v, themes %v", d.Shells, o.Shells, th.Shells)
	}
	if th.Developer != "someone" || th.IconURL != "" || th.Homepage != "" {
		t.Errorf("themes = %+v", th)
	}
}

func TestSummary(t *testing.T) {
	long := strings.Repeat("word ", 60)
	for in, want := range map[string]string{
		"Disable the screensaver and auto suspend":   "Disable the screensaver and auto suspend",
		"Shows  a   clock.\n\nMore below.":           "Shows a clock.",
		"A dock for the Shell. It moves the dash.":   "A dock for the Shell.",
		"v2. Now with more. And a lot more of that.": "v2. Now with more.",
	} {
		if got := summary(in); got != want {
			t.Errorf("summary(%q) = %q, want %q", in, got, want)
		}
	}
	if got := summary(long); len([]rune(got)) > 141 || !strings.HasSuffix(got, "word…") {
		t.Errorf("long summary = %q", got)
	}
}

// TestLive lists the real site (about 90 requests, one a second). Run it with UAS_LIVE=1.
func TestLive(t *testing.T) {
	if os.Getenv("UAS_LIVE") == "" {
		t.Skip("set UAS_LIVE=1")
	}
	ls, err := (&Fetcher{Client: http.DefaultClient}).Fetch(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	icons, newest := 0, map[string]int{}
	for _, l := range ls {
		if l.IconURL != "" {
			icons++
		}
		newest[l.Shells[len(l.Shells)-1]]++
	}
	t.Logf("%d extensions, %d with an icon, by newest GNOME release: %v", len(ls), icons, newest)
}
