package session

import (
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/tyler-builds/uas/internal/catalog"
	"github.com/tyler-builds/uas/internal/client/backend"
	"github.com/tyler-builds/uas/internal/client/backend/github"
	"github.com/tyler-builds/uas/internal/client/fromurl"
	"github.com/tyler-builds/uas/internal/client/platform"
	"github.com/tyler-builds/uas/internal/client/settings"
	"github.com/tyler-builds/uas/internal/client/state"
	"github.com/tyler-builds/uas/internal/client/telemetry"
)

// fakeGitHub serves two repos through GitHub's REST API: small/tool, which ships an AppImage, and
// gnhen/midscroll, which ships only system packages. It counts requests.
func fakeGitHub(t *testing.T) (*httptest.Server, *atomic.Int32) {
	t.Helper()
	var n atomic.Int32
	asset := func(repo, tag, name string) string {
		return `{"name": "` + name + `", "size": 3, "digest": "sha256:` + strings.Repeat("d", 64) +
			`", "browser_download_url": "https://github.com/` + repo + `/releases/download/` + tag + `/` + name + `"}`
	}
	bodies := map[string]string{
		"/repos/small/tool": `{"full_name": "small/tool", "name": "tool", "description": "A small tool",
			"html_url": "https://github.com/small/tool", "created_at": "2025-01-01T00:00:00Z",
			"stargazers_count": 4, "owner": {"login": "small"}}`,
		"/repos/small/tool/releases": `[{"tag_name": "v0.3", "published_at": "2026-10-01T00:00:00Z", "assets": [` +
			asset("small/tool", "v0.3", "Tool-0.3-x86_64.AppImage") + `]}]`,
		"/repos/gnhen/midscroll": `{"full_name": "gnhen/midscroll", "name": "midscroll",
			"html_url": "https://github.com/gnhen/midscroll", "created_at": "2026-07-20T00:00:00Z",
			"stargazers_count": 222, "owner": {"login": "gnhen"}}`,
		"/repos/gnhen/midscroll/releases": `[{"tag_name": "v1.15", "published_at": "2026-08-29T00:00:00Z", "assets": [` +
			asset("gnhen/midscroll", "v1.15", "midscroll-1.15-1.fc44.noarch.rpm") + `,` +
			asset("gnhen/midscroll", "v1.15", "midscroll_1.15-1_all.deb") + `]}]`,
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n.Add(1)
		body, ok := bodies[r.URL.Path]
		if !ok {
			http.NotFound(w, r)
			return
		}
		w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	return srv, &n
}

// linkSession has a catalog with a GitHub app (example/notes) and a Flathub app whose code is on GitHub
// (example/editor), a GitHub backend, and a fake GitHub for everything else.
func linkSession(t *testing.T) (*Session, *atomic.Int32) {
	t.Helper()
	ctx := context.Background()
	dir := t.TempDir()
	path := filepath.Join(dir, "catalog.db")
	apps := []catalog.App{
		{ID: "github:example/notes", Kind: catalog.KindDesktopApp, Name: "Notes",
			Listings: []catalog.Listing{{Source: catalog.SourceGitHub, Ref: "example/notes", Name: "Notes"}}},
		{ID: "org.example.Editor", Kind: catalog.KindDesktopApp, Name: "Editor",
			Listings: []catalog.Listing{{Source: catalog.SourceFlathub, Ref: "app/org.example.Editor/x86_64/stable",
				AppStreamID: "org.example.Editor", Name: "Editor", VCSURL: "https://github.com/example/editor.git"}}},
	}
	if err := catalog.Write(ctx, path, apps, catalog.BuildInfo{BuiltAt: time.Now(), Apps: len(apps)}); err != nil {
		t.Fatal(err)
	}
	srv, n := fakeGitHub(t)
	gh := &github.Backend{State: state.Store{Path: filepath.Join(dir, "state.json")}}
	s := &Session{Settings: settings.Default(), Telemetry: telemetry.Nop{}, CatalogPath: path,
		Platform: platform.Info{Arch: "x86_64"},
		Backends: map[catalog.SourceID]backend.Backend{catalog.SourceGitHub: gh},
		Links:    &fromurl.Resolver{Client: srv.Client(), API: srv.URL, Arch: "x86_64"}}
	return s, n
}

func TestResolveAppFromLink(t *testing.T) {
	s, requests := linkSession(t)
	ctx := context.Background()
	db, err := catalog.Open(s.CatalogPath)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	// Apps the catalog has open as themselves, found by their release listing or their code's link, and
	// without asking GitHub.
	for link, want := range map[string]string{
		"https://github.com/Example/Notes/releases/tag/v1": "github:example/notes",
		"github.com/example/editor":                        "org.example.Editor",
	} {
		app, err := s.ResolveApp(ctx, db, link)
		if err != nil || app.ID != want || app.Live() {
			t.Errorf("ResolveApp(%q) = %+v, %v; want %s", link, app, err, want)
		}
	}
	if n := requests.Load(); n != 0 {
		t.Errorf("%d requests to GitHub for apps in the catalog", n)
	}

	// Others are read from GitHub, by link or by the ID their page has.
	for _, id := range []string{"https://github.com/small/tool", "github:small/tool"} {
		app, err := s.ResolveApp(ctx, db, id)
		if err != nil {
			t.Fatalf("ResolveApp(%q): %v", id, err)
		}
		if !app.Live() || app.ID != "github:small/tool" || app.Name != "Tool" || app.Summary != "A small tool" ||
			len(app.Listings) != 1 || app.Listings[0].Trust == nil {
			t.Errorf("ResolveApp(%q) = %+v", id, app)
		}
		l := app.Listings[0]
		if !s.Available(l) || s.Unavailable(l) != "" {
			t.Errorf("%s: not installable: %s", id, s.Unavailable(l))
		}
		if notes := strings.Join(s.InstallNotes(l), "\n"); !strings.Contains(notes, "Not in uas's catalog") ||
			!strings.Contains(notes, "never runs scripts") {
			t.Errorf("install notes:\n%s", notes)
		}
	}
	if n := requests.Load(); n != 2 {
		t.Errorf("%d requests to GitHub; the second lookup should be kept from the first", n)
	}

	// An app whose release has only system packages is shown, with why it can't be installed.
	app, err := s.ResolveApp(ctx, db, "https://github.com/gnhen/midscroll")
	if err != nil {
		t.Fatal(err)
	}
	why := s.Unavailable(app.Listings[0])
	if s.Available(app.Listings[0]) || !strings.Contains(why, "midscroll-1.15-1.fc44.noarch.rpm") ||
		!strings.Contains(why, "no AppImage or Flatpak bundle for x86_64") {
		t.Errorf("midscroll: %q", why)
	}

	if _, err := s.ResolveApp(ctx, db, "https://github.com/no/such-repo"); err == nil {
		t.Error("a repo GitHub doesn't have resolved")
	}
	if _, err := s.ResolveApp(ctx, db, "https://gitlab.com/owner/app"); err == nil {
		t.Error("a GitLab link resolved")
	}
}

func TestRestoreAppFromLink(t *testing.T) {
	s, _ := linkSession(t)
	ex := Export{Version: ExportVersion, Apps: []ExportedApp{
		{Source: catalog.SourceGitHub, Ref: "small/tool", AppID: "small/tool", Name: "tool", Scope: "user", FromURL: true},
		{Source: catalog.SourceGitHub, Ref: "gnhen/midscroll", AppID: "gnhen/midscroll", Name: "midscroll", FromURL: true},
		{Source: catalog.SourceGitHub, Ref: "gone/app", AppID: "gone/app", Name: "gone"},
	}}
	plan, err := s.PlanRestore(context.Background(), ex)
	if err != nil {
		t.Fatal(err)
	}
	if len(plan) != 3 {
		t.Fatalf("plan = %+v", plan)
	}
	if it := plan[0]; it.Action != RestoreInstall || !it.Listing.Live || it.Listing.Ref != "small/tool" ||
		!strings.Contains(it.Note, "isn't in uas's catalog") {
		t.Errorf("small/tool: %+v", it)
	}
	if it := plan[1]; it.Action != RestoreUnavailable || !strings.Contains(it.Note, "AppImage") {
		t.Errorf("midscroll: %+v", it)
	}
	// Not installed from a link: as before, not in the catalog.
	if it := plan[2]; it.Action != RestoreUnavailable || it.Note != "not in the catalog" {
		t.Errorf("gone/app: %+v", it)
	}
}
