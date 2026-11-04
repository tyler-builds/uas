// Command uas-indexer builds the uas catalog from every source.
package main

import (
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/tyler-builds/uas/internal/indexer"
	"github.com/tyler-builds/uas/internal/indexer/fetch"
	"github.com/tyler-builds/uas/internal/indexer/fetch/fedora"
	"github.com/tyler-builds/uas/internal/indexer/fetch/flathub"
	"github.com/tyler-builds/uas/internal/indexer/fetch/github"
	"github.com/tyler-builds/uas/internal/indexer/fetch/homebrew"
	"github.com/tyler-builds/uas/internal/indexer/publish"
	"github.com/tyler-builds/uas/internal/manifest"
)

func main() {
	out := flag.String("out", "", "path of the catalog database to write (required)")
	cache := flag.String("cache", defaultCacheDir(), "directory for downloaded source data")
	arch := flag.String("arch", "x86_64", "CPU architecture to index: x86_64 or aarch64")
	flathubFile := flag.String("flathub-file", "", "read Flathub AppStream XML from this file instead of downloading it")
	publishURL := flag.String("publish", "", "after building, publish to the catalog Worker at this URL\n"+
		"(needs $UAS_SIGNING_KEY and $UAS_UPLOAD_TOKEN)")
	publishDir := flag.String("publish-dir", "", "after building, publish into this local directory (needs $UAS_SIGNING_KEY)")
	keep := flag.Int("keep", 3, "number of published catalogs to keep")
	githubCacheFor := flag.Duration("github-cache-for", 20*time.Hour, "reuse GitHub results younger than this")
	fedoraReleases := flag.String("fedora-releases", "", "comma-separated Fedora releases to index (default: the two newest)")
	sources := flag.String("sources", "flathub,fedora,homebrew,github", "comma-separated sources to index\n"+
		"(github needs $GITHUB_TOKEN)")
	keygen := flag.String("keygen", "", "write a new signing key to this file, print its public key, and exit")
	flag.Parse()
	if *keygen != "" {
		if err := generateKey(*keygen); err != nil {
			fmt.Fprintln(os.Stderr, "uas-indexer:", err)
			os.Exit(1)
		}
		return
	}
	if *out == "" || flag.NArg() > 0 {
		fmt.Fprintln(os.Stderr, "usage: uas-indexer -out catalog.db [flags]")
		flag.PrintDefaults()
		os.Exit(2)
	}
	var store publish.Store
	switch {
	case *publishURL != "" && *publishDir != "":
		fmt.Fprintln(os.Stderr, "uas-indexer: use -publish or -publish-dir, not both")
		os.Exit(2)
	case *publishURL != "":
		token := os.Getenv("UAS_UPLOAD_TOKEN")
		if token == "" {
			fmt.Fprintln(os.Stderr, "uas-indexer: -publish needs $UAS_UPLOAD_TOKEN")
			os.Exit(2)
		}
		store = &publish.HTTPStore{BaseURL: *publishURL, Token: token, Client: &http.Client{Timeout: 10 * time.Minute}}
	case *publishDir != "":
		store = &publish.DirStore{Dir: *publishDir}
	}
	var signingKey ed25519.PrivateKey
	if store != nil {
		var err error
		if signingKey, err = manifest.ParsePrivateKey(os.Getenv("UAS_SIGNING_KEY")); err != nil {
			fmt.Fprintln(os.Stderr, "uas-indexer: $UAS_SIGNING_KEY:", err)
			os.Exit(2)
		}
	}

	log := slog.New(slog.NewTextHandler(os.Stderr, nil))
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	releases, err := parseInts(*fedoraReleases)
	if err != nil {
		fmt.Fprintln(os.Stderr, "uas-indexer: -fedora-releases:", err)
		os.Exit(2)
	}
	client := &http.Client{Timeout: 10 * time.Minute}
	available := map[string]fetch.Fetcher{
		"flathub": &flathub.Fetcher{Client: client, CacheDir: filepath.Join(*cache, "flathub"), Arch: *arch,
			File: *flathubFile},
		"fedora": &fedora.Fetcher{Client: client, CacheDir: filepath.Join(*cache, "fedora"), Arch: *arch,
			Releases: releases, Log: log},
		"homebrew": &homebrew.Fetcher{Client: client, CacheDir: filepath.Join(*cache, "homebrew"), Arch: *arch},
		"github": &github.Fetcher{Client: client, Token: os.Getenv("GITHUB_TOKEN"), Arch: *arch, Log: log,
			CacheDir: filepath.Join(*cache, "github"), CacheFor: *githubCacheFor},
	}
	var fetchers []fetch.Fetcher
	for _, name := range strings.Split(*sources, ",") {
		f, ok := available[strings.TrimSpace(name)]
		if !ok {
			fmt.Fprintf(os.Stderr, "uas-indexer: unknown source %q\n", name)
			os.Exit(2)
		}
		fetchers = append(fetchers, f)
	}

	start := time.Now()
	info, err := indexer.Build(ctx, fetchers, *out, log)
	if err != nil {
		log.Error("build failed", "err", err)
		os.Exit(1)
	}
	log.Info("catalog written", "path", *out, "apps", info.Apps, "took", time.Since(start).Round(time.Millisecond))
	if n, err := fetch.PruneCache(*cache, 3*24*time.Hour); err != nil {
		log.Warn("pruning the download cache failed", "err", err)
	} else if n > 0 {
		log.Info("pruned download cache", "files", n)
	}

	if store != nil {
		m, err := publish.Publish(ctx, store, *out, info, publish.Options{Key: signingKey, Keep: *keep, Log: log})
		if err != nil {
			log.Error("publish failed", "err", err)
			os.Exit(1)
		}
		log.Info("published", "catalog", m.Catalog.Path, "size", m.Catalog.Size,
			"uncompressed", m.Catalog.UncompressedSize)
	}
}

// generateKey writes a new signing key to path, refusing to overwrite one, and prints the public key for
// embedding in the client.
func generateKey(path string) error {
	private, public, err := manifest.GenerateKey()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	if _, err := f.WriteString(private + "\n"); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	fmt.Printf("private key written to %s\npublic key: %s\nkey id:     %s\n", path,
		base64.StdEncoding.EncodeToString(public), manifest.KeyID(public))
	return nil
}

func parseInts(s string) ([]int, error) {
	var out []int
	for _, f := range strings.Split(s, ",") {
		if f = strings.TrimSpace(f); f == "" {
			continue
		}
		n, err := strconv.Atoi(f)
		if err != nil {
			return nil, err
		}
		out = append(out, n)
	}
	return out, nil
}

func defaultCacheDir() string {
	dir, err := os.UserCacheDir()
	if err != nil {
		return filepath.Join(os.TempDir(), "uas-indexer")
	}
	return filepath.Join(dir, "uas-indexer")
}
