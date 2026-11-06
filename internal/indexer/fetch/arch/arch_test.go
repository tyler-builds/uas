package arch

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
)

func gzipped(b []byte) []byte {
	var buf bytes.Buffer
	w := gzip.NewWriter(&buf)
	w.Write(b)
	w.Close()
	return buf.Bytes()
}

func db(descs map[string]string) []byte {
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	for dir, desc := range descs {
		tw.WriteHeader(&tar.Header{Name: dir + "/desc", Mode: 0o644, Size: int64(len(desc)), Typeflag: tar.TypeReg})
		tw.Write([]byte(desc))
	}
	tw.Close()
	return gzipped(buf.Bytes())
}

const xml = `<?xml version="1.0"?>
<components version="1.0" origin="archlinux-arch-extra">
  <component type="desktop-application">
    <id>org.gnome.gitg</id>
    <pkgname>gitg</pkgname>
    <name>gitg</name>
    <summary>Graphical user interface for git</summary>
    <icon type="cached" width="128" height="128">gitg_org.gnome.gitg.jxl</icon>
  </component>
  <component type="addon"><id>x</id><pkgname>gitg</pkgname><name>x</name></component>
</components>`

func TestFetch(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/json":
			w.Write([]byte(`{"pkgname": "archlinux-appstream-data", "pkgver": "20261017"}`))
		case "/extra/os/x86_64/extra.db":
			w.Write(db(map[string]string{"gitg-44-2": "%NAME%\ngitg\n\n%VERSION%\n44-2\n\n%BUILDDATE%\n1700000000\n"}))
		case "/sources/20261017/extra/Components-x86_64.xml.gz":
			w.Write(gzipped([]byte(xml)))
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	f := &Fetcher{Client: srv.Client(), CacheDir: t.TempDir(), Arch: "x86_64", PackageJSON: srv.URL + "/json",
		Sources: srv.URL + "/sources", Mirror: srv.URL}
	ls, err := f.Fetch(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(ls) != 1 {
		t.Fatalf("got %+v", ls)
	}
	l := ls[0]
	if l.Ref != "arch/gitg" || l.Package != "gitg" || l.Version != "44-2" || l.Distro != "arch" ||
		l.Released.Year() != 2023 || l.IconURL != "" {
		t.Errorf("listing = %+v", l)
	}
	// Other architectures have no Arch listings.
	if ls, err := (&Fetcher{Arch: "aarch64"}).Fetch(context.Background()); err != nil || len(ls) != 0 {
		t.Errorf("aarch64: %v %v", ls, err)
	}
}

// TestLive reads the real Arch data (about 15 MB). Run it with UAS_LIVE=1.
func TestLive(t *testing.T) {
	if os.Getenv("UAS_LIVE") == "" {
		t.Skip("set UAS_LIVE=1")
	}
	ls, err := (&Fetcher{Client: http.DefaultClient, CacheDir: t.TempDir(), Arch: "x86_64"}).Fetch(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("%d listings; e.g. %s %s %s", len(ls), ls[0].Ref, ls[0].Name, ls[0].Version)
}
