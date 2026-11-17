package appearance

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/md5"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"

	looks "github.com/tyler-builds/uas/internal/appearance"
	"github.com/tyler-builds/uas/internal/catalog"
	"github.com/tyler-builds/uas/internal/client/state"
)

// routed answers every request with a handler, whatever its host, so the installer's checks of where it
// downloads from stay as they are.
type routed struct {
	h    http.Handler
	mu   sync.Mutex
	hits []string
}

func (r *routed) RoundTrip(req *http.Request) (*http.Response, error) {
	r.mu.Lock()
	r.hits = append(r.hits, req.URL.String())
	r.mu.Unlock()
	rec := httptest.NewRecorder()
	r.h.ServeHTTP(rec, req)
	return rec.Result(), nil
}

// fakeGSettings is gsettings for org.gnome.desktop.interface, in memory.
type fakeGSettings struct {
	mu     sync.Mutex
	values map[string]string
	calls  []string
	// keys are what list-keys answers; nil makes it fail, as on a system without the schema's keys listed.
	keys []string
	// fail makes a set of a key fail.
	fail map[string]bool
	// fonts is what fc-list answers; "" makes it fail.
	fonts string
	// shellSchema is where User Themes' schema is: "" the system's, "user" only in the extension's own folder
	// (gsettings sees it with --schemadir), "none" nowhere.
	shellSchema string
}

func (f *fakeGSettings) Run(_ context.Context, name string, args []string, _ func(string)) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, name+" "+strings.Join(args, " "))
	if name == "fc-cache" {
		return "", nil
	}
	if name == "fc-list" {
		if f.fonts == "" {
			return "", fmt.Errorf("no fontconfig")
		}
		return f.fonts, nil
	}
	if name == "gsettings" && len(args) == 2 && args[0] == "list-keys" && args[1] == gnomeSchema {
		if f.keys == nil {
			return "", fmt.Errorf("no keys")
		}
		return strings.Join(f.keys, "\n") + "\n", nil
	}
	if name == "gsettings" && len(args) == 2 && args[0] == "list-keys" && args[1] == gnomeBackground {
		return "picture-uri\npicture-uri-dark\npicture-options\nprimary-color\n", nil
	}
	if name == "gsettings" && len(args) == 2 && args[0] == "list-keys" && args[1] == userThemesSchema {
		if f.shellSchema != "" {
			return "", fmt.Errorf("No such schema “%s”", userThemesSchema)
		}
		return "name\n", nil
	}
	schemaDir := false
	if len(args) > 2 && args[0] == "--schemadir" {
		args, schemaDir = args[2:], true
	}
	if name == "gsettings" && len(args) >= 3 && args[1] == userThemesSchema &&
		(f.shellSchema == "none" || (f.shellSchema == "user" && !schemaDir)) {
		return "", fmt.Errorf("No such schema “%s”", userThemesSchema)
	}
	if name != "gsettings" || len(args) < 3 ||
		(args[1] != gnomeSchema && args[1] != gnomeBackground && args[1] != gnomeScreensaver && args[1] != userThemesSchema) {
		return "", fmt.Errorf("unexpected %s %v", name, args)
	}
	// The interface's keys by their name, the others' by schema and name (bg picture-uri, shell name).
	key := args[2]
	switch args[1] {
	case userThemesSchema:
		key = "shell " + key
	case gnomeBackground:
		key = "bg " + key
	case gnomeScreensaver:
		key = "lock " + key
	}
	if args[0] == "set" && f.fail[key] {
		return "", fmt.Errorf("gsettings: can't set %s", key)
	}
	switch args[0] {
	case "get":
		return quoteGVariant(f.values[key]) + "\n", nil
	case "set":
		f.values[key] = parseGVariantString(args[3])
	case "reset":
		f.values[key] = "default-" + args[2]
		if args[1] == userThemesSchema {
			f.values[key] = "" // the Shell's own look
		}
	}
	return "", nil
}

func tarGz(t *testing.T, files map[string]string, links map[string]string) []byte {
	t.Helper()
	var raw bytes.Buffer
	tw := tar.NewWriter(&raw)
	names := make([]string, 0, len(files))
	for n := range files {
		names = append(names, n)
	}
	slices.Sort(names)
	for _, n := range names {
		tw.WriteHeader(&tar.Header{Name: n, Mode: 0o644, Size: int64(len(files[n])), Typeflag: tar.TypeReg})
		tw.Write([]byte(files[n]))
	}
	for n, target := range links {
		tw.WriteHeader(&tar.Header{Name: n, Typeflag: tar.TypeSymlink, Linkname: target, Mode: 0o777})
	}
	tw.Close()
	var out bytes.Buffer
	zw := gzip.NewWriter(&out)
	zw.Write(raw.Bytes())
	zw.Close()
	return out.Bytes()
}

