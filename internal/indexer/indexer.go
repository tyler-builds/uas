// Package indexer runs the fetch, merge and write steps that produce a catalog.
package indexer

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
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
	// Sources are independent, so they are fetched at the same time; the slow ones (GitHub, COPR) then
	// overlap instead of adding up.
	type result struct {
		listings []catalog.Listing
		status   catalog.SourceStatus
		err      error
	}
	results := make([]result, len(fetchers))
	var wg sync.WaitGroup
	for i, f := range fetchers {
		wg.Go(func() {
			start := time.Now()
			ls, err := f.Fetch(ctx)
			results[i] = result{listings: ls, err: err,
				status: catalog.SourceStatus{Source: f.Source(), Listings: len(ls), FetchedAt: time.Now().UTC()}}
			if err == nil {
				log.Info("fetched", "source", f.Source(), "listings", len(ls),
					"took", time.Since(start).Round(time.Millisecond))
			}
		})
	}
	wg.Wait()
	if ctx.Err() != nil {
		return info, ctx.Err()
	}
	var all []catalog.Listing
	for _, r := range results {
		st := r.status
		if r.err != nil {
			st.Error, st.Listings = r.err.Error(), 0
			log.Error("fetch failed", "source", st.Source, "err", r.err)
		} else {
			all = append(all, r.listings...)
		}
		info.Sources = append(info.Sources, st)
	}
	if len(all) == 0 {
		return info, errors.New("no listings fetched from any source")
	}

	apps, hidden := merge.Merge(all)
	for _, h := range hidden {
		log.Debug("hidden as an unofficial copy", "source", h.Source, "ref", h.Ref, "of", h.Of, "why", h.Reason)
	}
	if len(hidden) > 0 {
		log.Info("unofficial copies hidden", "listings", len(hidden))
	}
	info.Apps = len(apps)
	if err := catalog.Write(ctx, out, apps, info); err != nil {
		return info, fmt.Errorf("write catalog: %w", err)
	}
	return info, nil
}
