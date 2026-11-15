package nerdfonts

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"

	"github.com/tyler-builds/uas/internal/catalog"
)

func server(t *testing.T, withInfo bool) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var file string
		switch r.URL.Path {
		case "/repos/ryanoasis/nerd-fonts/releases/latest":
			file = "testdata/release.json"
		case "/ryanoasis/nerd-fonts/v3.5.1/bin/scripts/lib/fonts.json":
			if !withInfo {
				http.NotFound(w, r)
				return
			}
			file = "testdata/fonts.json"
		default:
			http.NotFound(w, r)
			return
		}
		b, _ := os.ReadFile(file)
		w.Write(b)
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestFetch(t *testing.T) {
	srv := server(t, true)
	ls, err := (&Fetcher{Client: srv.Client(), API: srv.URL, Raw: srv.URL}).Fetch(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	// The three .tar.xz files; the zips are left out.
	if len(ls) != 3 || ls[0].Ref != "0xProto" || ls[1].Ref != "JetBrainsMono" || ls[2].Ref != "SourceCodePro" {
		t.Fatalf("listings = %+v", ls)
	}
	sauce := ls[2]
	a := sauce.Assets[0]
	if sauce.Name != "SauceCodePro Nerd Font" || sauce.Kind != catalog.KindFont || sauce.Version != "3.5.1" ||
		sauce.Installs == 0 || sauce.License != "OFL-1.1" || sauce.Categories[0] != "Monospace" ||
		a.Kind != catalog.AssetFontArchive || len(a.SHA256) != 64 || a.Name != "SourceCodePro.tar.xz" ||
		a.URL != "https://github.com/ryanoasis/nerd-fonts/releases/download/v3.5.1/SourceCodePro.tar.xz" {
		t.Errorf("SauceCodePro = %+v", sauce)
	}
	if ls[0].Summary != "A programming font focused on source code legibility" {
		t.Errorf("summary = %q", ls[0].Summary)
	}
	// Without fonts.json the listings are plainer, but there.
	srv = server(t, false)
	ls, err = (&Fetcher{Client: srv.Client(), API: srv.URL, Raw: srv.URL}).Fetch(context.Background())
	if err != nil || len(ls) != 3 || ls[2].Name != "SourceCodePro Nerd Font" {
		t.Errorf("without fonts.json: %v, %v", ls, err)
	}
}
