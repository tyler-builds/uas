package github

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"image"
	"image/png"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/tyler-builds/uas/internal/catalog"
)

func TestResolveImage(t *testing.T) {
	base := "https://raw.githubusercontent.com/o/r/main/README.md"
	for _, c := range []struct{ src, want string }{
		{"docs/shot.png", "https://raw.githubusercontent.com/o/r/main/docs/shot.png"},
		{"./img/a.JPG", "https://raw.githubusercontent.com/o/r/main/img/a.JPG"},
		{"https://github.com/o/r/blob/main/docs/x.png", "https://raw.githubusercontent.com/o/r/main/docs/x.png"},
		{"https://github.com/user-attachments/assets/0f1e", "https://github.com/user-attachments/assets/0f1e"},
		{"http://example.org/a.png", ""},   // not https
		{"https://example.org/page", ""},   // not a picture
		{"data:image/png;base64,AAAA", ""}, // inline
		{"https://example.org/a.mp4", ""},  // a video
	} {
		if got := resolveImage(c.src, base); got != c.want {
			t.Errorf("resolveImage(%q) = %q, want %q", c.src, got, c.want)
		}
	}
}

// pngOf is a blank PNG picture of the given size.
func pngOf(w, h int) []byte {
	var b bytes.Buffer
	png.Encode(&b, image.NewGray(image.Rect(0, 0, w, h)))
	return b.Bytes()
}