func sums(b []byte) (sha, md string) {
	s := sha256.Sum256(b)
	m := md5.Sum(b)
	return hex.EncodeToString(s[:]), hex.EncodeToString(m[:])
}

// world is a home folder, a fake Pling and GitHub, and a GNOME desktop.
type world struct {
	in       *Installer
	gs       *fakeGSettings
	transp   *routed
	files    map[string][]byte // download path -> body
	pling    map[string]any    // the item the API returns
	sysIcons string
}

func newWorld(t *testing.T) *world {
	t.Helper()
	root := t.TempDir()
	w := &world{files: map[string][]byte{}, pling: map[string]any{}}
	w.gs = &fakeGSettings{values: map[string]string{KeyIconTheme: "Adwaita", KeyCursorTheme: "Adwaita",
		KeyFont: "Adwaita Sans 11", KeyDocumentFont: "Adwaita Sans 12", KeyMonospaceFont: "Adwaita Mono 11"}}
	mux := http.NewServeMux()
	mux.HandleFunc("/ocs/v1/content/data/", func(rw http.ResponseWriter, r *http.Request) {
		json.NewEncoder(rw).Encode(map[string]any{"status": "ok", "data": []any{w.pling}})
	})
	mux.HandleFunc("/", func(rw http.ResponseWriter, r *http.Request) {
		b, ok := w.files[r.URL.Path]
		if !ok {
			http.NotFound(rw, r)
			return
		}
		rw.Write(b)
	})
	w.transp = &routed{h: mux}
	w.sysIcons = filepath.Join(root, "usr", "share")
	os.MkdirAll(filepath.Join(w.sysIcons, "icons", "Adwaita", "cursors"), 0o755)
	w.in = &Installer{Data: filepath.Join(root, "data"), Home: filepath.Join(root, "home"),
		SystemData: []string{w.sysIcons}, Cache: filepath.Join(root, "cache"), Config: filepath.Join(root, "config"),
		State:  state.Store{Path: filepath.Join(root, "data", "uas", "state.json")},
		Client: &http.Client{Transport: w.transp}, Runner: w.gs, Desktop: &GNOME{Runner: w.gs},
		PlingAPI: "https://api.opendesktop.org/ocs/v1", Limits: looks.DefaultLimits}
	return w
}

// offer puts a file on the fake Pling as the item's slot 1 and returns its catalog asset.
func (w *world) offer(name string, body []byte, themes ...string) catalog.Asset {
	sha, md := sums(body)
	w.files["/api/files/download/j/token/"+name] = body
	w.pling["downloadname1"] = name
	w.pling["downloadlink1"] = "https://files06.pling.com/api/files/download/j/token/" + name
	w.pling["downloadmd5sum1"] = md
	return catalog.Asset{Kind: catalog.AssetTheme, Name: name, Slot: 1, MD5: md, SHA256: sha,
		Size: int64(len(body)), Themes: themes}
}

func iconTheme(name string) map[string]string {
	return map[string]string{
		"pack/" + name + "/index.theme":              "[Icon Theme]\nName=" + name + "\nDirectories=48/apps\n",
		"pack/" + name + "/48/apps/firefox.svg":      "<svg/>",
		"pack/" + name + "-Dark/index.theme":         "[Icon Theme]\nName=" + name + " Dark\nInherits=" + name + "\nDirectories=48/apps\n",
		"pack/" + name + "-Dark/48/apps/firefox.svg": "<svg dark/>",
	}
}

