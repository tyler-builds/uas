package forge

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/tyler-builds/uas/internal/catalog"
)

var now = time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)

// fakeFile is what the fake forge serves as every release file.
var fakeFile = []byte("not really an AppImage")

func fakeSum() string {
	s := sha256.Sum256(fakeFile)
	return hex.EncodeToString(s[:])
}

func TestCodeberg(t *testing.T) {
	var downloads atomic.Int32
	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		file := func(name string) map[string]any {
			return map[string]any{"name": name, "size": len(fakeFile), "download_count": 500,
				"browser_download_url": srv.URL + "/" + r.URL.Query().Get("owner") + name}
		}
		switch {
		case r.URL.Path == "/api/v1/repos/search" && r.URL.Query().Get("page") == "1":
			json.NewEncoder(w).Encode(map[string]any{"data": []map[string]any{
				{"full_name": "tenacity/tenacity", "name": "tenacity", "description": "Audio editor",
					"html_url": "https://codeberg.org/tenacity/tenacity", "stars_count": 1000,
					"created_at": "2021-07-01T00:00:00Z", "release_counter": 10, "owner": map[string]any{"login": "tenacity"}},
				{"full_name": "someone/mirror", "stars_count": 900, "mirror": true, "release_counter": 3},
				{"full_name": "someone/lib", "name": "lib", "stars_count": 800, "release_counter": 3,
					"created_at": "2020-01-01T00:00:00Z"},
				{"full_name": "small/app", "stars_count": 3, "release_counter": 3},
			}})
		case r.URL.Path == "/api/v1/repos/search":
			json.NewEncoder(w).Encode(map[string]any{"data": []any{}})
		case r.URL.Path == "/api/v1/repos/tenacity/tenacity/releases":
			json.NewEncoder(w).Encode([]map[string]any{
				{"tag_name": "v1.4.0-beta", "published_at": "2026-10-20T00:00:00Z", "prerelease": true},
				{"tag_name": "v1.3.5", "published_at": "2026-09-01T00:00:00Z", "assets": []any{
					map[string]any{"name": "tenacity-1.3.5-x86_64.AppImage", "size": len(fakeFile),
						"download_count": 500,
						"browser_download_url": "https://codeberg.org/tenacity/tenacity/releases/download/v1.3.5/tenacity-1.3.5-x86_64.AppImage"},
					map[string]any{"name": "tenacity-1.3.5-aarch64.AppImage", "size": 10,
						"browser_download_url": "https://codeberg.org/tenacity/tenacity/releases/download/v1.3.5/tenacity-1.3.5-aarch64.AppImage"},
				}},
				{"tag_name": "v1.3.4", "published_at": "2026-05-01T00:00:00Z"},
			})
		case r.URL.Path == "/api/v1/repos/someone/lib/releases":
			json.NewEncoder(w).Encode([]map[string]any{{"tag_name": "v2", "published_at": "2026-09-01T00:00:00Z",
				"assets": []any{file("lib.tar.gz")}}})
		case strings.HasSuffix(r.URL.Path, ".AppImage"):
			downloads.Add(1)
			w.Write(fakeFile)
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	// Release files live on codeberg.org; send those requests to the test server too.
	client := &http.Client{Transport: rewrite{to: srv.URL}}
	dir := t.TempDir()
	f := NewCodeberg(Builder{Client: client, Arch: "x86_64", MediaDir: dir, Now: func() time.Time { return now }})
	f.API = srv.URL + "/api/v1"
	f.MinStars = 10
	f.pace.every = 0
	ls, err := f.Fetch(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(ls) != 1 {
		t.Fatalf("got %d listings, want 1: %+v", len(ls), ls)
	}
	l := ls[0]
	if l.Source != catalog.SourceCodeberg || l.Ref != "codeberg.org/tenacity/tenacity" || l.Version != "1.3.5" ||
		l.Name != "tenacity" {
		t.Errorf("listing = %+v", l)
	}
	if len(l.Assets) != 1 || l.Assets[0].SHA256 != fakeSum() || l.Assets[0].Size != int64(len(fakeFile)) ||
		!strings.Contains(l.Assets[0].Name, "x86_64") {
		t.Errorf("assets = %+v", l.Assets)
	}
	if l.Trust == nil || l.Trust.Score < DefaultMinScore {
		t.Fatalf("trust = %+v", l.Trust)
	}
	var license string
	for _, s := range l.Trust.Signals {
		if s.Name == "License" {
			license = s.Detail
		}
	}
	if !strings.Contains(license, "Codeberg") {
		t.Errorf("license signal = %q", license)
	}

	// A second run reuses the recorded checksum instead of downloading again.
	if _, err := f.Fetch(context.Background()); err != nil {
		t.Fatal(err)
	}
	if n := downloads.Load(); n != 1 {
		t.Errorf("downloaded %d times, want 1", n)
	}
}

func TestGitLabNeedsToken(t *testing.T) {
	f := NewGitLab(Builder{Arch: "x86_64"}, "")
	if _, err := f.Fetch(context.Background()); err == nil || !strings.Contains(err.Error(), "GITLAB_TOKEN") {
		t.Fatalf("err = %v", err)
	}
}

func TestGitLab(t *testing.T) {
	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer tok" {
			http.Error(w, "no token", http.StatusUnauthorized)
			return
		}
		switch {
		case r.URL.Path == "/api/v4/projects" && r.URL.Query().Get("page") == "1":
			json.NewEncoder(w).Encode([]map[string]any{
				{"id": 1, "path_with_namespace": "group/sub/app", "name": "App", "web_url": "https://gitlab.com/group/sub/app",
					"star_count": 400, "created_at": "2020-01-01T00:00:00Z", "namespace": map[string]any{"name": "Group"}},
				{"id": 2, "path_with_namespace": "fork/app", "star_count": 300, "forked_from_project": map[string]any{"id": 1}},
			})
		case r.URL.Path == "/api/v4/projects":
			json.NewEncoder(w).Encode([]any{})
		case r.URL.Path == "/api/v4/projects/1/releases":
			json.NewEncoder(w).Encode([]map[string]any{
				{"tag_name": "v2.0-rc1", "released_at": "2026-10-01T00:00:00Z"},
				{"tag_name": "v1.9", "released_at": "2026-08-01T00:00:00Z", "assets": map[string]any{"links": []any{
					map[string]any{"name": "App-x86_64.AppImage", "url": "https://example.org/App-x86_64.AppImage",
						"direct_asset_url": "https://gitlab.com/group/sub/app/-/releases/v1.9/downloads/App-x86_64.AppImage"},
				}}},
			})
		case r.URL.Path == "/api/v4/projects/1":
			json.NewEncoder(w).Encode(map[string]any{"license": map[string]any{"key": "gpl-3.0"}})
		case strings.HasSuffix(r.URL.Path, ".AppImage"):
			w.Write(fakeFile)
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	client := &http.Client{Transport: rewrite{to: srv.URL, header: "Bearer tok"}}
	f := NewGitLab(Builder{Client: client, Arch: "x86_64", MediaDir: t.TempDir(), Now: func() time.Time { return now }}, "tok")
	f.API = srv.URL + "/api/v4"
	f.pace.every = 0
	ls, err := f.Fetch(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(ls) != 1 {
		t.Fatalf("got %d listings: %+v", len(ls), ls)
	}
	l := ls[0]
	if l.Ref != "gitlab.com/group/sub/app" || l.Version != "1.9" || l.License != "GPL-3.0" || l.Developer != "Group" ||
		len(l.Assets) != 1 || l.Assets[0].SHA256 != fakeSum() {
		t.Errorf("listing = %+v", l)
	}
}

func TestPacer(t *testing.T) {
	p := pacer{every: 20 * time.Millisecond}
	start := time.Now()
	for range 4 {
		if err := p.wait(context.Background()); err != nil {
			t.Fatal(err)
		}
	}
	if d := time.Since(start); d < 55*time.Millisecond {
		t.Errorf("4 requests took %v, want at least 60ms", d)
	}
}

func TestRateLimitedEndsFetch(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Retry-After", "1588")
		w.WriteHeader(http.StatusTooManyRequests)
	}))
	defer srv.Close()
	f := NewCodeberg(Builder{Client: srv.Client(), Arch: "x86_64", MediaDir: t.TempDir()})
	f.API = srv.URL
	start := time.Now()
	_, err := f.Fetch(context.Background())
	if err == nil || !strings.Contains(err.Error(), "rate limited") {
		t.Fatalf("err = %v", err)
	}
	if time.Since(start) > 5*time.Second {
		t.Error("waited out a long rate limit")
	}
}

