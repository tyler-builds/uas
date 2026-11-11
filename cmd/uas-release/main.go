// Command uas-release prepares the files of a GitHub release of uas: the program for each architecture under
// its release name (uas-linux-<arch>, with no version, so releases/latest/download/ links always work), the
// signed description that installed copies of uas check before updating themselves (uas-release.json), and a
// SHA256SUMS file for people who check a download by hand.
//
//	uas-release -version 0.6.0 -out dist/github dist/uas-0.6.0-linux-x86_64 dist/uas-0.6.0-linux-aarch64
//
// It needs $UAS_SIGNING_KEY (the catalog's signing key). The programs come from
// packaging/linux/build-release.sh. Uploading is left to the release workflow (gh release create).
package main

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/tyler-builds/uas/internal/manifest"
)

// fileName matches a program from build-release.sh.
var fileName = regexp.MustCompile(`^uas-(\d+\.\d+\.\d+)-linux-(x86_64|aarch64)$`)

func main() {
	ver := flag.String("version", "", "the version to release, such as 0.6.0 (required)")
	out := flag.String("out", "", "write the release's files into this directory (required)")
	flag.Parse()
	if err := run(*ver, *out, flag.Args(), time.Now()); err != nil {
		fmt.Fprintln(os.Stderr, "uas-release:", err)
		os.Exit(1)
	}
}

func run(ver, out string, files []string, now time.Time) error {
	ver = strings.TrimPrefix(ver, "v")
	if _, err := manifest.ParseVersion(ver); err != nil {
		return err
	}
	if out == "" {
		return errors.New("-out is required")
	}
	if len(files) == 0 {
		return errors.New("give the release's programs")
	}
	key, err := manifest.ParsePrivateKey(os.Getenv("UAS_SIGNING_KEY"))
	if err != nil {
		return fmt.Errorf("$UAS_SIGNING_KEY: %w", err)
	}
	if err := os.MkdirAll(out, 0o755); err != nil {
		return err
	}

	rel := manifest.Release{Version: ver, Released: now.UTC().Truncate(time.Second)}
	var sums strings.Builder
	for _, a := range files {
		m := fileName.FindStringSubmatch(filepath.Base(a))
		if m == nil || m[1] != ver {
			return fmt.Errorf("%s is not a program of uas %s", a, ver)
		}
		if _, ok := rel.File(m[2]); ok {
			return fmt.Errorf("two programs for %s", m[2])
		}
		b, err := os.ReadFile(a)
		if err != nil {
			return err
		}
		sum := sha256.Sum256(b)
		f := manifest.ReleaseFile{Arch: m[2], Name: manifest.AssetName(m[2]), SHA256: hex.EncodeToString(sum[:]),
			Size: int64(len(b))}
		if err := os.WriteFile(filepath.Join(out, f.Name), b, 0o755); err != nil {
			return err
		}
		rel.Programs = append(rel.Programs, f)
		fmt.Fprintf(&sums, "%s  %s\n", f.SHA256, f.Name)
		fmt.Printf("%s (%d bytes, sha256 %s)\n", f.Name, f.Size, f.SHA256)
	}
	signed, err := manifest.SignRelease(rel, key)
	if err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(out, manifest.ReleaseAsset), signed, 0o644); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(out, manifest.ChecksumsAsset), []byte(sums.String()), 0o644); err != nil {
		return err
	}
	fmt.Printf("signed uas %s in %s\n", ver, filepath.Join(out, manifest.ReleaseAsset))
	return nil
}