func TestThemeInstallUseRevertRemove(t *testing.T) {
	w := newWorld(t)
	ctx := context.Background()
	body := tarGz(t, iconTheme("Papirus"), map[string]string{"pack/Papirus/48/apps/browser.svg": "firefox.svg"})
	a := w.offer("papirus.tar.gz", body, "Papirus", "Papirus-Dark")
	l := catalog.Listing{Source: catalog.SourcePling, Ref: "1166289", Kind: catalog.KindIconTheme, Name: "Papirus",
		Version: "20250501", Assets: []catalog.Asset{a}}

	rec, err := w.in.Install(ctx, l, "Papirus-Dark", nil)
	if err != nil {
		t.Fatal(err)
	}
	if rec.ID != "pling:1166289" || !slices.Equal(rec.Themes, []string{"Papirus", "Papirus-Dark"}) ||
		rec.SHA256 != a.SHA256 || len(rec.Paths) != 2 {
		t.Fatalf("record = %+v", rec)
	}
	if b, err := os.ReadFile(filepath.Join(w.in.IconsDir(), "Papirus", "48", "apps", "browser.svg")); err != nil ||
		string(b) != "<svg/>" {
		t.Errorf("linked icon = %q, %v", b, err)
	}
	// The download link was asked for at install time, then used.
	if len(w.transp.hits) != 2 || !strings.Contains(w.transp.hits[0], "/content/data/1166289") {
		t.Errorf("requests = %v", w.transp.hits)
	}
	// Recorded in the state file.
	recs, _ := w.in.Records()
	if len(recs) != 1 || recs[0].ID != rec.ID {
		t.Errorf("Records = %+v", recs)
	}
	themes, _ := w.in.Themes()
	var names []string
	for _, th := range themes {
		names = append(names, th.Name+"/"+th.By)
	}
	if !slices.Equal(names, []string{"Adwaita/", "Papirus/pling:1166289", "Papirus-Dark/pling:1166289"}) {
		t.Errorf("Themes = %v", names)
	}

	// Use, use another, then revert to what was there before uas.
	if err := w.in.Use(ctx, KeyIconTheme, "Papirus-Dark"); err != nil {
		t.Fatal(err)
	}
	if err := w.in.Use(ctx, KeyIconTheme, "Papirus"); err != nil {
		t.Fatal(err)
	}
	if got := w.in.Current(ctx)[KeyIconTheme]; got != "Papirus" {
		t.Errorf("icon theme = %q", got)
	}
	saved, _ := w.in.Saved()
	if len(saved) != 1 || saved[0].Previous != "Adwaita" || saved[0].Value != "Papirus" {
		t.Errorf("Saved = %+v", saved)
	}
	if err := w.in.Revert(ctx, KeyIconTheme); err != nil {
		t.Fatal(err)
	}
	if got := w.gs.values[KeyIconTheme]; got != "Adwaita" {
		t.Errorf("after Revert, icon theme = %q", got)
	}

	// Removing the theme in use switches back first.
	w.in.Use(ctx, KeyIconTheme, "Papirus-Dark")
	if err := w.in.Remove(ctx, rec.ID, nil); err != nil {
		t.Fatal(err)
	}
	if got := w.gs.values[KeyIconTheme]; got != "Adwaita" {
		t.Errorf("after Remove, icon theme = %q", got)
	}
	for _, p := range rec.Paths {
		if _, err := os.Stat(p); err == nil {
			t.Errorf("%s is still there", p)
		}
	}
	if recs, _ := w.in.Records(); len(recs) != 0 {
		t.Errorf("Records after Remove = %+v", recs)
	}
	if saved, _ := w.in.Saved(); len(saved) != 0 {
		t.Errorf("Saved after Remove = %+v", saved)
	}
}

