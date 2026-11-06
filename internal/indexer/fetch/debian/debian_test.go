package debian

import (
	"compress/gzip"
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/tyler-builds/uas/internal/catalog"
	"github.com/tyler-builds/uas/internal/indexer/fetch/distro"
)

func TestParsePackages(t *testing.T) {
	pkgs := map[string]distro.Package{}
	in := "Package: gitg\nVersion: 44-1\nDescription: x\n continued\n\nPackage: htop\nArchitecture: amd64\nVersion: 3.4.1-5\n"
	if err := parsePackages(strings.NewReader(in), pkgs); err != nil {
		t.Fatal(err)
	}
	if pkgs["gitg"].Version != "44-1" || pkgs["htop"].Version != "3.4.1-5" {
		t.Errorf("pkgs = %+v", pkgs)
	}
}

const meta = `Dist: jammy
Name: Jammy Jellyfish
Version: 22.04.5 LTS
Supported: 1

Dist: plucky
Version: 25.04
Supported: 0

Dist: noble
Version: 24.04.5 LTS
Supported: 1
`

const components = `---
File: DEP-11
MediaBaseUrl: https://appstream.ubuntu.com/media/noble
---
Type: desktop-application
ID: org.gnome.gitg
Package: gitg
Name:
  C: gitg
Summary:
  C: Graphical user interface for git
---
Type: desktop-application
ID: missing.App
Package: notinindex
Name:
  C: Missing
`

func gz(s string) []byte {
	var b strings.Builder
	w := gzip.NewWriter(&b)
	w.Write([]byte(s))
	w.Close()
	return []byte(b.String())
}

func TestFetchUbuntu(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/meta-release":
			w.Write([]byte(meta))
		case r.URL.Path == "/dists/noble/main/binary-amd64/Packages.xz",
			r.URL.Path == "/dists/jammy/main/binary-amd64/Packages.xz":
			w.Write([]byte("Package: gitg\nVersion: 44-1\n")) // plain text reads too
		case r.URL.Path == "/dists/noble-updates/main/binary-amd64/Packages.xz":
			w.Write([]byte("Package: gitg\nVersion: 44-1ubuntu0.1\n"))
		case strings.HasSuffix(r.URL.Path, "/main/dep11/Components-amd64.yml.gz"):
			w.Write(gz(components))
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	f := &Fetcher{Distribution: catalog.SourceUbuntu, Client: srv.Client(), CacheDir: t.TempDir(), Arch: "x86_64",
		Mirror: srv.URL, MetaRelease: srv.URL + "/meta-release"}
	ls, err := f.Fetch(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(ls) != 2 {
		t.Fatalf("got %d listings: %+v", len(ls), ls)
	}
	byDistro := map[string]catalog.Listing{}
	for _, l := range ls {
		byDistro[l.Distro] = l
	}
	n := byDistro["ubuntu-24.04"]
	if n.Source != catalog.SourceUbuntu || n.Ref != "ubuntu-24.04/gitg" || n.Package != "gitg" ||
		n.Version != "44-1ubuntu0.1" || n.AppStreamID != "org.gnome.gitg" {
		t.Errorf("noble listing = %+v", n)
	}
	if byDistro["ubuntu-22.04"].Version != "44-1" {
		t.Errorf("jammy listing = %+v", byDistro["ubuntu-22.04"])
	}
}

// TestDebianLive reads Debian stable's main component (about 20 MB from deb.debian.org). Run it with
// UAS_LIVE=1.
func TestDebianLive(t *testing.T) {
	if os.Getenv("UAS_LIVE") == "" {
		t.Skip("set UAS_LIVE=1 to run against deb.debian.org")
	}
	f := &Fetcher{Distribution: catalog.SourceDebian, Client: http.DefaultClient, CacheDir: t.TempDir(), Arch: "x86_64"}
	ls, err := f.Fetch(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("%d listings; first: %+v", len(ls), ls[0])
}
