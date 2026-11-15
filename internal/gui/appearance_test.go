package gui

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
	"image/png"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/tyler-builds/jamb/access"
	"github.com/tyler-builds/jamb/ui/uitest"

	archives "github.com/tyler-builds/uas/internal/appearance"
	"github.com/tyler-builds/uas/internal/catalog"
	"github.com/tyler-builds/uas/internal/client/appearance"
	"github.com/tyler-builds/uas/internal/client/backend"
	"github.com/tyler-builds/uas/internal/client/session"
	"github.com/tyler-builds/uas/internal/client/settings"
	"github.com/tyler-builds/uas/internal/client/state"
	"github.com/tyler-builds/uas/internal/client/telemetry"
)

// fakeGNOME is gsettings for org.gnome.desktop.interface, in memory, so tests never change the real
// desktop.
type fakeGNOME struct {
	mu     sync.Mutex
	values map[string]string
}

func newFakeGNOME() *fakeGNOME {
	return &fakeGNOME{values: map[string]string{appearance.KeyIconTheme: "Adwaita",
		appearance.KeyCursorTheme: "Adwaita", appearance.KeyFont: "Adwaita Sans 11",
		appearance.KeyDocumentFont: "Adwaita Sans 12", appearance.KeyMonospaceFont: "Adwaita Mono 11"}}
}

func (f *fakeGNOME) get(key string) string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.values[key]
}

func (f *fakeGNOME) Run(_ context.Context, name string, args []string, _ func(string)) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	switch {
	case name == "fc-cache":
		return "", nil
	case name != "gsettings" || len(args) < 3:
		return "", fmt.Errorf("unexpected %s %v", name, args)
	case args[0] == "get":
		return "'" + f.values[args[2]] + "'\n", nil
	case args[0] == "set":
		f.values[args[2]] = strings.Trim(args[3], "'")
	case args[0] == "reset":
		f.values[args[2]] = ""
	}
	return "", nil
}

// routedTransport answers every request with a handler, whatever its host.
type routedTransport struct{ h http.Handler }

func (r routedTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	rec := httptest.NewRecorder()
	r.h.ServeHTTP(rec, req)
	return rec.Result(), nil
}

// iconArchive is an icon theme archive with two variants, the second inheriting from the first.
func iconArchive(t *testing.T) []byte {
	t.Helper()
	var raw bytes.Buffer
	tw := tar.NewWriter(&raw)
	for name, body := range map[string]string{
		"Kora/index.theme":              "[Icon Theme]\nName=Kora\nDirectories=48/apps\n",
		"Kora/48/apps/firefox.svg":      "<svg/>",
		"Kora-Dark/index.theme":         "[Icon Theme]\nName=Kora Dark\nInherits=Kora\nDirectories=48/apps\n",
		"Kora-Dark/48/apps/firefox.svg": "<svg dark/>",
	} {
		tw.WriteHeader(&tar.Header{Name: name, Mode: 0o644, Size: int64(len(body)), Typeflag: tar.TypeReg})
		tw.Write([]byte(body))
	}
	tw.Close()
	var out bytes.Buffer
	zw := gzip.NewWriter(&out)
	zw.Write(raw.Bytes())
	zw.Close()
	return out.Bytes()
}

