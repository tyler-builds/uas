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
	r, err := Fetch(ctx, fetchers, log)
	if err != nil {
		return catalog.BuildInfo{}, err
	}
	return r.Write(ctx, out, func(catalog.Listing) bool { return true }, log)
}

// Fetched is every listing of one indexer run, with how each source went.
type Fetched struct {
	BuiltAt  time.Time
	Listings []catalog.Listing
	Sources  []catalog.SourceStatus
}

// Fetch runs every fetcher at once. It fails only if no source returned anything.
func Fetch(ctx context.Context, fetchers []fetch.Fetcher, log *slog.Logger) (*Fetched, error) {
	f := &Fetched{BuiltAt: time.Now().UTC()}
	// Sources are independent, so they are fetched at the same time; the slow ones (GitHub, COPR) then
	// overlap instead of adding up.
	type result struct {
		listings []catalog.Listing
		status   catalog.SourceStatus
		err      error
	}
	results := make([]result, len(fetchers))
	var wg sync.WaitGroup
	for i, fe := range fetchers {
		wg.Go(func() {
			start := time.Now()
			ls, err := fe.Fetch(ctx)
			results[i] = result{listings: ls, err: err,
				status: catalog.SourceStatus{Source: fe.Source(), Listings: len(ls), FetchedAt: time.Now().UTC()}}
			if err == nil {
				log.Info("fetched", "source", fe.Source(), "listings", len(ls),
					"took", time.Since(start).Round(time.Millisecond))
			}
		})
	}
	wg.Wait()
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	for _, r := range results {
		st := r.status
		if r.err != nil {
			st.Error, st.Listings = r.err.Error(), 0
			log.Error("fetch failed", "source", st.Source, "err", r.err)
		} else {
			f.Listings = append(f.Listings, r.listings...)
		}
		f.Sources = append(f.Sources, st)
	}
	if len(f.Listings) == 0 {
		return nil, errors.New("no listings fetched from any source")
	}
	return f, nil
}

// Write merges the listings that keep allows into apps and writes them as a catalog to out. Only the sources
// with listings kept (or that failed and would have had some) are in its build info.
func (f *Fetched) Write(ctx context.Context, out string, keep func(catalog.Listing) bool, log *slog.Logger) (
	catalog.BuildInfo, error) {
	var ls []catalog.Listing
	count := map[catalog.SourceID]int{}
	for _, l := range f.Listings {
		if keep(l) {
			ls = append(ls, l)
			count[l.Source]++
		}
	}
	info := catalog.BuildInfo{BuiltAt: f.BuiltAt}
	for _, st := range f.Sources {
		probe := catalog.Listing{Source: st.Source}
		if count[st.Source] == 0 && !(st.Error != "" && keep(probe)) {
			continue
		}
		st.Listings = count[st.Source]
		info.Sources = append(info.Sources, st)
	}
	apps, hidden := merge.Merge(ls)
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
