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

	"github.com/tyler-builds/uas/internal/catalog"
	"github.com/tyler-builds/uas/internal/indexer"
	"github.com/tyler-builds/uas/internal/indexer/fetch"
	archlinux "github.com/tyler-builds/uas/internal/indexer/fetch/arch"
	"github.com/tyler-builds/uas/internal/indexer/fetch/copr"
	"github.com/tyler-builds/uas/internal/indexer/fetch/debian"
	"github.com/tyler-builds/uas/internal/indexer/fetch/fedora"
	"github.com/tyler-builds/uas/internal/indexer/fetch/flathub"
	"github.com/tyler-builds/uas/internal/indexer/fetch/forge"
	"github.com/tyler-builds/uas/internal/indexer/fetch/github"
	"github.com/tyler-builds/uas/internal/indexer/fetch/homebrew"
	"github.com/tyler-builds/uas/internal/indexer/fetch/snap"
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
	slowCacheFor := flag.Duration("slow-cache-for", 20*time.Hour,
		"reuse results of slow sources (GitHub, COPR) younger than this")
	fedoraReleases := flag.String("fedora-releases", "", "comma-separated Fedora releases to index (default: the two newest)")
	defaultSources := "flathub,fedora,homebrew,github,copr,codeberg,debian,ubuntu,arch,snap"
	if os.Getenv("GITLAB_TOKEN") != "" {
		defaultSources += ",gitlab"
	}
	sources := flag.String("sources", defaultSources, "comma-separated sources to index\n"+
		"(github needs $GITHUB_TOKEN; gitlab needs $GITLAB_TOKEN and is left out without it)")
	codebergMinStars := flag.Int("codeberg-min-stars", forge.DefaultCodebergMinStars,
		"check Codeberg repos with at least this many stars")
	editionsDir := flag.String("editions-dir", "", "also write one catalog per edition (kind of system) here;\n"+
		"publishing writes them next to -out when this is empty")
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
	// What GitHub apps say about themselves inside their AppImages: names, icons, screenshots. Fedora's icons
	// are kept here too, so one upload step publishes both.
	mediaDir := filepath.Join(*cache, "github", "media")
	available := map[string]fetch.Fetcher{
		"flathub": &flathub.Fetcher{Client: client, CacheDir: filepath.Join(*cache, "flathub"), Arch: *arch,
			File: *flathubFile},
		"fedora": &fedora.Fetcher{Client: client, CacheDir: filepath.Join(*cache, "fedora"), Arch: *arch,
			Releases: releases, IconDir: github.IconDir(mediaDir), IconBase: *publishURL, Log: log},
		"homebrew": &homebrew.Fetcher{Client: client, CacheDir: filepath.Join(*cache, "homebrew"), Arch: *arch},
		"copr": &fetch.Cached{
			Fetcher: &copr.Fetcher{Client: client, CacheDir: filepath.Join(*cache, "copr"), Arch: *arch,
				Releases: releases, Log: log},
			Dir: filepath.Join(*cache, "results"), For: *slowCacheFor, Stale: 7 * 24 * time.Hour, Log: log,
			Key: fmt.Sprintf("schema=%d arch=%s releases=%s", catalog.SchemaVersion, *arch, *fedoraReleases),
		},
		"github": &fetch.Cached{
			Fetcher: &github.Fetcher{Client: client, Token: os.Getenv("GITHUB_TOKEN"), Arch: *arch, Log: log,
				SweepMinStars: github.DefaultSweepMinStars, SweepCache: filepath.Join(*cache, "github", "sweep.json"),
				MediaDir: mediaDir, IconBase: *publishURL},
			Dir: filepath.Join(*cache, "results"), For: *slowCacheFor, Stale: 7 * 24 * time.Hour, Log: log,
			Key: fmt.Sprintf("schema=%d arch=%s queries=%s sweep=%d media=%d listing=%d icons=%s",
				catalog.SchemaVersion, *arch, strings.Join(github.DefaultQueries, "|"), github.DefaultSweepMinStars,
				github.MediaVersion, github.ListingVersion, *publishURL),
		},
	}
	forgeBuilder := forge.Builder{Client: client, Arch: *arch, MediaDir: mediaDir, IconBase: *publishURL, Log: log}
	forgeKey := fmt.Sprintf("schema=%d arch=%s media=%d/%d icons=%s notes=2", catalog.SchemaVersion, *arch,
		forge.MediaVersion, github.MediaVersion, *publishURL)
	codeberg := forge.NewCodeberg(forgeBuilder)
	codeberg.MinStars = *codebergMinStars
	available["codeberg"] = &fetch.Cached{Fetcher: codeberg,
		Dir: filepath.Join(*cache, "results"), For: *slowCacheFor, Stale: 7 * 24 * time.Hour, Log: log,
		Key: forgeKey + fmt.Sprintf(" min-stars=%d", *codebergMinStars)}
	available["gitlab"] = &fetch.Cached{Fetcher: forge.NewGitLab(forgeBuilder, os.Getenv("GITLAB_TOKEN")),
		Dir: filepath.Join(*cache, "results"), For: *slowCacheFor, Stale: 7 * 24 * time.Hour, Log: log,
		Key: forgeKey + fmt.Sprintf(" min-stars=%d", forge.DefaultGitLabMinStars)}
	available["debian"] = &debian.Fetcher{Distribution: catalog.SourceDebian, Client: client,
		CacheDir: filepath.Join(*cache, "apt"), Arch: *arch, Log: log}
	available["ubuntu"] = &debian.Fetcher{Distribution: catalog.SourceUbuntu, Client: client,
		CacheDir: filepath.Join(*cache, "apt"), Arch: *arch, Log: log}
	available["arch"] = &archlinux.Fetcher{Client: client, CacheDir: *cache, Arch: *arch, Log: log,
		IconDir: github.IconDir(mediaDir), IconBase: *publishURL}
	// The Snap Store takes a few minutes at one request a second, so it is cached like GitHub and COPR.
	available["snap"] = &fetch.Cached{Fetcher: &snap.Fetcher{Client: client, Arch: *arch, Log: log},
		Dir: filepath.Join(*cache, "results"), For: *slowCacheFor, Stale: 7 * 24 * time.Hour, Log: log,
		Key: fmt.Sprintf("schema=%d arch=%s v=3", catalog.SchemaVersion, *arch)}
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
	fetched, err := indexer.Fetch(ctx, fetchers, log)
	if err != nil {
		log.Error("build failed", "err", err)
		os.Exit(1)
	}
	// Every source in one catalog, for local use; editions (one per kind of system) when publishing or asked.
	info, err := fetched.Write(ctx, *out, func(catalog.Listing) bool { return true }, log)
	if err != nil {
		log.Error("build failed", "err", err)
		os.Exit(1)
	}
	log.Info("catalog written", "path", *out, "apps", info.Apps, "took", time.Since(start).Round(time.Millisecond))
	var editions map[string]indexer.Built
	if store != nil || *editionsDir != "" {
		dir := *editionsDir
		if dir == "" {
			dir = *out + ".editions"
		}
		if err := os.MkdirAll(dir, 0o755); err != nil {
			log.Error("build failed", "err", err)
			os.Exit(1)
		}
		if editions, err = fetched.WriteEditions(ctx, dir, log); err != nil {
			log.Error("build failed", "err", err)
			os.Exit(1)
		}
	}
	if n, err := fetch.PruneCache(*cache, 3*24*time.Hour); err != nil {
		log.Warn("pruning the download cache failed", "err", err)
	} else if n > 0 {
		log.Info("pruned download cache", "files", n)
	}

	if store != nil {
		// Icons first: the catalogs point at them.
		if _, err := publish.Icons(ctx, store, github.IconDir(mediaDir), log); err != nil {
			// Not fatal: apps show a placeholder until a later run uploads their icons.
			log.Warn("icon upload failed", "err", err)
		}
		eds := map[string]publish.Edition{}
		for name, b := range editions {
			eds[name] = publish.Edition{Path: b.Path, Info: b.Info}
		}
		m, err := publish.PublishEditions(ctx, store, eds, fetched.BuiltAt, info.Sources,
			publish.Options{Key: signingKey, Keep: *keep, Log: log})
		if err != nil {
			log.Error("publish failed", "err", err)
			os.Exit(1)
		}
		for name, f := range m.Editions {
			log.Info("published", "edition", name, "size", f.Size, "uncompressed", f.UncompressedSize)
		}
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
