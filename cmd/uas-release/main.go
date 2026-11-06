// Command uas-release publishes release archives of uas beside the catalog, with a signed
// releases/latest.json that clients check before updating themselves.
//
//	uas-release -version 0.2.0 -publish https://uas-catalog.example.workers.dev dist/*.tar.gz
//
// It needs $UAS_SIGNING_KEY (the catalog's signing key) and, with -publish, $UAS_UPLOAD_TOKEN. Archives come
// from packaging/linux/build-release.sh. A version older than the published one is refused.
package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/tyler-builds/uas/internal/client/refresh"
	"github.com/tyler-builds/uas/internal/indexer/publish"
	"github.com/tyler-builds/uas/internal/manifest"
)

var archiveName = regexp.MustCompile(`^uas-(\d+\.\d+\.\d+)-linux-(x86_64|aarch64)\.tar\.gz$`)

func main() {
	ver := flag.String("version", "", "the version to publish, such as 0.2.0 (required)")
	publishURL := flag.String("publish", "", "publish to the catalog Worker at this URL")
	publishDir := flag.String("publish-dir", "", "publish into this local directory instead")
	flag.Parse()
	if err := run(*ver, *publishURL, *publishDir, flag.Args()); err != nil {
		fmt.Fprintln(os.Stderr, "uas-release:", err)
		os.Exit(1)
	}
}

func run(ver, publishURL, publishDir string, archives []string) error {
	ver = strings.TrimPrefix(ver, "v")
	if _, err := manifest.ParseVersion(ver); err != nil {
		return err
	}
	if len(archives) == 0 {
		return errors.New("give the release archives to publish")
	}
	key, err := manifest.ParsePrivateKey(os.Getenv("UAS_SIGNING_KEY"))
	if err != nil {
		return fmt.Errorf("$UAS_SIGNING_KEY: %w", err)
	}
	var store publish.Store
	switch {
	case publishURL != "" && publishDir == "":
		token := os.Getenv("UAS_UPLOAD_TOKEN")
		if token == "" {
			return errors.New("-publish needs $UAS_UPLOAD_TOKEN")
		}
		store = &publish.HTTPStore{BaseURL: publishURL, Token: token, Client: &http.Client{Timeout: 10 * time.Minute}}
	case publishDir != "" && publishURL == "":
		store = &publish.DirStore{Dir: publishDir}
	default:
		return errors.New("use -publish or -publish-dir")
	}
	ctx := context.Background()

	if publishURL != "" {
		if err := checkNewer(ctx, publishURL, ver); err != nil {
			return err
		}
	}
	rel := manifest.Release{Version: ver, Released: time.Now().UTC().Truncate(time.Second)}
	for _, a := range archives {
		m := archiveName.FindStringSubmatch(filepath.Base(a))
		if m == nil || m[1] != ver {
			return fmt.Errorf("%s is not a uas %s archive", a, ver)
		}
		b, err := os.ReadFile(a)
		if err != nil {
			return err
		}
		sum := sha256.Sum256(b)
		f := manifest.ReleaseFile{Arch: m[2], Path: manifest.ReleasePath(ver, m[2]),
			SHA256: hex.EncodeToString(sum[:]), Size: int64(len(b))}
		// Archives first: latest.json points at them.
		if err := store.Put(ctx, f.Path, bytes.NewReader(b), f.Size, "application/gzip"); err != nil {
			return err
		}
		fmt.Printf("uploaded %s (%d bytes, sha256 %s)\n", f.Path, f.Size, f.SHA256)
		rel.Files = append(rel.Files, f)
	}
	signed, err := manifest.SignRelease(rel, key)
	if err != nil {
		return err
	}
	if err := store.Put(ctx, manifest.ReleaseName, bytes.NewReader(signed), int64(len(signed)),
		"application/json"); err != nil {
		return err
	}
	fmt.Printf("published uas %s\n", ver)
	return nil
}

// checkNewer refuses to publish a version that isn't newer than the published one.
func checkNewer(ctx context.Context, base, ver string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimSuffix(base, "/")+"/"+manifest.ReleaseName, nil)
	if err != nil {
		return err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		return nil // the first release
	}
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("read the published release: %s", resp.Status)
	}
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return err
	}
	cur, err := manifest.VerifyRelease(raw, refresh.TrustedKeys())
	if err != nil {
		return fmt.Errorf("the published release doesn't verify: %w", err)
	}
	have, _ := manifest.ParseVersion(cur.Version)
	want, _ := manifest.ParseVersion(ver)
	if !want.Newer(have) {
		return fmt.Errorf("uas %s is published already; %s is not newer", cur.Version, ver)
	}
	return nil
}
