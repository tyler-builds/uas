package googlefonts

import (
	"context"
	"crypto/sha1"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"

	"github.com/tyler-builds/uas/internal/catalog"
)

const commit = "cc39c4c2c304bc368c9b7c311ea2bcc6013299fe"

type gfServer struct {
	*httptest.Server
	mu        sync.Mutex
	fontGets  int
	metaFails bool
}

func blobSHA(b []byte) string {
	h := sha1.New()
	fmt.Fprintf(h, "blob %d\x00", len(b))
	h.Write(b)
	return hex.EncodeToString(h.Sum(nil))
}

func newServer(t *testing.T) *gfServer {
	t.Helper()
	read := func(p string) []byte {
		b, err := os.ReadFile(p)
		if err != nil {
			t.Fatal(err)
		}
		return b
	}
	inter := read("../../../gui/fonts/Inter-Regular.ttf")
	files := map[string][]byte{
		"ofl/roboto/METADATA.pb":                  read("testdata/roboto.METADATA.pb"),
		"ofl/roboto/Roboto[wdth,wght].ttf":        inter,
		"ofl/roboto/Roboto-Italic[wdth,wght].ttf": inter,
		"ofl/roboto/OFL.txt":                      []byte("licence"),
		"ofl/lato/METADATA.pb":                    read("testdata/lato.METADATA.pb"),
		"ofl/lato/DESCRIPTION.en_us.html":         read("testdata/lato.DESCRIPTION.en_us.html"),
		"apache/broken/METADATA.pb":               []byte("name: \"Broken\"\nfonts {\n  style: \"normal\"\n  weight: 400\n  filename: \"Broken-Regular.ttf\"\n}\n"),
		"apache/broken/Broken-Regular.ttf":        []byte("this is not a font"),
		"README.md":                               []byte("Google Fonts"),
	}
	// Lato's eighteen files.
	for _, w := range []string{"Thin", "ExtraLight", "Light", "Regular", "Medium", "SemiBold", "Bold", "ExtraBold", "Black"} {
		files["ofl/lato/Lato-"+w+".ttf"] = inter
		it := w + "Italic"
		if w == "Regular" {
			it = "Italic"
		}
		files["ofl/lato/Lato-"+it+".ttf"] = inter
	}
	// Each font file different, as real ones are: fonts are pinned by their blob hash.
	for p, b := range files {
		if strings.HasSuffix(p, ".ttf") && !strings.Contains(p, "broken") {
			files[p] = append(append([]byte{}, b...), []byte(p)...)
		}
	}
	var tree []treeEntry
	for p, b := range files {
		tree = append(tree, treeEntry{Path: p, Type: "blob", SHA: blobSHA(b), Size: int64(len(b))})
	}
	tree = append(tree, treeEntry{Path: "ofl", Type: "tree", SHA: strings.Repeat("1", 40)})
	s := &gfServer{}
	s.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/repos/google/fonts/commits/main":
			fmt.Fprintf(w, `{"sha":%q}`, commit)
		case r.URL.Path == "/repos/google/fonts/git/trees/"+commit && r.URL.Query().Get("recursive") == "1":
			json.NewEncoder(w).Encode(map[string]any{"sha": commit, "tree": tree, "truncated": false})
		case r.URL.Path == "/metadata":
			if s.metaFails {
				http.Error(w, "nope", http.StatusInternalServerError)
				return
			}
			w.Write(append([]byte(")]}'"), read("testdata/metadata.json")...))
		case strings.HasPrefix(r.URL.Path, "/google/fonts/"+commit+"/"):
			p := strings.TrimPrefix(r.URL.Path, "/google/fonts/"+commit+"/")
			b, ok := files[p]
			if !ok {
				http.NotFound(w, r)
				return
			}
			if strings.HasSuffix(p, ".ttf") {
				s.mu.Lock()
				s.fontGets++
				s.mu.Unlock()
			}
			w.Write(b)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(s.Close)
	return s
}

func (s *gfServer) fetcher(cache string) *Fetcher {
	return &Fetcher{Client: s.Client(), API: s.URL, Raw: s.URL, Metadata: s.URL + "/metadata", CacheDir: cache,
		Every: 1}
}

