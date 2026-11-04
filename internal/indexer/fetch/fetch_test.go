package fetch

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestGetUsesETag(t *testing.T) {
	hits := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		if r.Header.Get("If-None-Match") == `"v1"` {
			w.WriteHeader(http.StatusNotModified)
			return
		}
		w.Header().Set("ETag", `"v1"`)
		w.Write([]byte("hello"))
	}))
	defer srv.Close()

	dir := t.TempDir()
	ctx := context.Background()
	p1, err := Get(ctx, srv.Client(), srv.URL+"/data.xml", dir)
	if err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-10 * 24 * time.Hour)
	os.Chtimes(p1, old, old)
	p2, err := Get(ctx, srv.Client(), srv.URL+"/data.xml", dir)
	if err != nil || p2 != p1 || hits != 2 {
		t.Fatalf("second Get = %q, %v, hits %d", p2, err, hits)
	}
	if b, _ := os.ReadFile(p2); string(b) != "hello" {
		t.Errorf("cached body = %q", b)
	}

	// The 304 refreshed the file's time, so pruning keeps it and deletes only the stale file.
	stale := filepath.Join(dir, "stale-primary.xml.zst")
	os.WriteFile(stale, []byte("x"), 0o644)
	os.Chtimes(stale, old, old)
	n, err := PruneCache(dir, 3*24*time.Hour)
	if err != nil || n != 1 {
		t.Fatalf("PruneCache = %d, %v", n, err)
	}
	if _, err := os.Stat(p1); err != nil {
		t.Error("pruned a file that is still in use")
	}
	if _, err := PruneCache(filepath.Join(dir, "missing"), time.Hour); err != nil {
		t.Errorf("PruneCache on a missing dir: %v", err)
	}
}
