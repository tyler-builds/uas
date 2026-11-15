package pling

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/md5"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/tyler-builds/uas/internal/catalog"
)

// tgz builds a .tar.gz of files (name to body; a body starting with "->" is a symlink).
func tgz(t *testing.T, files map[string]string) []byte {
	t.Helper()
	var raw bytes.Buffer
	tw := tar.NewWriter(&raw)
	names := make([]string, 0, len(files))
	for n := range files {
		names = append(names, n)
	}
	slices.Sort(names)
	for _, n := range names {
		body := files[n]
		h := &tar.Header{Name: n, Mode: 0o644, Size: int64(len(body)), Typeflag: tar.TypeReg}
		if target, ok := strings.CutPrefix(body, "->"); ok {
			h.Typeflag, h.Linkname, h.Size = tar.TypeSymlink, target, 0
		}
		if err := tw.WriteHeader(h); err != nil {
			t.Fatal(err)
		}
		if h.Typeflag == tar.TypeReg {
			tw.Write([]byte(body))
		}
	}
	tw.Close()
	var out bytes.Buffer
	zw := gzip.NewWriter(&out)
	zw.Write(raw.Bytes())
	zw.Close()
	return out.Bytes()
}

func icons(name string) map[string]string {
	return map[string]string{name + "/index.theme": "[Icon Theme]\nName=" + name + "\nDirectories=48x48/apps\n",
		name + "/48x48/apps/a.svg": "<svg/>", name + "/48x48/apps/b.svg": "->a.svg"}
}

func cursors(name string) map[string]string {
	return map[string]string{name + "/index.theme": "[Icon Theme]\nName=" + name + "\n",
		name + "/cursors/default": "Xcur", name + "/cursors/left_ptr": "->default"}
}

func merge(ms ...map[string]string) map[string]string {
	out := map[string]string{}
	for _, m := range ms {
		for k, v := range m {
			out[k] = v
		}
	}
	return out
}

// plingServer serves the saved lists, with each item's files replaced by archives built here.
type plingServer struct {
	*httptest.Server
	mu        sync.Mutex
	downloads int
}

func newServer(t *testing.T) *plingServer {
	t.Helper()
	archives := map[string][]byte{
		"1305251/1": tgz(t, icons("candy-icons")),
		"1166289/1": tgz(t, merge(icons("Papirus"), icons("Papirus-Dark"), map[string]string{"README.md": "hi"})),
		// Kora's shape: a helper script at the theme's top, which is left out, not the file.
		"1166289/2": tgz(t, merge(icons("Papirus"), map[string]string{
			"Papirus/create-new-icon-theme.cache.sh": "#!/bin/sh\ngtk-update-icon-cache .\n"})),
		// A program refuses the file.
		"1166289/3": tgz(t, merge(icons("Papirus"), icons("Papirus-Light"),
			map[string]string{"Papirus-Light/bin/helper": "\x7fELF\x02\x01\x01"})),
		"1411743/1": tgz(t, cursors("WhiteSur-cursors")),
		"1197198/2": tgz(t, map[string]string{"Bibata/Install.inf": "[Windows]"}),
		"1197198/3": tgz(t, cursors("Bibata-Modern-Ice")),
	}
	s := &plingServer{}
	lists := map[string][]byte{}
	for cat, file := range map[string]string{"132": "testdata/icons.json", "107": "testdata/cursors.json"} {
		raw, err := os.ReadFile(file)
		if err != nil {
			t.Fatal(err)
		}
		var page map[string]any
		dec := json.NewDecoder(bytes.NewReader(raw))
		dec.UseNumber()
		if err := dec.Decode(&page); err != nil {
			t.Fatal(err)
		}
		for _, it := range page["data"].([]any) {
			m := it.(map[string]any)
			id := fmt.Sprint(m["id"])
			for n := 1; n <= 3; n++ {
				if _, ok := m[fmt.Sprintf("downloadlink%d", n)]; !ok {
					continue
				}
				// Links carry raw spaces, as Pling's do.
				m[fmt.Sprintf("downloadlink%d", n)] = fmt.Sprintf("{{base}}/dl/%s/%d/file name", id, n)
				if b, ok := archives[fmt.Sprintf("%s/%d", id, n)]; ok {
					sum := md5.Sum(b)
					m[fmt.Sprintf("downloadmd5sum%d", n)] = hex.EncodeToString(sum[:])
				}
			}
		}
		out, _ := json.Marshal(page)
		lists[cat] = out
	}
	s.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/content/data":
			if r.URL.Query().Get("page") != "0" || r.URL.Query().Get("sortmode") != "down" {
				w.Write([]byte(`{"status":"ok","data":[]}`))
				return
			}
			w.Write(bytes.ReplaceAll(lists[r.URL.Query().Get("categories")], []byte("{{base}}"), []byte(s.URL)))
		case strings.HasPrefix(r.URL.Path, "/dl/"):
			if !strings.HasSuffix(r.URL.Path, "/file name") {
				http.Error(w, "bad link", http.StatusBadRequest)
				return
			}
			parts := strings.Split(r.URL.Path, "/")
			b, ok := archives[parts[2]+"/"+parts[3]]
			if !ok {
				http.NotFound(w, r)
				return
			}
			s.mu.Lock()
			s.downloads++
			s.mu.Unlock()
			w.Write(b)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(s.Close)
	return s
}