// TestAppearanceInstallUseRemove browses the Icons tab, opens a theme, picks a variant, installs and uses
// it, and removes it, against a fake Pling and a fake GNOME.
func TestAppearanceInstallUseRemove(t *testing.T) {
	dir := t.TempDir()
	archive := iconArchive(t)
	sha, md := sha256.Sum256(archive), md5.Sum(archive)
	asset := catalog.Asset{Kind: catalog.AssetTheme, Name: "kora.tar.gz", Slot: 1, MD5: hex.EncodeToString(md[:]),
		SHA256: hex.EncodeToString(sha[:]), Size: int64(len(archive)), Themes: []string{"Kora", "Kora-Dark"}}
	l := catalog.Listing{Source: catalog.SourcePling, Ref: "1209330", Kind: catalog.KindIconTheme, Name: "Kora",
		Summary: "An icon theme", Developer: "bikass", Installs: 250000, Score: 50, Version: "1.6",
		Assets: []catalog.Asset{asset}}
	id := catalog.AppearanceAppID(l.Source, l.Ref)
	path := filepath.Join(dir, "catalog.db")
	apps := []catalog.App{{ID: id, Kind: l.Kind, Name: l.Name, Summary: l.Summary, Developer: l.Developer,
		Popularity: l.Installs, Listings: []catalog.Listing{l}}}
	if err := catalog.Write(context.Background(), path, apps, catalog.BuildInfo{BuiltAt: time.Now(),
		Appearance: []catalog.SourceStatus{{Source: catalog.SourcePling, Listings: 1}}}); err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/ocs/v1/content/data/1209330", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{"status": "ok", "data": []any{map[string]any{
			"downloadname1": asset.Name, "downloadmd5sum1": asset.MD5,
			"downloadlink1": "https://files.pling.com/api/files/download/j/x/kora.tar.gz"}}})
	})
	mux.HandleFunc("/api/files/download/j/x/kora.tar.gz", func(w http.ResponseWriter, r *http.Request) {
		w.Write(archive)
	})
	gs := newFakeGNOME()
	in := &appearance.Installer{Data: filepath.Join(dir, "data"), Cache: filepath.Join(dir, "cache"),
		State:  state.Store{Path: filepath.Join(dir, "data", "uas", "state.json")},
		Client: &http.Client{Transport: routedTransport{mux}}, Runner: gs, Desktop: &appearance.GNOME{Runner: gs},
		PlingAPI: appearance.PlingAPI, Limits: archives.DefaultLimits}
	s := &session.Session{Settings: settings.Default(), Telemetry: telemetry.Nop{}, CatalogPath: path, Looks: in,
		Backends: map[catalog.SourceID]backend.Backend{}}
	a := New(s)
	a.icons.dir = filepath.Join(dir, "images")
	a.startup.Do(func() {})
	u := uitest.New(t, a.Root, uitest.Size(1280, 860))
	defer u.Close()

	a.goTo(view{page: pageAppearance})
	u.WaitFor("the Icons tab", func() bool { return u.HasName("Icons") })
	u.ByName("Icons").Click()
	u.WaitFor("the Kora card", func() bool { return u.Has("look-card:" + id) })
	waitText(t, u, "look-card:"+id, "250k downloads")
	u.Click("look-card:" + id)
	u.WaitFor("the theme's page", func() bool { return u.Has("look:" + id) })
	// Unrated: Pling's 50 isn't shown as a rating.
	waitText(t, u, "look:"+id, "Not rated")
	if strings.Contains(u.Get("look:"+id).Text(), "50 of 100") {
		t.Error("an unrated item shows a rating")
	}
	a.lookTabVariant(t, u, id, "Kora-Dark")
	clickButton(u, "Install and use")
	u.WaitFor("the install", func() bool { return gs.get(appearance.KeyIconTheme) == "Kora-Dark" })
	for _, th := range []string{"Kora", "Kora-Dark"} {
		if _, err := os.Stat(filepath.Join(in.IconsDir(), th, "index.theme")); err != nil {
			t.Error(err)
		}
	}
	waitText(t, u, "look:"+id, "In use")

	// The Installed tab marks it and offers Revert.
	a.goTo(view{page: pageAppearance})
	a.lookTab.Set(lookInstalled)
	u.WaitFor("the Installed tab", func() bool { return u.HasRole(access.RoleButton, "Revert") })
	clickButton(u, "Revert")
	u.WaitFor("the revert", func() bool { return gs.get(appearance.KeyIconTheme) == "Adwaita" })

	// Remove asks first.
	clickButton(u, "Remove Kora")
	u.WaitFor("the remove dialog", func() bool { return u.HasRole(access.RoleAlertDialog, "Remove Kora?") })
	u.ByRole(access.RoleAlertDialog, "Remove Kora?").ByRole(access.RoleButton, "Remove").Click()
	u.WaitFor("the removal", func() bool {
		recs, _ := in.Records()
		return len(recs) == 0
	})
	if _, err := os.Stat(filepath.Join(in.IconsDir(), "Kora")); err == nil {
		t.Error("the theme is still there")
	}
}

