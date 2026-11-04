package publish

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

func TestIcons(t *testing.T) {
	src := t.TempDir()
	a, b := strings.Repeat("a", 64)+".png", strings.Repeat("b", 64)+".svg"
	for _, n := range []string{a, b, "notes.txt"} {
		os.WriteFile(filepath.Join(src, n), []byte(n), 0o644)
	}
	store := &DirStore{Dir: t.TempDir()}
	n, err := Icons(context.Background(), store, src, nil)
	if err != nil || n != 2 {
		t.Fatalf("first upload: %d, %v", n, err)
	}
	if got, _ := os.ReadFile(filepath.Join(store.Dir, "icons", a)); string(got) != a {
		t.Errorf("stored icon = %q", got)
	}
	// Already stored: nothing to do.
	if n, err := Icons(context.Background(), store, src, nil); err != nil || n != 0 {
		t.Errorf("second upload: %d, %v", n, err)
	}
	if n, err := Icons(context.Background(), store, filepath.Join(src, "missing"), nil); err != nil || n != 0 {
		t.Errorf("missing dir: %d, %v", n, err)
	}
}

// flakyStore fails every other Put, as a dropped connection would.
type flakyStore struct {
	DirStore
	mu    sync.Mutex
	calls int
}

func (s *flakyStore) Put(ctx context.Context, key string, body io.Reader, size int64, ct string) error {
	s.mu.Lock()
	s.calls++
	fail := s.calls%2 == 1
	s.mu.Unlock()
	if fail {
		return errors.New("connection reset by peer")
	}
	return s.DirStore.Put(ctx, key, body, size, ct)
}

func TestIconsRetry(t *testing.T) {
	src := t.TempDir()
	for _, c := range "abc" {
		os.WriteFile(filepath.Join(src, strings.Repeat(string(c), 64)+".png"), []byte("x"), 0o644)
	}
	store := &flakyStore{DirStore: DirStore{Dir: t.TempDir()}}
	n, err := Icons(context.Background(), store, src, nil)
	if err != nil || n != 3 {
		t.Fatalf("uploaded %d, err %v; want all 3 after retries", n, err)
	}
}
