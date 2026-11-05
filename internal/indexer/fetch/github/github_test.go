package github

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
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
		Queries: []string{"q1", "q2"}, Now: func() time.Time { return time.Date(2026, 11, 4, 0, 0, 0, 0, time.UTC) }}
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

func TestDisplayName(t *testing.T) {
	for _, c := range []struct{ repo, file, want string }{
		{"t3code", "T3-Code-0.1.7-x86_64.AppImage", "T3 Code"},
		{"notes", "Notes-2.4.0-x86_64.AppImage", "Notes"},
		{"joplin", "Joplin-3.1.24.AppImage", "Joplin"},
		{"cool-app", "Something.Else-1.0.AppImage", "cool-app"}, // spells another name
		{"fluent-reader", "Fluent.Reader.1.1.4.AppImage", "Fluent Reader"},
		{"app", "v2.0-linux.AppImage", "app"},
		{"ghostty-appimage", "Ghostty-1.4.0-anylinux-x86_64.AppImage", "Ghostty"},
		{"OBS-Studio-AppImage", "OBS-Studio-32.0.1-x86_64.AppImage", "OBS Studio"},
		{"foo-appimage", "bar-1.0.AppImage", "foo"},
	} {
		if got := displayName(c.repo, []catalog.Asset{{Name: c.file}}); got != c.want {
			t.Errorf("displayName(%q, %q) = %q, want %q", c.repo, c.file, got, c.want)
		}
	}
}

// TestSweep finds a repo only the sweep sees, then reuses the sweep's finds on the next run.
func TestSweep(t *testing.T) {
	fixture, err := os.ReadFile("testdata/search.json")
	if err != nil {
		t.Fatal(err)
	}
	var found struct {
		Data struct {
			Search struct {
				Nodes json.RawMessage `json:"nodes"`
			} `json:"search"`
		} `json:"data"`
	}
	if err := json.Unmarshal(fixture, &found); err != nil {
		t.Fatal(err)
	}
	sweeps := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Query     string `json:"query"`
			Variables struct {
				Q string `json:"q"`
			} `json:"variables"`
		}
		json.NewDecoder(r.Body).Decode(&body)
		switch {
		case strings.Contains(body.Query, "repositoryCount"):
			w.Write([]byte(`{"data":{"search":{"repositoryCount":5}}}`))
		case strings.Contains(body.Query, "nodes(ids"):
			w.Write([]byte(`{"data":{"nodes":` + string(found.Data.Search.Nodes) + `}}`))
		case strings.Contains(body.Variables.Q, "pushed:>="):
			sweeps++
			w.Write(fixture)
		default: // the readme and topic searches find nothing
			w.Write([]byte(`{"data":{"search":{"pageInfo":{"hasNextPage":false},"nodes":[]}}}`))
		}
	}))
	defer srv.Close()

	cache := filepath.Join(t.TempDir(), "github", "sweep.json")
	now := time.Date(2026, 11, 4, 0, 0, 0, 0, time.UTC)
	for run := range 2 {
		f := &Fetcher{Client: srv.Client(), Token: "test-token", Arch: "x86_64", Endpoint: srv.URL,
			Queries: []string{"q1"}, SweepMinStars: 2000, SweepCache: cache, Now: func() time.Time { return now }}
		ls, err := f.Fetch(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		if len(ls) != 1 || ls[0].Ref != "example/notes" || ls[0].Name != "Notes" {
			t.Fatalf("run %d: listings = %+v", run, ls)
		}
		if sweeps != 1 {
			t.Fatalf("run %d: %d sweeps, want 1 (the second run reuses the first)", run, sweeps)
		}
	}
}