func TestThemeInstallRefuses(t *testing.T) {
	ctx := context.Background()
	cursor := func(name string) map[string]string {
		return map[string]string{name + "/cursors/default": "Xcur", name + "/index.theme": "[Icon Theme]\nName=x\n"}
	}
	t.Run("checksum mismatch", func(t *testing.T) {
		w := newWorld(t)
		a := w.offer("c.tar.gz", tarGz(t, cursor("Oreo"), nil), "Oreo")
		a.SHA256 = strings.Repeat("0", 64)
		l := catalog.Listing{Source: catalog.SourcePling, Ref: "1", Kind: catalog.KindCursorTheme, Name: "Oreo",
			Assets: []catalog.Asset{a}}
		if _, err := w.in.Install(ctx, l, "", nil); err == nil || !strings.Contains(err.Error(), "SHA-256") {
			t.Errorf("Install = %v", err)
		}
		if _, err := os.Stat(filepath.Join(w.in.IconsDir(), "Oreo")); err == nil {
			t.Error("installed anyway")
		}
	})
	t.Run("file changed on Pling", func(t *testing.T) {
		w := newWorld(t)
		a := w.offer("c.tar.gz", tarGz(t, cursor("Oreo"), nil), "Oreo")
		w.pling["downloadmd5sum1"] = "ffff"
		l := catalog.Listing{Source: catalog.SourcePling, Ref: "1", Kind: catalog.KindCursorTheme, Name: "Oreo",
			Assets: []catalog.Asset{a}}
		if _, err := w.in.Install(ctx, l, "", nil); err == nil || !strings.Contains(err.Error(), "changed on Pling") {
			t.Errorf("Install = %v", err)
		}
	})
	t.Run("link off Pling", func(t *testing.T) {
		w := newWorld(t)
		a := w.offer("c.tar.gz", tarGz(t, cursor("Oreo"), nil), "Oreo")
		w.pling["downloadlink1"] = "https://evil.example/c.tar.gz"
		l := catalog.Listing{Source: catalog.SourcePling, Ref: "1", Kind: catalog.KindCursorTheme, Name: "Oreo",
			Assets: []catalog.Asset{a}}
		if _, err := w.in.Install(ctx, l, "", nil); err == nil || !strings.Contains(err.Error(), "isn't Pling's") {
			t.Errorf("Install = %v", err)
		}
	})
	t.Run("theme uas didn't install", func(t *testing.T) {
		w := newWorld(t)
		os.MkdirAll(filepath.Join(w.in.IconsDir(), "Oreo"), 0o755)
		a := w.offer("c.tar.gz", tarGz(t, cursor("Oreo"), nil), "Oreo")
		l := catalog.Listing{Source: catalog.SourcePling, Ref: "1", Kind: catalog.KindCursorTheme, Name: "Oreo",
			Assets: []catalog.Asset{a}}
		if _, err := w.in.Install(ctx, l, "", nil); err == nil || !strings.Contains(err.Error(), "didn't install") {
			t.Errorf("Install = %v", err)
		}
		if len(w.transp.hits) != 0 {
			t.Errorf("downloaded anyway: %v", w.transp.hits)
		}
	})
	t.Run("hides a system theme", func(t *testing.T) {
		w := newWorld(t)
		a := w.offer("c.tar.gz", tarGz(t, cursor("Adwaita"), nil), "Adwaita")
		l := catalog.Listing{Source: catalog.SourcePling, Ref: "1", Kind: catalog.KindCursorTheme, Name: "Adwaita",
			Assets: []catalog.Asset{a}}
		if _, err := w.in.Install(ctx, l, "", nil); err == nil || !strings.Contains(err.Error(), "would hide it") {
			t.Errorf("Install = %v", err)
		}
	})
	t.Run("archive with a program", func(t *testing.T) {
		w := newWorld(t)
		files := cursor("Oreo")
		files["Oreo/tool"] = "\x7fELF\x02\x01"
		a := w.offer("c.tar.gz", tarGz(t, files, nil))
		l := catalog.Listing{Source: catalog.SourcePling, Ref: "1", Kind: catalog.KindCursorTheme, Name: "Oreo",
			Assets: []catalog.Asset{a}}
		if _, err := w.in.Install(ctx, l, "", nil); err == nil || !looks.IsRefused(err) {
			t.Errorf("Install = %v", err)
		}
		if _, err := os.Stat(filepath.Join(w.in.IconsDir(), "Oreo")); err == nil {
			t.Error("installed anyway")
		}
	})
}

// TestThemeWithHelperScript installs an archive shaped like Kora's: an icon theme with a helper script at its
// top, which the indexer lists. What the indexer pins (the archive's SHA-256, the themes Inspect finds, the
// files it leaves out) is what the install finds and records, and the script is never written.
func TestThemeWithHelperScript(t *testing.T) {
	w := newWorld(t)
	files := map[string]string{
		"kora/index.theme":                       "[Icon Theme]\nName=Kora\nDirectories=48/apps\n",
		"kora/48/apps/firefox.svg":               "<svg/>",
		"kora/create-new-icon-theme.cache.sh":    "#!/bin/bash\ngtk-update-icon-cache -f .\n",
		"kora-light/index.theme":                 "[Icon Theme]\nName=Kora Light\nInherits=kora\nDirectories=48/apps\n",
		"kora-light/48/apps/firefox.svg":         "<svg light/>",
		"kora-light/create-new-icon-theme.cache": "#!/bin/sh\n",
	}
	body := tarGz(t, files, map[string]string{"kora/48/apps/run.svg": "../../create-new-icon-theme.cache.sh"})

	// What the indexer does with the downloaded file.
	p := filepath.Join(t.TempDir(), "kora-2-0-5.tar.gz")
	os.WriteFile(p, body, 0o644)
	c, err := looks.Inspect(p, looks.DefaultLimits)
	if err != nil {
		t.Fatalf("Inspect = %v", err)
	}
	var pinned []string
	for _, th := range c.Of(catalog.KindIconTheme) {
		pinned = append(pinned, th.Name)
	}
	a := w.offer("kora-2-0-5.tar.gz", body, pinned...)
	a.LeftOut = len(c.LeftOut)
	if !slices.Equal(pinned, []string{"kora", "kora-light"}) || a.LeftOut != 2 {
		t.Fatalf("pinned themes %v, left out %v", pinned, c.LeftOut)
	}

	l := catalog.Listing{Source: catalog.SourcePling, Ref: "1256209", Kind: catalog.KindIconTheme, Name: "Kora",
		Assets: []catalog.Asset{a}}
	rec, err := w.in.Install(context.Background(), l, "kora", nil)
	if err != nil {
		t.Fatal(err)
	}
	if rec.SHA256 != a.SHA256 || !sameSet(rec.Themes, pinned) || rec.LeftOut != a.LeftOut {
		t.Errorf("record = %+v, asset = %+v", rec, a)
	}
	if b, err := os.ReadFile(filepath.Join(w.in.IconsDir(), "kora-light", "48", "apps", "firefox.svg")); err != nil ||
		string(b) != "<svg light/>" {
		t.Errorf("kora-light icon = %q, %v", b, err)
	}
	filepath.WalkDir(w.in.IconsDir(), func(path string, d os.DirEntry, err error) error {
		if err == nil && !d.IsDir() {
			if b, _ := os.ReadFile(path); strings.HasPrefix(string(b), "#!") || strings.Contains(path, "cache.sh") ||
				strings.HasSuffix(path, "run.svg") {
				t.Errorf("%s was written", path)
			}
		}
		return nil
	})
}

