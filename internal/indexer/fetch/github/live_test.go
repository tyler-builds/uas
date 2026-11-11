package github

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/tyler-builds/uas/internal/catalog"
)

// fakeREST serves GitHub REST answers by path, with a status per path for errors.
func fakeREST(t *testing.T, bodies map[string]string, status map[string]int) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "" {
			t.Errorf("%s sent a token", r.URL)
		}
		if code, ok := status[r.URL.Path]; ok {
			w.WriteHeader(code)
			return
		}
		body, ok := bodies[r.URL.RequestURI()]
		if !ok {
			http.NotFound(w, r)
			return
		}
		w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	return srv
}

const liveRepo = `{"full_name": "Small/Tool", "name": "Tool", "description": "Does one thing",
	"homepage": "https://tool.example", "html_url": "https://github.com/Small/Tool", "stargazers_count": 9,
	"created_at": "2024-01-01T00:00:00Z", "archived": %ARCHIVED%, "fork": false,
	"license": {"spdx_id": "MIT"}, "owner": {"login": "Small"}}`

var liveReleases = `[
	{"tag_name": "v2.1-beta", "published_at": "2026-11-01T00:00:00Z", "prerelease": true, "body": "beta",
	 "assets": []},
	{"tag_name": "v2.0", "published_at": "2026-10-01T00:00:00Z", "prerelease": false,
	 "body": "## Fixed\nCrash on start", "html_url": "https://github.com/Small/Tool/releases/tag/v2.0",
	 "assets": [
		{"name": "Tool-2.0-aarch64.AppImage", "browser_download_url": "https://github.com/Small/Tool/releases/download/v2.0/Tool-2.0-aarch64.AppImage", "size": 10, "digest": "sha256:` + aSum + `", "download_count": 3},
		{"name": "Tool-2.0-x86_64.AppImage", "browser_download_url": "https://github.com/Small/Tool/releases/download/v2.0/Tool-2.0-x86_64.AppImage", "size": 20, "digest": "sha256:` + bSum + `", "download_count": 40},
		{"name": "tool_2.0_amd64.deb", "browser_download_url": "https://github.com/Small/Tool/releases/download/v2.0/tool_2.0_amd64.deb", "size": 5, "download_count": 7}]},
	{"tag_name": "v1.0", "published_at": "2026-03-01T00:00:00Z", "prerelease": false, "body": "First",
	 "assets": []}]`

var (
	aSum = strings.Repeat("a", 64)
	bSum = strings.Repeat("b", 64)
)

func TestLive(t *testing.T) {
	srv := fakeREST(t, map[string]string{
		"/repos/small/tool":                     strings.ReplaceAll(liveRepo, "%ARCHIVED%", "true"),
		"/repos/Small/Tool/releases?per_page=8": liveReleases,
	}, nil)
	f := &Fetcher{Client: srv.Client(), RESTEndpoint: srv.URL, Arch: "x86_64",
		Now: func() time.Time { return time.Date(2026, 11, 11, 0, 0, 0, 0, time.UTC) }}
	l, err := f.Live(context.Background(), "small/tool", true)
	if err != nil {
		t.Fatal(err)
	}
	// The repo's own name and case, its latest stable release, and the x86_64 AppImage.
	if !l.Live || l.Source != catalog.SourceGitHub || l.Ref != "Small/Tool" || l.Version != "2.0" ||
		l.Name != "Tool" || l.Summary != "Does one thing" || l.License != "MIT" || l.Stars != 9 {
		t.Errorf("listing = %+v", l)
	}
	if len(l.Assets) != 1 || l.Assets[0].Name != "Tool-2.0-x86_64.AppImage" || l.Assets[0].SHA256 != bSum {
		t.Errorf("assets = %+v", l.Assets)
	}
	if l.Trust == nil || l.Trust.Score == 0 || !strings.Contains(strings.Join(l.Trust.Flags, "|"), "Archived") {
		t.Errorf("trust = %+v", l.Trust)
	}
	if len(l.Changelog) != 2 || l.Changelog[0].Version != "2.0" || !strings.Contains(l.Changelog[0].Notes, "Crash") {
		t.Errorf("changelog = %+v", l.Changelog)
	}
}

func TestLiveNothingToInstall(t *testing.T) {
	releases := `[{"tag_name": "v1.15", "published_at": "2026-08-29T00:00:00Z", "assets": [
		{"name": "midscroll-1.15-1.fc44.noarch.rpm", "browser_download_url": "https://github.com/Small/Tool/releases/download/v1.15/midscroll-1.15-1.fc44.noarch.rpm", "size": 5, "digest": "sha256:` + aSum + `"},
		{"name": "midscroll_1.15-1_all.deb", "browser_download_url": "https://github.com/Small/Tool/releases/download/v1.15/midscroll_1.15-1_all.deb", "size": 5}]}]`
	srv := fakeREST(t, map[string]string{
		"/repos/Small/Tool":                     strings.ReplaceAll(liveRepo, "%ARCHIVED%", "false"),
		"/repos/Small/Tool/releases?per_page=8": releases,
	}, nil)
	f := &Fetcher{Client: srv.Client(), RESTEndpoint: srv.URL, Arch: "x86_64"}
	l, err := f.Live(context.Background(), "Small/Tool", false)
	if err != nil {
		t.Fatal(err)
	}
	// Kept, unlike in the catalog, so the page can say why there is nothing to install.
	if len(l.Assets) != 1 || l.Assets[0].Kind != catalog.AssetRPM || l.Trust == nil {
		t.Errorf("listing = %+v", l)
	}
}

func TestLiveErrors(t *testing.T) {
	srv := fakeREST(t, map[string]string{
		"/repos/no/releases":                    strings.ReplaceAll(liveRepo, "%ARCHIVED%", "false"),
		"/repos/Small/Tool/releases?per_page=8": `[{"tag_name": "v0.1", "prerelease": true, "assets": []}]`,
	}, map[string]int{"/repos/limited/repo": http.StatusForbidden})
	f := &Fetcher{Client: srv.Client(), RESTEndpoint: srv.URL}
	ctx := context.Background()
	if _, err := f.Live(ctx, "missing/repo", false); !errors.Is(err, ErrNotFound) {
		t.Errorf("missing repo: %v", err)
	}
	if _, err := f.Live(ctx, "limited/repo", false); !errors.Is(err, ErrRateLimited) {
		t.Errorf("rate limited: %v", err)
	}
	if _, err := f.Live(ctx, "no/releases", false); !errors.Is(err, ErrNoRelease) {
		t.Errorf("only a prerelease: %v", err)
	}
	if _, err := f.Live(ctx, "../etc", false); err == nil {
		t.Error("a path that isn't a repo was looked up")
	}
}
