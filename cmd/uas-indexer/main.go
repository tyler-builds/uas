// Command uas-indexer builds the uas catalog from every source.
package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"time"

	"github.com/tyler-builds/uas/internal/indexer"
	"github.com/tyler-builds/uas/internal/indexer/fetch"
	"github.com/tyler-builds/uas/internal/indexer/fetch/flathub"
)

func main() {
	out := flag.String("out", "", "path of the catalog database to write (required)")
	cache := flag.String("cache", defaultCacheDir(), "directory for downloaded source data")
	arch := flag.String("arch", "x86_64", "CPU architecture to index: x86_64 or aarch64")
	flathubFile := flag.String("flathub-file", "", "read Flathub AppStream XML from this file instead of downloading it")
	flag.Parse()
	if *out == "" || flag.NArg() > 0 {
		fmt.Fprintln(os.Stderr, "usage: uas-indexer -out catalog.db [flags]")
		flag.PrintDefaults()
		os.Exit(2)
	}

	log := slog.New(slog.NewTextHandler(os.Stderr, nil))
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	client := &http.Client{Timeout: 10 * time.Minute}
	fetchers := []fetch.Fetcher{
		&flathub.Fetcher{Client: client, CacheDir: filepath.Join(*cache, "flathub"), Arch: *arch, File: *flathubFile},
	}

	start := time.Now()
	info, err := indexer.Build(ctx, fetchers, *out, log)
	if err != nil {
		log.Error("build failed", "err", err)
		os.Exit(1)
	}
	log.Info("catalog written", "path", *out, "apps", info.Apps, "took", time.Since(start).Round(time.Millisecond))
}

func defaultCacheDir() string {
	dir, err := os.UserCacheDir()
	if err != nil {
		return filepath.Join(os.TempDir(), "uas-indexer")
	}
	return filepath.Join(dir, "uas-indexer")
}