// lookTabVariant picks a variant on a theme's page.
func (a *App) lookTabVariant(t *testing.T, u *uitest.UI, id, variant string) {
	t.Helper()
	u.WaitFor("the variant picker", func() bool { return u.HasRole(access.RoleComboBox, "Variant") })
	u.ByRole(access.RoleComboBox, "Variant").Click()
	u.WaitFor("the variant "+variant, func() bool { return u.HasRole(access.RoleListItem, variant) })
	u.ByRole(access.RoleListItem, variant).Click()
	u.WaitFor("the variant to be picked", func() bool {
		return u.ByRole(access.RoleComboBox, "Variant").Value() == variant
	})
}

// TestFontSampleRenders draws a sample from a real font file.
func TestFontSampleRenders(t *testing.T) {
	data, err := os.ReadFile("fonts/Inter-Regular.ttf")
	if err != nil {
		t.Fatal(err)
	}
	light, _ := Themes()
	s, err := renderSample(data, "The quick brown fox", 24, light.Text, 600)
	if err != nil || s.img == nil || s.w < 100 || s.h < 20 {
		t.Fatalf("renderSample = %+v, %v", s, err)
	}
	// Cut at the width given.
	if s, err := renderSample(data, strings.Repeat("Wide ", 100), 24, light.Text, 300); err != nil || s.w > 300 {
		t.Errorf("wide sample = %v wide, %v", s.w, err)
	}
	// Inter has no CJK letters.
	if _, err := renderSample(data, "日本語", 24, light.Text, 600); err != errNoGlyphs {
		t.Errorf("CJK sample: %v", err)
	}
}

