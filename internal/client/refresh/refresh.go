// Package refresh downloads the latest published catalog and installs it locally after checking it.
package refresh

import (
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/klauspost/compress/zstd"

	"github.com/tyler-builds/uas/internal/catalog"
	"github.com/tyler-builds/uas/internal/manifest"
)

// BaseURLEnv overrides where catalogs are downloaded from.
const BaseURLEnv = "UAS_CATALOG_URL"

// Limits on what a client will download, whatever a manifest says.
const (
	maxManifest = 1 << 20
	maxCatalog  = 512 << 20
	maxDB       = 2 << 30
)

// Options control a refresh.
type Options struct {
	BaseURL     string
	TrustedKeys []ed25519.PublicKey
	// CatalogPath is where the catalog database lives. The verified manifest is kept next to it as
	// manifest.json.
	CatalogPath string
	Client      *http.Client
	UserAgent   string
	// Edition is the catalog edition to download (platform.Info.Edition); the manifest's fallback when empty.
	Edition string
}

// Result reports what a refresh did.
type Result struct {
	Updated  bool
	Manifest manifest.Manifest
}

// ManifestPath returns where the local copy of the manifest for catalogPath is kept.
func ManifestPath(catalogPath string) string {
	return filepath.Join(filepath.Dir(catalogPath), manifest.Name)
}

// Refresh fetches the published manifest and, if it names a different catalog than the local one, downloads,
// verifies and installs that catalog.
//
// It refuses a manifest older than the local one, so a stale or replayed manifest cannot roll the catalog back.
func Refresh(ctx context.Context, opt Options) (Result, error) {
	base := strings.TrimSuffix(opt.BaseURL, "/")
	raw, err := get(ctx, opt, base+"/"+manifest.Name, maxManifest)
	if err != nil {
		return Result{}, err
	}
	m, err := manifest.Verify(raw, opt.TrustedKeys)
	if err != nil {
		return Result{}, err
	}
	if m.SchemaVersion != catalog.SchemaVersion {
		return Result{}, fmt.Errorf("the published catalog uses format %d, but this uas reads format %d; update uas",
			m.SchemaVersion, catalog.SchemaVersion)
	}

	file := m.For(opt.Edition)
	manifestPath := ManifestPath(opt.CatalogPath)
	if local, err := loadLocal(manifestPath, opt.TrustedKeys); err == nil {
		if installedHash(opt.CatalogPath, local) == file.SHA256 && fileExists(opt.CatalogPath) {
			return Result{Manifest: m}, nil
		}
		if m.BuiltAt.Before(local.BuiltAt) {
			return Result{}, fmt.Errorf("the server offered a catalog from %s, older than the local one from %s; "+
				"keeping the local one", m.BuiltAt.Format("2006-01-02 15:04"), local.BuiltAt.Format("2006-01-02 15:04"))
		}
	}
	if file.Size > maxCatalog || file.UncompressedSize > maxDB {
		return Result{}, errors.New("the published catalog is larger than uas allows")
	}

	if err := os.MkdirAll(filepath.Dir(opt.CatalogPath), 0o755); err != nil {
		return Result{}, err
	}
	tmp := opt.CatalogPath + ".download"
	defer os.Remove(tmp)
	if err := download(ctx, opt, base+"/"+file.Path, file, tmp); err != nil {
		return Result{}, err
	}
	// Open the new catalog before switching to it, to catch a broken file early.
	db, err := catalog.Open(tmp)
	if err != nil {
		return Result{}, fmt.Errorf("downloaded catalog: %w", err)
	}
	db.Close()

	if err := os.Rename(tmp, opt.CatalogPath); err != nil {
		return Result{}, err
	}
	if err := writeFile(manifestPath, raw); err != nil {
		return Result{}, err
	}
	if err := writeFile(hashPath(opt.CatalogPath), []byte(file.SHA256+"\n")); err != nil {
		return Result{}, err
	}
	return Result{Updated: true, Manifest: m}, nil
}

// hashPath is where the hash of the installed catalog file is kept, since the manifest names one per edition.
func hashPath(catalogPath string) string { return catalogPath + ".sha256" }

// installedHash returns the hash of the catalog file installed at catalogPath: the one recorded beside it, or
// for a catalog installed before editions, the manifest's single catalog.
func installedHash(catalogPath string, local manifest.Manifest) string {
	if b, err := os.ReadFile(hashPath(catalogPath)); err == nil {
		return strings.TrimSpace(string(b))
	}
	return local.Catalog.SHA256
}

// LocalManifest returns the verified manifest of the installed catalog.
func LocalManifest(catalogPath string, trusted []ed25519.PublicKey) (manifest.Manifest, error) {
	return loadLocal(ManifestPath(catalogPath), trusted)
}

func loadLocal(path string, trusted []ed25519.PublicKey) (manifest.Manifest, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return manifest.Manifest{}, err
	}
	return manifest.Verify(b, trusted)
}

func get(ctx context.Context, opt Options, url string, limit int64) ([]byte, error) {
	resp, err := request(ctx, opt, url)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(io.LimitReader(resp.Body, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(b)) > limit {
		return nil, fmt.Errorf("GET %s: response too large", url)
	}
	return b, nil
}

func request(ctx context.Context, opt Options, url string) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	if opt.UserAgent != "" {
		req.Header.Set("User-Agent", opt.UserAgent)
	}
	resp, err := opt.Client.Do(req)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		resp.Body.Close()
		return nil, fmt.Errorf("GET %s: %s", url, resp.Status)
	}
	return resp, nil
}

// download fetches the compressed catalog, checks its size and hash, and writes the decompressed database to
// dst. Nothing is decompressed until the hash matches the signed manifest.
func download(ctx context.Context, opt Options, url string, f manifest.File, dst string) error {
	resp, err := request(ctx, opt, url)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	z, err := os.CreateTemp(filepath.Dir(dst), ".catalog-*.zst")
	if err != nil {
		return err
	}
	defer os.Remove(z.Name())
	defer z.Close()
	h := sha256.New()
	n, err := io.Copy(io.MultiWriter(z, h), io.LimitReader(resp.Body, f.Size+1))
	if err != nil {
		return fmt.Errorf("download catalog: %w", err)
	}
	if n != f.Size {
		return fmt.Errorf("download catalog: got %d bytes, expected %d", n, f.Size)
	}
	if got := hex.EncodeToString(h.Sum(nil)); got != f.SHA256 {
		return errors.New("download catalog: hash does not match the signed manifest")
	}

	if _, err := z.Seek(0, io.SeekStart); err != nil {
		return err
	}
	zr, err := zstd.NewReader(z, zstd.WithDecoderMaxMemory(256<<20))
	if err != nil {
		return err
	}
	defer zr.Close()
	out, err := os.Create(dst)
	if err != nil {
		return err
	}
	n, err = io.Copy(out, io.LimitReader(zr, f.UncompressedSize+1))
	if cerr := out.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return fmt.Errorf("decompress catalog: %w", err)
	}
	if n != f.UncompressedSize {
		return fmt.Errorf("decompress catalog: got %d bytes, expected %d", n, f.UncompressedSize)
	}
	return nil
}

func writeFile(path string, b []byte) error {
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}
