package publish

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"regexp"
	"sync"
	"time"
)

// iconName is the file name of an icon: the SHA-256 of its contents and its format.
var iconName = regexp.MustCompile(`^[0-9a-f]{64}\.(png|svg)$`)

// iconWorkers is how many icons upload at once.
const iconWorkers = 8

// Icons uploads the icons in dir that the store doesn't have yet. Icons are named by their hash, so one that
// is stored never changes, and the catalog can point at it before or after this runs. Each upload is
// retried; an icon that still fails is left for the next run (the app shows a placeholder meanwhile), and
// only if every upload fails is that an error.
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
	var todo []string
	for _, e := range entries {
		if name := e.Name(); iconName.MatchString(name) && !have["icons/"+name] {
			todo = append(todo, name)
		}
	}
	var (
		mu       sync.Mutex
		uploaded int
		failed   []error
		wg       sync.WaitGroup
	)
	work := make(chan string)
	for range iconWorkers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for name := range work {
				err := putIconRetrying(ctx, store, filepath.Join(dir, name), name)
				mu.Lock()
				if err != nil {
					failed = append(failed, err)
				} else {
					uploaded++
				}
				mu.Unlock()
			}
		}()
	}
	for _, name := range todo {
		work <- name
	}
	close(work)
	wg.Wait()
	if log != nil && len(todo) > 0 {
		log.Info("icons uploaded", "new", uploaded, "failed", len(failed), "stored", len(stored))
		if len(failed) > 0 {
			log.Warn("some icons will be uploaded next run", "first_error", failed[0])
		}
	}
	if len(failed) > 0 && uploaded == 0 {
		return 0, errors.Join(failed[:min(3, len(failed))]...)
	}
	return uploaded, nil
}

// putIconRetrying uploads an icon, trying again after a short wait when the connection fails.
func putIconRetrying(ctx context.Context, store Store, path, name string) error {
	var err error
	for attempt := range 4 {
		if attempt > 0 {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(time.Duration(attempt) * 2 * time.Second):
			}
		}
		if err = putIcon(ctx, store, path, name); err == nil {
			return nil
		}
	}
	return err
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