func TestThemeUpdateReplaces(t *testing.T) {
	w := newWorld(t)
	ctx := context.Background()
	v1 := w.offer("t.tar.gz", tarGz(t, iconTheme("Tela"), nil), "Tela", "Tela-Dark")
	l := catalog.Listing{Source: catalog.SourcePling, Ref: "9", Kind: catalog.KindIconTheme, Name: "Tela",
		Version: "1", Assets: []catalog.Asset{v1}}
	if _, err := w.in.Install(ctx, l, "", nil); err != nil {
		t.Fatal(err)
	}
	files := iconTheme("Tela")
	files["pack/Tela/48/apps/new.svg"] = "<svg new/>"
	v2 := w.offer("t.tar.gz", tarGz(t, files, nil), "Tela", "Tela-Dark")
	l2 := l
	l2.Version, l2.Assets = "2", []catalog.Asset{v2}
	recs, _ := w.in.Records()
	ups := Updates(recs, func(id string) (catalog.Listing, bool) { return l2, id == "pling:9" })
	if len(ups) != 1 || ups[0].Version() != "2" || ups[0].Variant() != "Tela" {
		t.Fatalf("Updates = %+v", ups)
	}
	// None against the same listing.
	if got := Updates(recs, func(string) (catalog.Listing, bool) { return l, true }); len(got) != 0 {
		t.Errorf("Updates against the installed version = %+v", got)
	}
	if _, err := w.in.Install(ctx, ups[0].Listing, ups[0].Variant(), nil); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(w.in.IconsDir(), "Tela", "48", "apps", "new.svg")); err != nil {
		t.Error(err)
	}
	recs, _ = w.in.Records()
	if len(recs) != 1 || recs[0].SHA256 != v2.SHA256 || recs[0].Version != "2" {
		t.Errorf("Records = %+v", recs)
	}
}

