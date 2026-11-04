package copr

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/tyler-builds/uas/internal/catalog"
)

func gz(s string) []byte {
	var b bytes.Buffer
	w := gzip.NewWriter(&b)
	w.Write([]byte(s))
	w.Close()
	return b.Bytes()
}

const appstreamXML = `<components origin="copr">
  <component type="desktop-application"><id>org.example.Notes</id><pkgname>notes</pkgname>
    <name>Notes</name><summary>Take notes</summary></component>
  <component type="addon"><id>org.example.Notes.Plugin</id><pkgname>notes-plugin</pkgname><name>Plugin</name></component>
</components>`

const primaryXML = `<metadata xmlns="http://linux.duke.edu/metadata/common">
<package type="rpm"><name>notes</name><arch>x86_64</arch><version epoch="0" ver="1.0" rel="1.fc45"/>
  <time build="1760000000"/><location href="notes-1.0-1.fc45.x86_64.rpm"/></package>
<package type="rpm"><name>notes</name><arch>x86_64</arch><version epoch="0" ver="1.1" rel="1.fc45"/>
  <time build="1770000000"/><location href="notes-1.1-1.fc45.x86_64.rpm"/></package>
<package type="rpm"><name>notes-plugin</name><arch>x86_64</arch><version epoch="0" ver="1.0" rel="1.fc45"/>
  <time build="1760000000"/><location href="notes-plugin-1.0-1.fc45.x86_64.rpm"/></package>
</metadata>`

func repomd(withAppStream bool) []byte {
	s := `<repomd><data type="primary"><location href="repodata/p-primary.xml.gz"/></data>`
	if withAppStream {
		s += `<data type="appstream"><location href="repodata/a-appstream.xml.gz"/></data>`
	}
	return []byte(s + `</repomd>`)
}

func TestFetch(t *testing.T) {
	var srv *httptest.Server
	now := time.Date(2026, 11, 4, 0, 0, 0, 0, time.UTC)
	files := map[string][]byte{}
	addRepo := func(full string, withAppStream bool) {
		base := "/results/" + full + "/fedora-45-x86_64/repodata/"
		files[base+"repomd.xml"] = repomd(withAppStream)
		files[base+"p-primary.xml.gz"] = gz(primaryXML)
		files[base+"a-appstream.xml.gz"] = gz(appstreamXML)
	}
	addRepo("@team/notes", true)
	addRepo("solo/stale", true)
	addRepo("solo/noappstream", false)
	builds := map[string]string{
		"@team/notes": fmt.Sprintf(`{"items": [{"state": "succeeded", "ended_on": %d, "submitter": "a"},
			{"state": "succeeded", "ended_on": %d, "submitter": "b"}]}`, now.AddDate(0, -1, 0).Unix(), now.AddDate(0, -2, 0).Unix()),
		"solo/stale": fmt.Sprintf(`{"items": [{"state": "failed", "ended_on": %d, "submitter": "solo"}]}`,
			now.AddDate(-3, 0, 0).Unix()),
	}

	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api_3/project/list":
			if r.URL.Query().Get("offset") != "0" {
				w.Write([]byte(`{"items": []}`))
				return
			}
			repo := func(full string) string {
				return fmt.Sprintf(`{"fedora-45-x86_64": "%s/results/%s/fedora-45-x86_64/", "fedora-45-i386": "x"}`, srv.URL, full)
			}
			fmt.Fprintf(w, `{"items": [
				{"full_name": "@team/notes", "ownername": "@team", "name": "notes", "appstream": true,
				 "description": "Builds of the Notes app for Fedora", "homepage": "https://notes.example.org",
				 "chroot_repos": %s},
				{"full_name": "solo/stale", "ownername": "solo", "name": "stale", "appstream": true, "chroot_repos": %s},
				{"full_name": "solo/noappstream", "ownername": "solo", "name": "noappstream", "appstream": true,
				 "chroot_repos": %s},
				{"full_name": "solo/unlisted", "ownername": "solo", "name": "unlisted", "appstream": true,
				 "unlisted_on_hp": true, "chroot_repos": %s},
				{"full_name": "solo/elsewhere", "ownername": "solo", "name": "elsewhere", "appstream": true,
				 "chroot_repos": {"fedora-45-x86_64": "https://evil.example/repo/"}},
				{"full_name": "solo/old", "ownername": "solo", "name": "old", "appstream": true,
				 "chroot_repos": {"fedora-40-x86_64": "x"}}
			]}`, repo("@team/notes"), repo("solo/stale"), repo("solo/noappstream"), repo("solo/unlisted"))
		case "/api_3/build/list":
			full := r.URL.Query().Get("ownername") + "/" + r.URL.Query().Get("projectname")
			if r.URL.Query().Get("order_type") != "DESC" {
				t.Errorf("builds not requested newest first")
			}
			w.Write([]byte(builds[full]))
		default:
			b, ok := files[r.URL.Path]
			if !ok {
				http.NotFound(w, r)
				return
			}
			w.Write(b)
		}
	}))
	defer srv.Close()

	f := &Fetcher{Client: srv.Client(), CacheDir: t.TempDir(), Arch: "x86_64", Releases: []int{45},
		APIBase: srv.URL + "/api_3", DownloadBase: srv.URL + "/results/", Workers: 2, Now: func() time.Time { return now }}
	ls, err := f.Fetch(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	// stale scores too low, noappstream has no AppStream data, unlisted and old are filtered out, and
	// elsewhere points outside COPR's download area.
	if len(ls) != 1 {
		b, _ := json.MarshalIndent(ls, "", " ")
		t.Fatalf("listings:\n%s", b)
	}
	l := ls[0]
	if l.Ref != "fedora-45/@team/notes/notes" || l.Package != "notes" || l.Project != "@team/notes" ||
		l.Distro != "fedora-45" || l.Version != "1.1-1.fc45" || l.Source != catalog.SourceCOPR ||
		l.Homepage != "https://notes.example.org" {
		t.Errorf("listing = %+v", l)
	}
	if l.Trust == nil || l.Trust.Score < 80 {
		t.Errorf("trust = %+v", l.Trust)
	}
}

func TestNewestReleases(t *testing.T) {
	var ps []project
	for i := 0; i < 60; i++ {
		ps = append(ps, project{ChrootRepos: map[string]string{"fedora-43-x86_64": "", "fedora-44-x86_64": "",
			"fedora-45-x86_64": "", "fedora-rawhide-x86_64": "", "epel-9-x86_64": ""}})
	}
	ps = append(ps, project{ChrootRepos: map[string]string{"fedora-46-x86_64": ""}}) // too few to count
	if got := newestReleases(ps, "x86_64"); fmt.Sprint(got) != "[44 45]" {
		t.Errorf("releases = %v", got)
	}
	if !strings.HasPrefix(chroot(45, "x86_64"), "fedora-45") {
		t.Error("chroot")
	}
}
