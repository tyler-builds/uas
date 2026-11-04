package fetch

import (
	"context"
	"encoding/json"
	"log/slog"
	"os"
	"path/filepath"
	"time"

	"github.com/tyler-builds/uas/internal/catalog"
)

// Cached wraps a slow fetcher (GitHub, COPR) and reuses its last result while it is younger than For. Key
// describes the fetcher's settings; a result saved under a different key is not reused.
type Cached struct {
	Fetcher
	Dir string
	For time.Duration
	Key string
	Log *slog.Logger
	Now func() time.Time
}

type cachedResult struct {
	Key      string            `json:"key"`
	Saved    time.Time         `json:"saved"`
	Listings []catalog.Listing `json:"listings"`
}

func (c *Cached) path() string {
	return filepath.Join(c.Dir, string(c.Fetcher.Source())+"-listings.json")
}

func (c *Cached) now() time.Time {
	if c.Now != nil {
		return c.Now()
	}
	return time.Now()
}

// Fetch implements Fetcher.
func (c *Cached) Fetch(ctx context.Context) ([]catalog.Listing, error) {
	if b, err := os.ReadFile(c.path()); err == nil {
		var r cachedResult
		if json.Unmarshal(b, &r) == nil && r.Key == c.Key && c.now().Sub(r.Saved) <= c.For {
			if c.Log != nil {
				c.Log.Info("reusing cached results", "source", c.Fetcher.Source(), "saved", r.Saved,
					"listings", len(r.Listings))
			}
			return r.Listings, nil
		}
	}
	ls, err := c.Fetcher.Fetch(ctx)
	if err != nil {
		return nil, err
	}
	if err := c.save(ls); err != nil && c.Log != nil {
		c.Log.Warn("saving results failed", "source", c.Fetcher.Source(), "err", err)
	}
	return ls, nil
}

func (c *Cached) save(ls []catalog.Listing) error {
	b, err := json.Marshal(cachedResult{Key: c.Key, Saved: c.now(), Listings: ls})
	if err != nil {
		return err
	}
	if err := os.MkdirAll(c.Dir, 0o755); err != nil {
		return err
	}
	tmp := c.path() + ".tmp"
	if err := os.WriteFile(tmp, b, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, c.path())
}
