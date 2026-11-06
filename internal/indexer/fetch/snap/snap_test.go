package snap

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	"github.com/tyler-builds/uas/internal/catalog"
)

func TestFetch(t *testing.T) {
	icon := []media{{Type: "icon", URL: "https://dashboard.snapcraft.io/icon.png"}}
	pages := [][]pkg{
		{
			{Name: "vlc", Title: "VLC", Summary: "The ultimate media player", Publisher: "VideoLAN", Validation: "verified",
				Media:     append(icon, media{Type: "screenshot", URL: "https://dashboard.snapcraft.io/s.png"}),
				CommonIDs: []string{"org.videolan.VLC"}, License: "GPL-2.0+", Version: "3.0.21", Confinement: "strict"},
			{Name: "code", Title: "Code", Summary: "Code editing. Redefined.", Publisher: "Visual Studio Code",
				Validation: "verified", Media: icon, Confinement: "classic"},
		},
		{
			// No icon, no section and nobody vouching: dropped.
			{Name: "my-test-snap", Title: "test", Summary: "testing", Validation: "unproven"},
			{Name: "hello-world", Title: "Hello", Summary: "hi", Validation: "unproven", Media: icon},
		},
	}
	var calls int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.Header.Get("X-Ubuntu-Series") != "16" || r.Header.Get("X-Ubuntu-Architecture") != "amd64" {
			http.Error(w, "headers", http.StatusBadRequest)
			return
		}
		n := map[string]int{"1": 0, "2": 1}[r.URL.Query().Get("page")]
		var p page
		p.Embedded.Packages = pages[n]
		if n == 0 {
			p.Links.Next = &struct {
				Href string `json:"href"`
			}{"next"}
		}
		json.NewEncoder(w).Encode(p)
	}))
	defer srv.Close()
	f := &Fetcher{Client: srv.Client(), Arch: "x86_64", API: srv.URL, Every: time.Millisecond}
	ls, err := f.Fetch(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if calls != 2 || len(ls) != 2 {
		t.Fatalf("calls %d, listings %+v", calls, ls)
	}
	code, vlc := ls[0], ls[1]
	if vlc.Source != catalog.SourceSnap || vlc.Ref != "vlc" || vlc.AppStreamID != "org.videolan.VLC" || !vlc.Verified ||
		vlc.VerifiedAs != "VideoLAN" || len(vlc.Screenshots) != 1 || vlc.Classic {
		t.Errorf("vlc = %+v", vlc)
	}
	if !code.Classic {
		t.Errorf("code = %+v", code)
	}
}

// TestLive lists the real store (about 90 requests, one a second). Run it with UAS_LIVE=1.
func TestLive(t *testing.T) {
	if os.Getenv("UAS_LIVE") == "" {
		t.Skip("set UAS_LIVE=1")
	}
	ls, err := (&Fetcher{Client: http.DefaultClient, Arch: "x86_64"}).Fetch(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	verified, ids := 0, 0
	for _, l := range ls {
		if l.Verified {
			verified++
		}
		if l.AppStreamID != "" {
			ids++
		}
	}
	t.Logf("%d snaps kept, %d verified, %d with an AppStream ID", len(ls), verified, ids)
}
