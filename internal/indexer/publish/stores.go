package publish

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/tyler-builds/uas/internal/indexer/fetch"
)

// HTTPStore publishes through the uas catalog Worker (deploy/cloudflare), which stores files in R2.
type HTTPStore struct {
	BaseURL string // for example "https://uas-catalog.example.workers.dev"
	Token   string // the Worker's UPLOAD_TOKEN secret
	Client  *http.Client
}

func (s *HTTPStore) do(ctx context.Context, method, key string, body io.Reader, size int64,
	contentType string) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, method, strings.TrimSuffix(s.BaseURL, "/")+"/"+key, body)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+s.Token)
	req.Header.Set("User-Agent", fetch.UserAgent)
	if body != nil {
		req.ContentLength = size
		req.Header.Set("Content-Type", contentType)
	}
	resp, err := s.Client.Do(req)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode/100 != 2 {
		msg, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		resp.Body.Close()
		return nil, fmt.Errorf("%s %s: %s: %s", method, key, resp.Status, strings.TrimSpace(string(msg)))
	}
	return resp, nil
}

// Put implements Store.
func (s *HTTPStore) Put(ctx context.Context, key string, body io.Reader, size int64, contentType string) error {
	resp, err := s.do(ctx, http.MethodPut, key, body, size, contentType)
	if err != nil {
		return err
	}
	return resp.Body.Close()
}

// ListCatalogs implements Store.
func (s *HTTPStore) ListCatalogs(ctx context.Context) ([]Object, error) {
	resp, err := s.do(ctx, http.MethodGet, "_catalogs", nil, 0, "")
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	var objs []Object
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&objs); err != nil {
		return nil, fmt.Errorf("list catalogs: %w", err)
	}
	return objs, nil
}

// Delete implements Store.
func (s *HTTPStore) Delete(ctx context.Context, key string) error {
	resp, err := s.do(ctx, http.MethodDelete, key, nil, 0, "")
	if err != nil {
		return err
	}
	return resp.Body.Close()
}

// DirStore publishes into a local directory, for tests and for serving a catalog locally.
type DirStore struct {
	Dir string
}

// Put implements Store.
func (s *DirStore) Put(_ context.Context, key string, body io.Reader, _ int64, _ string) error {
	if err := os.MkdirAll(s.Dir, 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(s.Dir, ".upload-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := io.Copy(tmp, body); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), filepath.Join(s.Dir, filepath.Base(key)))
}

// ListCatalogs implements Store.
func (s *DirStore) ListCatalogs(context.Context) ([]Object, error) {
	matches, err := filepath.Glob(filepath.Join(s.Dir, "catalog-*.db.zst"))
	if err != nil {
		return nil, err
	}
	var objs []Object
	for _, m := range matches {
		st, err := os.Stat(m)
		if err != nil {
			return nil, err
		}
		objs = append(objs, Object{Key: filepath.Base(m), Size: st.Size(), Uploaded: st.ModTime()})
	}
	return objs, nil
}

// Delete implements Store.
func (s *DirStore) Delete(_ context.Context, key string) error {
	return os.Remove(filepath.Join(s.Dir, filepath.Base(key)))
}
