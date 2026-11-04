package publish

import (
	"context"
	"os"
	"path/filepath"
	"strings"
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
