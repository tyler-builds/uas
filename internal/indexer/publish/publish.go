// Package publish compresses a built catalog, signs a manifest for it and uploads both.
package publish

import (
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"log/slog"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/klauspost/compress/zstd"

	"github.com/tyler-builds/uas/internal/catalog"
	"github.com/tyler-builds/uas/internal/manifest"
)

// Object is a stored file.
type Object struct {
	Key      string    `json:"key"`
	Size     int64     `json:"size"`
	Uploaded time.Time `json:"uploaded"`
}

// Store is where catalogs are published.
type Store interface {
	Put(ctx context.Context, key string, body io.Reader, size int64, contentType string) error
	// ListCatalogs returns every stored catalog file.
	ListCatalogs(ctx context.Context) ([]Object, error)
	Delete(ctx context.Context, key string) error
}

// Options control a publish.
type Options struct {
	Key ed25519.PrivateKey
	// Keep is how many catalog files to keep, counting the new one. Older ones are deleted. Clients that
	// fetched the previous manifest a moment ago can still download its catalog.
	Keep int
	Log  *slog.Logger
}

// Publish uploads the catalog at dbPath and then a signed manifest that points at it. The catalog goes up
// first, so the manifest never points at a missing file.
func Publish(ctx context.Context, store Store, dbPath string, info catalog.BuildInfo, opt Options) (manifest.Manifest, error) {
	if opt.Keep < 2 {
		opt.Keep = 2
	}
	zpath, file, err := compress(dbPath)
	if err != nil {
		return manifest.Manifest{}, fmt.Errorf("compress: %w", err)
	}
	defer os.Remove(zpath)

	m := manifest.Manifest{SchemaVersion: catalog.SchemaVersion, BuiltAt: info.BuiltAt, Apps: info.Apps,
		Sources: info.Sources, Catalog: file}
	env, err := manifest.Sign(m, opt.Key)
	if err != nil {
		return m, err
	}

	f, err := os.Open(zpath)
	if err != nil {
		return m, err
	}
	defer f.Close()
	if err := store.Put(ctx, file.Path, f, file.Size, "application/zstd"); err != nil {
		return m, fmt.Errorf("upload catalog: %w", err)
	}
	opt.Log.Info("uploaded catalog", "path", file.Path, "size", file.Size)
	if err := store.Put(ctx, manifest.Name, strings.NewReader(string(env)), int64(len(env)),
		"application/json"); err != nil {
		return m, fmt.Errorf("upload manifest: %w", err)
	}
	opt.Log.Info("uploaded manifest", "built_at", m.BuiltAt)

	if err := prune(ctx, store, file.Path, opt.Keep, opt.Log); err != nil {
		// The new catalog is live; a failed cleanup only wastes space.
		opt.Log.Warn("pruning old catalogs failed", "err", err)
	}
	return m, nil
}

// compress writes a zstd copy of dbPath to a temp file and describes it.
func compress(dbPath string) (string, manifest.File, error) {
	in, err := os.Open(dbPath)
	if err != nil {
		return "", manifest.File{}, err
	}
	defer in.Close()
	out, err := os.CreateTemp("", "uas-catalog-*.db.zst")
	if err != nil {
		return "", manifest.File{}, err
	}
	ok := false
	defer func() {
		if !ok {
			out.Close()
			os.Remove(out.Name())
		}
	}()

	h := sha256.New()
	zw, err := zstd.NewWriter(io.MultiWriter(out, h), zstd.WithEncoderLevel(zstd.SpeedBestCompression))
	if err != nil {
		return "", manifest.File{}, err
	}
	n, err := io.Copy(zw, in)
	if err != nil {
		return "", manifest.File{}, err
	}
	if err := zw.Close(); err != nil {
		return "", manifest.File{}, err
	}
	size, err := out.Seek(0, io.SeekCurrent)
	if err != nil {
		return "", manifest.File{}, err
	}
	if err := out.Close(); err != nil {
		return "", manifest.File{}, err
	}
	sum := hex.EncodeToString(h.Sum(nil))
	ok = true
	return out.Name(), manifest.File{Path: manifest.CatalogPath(sum), SHA256: sum, Size: size,
		UncompressedSize: n}, nil
}

// prune deletes all but the newest keep catalogs. The current catalog is never deleted.
func prune(ctx context.Context, store Store, current string, keep int, log *slog.Logger) error {
	objs, err := store.ListCatalogs(ctx)
	if err != nil {
		return err
	}
	sort.Slice(objs, func(i, j int) bool { return objs[i].Uploaded.After(objs[j].Uploaded) })
	kept := 1 // the current catalog
	for _, o := range objs {
		if o.Key == current {
			continue
		}
		if kept < keep {
			kept++
			continue
		}
		if err := store.Delete(ctx, o.Key); err != nil {
			return err
		}
		log.Info("deleted old catalog", "path", o.Key)
	}
	return nil
}
