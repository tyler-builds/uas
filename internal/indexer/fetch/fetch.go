// Package fetch defines source fetchers and the HTTP download cache they share.
package fetch

import (
	"bufio"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/klauspost/compress/zstd"
	"github.com/ulikunitz/xz"

	"github.com/tyler-builds/uas/internal/catalog"
)

// Fetcher gets every listing one source offers.
type Fetcher interface {
	Source() catalog.SourceID
	Fetch(ctx context.Context) ([]catalog.Listing, error)
}

// UserAgent is sent with every request the indexer makes.
const UserAgent = "uas-indexer"

// MaxDownload caps a single download, so a misbehaving server cannot fill the disk.
const MaxDownload = 1 << 30

type cacheMeta struct {
	URL          string `json:"url"`
	ETag         string `json:"etag,omitempty"`
	LastModified string `json:"last_modified,omitempty"`
}

var unsafeChars = regexp.MustCompile(`[^A-Za-z0-9._-]`)

// Get downloads rawURL into cacheDir and returns the local file path. If a cached copy exists, it sends a
// conditional request and reuses the copy when the server says it has not changed.
func Get(ctx context.Context, client *http.Client, rawURL, cacheDir string) (string, error) {
	u, err := url.Parse(rawURL)
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(cacheDir, 0o755); err != nil {
		return "", err
	}
	sum := sha256.Sum256([]byte(rawURL))
	name := hex.EncodeToString(sum[:8]) + "-" + unsafeChars.ReplaceAllString(path.Base(u.Path), "_")
	dataPath := filepath.Join(cacheDir, name)
	metaPath := dataPath + ".meta.json"

	var meta cacheMeta
	if _, err := os.Stat(dataPath); err == nil {
		if b, err := os.ReadFile(metaPath); err == nil {
			_ = json.Unmarshal(b, &meta)
		}
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("User-Agent", UserAgent)
	if meta.ETag != "" {
		req.Header.Set("If-None-Match", meta.ETag)
	}
	if meta.LastModified != "" {
		req.Header.Set("If-Modified-Since", meta.LastModified)
	}
	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	switch resp.StatusCode {
	case http.StatusNotModified:
		// Mark the copy as used, so PruneCache keeps it.
		now := time.Now()
		_ = os.Chtimes(dataPath, now, now)
		return dataPath, nil
	case http.StatusOK:
	default:
		return "", fmt.Errorf("GET %s: %s", rawURL, resp.Status)
	}

	tmp, err := os.CreateTemp(cacheDir, name+".*.part")
	if err != nil {
		return "", err
	}
	defer os.Remove(tmp.Name())
	n, err := io.Copy(tmp, io.LimitReader(resp.Body, MaxDownload+1))
	if cerr := tmp.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return "", fmt.Errorf("GET %s: %w", rawURL, err)
	}
	if n > MaxDownload {
		return "", fmt.Errorf("GET %s: larger than %d bytes", rawURL, MaxDownload)
	}
	if err := os.Rename(tmp.Name(), dataPath); err != nil {
		return "", err
	}

	meta = cacheMeta{URL: rawURL, ETag: resp.Header.Get("ETag"), LastModified: resp.Header.Get("Last-Modified")}
	b, err := json.Marshal(meta)
	if err != nil {
		return "", err
	}
	if err := os.WriteFile(metaPath, b, 0o644); err != nil {
		return "", err
	}
	return dataPath, nil
}

// PruneCache deletes cached downloads (and their metadata) that no run has used for maxAge. Sources such as
// Fedora name files by content hash, so without pruning the cache grows with every update.
func PruneCache(dir string, maxAge time.Duration) (int, error) {
	cutoff := time.Now().Add(-maxAge)
	removed := 0
	err := filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			if errors.Is(err, fs.ErrNotExist) {
				return nil
			}
			return err
		}
		if d.IsDir() || strings.HasSuffix(p, ".meta.json") {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		if info.ModTime().Before(cutoff) {
			if err := os.Remove(p); err != nil {
				return err
			}
			os.Remove(p + ".meta.json")
			removed++
		}
		return nil
	})
	return removed, err
}

// OpenDecompressed opens a file and transparently decompresses it if it is gzip-, zstd- or xz-compressed.
func OpenDecompressed(name string) (io.ReadCloser, error) {
	f, err := os.Open(name)
	if err != nil {
		return nil, err
	}
	br := bufio.NewReader(f)
	magic, err := br.Peek(6)
	if err != nil && err != io.EOF {
		f.Close()
		return nil, err
	}
	switch {
	case len(magic) >= 2 && magic[0] == 0x1f && magic[1] == 0x8b:
		zr, err := gzip.NewReader(br)
		if err != nil {
			f.Close()
			return nil, err
		}
		return readCloser{Reader: zr, close: func() error { zr.Close(); return f.Close() }}, nil
	case len(magic) >= 6 && bytes.Equal(magic[:6], []byte{0xfd, '7', 'z', 'X', 'Z', 0}):
		zr, err := xz.NewReader(br)
		if err != nil {
			f.Close()
			return nil, err
		}
		return readCloser{Reader: zr, close: f.Close}, nil
	case len(magic) >= 4 && bytes.Equal(magic[:4], []byte{0x28, 0xb5, 0x2f, 0xfd}):
		zr, err := zstd.NewReader(br, zstd.WithDecoderMaxMemory(256<<20))
		if err != nil {
			f.Close()
			return nil, err
		}
		return readCloser{Reader: zr, close: func() error { zr.Close(); return f.Close() }}, nil
	}
	return readCloser{Reader: br, close: f.Close}, nil
}

type readCloser struct {
	io.Reader
	close func() error
}

func (r readCloser) Close() error { return r.close() }