func TestFontInstallUseRemove(t *testing.T) {
	w := newWorld(t)
	ctx := context.Background()
	inter, err := os.ReadFile("../../gui/fonts/Inter-Regular.ttf")
	if err != nil {
		t.Fatal(err)
	}
	bold, _ := os.ReadFile("../../gui/fonts/Inter-Bold.ttf")
	asset := func(name string, b []byte) catalog.Asset {
		sha, _ := sums(b)
		w.files["/google/fonts/abc/ofl/inter/"+name] = b
		return catalog.Asset{Kind: catalog.AssetFont, Name: name, SHA256: sha, Size: int64(len(b)),
			URL: "https://raw.githubusercontent.com/google/fonts/abc/ofl/inter/" + name}
	}
	l := catalog.Listing{Source: catalog.SourceGoogleFonts, Ref: "inter", Kind: catalog.KindFont, Name: "Inter",
		Assets: []catalog.Asset{asset("Inter-Regular.ttf", inter), asset("Inter-Bold.ttf", bold)}}

	// A preview downloads the first file into the cache; the install then reuses it.
	if _, err := w.in.FontFile(ctx, l.Assets[0]); err != nil {
		t.Fatal(err)
	}
	if err := w.in.EnsureFontsDir(); err != nil {
		t.Fatal(err)
	}
	rec, err := w.in.Install(ctx, l, "", nil)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(rec.Families, []string{"Inter"}) || rec.SHA256 != Digest(l.Assets) || len(rec.Paths) != 1 {
		t.Fatalf("record = %+v", rec)
	}
	dir := rec.Paths[0]
	if filepath.Dir(dir) != w.in.FontsDir() {
		t.Errorf("installed into %s", dir)
	}
	for _, f := range []string{"Inter-Regular.ttf", "Inter-Bold.ttf"} {
		if _, err := os.Stat(filepath.Join(dir, f)); err != nil {
			t.Error(err)
		}
	}
	downloads := 0
	for _, h := range w.transp.hits {
		if strings.Contains(h, "Inter-Regular") {
			downloads++
		}
	}
	if downloads != 1 {
		t.Errorf("Inter-Regular downloaded %d times", downloads)
	}
	if !slices.ContainsFunc(w.gs.calls, func(c string) bool { return strings.HasPrefix(c, "fc-cache") }) {
		t.Error("fc-cache wasn't run")
	}
	// Use as the monospace font: the size stays.
	if err := w.in.Use(ctx, KeyMonospaceFont, "Inter"); err != nil {
		t.Fatal(err)
	}
	if got := w.gs.values[KeyMonospaceFont]; got != "Inter 11" {
		t.Errorf("monospace font = %q", got)
	}
	if err := w.in.Remove(ctx, rec.ID, nil); err != nil {
		t.Fatal(err)
	}
	if got := w.gs.values[KeyMonospaceFont]; got != "Adwaita Mono 11" {
		t.Errorf("after Remove, monospace font = %q", got)
	}
	if _, err := os.Stat(dir); err == nil {
		t.Error("fonts still there")
	}

	// A file that doesn't match its checksum is refused, and nothing is installed.
	bad := l
	bad.Ref = "bad"
	bad.Assets = []catalog.Asset{l.Assets[1]}
	bad.Assets[0].SHA256 = strings.Repeat("1", 64)
	if _, err := w.in.Install(ctx, bad, "", nil); err == nil {
		t.Error("installed a font with the wrong checksum")
	}
	// Nor from anywhere but Google's repo.
	off := l
	off.Assets = []catalog.Asset{l.Assets[1]}
	off.Assets[0].URL = "https://example.com/Inter-Bold.ttf"
	if _, err := w.in.Install(ctx, off, "", nil); err == nil {
		t.Error("installed a font from elsewhere")
	}
}

func TestFontArchiveInstall(t *testing.T) {
	w := newWorld(t)
	inter, _ := os.ReadFile("../../gui/fonts/Inter-Regular.ttf")
	body := tarGz(t, map[string]string{"InterNerdFont-Regular.ttf": string(inter), "README.md": "hi"}, nil)
	sha, _ := sums(body)
	w.files["/ryanoasis/nerd-fonts/releases/download/v3.5.1/Inter.tar.xz"] = body
	l := catalog.Listing{Source: catalog.SourceNerdFonts, Ref: "Inter", Kind: catalog.KindFont, Name: "Inter Nerd Font",
		Assets: []catalog.Asset{{Kind: catalog.AssetFontArchive, Name: "Inter.tar.xz", SHA256: sha, Size: int64(len(body)),
			URL: "https://github.com/ryanoasis/nerd-fonts/releases/download/v3.5.1/Inter.tar.xz"}}}
	rec, err := w.in.Install(context.Background(), l, "", nil)
	if err != nil {
		t.Fatal(err)
	}
	entries, _ := os.ReadDir(rec.Paths[0])
	if len(entries) != 1 || entries[0].Name() != "InterNerdFont-Regular.ttf" || rec.SHA256 != sha {
		t.Errorf("installed %v, record %+v", entries, rec)
	}
}

func TestGVariantStrings(t *testing.T) {
	for _, s := range []string{"Adwaita", "it's", `back\slash`, "Papirus Dark"} {
		if got := parseGVariantString(quoteGVariant(s)); got != s {
			t.Errorf("round trip of %q = %q", s, got)
		}
	}
	if got := parseGVariantString(`"it's"`); got != "it's" {
		t.Errorf("double-quoted = %q", got)
	}
	if got := FontValue("Cantarell 11", "Inter"); got != "Inter 11" {
		t.Errorf("FontValue = %q", got)
	}
	if got := FontFamily("JetBrainsMono Nerd Font Mono 10.5"); got != "JetBrainsMono Nerd Font Mono" {
		t.Errorf("FontFamily = %q", got)
	}
}

