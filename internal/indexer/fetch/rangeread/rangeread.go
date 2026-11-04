// Package rangeread reads a remote file a few chunks at a time, with HTTP range requests.
//
// The indexer uses it to look inside AppImages on GitHub: their icon, desktop entry and metainfo take a
// handful of small reads, so there is no need to download the whole, often 100 MB, file.
package rangeread

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sync"

	"github.com/tyler-builds/uas/internal/indexer/fetch"
)

// ChunkSize is how much each request reads.
const ChunkSize = 64 << 10

// ErrBudget is returned once a reader has fetched more than its budget.
var ErrBudget = errors.New("rangeread: read budget used up")

// Reader is an io.ReaderAt over a remote file. Chunks it has read are kept, so reads that overlap are free.
type Reader struct {
	ctx    context.Context
	client *http.Client
	url    string
	size   int64
	budget int64 // bytes it may still fetch

	mu      sync.Mutex
	chunks  map[int64][]byte
	Fetched int64 // bytes fetched so far
}

// New returns a reader for the file at url, which is size bytes long. It fetches at most budget bytes, so a
// file laid out unusually can't make it download everything.
func New(ctx context.Context, client *http.Client, url string, size, budget int64) *Reader {
	return &Reader{ctx: ctx, client: client, url: url, size: size, budget: budget, chunks: map[int64][]byte{}}
}

// ReadAt implements io.ReaderAt.
func (r *Reader) ReadAt(p []byte, off int64) (int, error) {
	if off < 0 {
		return 0, errors.New("rangeread: negative offset")
	}
	if off >= r.size {
		return 0, io.EOF
	}
	n := 0
	for n < len(p) && off+int64(n) < r.size {
		pos := off + int64(n)
		index := pos / ChunkSize
		chunk, err := r.chunk(index)
		if err != nil {
			return n, err
		}
		n += copy(p[n:], chunk[pos-index*ChunkSize:])
	}
	if n < len(p) {
		return n, io.EOF
	}
	return n, nil
}

// chunk returns chunk i, fetching it if needed.
func (r *Reader) chunk(i int64) ([]byte, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if c, ok := r.chunks[i]; ok {
		return c, nil
	}
	start := i * ChunkSize
	end := min(start+ChunkSize, r.size) - 1
	if r.Fetched+end-start+1 > r.budget {
		return nil, ErrBudget
	}
	req, err := http.NewRequestWithContext(r.ctx, http.MethodGet, r.url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Range", fmt.Sprintf("bytes=%d-%d", start, end))
	req.Header.Set("User-Agent", fetch.UserAgent)
	resp, err := r.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusPartialContent {
		return nil, fmt.Errorf("rangeread: %s for a range request", resp.Status)
	}
	c, err := io.ReadAll(io.LimitReader(resp.Body, end-start+2))
	if err != nil {
		return nil, err
	}
	if int64(len(c)) != end-start+1 {
		return nil, fmt.Errorf("rangeread: got %d bytes for a %d-byte range", len(c), end-start+1)
	}
	r.Fetched += int64(len(c))
	r.chunks[i] = c
	return c, nil
}