// readmeServer serves a README and a repo's file list through a fake REST API, and the pictures in pics
// (by path) with their files.
func readmeServer(t *testing.T, readme string, files []string, pics map[string][]byte) *httptest.Server {
	var srv *httptest.Server
	srv = httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/readme") && readme != "":
			json.NewEncoder(w).Encode(map[string]string{"encoding": "base64",
				"content":      base64.StdEncoding.EncodeToString([]byte(readme)),
				"download_url": srv.URL + "/raw/README.md"})
		case strings.HasSuffix(r.URL.Path, "/git/trees/HEAD"):
			var tree []map[string]string
			for _, p := range files {
				tree = append(tree, map[string]string{"path": p, "type": "blob"})
			}
			json.NewEncoder(w).Encode(map[string]any{"tree": tree})
		case pics[r.URL.Path] != nil:
			w.Header().Set("Content-Type", "image/png")
			http.ServeContent(w, r, "", time.Time{}, bytes.NewReader(pics[r.URL.Path]))
		case strings.HasSuffix(r.URL.Path, ".gif"):
			w.Header().Set("Content-Type", "text/html") // a page pretending to be a picture
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestReadmeScreenshots(t *testing.T) {
	readme := `# Notes
[![build](https://img.shields.io/badge/build-passing-green)](x)
<img src="assets/logo.png" width="64">
![Main window](docs/main.png)
![](docs/fake.gif)
<img alt="A screenshot" src="docs/screenshot-dark.png"/>
![missing](docs/missing.png)
![Toolbar](docs/toolbar.png)
![Social card](docs/card-screen.png)
`
	srv := readmeServer(t, readme, nil, map[string][]byte{"/raw/docs/main.png": pngOf(1280, 800),
		"/raw/docs/screenshot-dark.png": pngOf(1280, 800), "/raw/docs/toolbar.png": pngOf(1280, 40),
		"/raw/docs/card-screen.png": pngOf(1280, 640)})
	f := &Fetcher{Client: srv.Client(), Token: "t", RESTEndpoint: srv.URL}
	shots := f.readmeScreenshots(context.Background(), "o/r")
	var urls []string
	for _, s := range shots {
		urls = append(urls, strings.TrimPrefix(s.URL, srv.URL))
	}
	// The one named like a screenshot comes first. The badge, logo, fake picture, missing file and the strip
	// too thin to be a screenshot and the social card are left out.
	want := []string{"/raw/docs/screenshot-dark.png", "/raw/docs/main.png"}
	if strings.Join(urls, " ") != strings.Join(want, " ") {
		t.Errorf("screenshots = %q, want %q", urls, want)
	}
}

func TestRepoScreenshots(t *testing.T) {
	files := []string{
		"README.md",
		"docs/demo.gif",                        // a demo, after the screenshots
		"screenshots/main window.png",          // shallow and named like one: first
		"screenshots/settings.png",             // too small
		"assets/screenshots/editor.webp",       // not served: left out
		".github/screenshots/dark.png",         // a hidden folder is fine
		"node_modules/x/screenshot.png",        // someone else's
		"src/tests/__screenshots__/button.png", // a test's
		"res/icons/screenshot.png",             // an icon
		"a/b/c/d/e/screenshot.png",             // too deep
		"docs/screenshot-guide.md",             // not a picture
		"web/public/screens/dashboard.jpg",     // a "screens" folder, after the "screenshot" ones
		"docs/screenshot@2x.png",               // a copy at double size
		"assets/preview.png",                   // usually a social card
	}
	pics := map[string][]byte{
		"/raw/o/r/HEAD/screenshots/main window.png":      pngOf(1600, 1000),
		"/raw/o/r/HEAD/screenshots/settings.png":         pngOf(300, 200),
		"/raw/o/r/HEAD/.github/screenshots/dark.png":     pngOf(1600, 1000),
		"/raw/o/r/HEAD/web/public/screens/dashboard.jpg": pngOf(1600, 1000),
		"/raw/o/r/HEAD/docs/demo.gif":                    pngOf(1600, 1000),
		"/raw/o/r/HEAD/node_modules/x/screenshot.png":    pngOf(1600, 1000),
		"/raw/o/r/HEAD/a/b/c/d/e/screenshot.png":         pngOf(1600, 1000),
		"/raw/o/r/HEAD/assets/preview.png":               pngOf(1600, 1000),
	}
	srv := readmeServer(t, "", files, pics)
	f := &Fetcher{Client: srv.Client(), Token: "t", RESTEndpoint: srv.URL, RawEndpoint: srv.URL + "/raw"}
	var urls []string
	for _, s := range f.repoScreenshots(context.Background(), "o/r") {
		urls = append(urls, strings.TrimPrefix(s.URL, srv.URL+"/raw/o/r/HEAD/"))
	}
	want := []string{"screenshots/main%20window.png", ".github/screenshots/dark.png",
		"web/public/screens/dashboard.jpg", "docs/demo.gif"}
	if strings.Join(urls, " ") != strings.Join(want, " ") {
		t.Errorf("screenshots = %q, want %q", urls, want)
	}
}

func TestSaveIconScalesDown(t *testing.T) {
	var buf bytes.Buffer
	png.Encode(&buf, image.NewNRGBA(image.Rect(0, 0, 512, 400)))
	f := &Fetcher{MediaDir: t.TempDir()}
	name, err := f.saveIcon(buf.Bytes(), ".png")
	if err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(filepath.Join(IconDir(f.MediaDir), name))
	if err != nil {
		t.Fatal(err)
	}
	cfg, _ := png.DecodeConfig(bytes.NewReader(b))
	if cfg.Width != 256 || cfg.Height != 200 {
		t.Errorf("icon is %dx%d, want 256x200", cfg.Width, cfg.Height)
	}
	if _, err := f.saveIcon([]byte("GIF89a"), ""); err == nil {
		t.Error("expected an error for an unknown format")
	}
}

func TestFirstSentence(t *testing.T) {
	if got := firstSentence("T3 Code is a desktop app. Work with agents."); got != "T3 Code is a desktop app." {
		t.Errorf("firstSentence = %q", got)
	}
}

// TestEnrichAppImage reads the name, categories and icon out of an AppImage served over HTTP, then reuses
// what it found.
func TestEnrichAppImage(t *testing.T) {
	mksquashfs, err := exec.LookPath("mksquashfs")
	if err != nil {
		t.Skip("mksquashfs not installed")
	}
	dir := t.TempDir()
	root := filepath.Join(dir, "root")
	os.MkdirAll(root, 0o755)
	os.WriteFile(filepath.Join(root, "t3code.desktop"), []byte("[Desktop Entry]\nName=T3 Code\n"+
		"Comment=A desktop app for coding agents. It does more.\nIcon=t3code\nCategories=Development;\n"), 0o644)
	var icon bytes.Buffer
	png.Encode(&icon, image.NewNRGBA(image.Rect(0, 0, 64, 64)))
	os.WriteFile(filepath.Join(root, "t3code.png"), icon.Bytes(), 0o644)
	img := filepath.Join(dir, "img.sqfs")
	if out, err := exec.Command(mksquashfs, root, img, "-quiet", "-noappend", "-comp", "gzip").CombinedOutput(); err != nil {
		t.Fatalf("mksquashfs: %v\n%s", err, out)
	}
	sq, _ := os.ReadFile(img)
	elf := make([]byte, 64) // an ELF64 header whose (empty) section header table ends at byte 64
	copy(elf, "\x7fELF")
	elf[4], elf[5] = 2, 1
	binary.LittleEndian.PutUint64(elf[0x28:], 64)
	binary.LittleEndian.PutUint16(elf[0x3a:], 64)
	appImage := append(elf, sq...)

	reads := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/T3.AppImage" {
			reads++
			http.ServeContent(w, r, "T3.AppImage", time.Time{}, bytes.NewReader(appImage))
			return
		}
		http.NotFound(w, r) // no README
	}))
	defer srv.Close()

	f := &Fetcher{Client: srv.Client(), Token: "t", RESTEndpoint: srv.URL, MediaDir: t.TempDir(),
		IconBase: "https://catalog.example/"}
	listing := func() []catalog.Listing {
		return []catalog.Listing{{Ref: "pingdotgg/t3code", Name: "t3code", Assets: []catalog.Asset{{
			Kind: catalog.AssetAppImage, URL: srv.URL + "/T3.AppImage", Size: int64(len(appImage))}}}}
	}
	ls := listing()
	f.enrich(context.Background(), ls)
	l := ls[0]
	if l.Name != "T3 Code" || l.Summary != "A desktop app for coding agents." ||
		strings.Join(l.Categories, ",") != "Development" {
		t.Errorf("listing = %+v", l)
	}
	if !strings.HasPrefix(l.IconURL, "https://catalog.example/icons/") || !strings.HasSuffix(l.IconURL, ".png") {
		t.Errorf("icon URL = %q", l.IconURL)
	}
	if _, err := os.Stat(filepath.Join(IconDir(f.MediaDir), strings.TrimPrefix(l.IconURL, "https://catalog.example/icons/"))); err != nil {
		t.Errorf("icon not saved: %v", err)
	}

	before := reads
	again := listing()
	f.enrich(context.Background(), again)
	if reads != before || again[0].IconURL != l.IconURL || again[0].Name != "T3 Code" {
		t.Errorf("second run read the AppImage again (%d reads) or differs: %+v", reads-before, again[0])
	}
}