func TestDetectDesktop(t *testing.T) {
	for in, want := range map[string]string{"GNOME": "gnome", "ubuntu:GNOME": "gnome", "KDE": "plasma", "XFCE": "",
		"": ""} {
		d := DetectDesktop(in, nil)
		got := ""
		if d != nil {
			got = d.Name()
		}
		if got != want {
			t.Errorf("DetectDesktop(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestFontNames(t *testing.T) {
	for value, want := range map[string][2]string{
		"Inter 11":                            {"Inter", "11"},
		"Fira Code weight=450 10":             {"Fira Code", "10"},
		"Cantarell Italic Light 15 @wght=200": {"Cantarell", "15"},
		"Source Code Pro Semi-Bold 10.5 #tnum=1 @wght=600,wdth=90": {"Source Code Pro", "10.5"},
		"JetBrainsMono Nerd Font Mono 10.5":                        {"JetBrainsMono Nerd Font Mono", "10.5"},
		"Noto Sans, Bold 12":                                       {"Noto Sans", "12"},
		"Inter":                                                    {"Inter", ""},
		"Bold":                                                     {"Bold", ""},
		"Monospace 14px":                                           {"Monospace", "14px"},
	} {
		if got := FontFamily(value); got != want[0] {
			t.Errorf("FontFamily(%q) = %q, want %q", value, got, want[0])
		}
		if got := FontSize(value); got != want[1] {
			t.Errorf("FontSize(%q) = %q, want %q", value, got, want[1])
		}
	}
	if got := FontValue("Fira Code weight=450 10", "Inter"); got != "Inter 10" {
		t.Errorf("FontValue = %q", got)
	}
	if got := FontValue("Inter", "Fira Code"); got != "Fira Code 11" {
		t.Errorf("FontValue without a size = %q", got)
	}
	// A font with variations is in use.
	rec := state.AppearanceInstall{Kind: string(catalog.KindFont), Families: []string{"Fira Code"}}
	if !inUse(rec, KeyMonospaceFont, "Fira Code weight=450 10") {
		t.Error("Fira Code weight=450 10 isn't seen as Fira Code")
	}
}

func TestNewSettings(t *testing.T) {
	ctx := context.Background()
	gs := &fakeGSettings{values: map[string]string{KeyColorScheme: SchemeDefault, KeyAccentColor: "blue"},
		keys: []string{"icon-theme", "color-scheme", "accent-color"}}
	g := &GNOME{Runner: gs}
	for _, key := range SettingKeys {
		if !g.Supports(key) {
			t.Errorf("GNOME doesn't support %s", key)
		}
	}
	if !g.Supports(KeyShellTheme) || g.Supports("nonsense") {
		t.Error("GNOME's Shell theme isn't supported, or a setting uas doesn't change is")
	}
	if err := g.Set(ctx, KeyColorScheme, SchemeDark); err != nil {
		t.Fatal(err)
	}
	if err := g.Set(ctx, KeyAccentColor, "it's"); err != nil {
		t.Fatal(err)
	}
	if v, _ := g.Get(ctx, KeyAccentColor); v != "it's" {
		t.Errorf("accent = %q", v)
	}
	if err := g.Reset(ctx, KeyColorScheme); err != nil {
		t.Fatal(err)
	}
	want := []string{
		"gsettings list-keys org.gnome.desktop.interface",
		"gsettings list-keys org.gnome.desktop.background",
		"gsettings set org.gnome.desktop.interface color-scheme 'prefer-dark'",
		`gsettings set org.gnome.desktop.interface accent-color 'it\'s'`,
		"gsettings get org.gnome.desktop.interface accent-color",
		"gsettings reset org.gnome.desktop.interface color-scheme",
	}
	if !slices.Equal(gs.calls, want) {
		t.Errorf("calls =\n%s\nwant\n%s", strings.Join(gs.calls, "\n"), strings.Join(want, "\n"))
	}

	// A GNOME from before accent colors (46) lists no accent-color, so uas doesn't offer it.
	old := &GNOME{Runner: &fakeGSettings{keys: []string{"icon-theme", "color-scheme"}}}
	if old.Supports(KeyAccentColor) || !old.Supports(KeyColorScheme) || !old.Supports(KeyIconTheme) {
		t.Error("GNOME 46 supports the wrong settings")
	}
	if err := old.Set(ctx, KeyAccentColor, "teal"); err != ErrUnsupported {
		t.Errorf("setting the accent on GNOME 46: %v", err)
	}
	// When gsettings can't list the keys, they are tried.
	if !(&GNOME{Runner: &fakeGSettings{}}).Supports(KeyAccentColor) {
		t.Error("an unlisted key isn't tried")
	}

	// Plasma changes neither, and no desktop at all changes nothing.
	p := &Plasma{ChangeIcons: "/bin/true"}
	if p.Supports(KeyColorScheme) || p.Supports(KeyAccentColor) || p.Supports(KeyFont) || !p.Supports(KeyIconTheme) {
		t.Error("Plasma supports the wrong settings")
	}
	in := &Installer{State: state.Store{Path: filepath.Join(t.TempDir(), "state.json")}}
	if err := in.Use(ctx, KeyColorScheme, SchemeDark); err != ErrUnsupported {
		t.Errorf("Use without a desktop: %v", err)
	}
	if err := in.RevertAll(ctx); err != ErrUnsupported {
		t.Errorf("RevertAll without a desktop: %v", err)
	}
	if len(in.Current(ctx)) != 0 {
		t.Error("Current without a desktop isn't empty")
	}
}

func TestValues(t *testing.T) {
	for in, want := range map[string]string{"dark": SchemeDark, "Light": SchemeLight, "default": SchemeDefault,
		"prefer-dark": "prefer-dark"} {
		if got := ParseValue(KeyColorScheme, in); got != want {
			t.Errorf("ParseValue(scheme, %q) = %q", in, got)
		}
	}
	if got := ParseValue(KeyAccentColor, "Teal"); got != "teal" {
		t.Errorf("ParseValue(accent, Teal) = %q", got)
	}
	if got := ParseValue(KeyIconTheme, "Papirus-Dark"); got != "Papirus-Dark" {
		t.Errorf("ParseValue(icons) = %q", got)
	}
	if err := CheckValue(KeyColorScheme, "dark"); err == nil || !strings.Contains(err.Error(), "default, light, dark") {
		t.Errorf("CheckValue(scheme, dark) = %v", err)
	}
	if CheckValue(KeyAccentColor, "mauve") == nil || CheckValue(KeyAccentColor, "slate") != nil ||
		CheckValue(KeyIconTheme, "") == nil || CheckValue(KeyIconTheme, "Anything") != nil {
		t.Error("CheckValue is wrong")
	}
	if ValueLabel(KeyColorScheme, SchemeDark) != "Dark" || ValueLabel(KeyAccentColor, "teal") != "Teal" ||
		ValueLabel(KeyIconTheme, "kora") != "kora" {
		t.Error("ValueLabel is wrong")
	}
}

func TestUseRevertAll(t *testing.T) {
	w := newWorld(t)
	ctx := context.Background()
	w.gs.values[KeyColorScheme] = SchemeDefault
	w.gs.values[KeyAccentColor] = "blue"
	if err := w.in.Use(ctx, KeyColorScheme, SchemeDark); err != nil {
		t.Fatal(err)
	}
	if err := w.in.Use(ctx, KeyAccentColor, "teal"); err != nil {
		t.Fatal(err)
	}
	if err := w.in.Use(ctx, KeyAccentColor, "mauve"); err == nil {
		t.Error("used an accent GNOME doesn't have")
	}
	// A font with a size is set as it is; a family alone keeps the size.
	if err := w.in.Use(ctx, KeyFont, "Inter 12"); err != nil {
		t.Fatal(err)
	}
	if err := w.in.Use(ctx, KeyMonospaceFont, "Fira Code"); err != nil {
		t.Fatal(err)
	}
	if w.gs.values[KeyFont] != "Inter 12" || w.gs.values[KeyMonospaceFont] != "Fira Code 11" {
		t.Errorf("fonts = %q, %q", w.gs.values[KeyFont], w.gs.values[KeyMonospaceFont])
	}
	// Setting a value it already has saves nothing.
	if err := w.in.Use(ctx, KeyIconTheme, "Adwaita"); err != nil {
		t.Fatal(err)
	}
	if saved, _ := w.in.Saved(); len(saved) != 4 {
		t.Errorf("Saved = %+v", saved)
	}
	// Setting one back to its value from before uas forgets it.
	if err := w.in.Use(ctx, KeyAccentColor, "blue"); err != nil {
		t.Fatal(err)
	}
	if _, ok := savedKey(w.in, KeyAccentColor); ok || w.gs.values[KeyAccentColor] != "blue" {
		t.Errorf("accent is %q, saved %v", w.gs.values[KeyAccentColor], ok)
	}
	if err := w.in.RevertAll(ctx); err != nil {
		t.Fatal(err)
	}
	for key, want := range map[string]string{KeyColorScheme: SchemeDefault, KeyFont: "Adwaita Sans 11",
		KeyMonospaceFont: "Adwaita Mono 11"} {
		if w.gs.values[key] != want {
			t.Errorf("after RevertAll, %s = %q, want %q", key, w.gs.values[key], want)
		}
	}
	if saved, _ := w.in.Saved(); len(saved) != 0 {
		t.Errorf("Saved after RevertAll = %+v", saved)
	}
}

func savedKey(in *Installer, key string) (state.SavedSetting, bool) {
	saved, _ := in.Saved()
	for _, s := range saved {
		if s.Key == key {
			return s, true
		}
	}
	return state.SavedSetting{}, false
}
