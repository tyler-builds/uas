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

// readmeServer serves a README through a fake REST API and answers HEAD requests for the pictures in it.
func readmeServer(t *testing.T, readme string) *httptest.Server {
	var srv *httptest.Server
	srv = httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/readme"):
			json.NewEncoder(w).Encode(map[string]string{"encoding": "base64",
				"content":      base64.StdEncoding.EncodeToString([]byte(readme)),
				"download_url": srv.URL + "/raw/README.md"})
		case r.URL.Path == "/raw/docs/main.png" || r.URL.Path == "/raw/docs/screenshot-dark.png":
			w.Header().Set("Content-Type", "image/png")
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
`
	srv := readmeServer(t, readme)
	f := &Fetcher{Client: srv.Client(), Token: "t", RESTEndpoint: srv.URL}
	shots := f.readmeScreenshots(context.Background(), "o/r")
	var urls []string
	for _, s := range shots {
		urls = append(urls, strings.TrimPrefix(s.URL, srv.URL))
	}
	// The one named like a screenshot comes first; the badge, logo, fake picture and missing file are left out.
	want := []string{"/raw/docs/screenshot-dark.png", "/raw/docs/main.png"}
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
