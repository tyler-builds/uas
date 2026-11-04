// Package indexer runs the fetch, merge and write steps that produce a catalog.
package indexer

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/tyler-builds/uas/internal/catalog"
	"github.com/tyler-builds/uas/internal/indexer/fetch"
	"github.com/tyler-builds/uas/internal/indexer/merge"
)

// Build fetches every source, merges the listings into apps and writes the catalog to out.
//
// A source that fails is recorded in the build info and skipped, so one broken source does not block the
// others. Build fails only if no source returned anything.
func Build(ctx context.Context, fetchers []fetch.Fetcher, out string, log *slog.Logger) (catalog.BuildInfo, error) {
	info := catalog.BuildInfo{BuiltAt: time.Now().UTC()}
	var all []catalog.Listing
	for _, f := range fetchers {
		start := time.Now()
		ls, err := f.Fetch(ctx)
		st := catalog.SourceStatus{Source: f.Source(), Listings: len(ls), FetchedAt: time.Now().UTC()}
		if err != nil {
			if ctx.Err() != nil {
				return info, ctx.Err()
			}
			st.Error, st.Listings = err.Error(), 0
			log.Error("fetch failed", "source", f.Source(), "err", err)
		} else {
			log.Info("fetched", "source", f.Source(), "listings", len(ls),
				"took", time.Since(start).Round(time.Millisecond))
			all = append(all, ls...)
		}
		info.Sources = append(info.Sources, st)
	}
	if len(all) == 0 {
		return info, errors.New("no listings fetched from any source")
	}

	apps := merge.Merge(all)
	info.Apps = len(apps)
	if err := catalog.Write(ctx, out, apps, info); err != nil {
		return info, fmt.Errorf("write catalog: %w", err)
	}
	return info, nil
}
