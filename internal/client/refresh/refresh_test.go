package refresh

import (
	"context"
	"crypto/ed25519"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/tyler-builds/uas/internal/catalog"
	"github.com/tyler-builds/uas/internal/indexer/publish"
	"github.com/tyler-builds/uas/internal/manifest"
)

type fixture struct {
	t       *testing.T
	key     ed25519.PrivateKey
	store   *publish.DirStore
	server  *httptest.Server
	options Options
}

func newFixture(t *testing.T) *fixture {
	s, _, err := manifest.GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	key, _ := manifest.ParsePrivateKey(s)
	store := &publish.DirStore{Dir: t.TempDir()}
	srv := httptest.NewServer(http.FileServer(http.Dir(store.Dir)))
	t.Cleanup(srv.Close)
	return &fixture{t: t, key: key, store: store, server: srv, options: Options{
		BaseURL:     srv.URL,
		TrustedKeys: []ed25519.PublicKey{key.Public().(ed25519.PublicKey)},
		CatalogPath: filepath.Join(t.TempDir(), "uas", "catalog.db"),
		Client:      srv.Client(),
	}}
}

// publish builds a one-app catalog named name and publishes it as built at builtAt.
func (f *fixture) publish(name string, builtAt time.Time) {
	f.t.Helper()
	db := filepath.Join(f.t.TempDir(), "catalog.db")
	apps := []catalog.App{{ID: "org.example." + name, Kind: catalog.KindDesktopApp, Name: name,
		Listings: []catalog.Listing{{Source: catalog.SourceFlathub, Ref: "app/org.example." + name + "/x86_64/stable",
			Name: name}}}}
	info := catalog.BuildInfo{BuiltAt: builtAt, Apps: 1}
	ctx := context.Background()
	if err := catalog.Write(ctx, db, apps, info); err != nil {
		f.t.Fatal(err)
	}
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	if _, err := publish.Publish(ctx, f.store, db, info, publish.Options{Key: f.key, Keep: 2, Log: log}); err != nil {
		f.t.Fatal(err)
	}
}

func (f *fixture) search(q string) []catalog.SearchResult {
	f.t.Helper()
	db, err := catalog.Open(f.options.CatalogPath)
	if err != nil {
		f.t.Fatal(err)
	}
	defer db.Close()
	rs, err := db.Search(context.Background(), q, 10, catalog.OrderRelevance)
	if err != nil {
		f.t.Fatal(err)
	}
	return rs
}

func TestRefresh(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	t0 := time.Unix(1770000000, 0).UTC()

	f.publish("Alpha", t0)
	res, err := Refresh(ctx, f.options)
	if err != nil {
		t.Fatal(err)
	}
	if !res.Updated || len(f.search("alpha")) != 1 {
		t.Fatalf("first refresh: %+v", res)
	}

	// Nothing changed on the server.
	if res, err := Refresh(ctx, f.options); err != nil || res.Updated {
		t.Fatalf("second refresh = %+v, %v", res, err)
	}

	f.publish("Beta", t0.Add(time.Hour))
	if res, err := Refresh(ctx, f.options); err != nil || !res.Updated || len(f.search("beta")) != 1 {
		t.Fatalf("refresh after new publish = %+v, %v", res, err)
	}

	// An older manifest is refused, and the local catalog stays.
	f.publish("Gamma", t0.Add(-time.Hour))
	if _, err := Refresh(ctx, f.options); err == nil || !strings.Contains(err.Error(), "older") {
		t.Fatalf("rollback err = %v", err)
	}
	if len(f.search("beta")) != 1 {
		t.Fatal("local catalog changed after a refused rollback")
	}

	// Only the newest two catalogs are kept on the server.
	objs, err := f.store.ListCatalogs(ctx)
	if err != nil || len(objs) != 2 {
		t.Fatalf("stored catalogs = %d, %v", len(objs), err)
	}
}

func TestRefreshRejectsTamperedCatalog(t *testing.T) {
	f := newFixture(t)
	f.publish("Alpha", time.Unix(1770000000, 0).UTC())
	objs, err := f.store.ListCatalogs(context.Background())
	if err != nil || len(objs) != 1 {
		t.Fatal(objs, err)
	}
	path := filepath.Join(f.store.Dir, objs[0].Key)
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	b[len(b)/2] ^= 0xff
	if err := os.WriteFile(path, b, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Refresh(context.Background(), f.options); err == nil || !strings.Contains(err.Error(), "hash") {
		t.Fatalf("err = %v, want a hash mismatch", err)
	}
	if fileExists(f.options.CatalogPath) {
		t.Error("a catalog was installed despite the bad hash")
	}
}

func TestRefreshRejectsUntrustedKey(t *testing.T) {
	f := newFixture(t)
	f.publish("Alpha", time.Unix(1770000000, 0).UTC())
	other, _, _ := manifest.GenerateKey()
	otherKey, _ := manifest.ParsePrivateKey(other)
	f.options.TrustedKeys = []ed25519.PublicKey{otherKey.Public().(ed25519.PublicKey)}
	if _, err := Refresh(context.Background(), f.options); err == nil || !strings.Contains(err.Error(), "unknown key") {
		t.Fatalf("err = %v, want unknown key", err)
	}
}