func TestFetch(t *testing.T) {
	srv := newServer(t)
	cache := t.TempDir()
	ls, err := srv.fetcher(cache).Fetch(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(ls) != 2 || ls[0].Ref != "lato" || ls[1].Ref != "roboto" {
		t.Fatalf("listings = %+v", ls)
	}
	lato, roboto := ls[0], ls[1]
	if roboto.Kind != catalog.KindFont || roboto.Source != catalog.SourceGoogleFonts || roboto.Name != "Roboto" ||
		roboto.License != "OFL-1.1" || roboto.Rank != 2 || roboto.Version != "2026-02-19" ||
		roboto.StoreURL != "https://fonts.google.com/specimen/Roboto" ||
		roboto.Homepage != "https://github.com/googlefonts/roboto-classic" ||
		roboto.Categories[0] != "Sans serif" || !strings.HasPrefix(roboto.Summary, "Sans serif · variable") {
		t.Errorf("Roboto = %+v", roboto)
	}
	if len(roboto.Assets) != 2 || roboto.Assets[0].Name != "Roboto[wdth,wght].ttf" || roboto.Assets[0].Style != "Variable" ||
		roboto.Assets[1].Style != "Variable Italic" || len(roboto.Assets[0].SHA256) != 64 ||
		roboto.Assets[0].URL != srv.URL+"/google/fonts/"+commit+"/ofl/roboto/Roboto%5Bwdth%2Cwght%5D.ttf" ||
		roboto.Assets[0].Kind != catalog.AssetFont {
		t.Errorf("Roboto assets = %+v", roboto.Assets)
	}
	if len(lato.Assets) != 18 || lato.Assets[0].Name != "Lato-Regular.ttf" || lato.Assets[0].Style != "Regular" ||
		lato.Assets[1].Style != "Thin" || lato.Assets[17].Style != "Black Italic" || lato.Developer != "Łukasz Dziedzic" {
		t.Errorf("Lato = %+v", lato.Assets)
	}
	if !strings.Contains(lato.Description, "Summer") || strings.Contains(lato.Description, "<p>") ||
		!strings.Contains(lato.Summary, "18 styles") {
		t.Errorf("Lato = %q / %q", lato.Summary, lato.Description)
	}
	first := srv.fontGets
	if first != 21 { // every file once, the broken one included
		t.Errorf("font downloads = %d", first)
	}
	// Again: nothing is downloaded again, and the site list failing costs only the ranks.
	srv.metaFails = true
	ls, err = srv.fetcher(cache).Fetch(context.Background())
	if err != nil || len(ls) != 2 || srv.fontGets != first || ls[1].Rank != 0 {
		t.Errorf("second run: %v, %v, %d downloads", ls, err, srv.fontGets)
	}
}

func TestFetchBudget(t *testing.T) {
	srv := newServer(t)
	f := srv.fetcher(t.TempDir())
	// Roboto (rank 2) comes first and fits; Lato's eighteen files don't.
	f.PinBytes = 3 * 412_000
	ls, err := f.Fetch(context.Background())
	if err != nil || len(ls) != 1 || ls[0].Ref != "roboto" {
		t.Errorf("Fetch = %v, %v", ls, err)
	}
}

func TestParseTextProto(t *testing.T) {
	fs, err := parseTextProto("# comment\nname: \"A\\303\\251 \\\"x\\\"\"\nweight: 400\nsource {\n  repository_url: 'https://x'\n  files { a: 1 }\n}\nsubsets: \"latin\" subsets: \"greek\"\n")
	if err != nil {
		t.Fatal(err)
	}
	if get(fs, "name") != "Aé \"x\"" || get(fs, "weight") != "400" || len(all(fs, "subsets")) != 2 ||
		get(all(fs, "source")[0].Msg, "repository_url") != "https://x" {
		t.Errorf("fields = %+v", fs)
	}
	for _, bad := range []string{"name: \"open", "a {", "}", "a: "} {
		if _, err := parseTextProto(bad); err == nil {
			t.Errorf("parsed %q", bad)
		}
	}
}
