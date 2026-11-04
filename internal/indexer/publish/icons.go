package publish

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"regexp"
)

// iconName is the file name of an icon: the SHA-256 of its contents and its format.
var iconName = regexp.MustCompile(`^[0-9a-f]{64}\.(png|svg)$`)

// Icons uploads the icons in dir that the store doesn't have yet. Icons are named by their hash, so one that
// is stored never changes, and the catalog can point at it before or after this runs.
func Icons(ctx context.Context, store Store, dir string, log *slog.Logger) (int, error) {
	entries, err := os.ReadDir(dir)
	if os.IsNotExist(err) {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	stored, err := store.ListIcons(ctx)
	if err != nil {
		return 0, err
	}
	have := map[string]bool{}
	for _, k := range stored {
		have[k] = true
	}
	uploaded := 0
	for _, e := range entries {
		name := e.Name()
		if !iconName.MatchString(name) || have["icons/"+name] {
			continue
		}
		if err := putIcon(ctx, store, filepath.Join(dir, name), name); err != nil {
			return uploaded, err
		}
		uploaded++
	}
	if log != nil && uploaded > 0 {
		log.Info("icons uploaded", "new", uploaded, "stored", len(stored))
	}
	return uploaded, nil
}

func putIcon(ctx context.Context, store Store, path, name string) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return err
	}
	contentType := "image/png"
	if filepath.Ext(name) == ".svg" {
		contentType = "image/svg+xml"
	}
	if err := store.Put(ctx, "icons/"+name, f, st.Size(), contentType); err != nil {
		return fmt.Errorf("upload icon: %w", err)
	}
	return nil
}
