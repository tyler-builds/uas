package gui

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/tyler-builds/jamb/access"
	"github.com/tyler-builds/jamb/ui/uitest"

	"github.com/tyler-builds/uas/internal/client/fromurl"
)

// linkApp is testApp with a fake GitHub that has small/new, a repo the catalog doesn't have.
func linkApp(t *testing.T) (*App, *fakeBackend) {
	t.Helper()
	a, _, gh := testApp(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/repos/small/new":
			w.Write([]byte(`{"full_name": "small/new", "name": "new", "description": "Brand new",
				"html_url": "https://github.com/small/new", "created_at": "2026-11-01T00:00:00Z",
				"stargazers_count": 2, "owner": {"login": "small"}}`))
		case "/repos/small/new/releases":
			w.Write([]byte(`[{"tag_name": "v0.1", "published_at": "2026-11-05T00:00:00Z", "assets": [
				{"name": "new-x86_64.AppImage", "size": 3, "digest": "sha256:` + strings.Repeat("e", 64) + `",
				 "browser_download_url": "https://github.com/small/new/releases/download/v0.1/new-x86_64.AppImage"}]}]`))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	a.s.Links = &fromurl.Resolver{Client: srv.Client(), API: srv.URL, Arch: "x86_64"}
	return a, gh
}

func TestLinkToCatalogApp(t *testing.T) {
	a, _ := linkApp(t)
	u := uitest.New(t, a.Root, uitest.Size(1100, 760))
	defer u.Close()
	u.WaitFor("the search field", func() bool { return u.HasRole(access.RoleTextField, "Search apps") })
	u.ByRole(access.RoleTextField, "Search apps").Type("https://github.com/Example/Tool/releases")
	// The catalog has it: its own row, which opens its usual page.
	u.WaitFor("the tool row", func() bool { return u.Has("row:github:example/tool") })
}

func TestInstallFromLink(t *testing.T) {
	a, _ := linkApp(t)
	a.goTo(view{page: pageSearch})
	u := uitest.New(t, a.Root, uitest.Size(1100, 900))
	defer u.Close()
	clickButton(u, "Install from a link")
	u.WaitFor("the link dialog", func() bool { return u.HasRole(access.RoleAlertDialog, "Install from a link") })
	u.ByRole(access.RoleTextField, "Link").Type("github.com/small/new")
	clickButton(u, "Open")

	key := "detail:github:small/new"
	u.WaitFor("the app's page", func() bool { return u.Has(key) })
	waitText(t, u, key, "Not in uas's catalog")
	waitText(t, u, key, "Brand new")
	waitText(t, u, key, "Trust score")
	waitText(t, u, key, "New project")

	clickButton(u, "Install")
	u.WaitFor("the confirmation", func() bool { return a.confirm.Peek() != nil })
	if p := a.confirm.Peek(); !p.listing.Live || p.listing.Ref != "small/new" {
		t.Errorf("confirming %+v", p.listing)
	}
	u.WaitFor("the warning", func() bool {
		return u.HasRole(access.RoleAlertDialog, "Install new from GitHub · 0.1 (trust "+
			fmt.Sprint(a.confirm.Peek().listing.Trust.Score)+")?")
	})
	u.Press("escape")
}