// TestAppearanceLive runs the real Appearance pages against a real catalog and the real Pling, Google Fonts
// and GitHub, in a temporary home folder, with a fake GNOME so the desktop's settings stay as they are. It
// installs, uses and removes a font, an icon theme (a variant of a multi-variant one) and a cursor theme,
// and saves screenshots. It needs UAS_LOOKS_LIVE (a folder for the screenshots), UAS_CATALOG, and the IDs
// in UAS_LOOKS_FONT, UAS_LOOKS_ICONS (with UAS_LOOKS_VARIANT) and UAS_LOOKS_CURSOR.
func TestAppearanceLive(t *testing.T) {
	out, path := os.Getenv("UAS_LOOKS_LIVE"), os.Getenv("UAS_CATALOG")
	if out == "" || path == "" {
		t.Skip("set UAS_LOOKS_LIVE and UAS_CATALOG")
	}
	os.MkdirAll(out, 0o755)
	useBundledFont(t)
	home := t.TempDir()
	gs := newFakeGNOME()
	in := &appearance.Installer{Data: filepath.Join(home, ".local", "share"), Home: home,
		SystemData: []string{"/usr/local/share", "/usr/share"}, Cache: filepath.Join(home, ".cache", "uas", "fonts"),
		State:  state.Store{Path: filepath.Join(home, ".local", "share", "uas", "state.json")},
		Client: &http.Client{Timeout: 10 * time.Minute}, Runner: gs, Desktop: &appearance.GNOME{Runner: gs},
		PlingAPI: appearance.PlingAPI, Limits: archives.DefaultLimits}
	s := &session.Session{Settings: settings.Default(), Telemetry: telemetry.Nop{}, CatalogPath: path, Looks: in,
		Backends: map[catalog.SourceID]backend.Backend{}}
	a := New(s)
	a.icons.dir = filepath.Join(os.TempDir(), "uas-shot-images")
	a.startup.Do(func() {})
	light, dark := Themes()
	_ = light
	u := uitest.New(t, a.Root, uitest.Size(1440, 1000), uitest.Theme(dark))
	defer u.Close()
	settle := func(rounds int) {
		for i := 0; i < rounds; i++ {
			u.Advance(100 * time.Millisecond)
			time.Sleep(60 * time.Millisecond)
		}
	}
	shot := func(name string) {
		settle(40)
		f, err := os.Create(filepath.Join(out, name+".png"))
		if err != nil {
			t.Fatal(err)
		}
		png.Encode(f, u.Screenshot())
		f.Close()
	}
	waitLong := func(what string, cond func() bool) {
		deadline := time.Now().Add(5 * time.Minute)
		for !cond() {
			if time.Now().After(deadline) {
				t.Fatalf("%s did not happen within 5 minutes", what)
			}
			u.Advance(100 * time.Millisecond)
			time.Sleep(50 * time.Millisecond)
		}
	}
	jobDone := func(id string) func() bool {
		return func() bool { _, busy := a.running(id); return !busy }
	}

	// Browse and search each tab.
	a.goTo(view{page: pageAppearance})
	for _, tab := range []struct{ tab, q string }{{lookFonts, "mono"}, {lookIcons, "papirus"}, {lookCursors, "cursors"}} {
		a.lookTab.Set(tab.tab)
		a.listFilter.Set("")
		shot("browse-" + tab.tab)
		a.listFilter.Set(tab.q)
		shot("search-" + tab.tab)
	}
	a.listFilter.Set("")

	install := func(id, variant, use string) state.AppearanceInstall {
		a.openLook(id)
		u.WaitFor("the page of "+id, func() bool { return u.Has("look:" + id) })
		if variant != "" {
			a.lookTabVariant(t, u, id, variant)
		}
		shot("page-" + strings.ReplaceAll(id, ":", "-"))
		clickButton(u, "Install and use")
		settle(5)
		waitLong("the install of "+id, jobDone(id))
		recs, _ := in.Records()
		i := slices.IndexFunc(recs, func(r state.AppearanceInstall) bool { return r.ID == id })
		if i < 0 {
			t.Fatalf("%s wasn't installed", id)
		}
		shot("installed-" + strings.ReplaceAll(id, ":", "-"))
		return recs[i]
	}
	font := install(os.Getenv("UAS_LOOKS_FONT"), "", "")
	t.Logf("font: %+v; monospace or interface now %q / %q", font, gs.get(appearance.KeyMonospaceFont),
		gs.get(appearance.KeyFont))
	icon := install(os.Getenv("UAS_LOOKS_ICONS"), os.Getenv("UAS_LOOKS_VARIANT"), "")
	t.Logf("icons: %+v; icon theme now %q", icon, gs.get(appearance.KeyIconTheme))
	if v := os.Getenv("UAS_LOOKS_VARIANT"); v != "" && gs.get(appearance.KeyIconTheme) != v {
		t.Errorf("icon theme = %q, want %q", gs.get(appearance.KeyIconTheme), v)
	}
	cursor := install(os.Getenv("UAS_LOOKS_CURSOR"), "", "")
	t.Logf("cursor: %+v; cursor theme now %q", cursor, gs.get(appearance.KeyCursorTheme))

	a.goTo(view{page: pageAppearance})
	a.lookTab.Set(lookInstalled)
	shot("installed-tab")

	for _, rec := range []state.AppearanceInstall{font, icon, cursor} {
		a.lookRemoving.Set(&rec)
		u.WaitFor("the remove dialog", func() bool { return u.HasRole(access.RoleAlertDialog, "Remove "+rec.Name+"?") })
		u.ByRole(access.RoleAlertDialog, "Remove "+rec.Name+"?").ByRole(access.RoleButton, "Remove").Click()
		settle(5)
		waitLong("the removal of "+rec.ID, jobDone(rec.ID))
		for _, p := range rec.Paths {
			if _, err := os.Stat(p); err == nil {
				t.Errorf("%s is still there", p)
			}
		}
	}
	if recs, _ := in.Records(); len(recs) != 0 {
		t.Errorf("left installed: %+v", recs)
	}
	for key, want := range map[string]string{appearance.KeyIconTheme: "Adwaita", appearance.KeyCursorTheme: "Adwaita",
		appearance.KeyFont: "Adwaita Sans 11", appearance.KeyMonospaceFont: "Adwaita Mono 11"} {
		if got := gs.get(key); got != want {
			t.Errorf("after removing, %s = %q, want %q", key, got, want)
		}
	}
	shot("after-remove")
}