func TestFetch(t *testing.T) {
	srv := newServer(t)
	cache := t.TempDir()
	now := time.Date(2026, 11, 15, 0, 0, 0, 0, time.UTC)
	var logged bytes.Buffer
	f := &Fetcher{Client: srv.Client(), API: srv.URL, CacheDir: cache, Every: time.Millisecond,
		Now: func() time.Time { return now }, Log: slog.New(slog.NewTextHandler(&logged, nil))}
	ls, err := f.Fetch(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	byRef := map[string]catalog.Listing{}
	for _, l := range ls {
		byRef[l.Ref] = l
	}
	if len(ls) != 4 {
		t.Fatalf("listings = %d", len(ls))
	}
	papirus := byRef["1166289"]
	if papirus.Kind != catalog.KindIconTheme || papirus.Name != "Papirus" || papirus.Installs != 2786767 ||
		papirus.Score != 93 || papirus.License != "GPL-3.0-only" || papirus.StoreURL != "https://www.pling.com/p/1166289/" {
		t.Errorf("Papirus = %+v", papirus)
	}
	// The file with a program inside is left out; the one with a script is kept, without the script. The others
	// keep their variants, by slot.
	if len(papirus.Assets) != 2 {
		t.Fatalf("Papirus assets = %+v", papirus.Assets)
	}
	a := papirus.Assets[0]
	if a.Kind != catalog.AssetTheme || a.Slot != 1 || a.Name != "papirus-icon-theme-20250501.tar.gz" ||
		a.URL != "" || len(a.SHA256) != 64 || a.MD5 == "" || !slices.Equal(a.Themes, []string{"Papirus", "Papirus-Dark"}) {
		t.Errorf("Papirus asset = %+v", a)
	}
	if a := papirus.Assets[1]; a.Slot != 2 || !slices.Equal(a.Themes, []string{"Papirus"}) || a.LeftOut != 1 {
		t.Errorf("Papirus asset 2 = %+v", a)
	}
	if papirus.Assets[0].LeftOut != 0 {
		t.Errorf("Papirus asset 1 left out %d", papirus.Assets[0].LeftOut)
	}
	// Each refusal is in the log at the default level, with the item, so a missing theme can be traced.
	for _, want := range []string{
		`msg="pling file refused" item=1166289 name=Papirus kind=icon-theme file=papirus-icon-theme-black-folders.tar.xz why="it contains a program (Papirus-Light/bin/helper)"`,
		`msg="pling file read, files that could run left out" item=1166289 name=Papirus file=papirus-icon-theme-adwaita-folders.tar.xz left_out=1`,
		`msg="pling file refused" item=1197198 name="Bibata Modern Ice"`,
	} {
		if !strings.Contains(logged.String(), want) {
			t.Errorf("log lacks %s:\n%s", want, logged.String())
		}
	}
	if papirus.Trust == nil || papirus.Trust.Score == 0 || papirus.Released.IsZero() {
		t.Errorf("trust/released = %+v %v", papirus.Trust, papirus.Released)
	}
	for _, s := range papirus.Screenshots {
		if !strings.HasPrefix(s.URL, "https://images.pling.com/") {
			t.Errorf("screenshot %s", s.URL)
		}
	}
	candy := byRef["1305251"]
	if strings.Contains(candy.Summary, "Support") || candy.Summary == "" {
		t.Errorf("Candy summary = %q", candy.Summary)
	}
	if strings.ContainsAny(candy.Description, "<>") || strings.Contains(candy.Description, "&hearts;") {
		t.Errorf("Candy description = %q", candy.Description)
	}
	bibata := byRef["1197198"]
	if bibata.Kind != catalog.KindCursorTheme || len(bibata.Assets) != 1 || bibata.Assets[0].Slot != 3 ||
		!slices.Equal(bibata.Assets[0].Themes, []string{"Bibata-Modern-Ice"}) {
		t.Errorf("Bibata = %+v", bibata.Assets)
	}
	if byRef["1411743"].Kind != catalog.KindCursorTheme {
		t.Errorf("WhiteSur = %+v", byRef["1411743"])
	}
	// Every archive once; Bibata's link to a web page never.
	if srv.downloads != 7 {
		t.Errorf("downloads = %d", srv.downloads)
	}
	first := srv.downloads

	// A second run reads nothing again.
	f2 := &Fetcher{Client: srv.Client(), API: srv.URL, CacheDir: cache, Every: time.Millisecond}
	ls2, err := f2.Fetch(context.Background())
	if err != nil || len(ls2) != 4 || srv.downloads != first {
		t.Errorf("second run: %d listings, %v, %d downloads (first %d)", len(ls2), err, srv.downloads, first)
	}
}

func TestFetchBudgetAndFloor(t *testing.T) {
	srv := newServer(t)
	cache := t.TempDir()
	// One new file a run: only Candy, whose one file is read first, is listed; the rest wait.
	f := &Fetcher{Client: srv.Client(), API: srv.URL, CacheDir: cache, Every: time.Millisecond, PinCount: 1}
	ls, err := f.Fetch(context.Background())
	if err != nil || len(ls) != 1 || ls[0].Ref != "1305251" {
		t.Fatalf("Fetch = %v, %v", ls, err)
	}
	// A floor above Papirus and every cursor theme.
	f = &Fetcher{Client: srv.Client(), API: srv.URL, CacheDir: cache, Every: time.Millisecond,
		MinDownloads: 3_000_000, MinCursorDownloads: 3_000_000}
	ls, err = f.Fetch(context.Background())
	if err != nil || len(ls) != 1 || ls[0].Ref != "1305251" {
		t.Errorf("with a floor: %v, %v", ls, err)
	}
}

func refs(ls []catalog.Listing) []string {
	var out []string
	for _, l := range ls {
		out = append(out, l.Ref)
	}
	slices.Sort(out)
	return out
}

// TestFetchBudgetShared checks that icon themes, read first, don't spend a run's whole budget before any
// cursor theme is read.
func TestFetchBudgetShared(t *testing.T) {
	// Two files: one goes to icon themes (Candy), one to cursor themes (WhiteSur). Before shares, both went
	// to icon themes (Candy and Papirus's first file) and no cursor theme was listed.
	srv := newServer(t)
	f := &Fetcher{Client: srv.Client(), API: srv.URL, CacheDir: t.TempDir(), Every: time.Millisecond, PinCount: 2}
	ls, err := f.Fetch(context.Background())
	if got := refs(ls); err != nil || !slices.Equal(got, []string{"1305251", "1411743"}) {
		t.Errorf("two files: %v, %v", got, err)
	}

	// 60 MiB: icon themes' half takes Candy (1.7 MiB) but not Papirus's first file (31 MiB, which would make
	// 33); cursor themes take what is left of the budget, and use 12 MiB of it; what they leave goes to
	// Papirus afterwards, whose first file fits and the second doesn't.
	srv = newServer(t)
	f = &Fetcher{Client: srv.Client(), API: srv.URL, CacheDir: t.TempDir(), Every: time.Millisecond,
		PinBytes: 60 << 20}
	ls, err = f.Fetch(context.Background())
	if got := refs(ls); err != nil || !slices.Equal(got, []string{"1197198", "1305251", "1411743"}) ||
		f.postponed != 1 || srv.downloads != 5 {
		t.Errorf("60 MiB: %v, %v; %d postponed, %d downloads", got, err, f.postponed, srv.downloads)
	}

	// Enough for all: what cursor themes leave lets Papirus finish in the same run, each file read once.
	srv = newServer(t)
	f = &Fetcher{Client: srv.Client(), API: srv.URL, CacheDir: t.TempDir(), Every: time.Millisecond,
		PinBytes: 100 << 20}
	ls, err = f.Fetch(context.Background())
	if got := refs(ls); err != nil || len(got) != 4 || f.postponed != 0 || srv.downloads != 7 {
		t.Errorf("100 MiB: %v, %v; %d postponed, %d downloads", got, err, f.postponed, srv.downloads)
	}
}

func TestPlainTextAndLicense(t *testing.T) {
	got := plainText("<h2>Title</h2><p>One &amp; two<br/>three</p><ul><li>a</li><li>b</li></ul>")
	if got != "Title\nOne & two\nthree\n\n• a\n• b" {
		t.Errorf("plainText = %q", got)
	}
	if license([]string{"unix", "gplv2-later"}) != "GPL-2.0-or-later" || license([]string{"linux"}) != "" {
		t.Error("license")
	}
	if s := summary("Support Me ♥", "Support me please\nA sugary icon theme. With colors."); s != "A sugary icon theme." {
		t.Errorf("summary = %q", s)
	}
}

func TestReasonKind(t *testing.T) {
	for why, want := range map[string]string{
		`a link in it leads to "x/y.svg", which isn't in the archive`:     "a link to a file missing from the archive",
		`"a/b" is a link to /usr/share/icons/x, outside the archive`:      "a path or link outside the archive",
		"it contains a program or script (a/b.desktop)":                   "a program or script inside",
		"it contains a program (a/bin/x)":                                 "a program or script inside",
		"it contains a library (a/libx.so)":                               "a program or script inside",
		`its theme "L" has another theme inside it (L/dist)`:              "a theme inside another theme",
		"no cursor theme inside":                                          "no theme of the item's kind inside",
		"download: the file doesn't match Pling's MD5":                    "didn't match Pling's MD5",
		"it is a 7-Zip archive, which uas doesn't open; it opens .tar.gz": "an archive format uas doesn't open",
	} {
		if got := reasonKind(why); got != want {
			t.Errorf("reasonKind(%q) = %q, want %q", why, got, want)
		}
	}
}
