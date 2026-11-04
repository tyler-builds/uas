package github

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
	fixture, err := os.ReadFile("testdata/search.json")
	if err != nil {
		t.Fatal(err)
	}
	var queries []string
	fail := 1 // the first request gets a 504, to exercise the retry
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer test-token" {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		if fail > 0 {
			fail--
			http.Error(w, "timeout", http.StatusGatewayTimeout)
			return
		}
		var body struct {
			Variables struct {
				Q string `json:"q"`
			} `json:"variables"`
		}
		json.NewDecoder(r.Body).Decode(&body)
		queries = append(queries, body.Variables.Q)
		w.Write(fixture)
	}))
	defer srv.Close()

	f := &Fetcher{Client: srv.Client(), Token: "test-token", Arch: "x86_64", Endpoint: srv.URL,
		Queries: []string{"q1", "q2"}, Now: func() time.Time { return time.Date(2026, 11, 4, 0, 0, 0, 0, time.UTC) },
		CacheDir: t.TempDir(), CacheFor: time.Hour}
	ls, err := f.Fetch(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(queries) != 2 {
		t.Errorf("queries = %q", queries)
	}
	// winonly has no Linux files, tiny scores too low, old is archived, sneaky links outside its repo.
	if len(ls) != 1 {
		t.Fatalf("listings = %+v", ls)
	}
	l := ls[0]
	if l.Ref != "example/notes" || l.Version != "2.4.0" || l.Summary != "Take notes" || l.Source != catalog.SourceGitHub ||
		l.VCSURL != "https://github.com/example/notes" || l.Developer != "example" {
		t.Errorf("listing = %+v", l)
	}
	if len(l.Assets) != 2 || l.Assets[0].Name != "Notes-2.4.0-x86_64.AppImage" || l.Assets[1].Kind != catalog.AssetRPM {
		t.Fatalf("assets = %+v", l.Assets)
	}
	if l.Assets[0].SHA256 != "cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc" || l.Assets[1].SHA256 != "" {
		t.Errorf("digests = %q, %q", l.Assets[0].SHA256, l.Assets[1].SHA256)
	}
	if l.Trust == nil || l.Trust.Score < 70 || len(l.Trust.Flags) != 0 {
		t.Errorf("trust = %+v", l.Trust)
	}

	// A second fetch within CacheFor makes no requests.
	again, err := f.Fetch(context.Background())
	if err != nil || len(again) != 1 || len(queries) != 2 {
		t.Errorf("cached fetch = %d listings, %v, %d queries", len(again), err, len(queries))
	}
}

func TestFetchNeedsToken(t *testing.T) {
	if _, err := (&Fetcher{Arch: "x86_64"}).Fetch(context.Background()); err == nil {
		t.Fatal("expected an error without a token")
	}
}

func TestPickAssetsARM(t *testing.T) {
	rel := &release{ReleaseAssets: struct {
		Nodes []asset `json:"nodes"`
	}{Nodes: []asset{
		{Name: "a.AppImage", DownloadURL: "https://github.com/o/r/releases/download/1/a.AppImage"},
		{Name: "a-aarch64.AppImage", DownloadURL: "https://github.com/o/r/releases/download/1/a-aarch64.AppImage"},
	}}}
	got := pickAssets("o/r", rel, "aarch64")
	if len(got) != 1 || got[0].Name != "a-aarch64.AppImage" {
		t.Errorf("assets = %+v", got)
	}
}