// rewrite sends every request to a test server, keeping its path.
type rewrite struct{ to, header string }

func (r rewrite) RoundTrip(req *http.Request) (*http.Response, error) {
	u := fmt.Sprintf("%s%s", r.to, req.URL.RequestURI())
	out, err := http.NewRequestWithContext(req.Context(), req.Method, u, req.Body)
	if err != nil {
		return nil, err
	}
	out.Header = req.Header.Clone()
	if r.header != "" && out.Header.Get("Authorization") == "" {
		out.Header.Set("Authorization", r.header)
	}
	return http.DefaultTransport.RoundTrip(out)
}

// TestCodebergLive reads real Codeberg repos with 300+ stars (about 60 requests, under Codeberg's 2,000 per
// 10 minutes) and downloads their files. Run it with UAS_LIVE=1.
func TestCodebergLive(t *testing.T) {
	if os.Getenv("UAS_LIVE") == "" {
		t.Skip("set UAS_LIVE=1 to run against codeberg.org")
	}
	f := NewCodeberg(Builder{Client: &http.Client{Timeout: 10 * time.Minute}, Arch: "x86_64",
		MediaDir: t.TempDir(), Log: slog.Default()})
	f.MinStars = 300
	ls, err := f.Fetch(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	for _, l := range ls {
		t.Logf("%s %q %s score=%d assets=%v icon=%v shots=%d", l.Ref, l.Name, l.Version, l.Trust.Score,
			l.Assets, l.IconURL != "", len(l.Screenshots))
	}
}
