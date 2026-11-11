package fromurl

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/tyler-builds/uas/internal/catalog"
)

func TestParse(t *testing.T) {
	gh := func(repo string) Link { return Link{Source: catalog.SourceGitHub, Repo: repo} }
	for _, tc := range []struct {
		in   string
		want Link
	}{
		{"https://github.com/gnhen/midscroll", gh("gnhen/midscroll")},
		{"  https://github.com/gnhen/midscroll/  ", gh("gnhen/midscroll")},
		{"http://www.github.com/gnhen/midscroll.git", gh("gnhen/midscroll")},
		{"github.com/nikkNizz/Drawish", gh("nikkNizz/Drawish")},
		{"https://github.com/nikkNizz/Drawish/releases", gh("nikkNizz/Drawish")},
		{"https://github.com/nikkNizz/Drawish/releases/tag/v3.3", gh("nikkNizz/Drawish")},
		{"https://github.com/nikkNizz/Drawish/releases/latest", gh("nikkNizz/Drawish")},
		{"https://github.com/nikkNizz/Drawish/releases/download/v3.3/Drawish_3.3.AppImage", gh("nikkNizz/Drawish")},
		{"https://github.com/owner/app/tree/main/src?tab=readme#install", gh("owner/app")},
		{"git@github.com:owner/my.app.git", gh("owner/my.app")},
		{"https://codeberg.org/owner/app/releases", Link{Source: catalog.SourceCodeberg, Repo: "owner/app"}},
	} {
		got, err := Parse(tc.in)
		if err != nil || got != tc.want {
			t.Errorf("Parse(%q) = %+v, %v; want %+v", tc.in, got, err, tc.want)
		}
	}
	for _, in := range []string{
		"", "owner/app", "firefox", "https://github.com/", "https://github.com/owner",
		"https://github.com/topics/appimage", "https://github.com/orgs/owner/repositories",
		"https://gitlab.com/owner/app", "https://github.com.evil.example/owner/app",
		"https://user@github.com/owner/app", "ftp://github.com/owner/app", "https://github.com/owner/..",
		"https://github.com/-owner/app", "https://github.com/owner/app with spaces",
	} {
		if l, err := Parse(in); !errors.Is(err, ErrNotLink) {
			t.Errorf("Parse(%q) = %+v, %v; want ErrNotLink", in, l, err)
		}
	}
}

func TestLinkNames(t *testing.T) {
	gh := Link{Source: catalog.SourceGitHub, Repo: "owner/app"}
	cb := Link{Source: catalog.SourceCodeberg, Repo: "owner/app"}
	if gh.Ref() != "owner/app" || gh.AppID() != "github:owner/app" || gh.URL() != "https://github.com/owner/app" {
		t.Errorf("GitHub link: %q %q %q", gh.Ref(), gh.AppID(), gh.URL())
	}
	if cb.Ref() != "codeberg.org/owner/app" || cb.AppID() != "codeberg:codeberg.org/owner/app" {
		t.Errorf("Codeberg link: %q %q", cb.Ref(), cb.AppID())
	}
}

func TestIsLink(t *testing.T) {
	for in, want := range map[string]bool{
		"https://github.com/a/b": true, "github.com/a/b": true, " www.github.com/a/b": true, "git@github.com:a/b": true,
		"firefox": false, "org.mozilla.firefox": false, "owner/app": false, "text editor": false,
	} {
		if got := IsLink(in); got != want {
			t.Errorf("IsLink(%q) = %v", in, got)
		}
	}
}

func TestResolverKeepsLookups(t *testing.T) {
	var requests atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		switch r.URL.Path {
		case "/repos/owner/app":
			w.Write([]byte(`{"full_name": "Owner/App", "name": "App", "html_url": "https://github.com/Owner/App",
				"created_at": "2025-01-01T00:00:00Z", "stargazers_count": 3, "owner": {"login": "Owner"}}`))
		case "/repos/Owner/App/releases":
			w.Write([]byte(`[{"tag_name": "v1.2", "published_at": "2026-10-01T00:00:00Z", "assets": [
				{"name": "App-x86_64.AppImage", "size": 3, "digest": "sha256:` + strings.Repeat("c", 64) + `",
				 "browser_download_url": "https://github.com/Owner/App/releases/download/v1.2/App-x86_64.AppImage"}]}]`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	now := time.Date(2026, 11, 11, 0, 0, 0, 0, time.UTC)
	r := &Resolver{Client: srv.Client(), API: srv.URL, Arch: "x86_64", Now: func() time.Time { return now }}
	ctx := context.Background()
	link, _ := Parse("https://github.com/owner/app")
	l, err := r.Listing(ctx, link, false)
	if err != nil {
		t.Fatal(err)
	}
	if !l.Live || l.Ref != "Owner/App" || l.Version != "1.2" || len(l.Assets) != 1 {
		t.Fatalf("listing = %+v", l)
	}
	// Kept for an hour, under the link's name and the repo's own.
	for _, repo := range []string{"owner/app", "Owner/App"} {
		if _, err := r.Listing(ctx, Link{Source: catalog.SourceGitHub, Repo: repo}, false); err != nil {
			t.Fatal(err)
		}
	}
	if n := requests.Load(); n != 2 {
		t.Errorf("%d requests, want 2", n)
	}
	now = now.Add(TTL + time.Minute)
	if _, err := r.Listing(ctx, link, false); err != nil || requests.Load() != 4 {
		t.Errorf("after an hour: %v, %d requests", err, requests.Load())
	}

	if _, err := r.Listing(ctx, Link{Source: catalog.SourceCodeberg, Repo: "owner/app"}, false); !errors.Is(err, ErrCodeberg) {
		t.Errorf("Codeberg: %v", err)
	}
}
