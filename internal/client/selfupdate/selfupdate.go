// Package selfupdate updates uas itself from its signed releases, published beside the catalog.
//
// A release is a signed description (releases/latest.json, checked against the keys the catalog is checked
// against) and an archive per architecture with uas and uas-gui. Updating downloads the archive, checks its
// size and SHA-256, and replaces both programs where they are installed, if that folder is the user's.
// Builds from source ("dev") never update themselves.
package selfupdate

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"strings"

	"github.com/tyler-builds/uas/internal/manifest"
	"github.com/tyler-builds/uas/internal/version"
)

// Programs are the files a release replaces, found in the folder of the running program.
var Programs = []string{"uas", "uas-gui"}

// Updater checks for and installs releases.
type Updater struct {
	BaseURL     string // the catalog's base URL
	TrustedKeys []ed25519.PublicKey
	Client      *http.Client
	Arch        string // "x86_64" or "aarch64"
	// Current is the running version; version.Version when empty.
	Current string
	// Dir is where the programs are installed; the running program's folder when empty.
	Dir string
}

// ErrDevBuild is returned when asked to update a build from source.
var ErrDevBuild = errors.New("this uas was built from source; update it the way you built it")

// Available is a newer release, if there is one.
type Available struct {
	Release manifest.Release
	File    manifest.ReleaseFile
}

// Check returns the latest release if it is newer than the running one, else nil.
func (u *Updater) Check(ctx context.Context) (*Available, error) {
	cur := u.Current
	if cur == "" {
		cur = version.Version
	}
	have, err := manifest.ParseVersion(cur)
	if err != nil {
		return nil, ErrDevBuild
	}
	raw, err := u.get(ctx, manifest.ReleaseName, 1<<20)
	if err != nil {
		return nil, err
	}
	rel, err := manifest.VerifyRelease(raw, u.TrustedKeys)
	if err != nil {
		return nil, err
	}
	latest, _ := manifest.ParseVersion(rel.Version) // validated by VerifyRelease
	if !latest.Newer(have) {
		return nil, nil
	}
	f, ok := rel.File(u.Arch)
	if !ok {
		return nil, fmt.Errorf("uas %s has no build for %s", rel.Version, u.Arch)
	}
	return &Available{Release: rel, File: f}, nil
}

// Install downloads a release and puts its programs in place of the running ones.
func (u *Updater) Install(ctx context.Context, a *Available, progress func(string)) error {
	dir, err := u.dir()
	if err != nil {
		return err
	}
	// Check that the folder is ours to change before downloading anything.
	probe, err := os.CreateTemp(dir, ".uas-update-*")
	if err != nil {
		return fmt.Errorf("uas is installed in %s, which this account can't change; update it the way you "+
			"installed it", dir)
	}
	probe.Close()
	os.Remove(probe.Name())

	report(progress, "Downloading uas "+a.Release.Version)
	archive, err := u.get(ctx, a.File.Path, a.File.Size+1)
	if err != nil {
		return err
	}
	sum := sha256.Sum256(archive)
	if int64(len(archive)) != a.File.Size || hex.EncodeToString(sum[:]) != a.File.SHA256 {
		return errors.New("the downloaded release doesn't match its signed checksum")
	}
	report(progress, "Checksum verified")
	files, err := extract(archive)
	if err != nil {
		return err
	}
	// Write every program first, then swap them in, so a failure leaves the old ones.
	var tmps []string
	defer func() {
		for _, t := range tmps {
			os.Remove(t)
		}
	}()
	for _, name := range Programs {
		b, ok := files[name]
		if !ok {
			return fmt.Errorf("the release has no %s", name)
		}
		t := filepath.Join(dir, "."+name+".update")
		if err := os.WriteFile(t, b, 0o755); err != nil {
			return err
		}
		tmps = append(tmps, t)
	}
	for i, name := range Programs {
		// Only programs that are installed here are replaced: someone may have only the CLI.
		if _, err := os.Stat(filepath.Join(dir, name)); err != nil && name != filepath.Base(u.self()) {
			continue
		}
		if err := os.Rename(tmps[i], filepath.Join(dir, name)); err != nil {
			return err
		}
	}
	report(progress, "Updated to uas "+a.Release.Version)
	return nil
}

func (u *Updater) self() string {
	exe, _ := os.Executable()
	return exe
}

func (u *Updater) dir() (string, error) {
	if u.Dir != "" {
		return u.Dir, nil
	}
	exe, err := os.Executable()
	if err != nil {
		return "", err
	}
	if exe, err = filepath.EvalSymlinks(exe); err != nil {
		return "", err
	}
	return filepath.Dir(exe), nil
}

// extract reads the programs out of a release archive. Archive entries are
// "uas-<version>-linux-<arch>/<file>"; only top-level files named in Programs are kept.
func extract(archive []byte) (map[string][]byte, error) {
	gz, err := gzip.NewReader(bytes.NewReader(archive))
	if err != nil {
		return nil, err
	}
	defer gz.Close()
	tr := tar.NewReader(gz)
	out := map[string][]byte{}
	for {
		h, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, err
		}
		if h.Typeflag != tar.TypeReg {
			continue
		}
		parts := strings.Split(path.Clean(h.Name), "/")
		if len(parts) != 2 {
			continue
		}
		for _, p := range Programs {
			if parts[1] == p {
				b, err := io.ReadAll(io.LimitReader(tr, 512<<20))
				if err != nil {
					return nil, err
				}
				out[p] = b
			}
		}
	}
	return out, nil
}

func (u *Updater) get(ctx context.Context, name string, limit int64) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimSuffix(u.BaseURL, "/")+"/"+name, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "uas/"+version.Version)
	resp, err := u.Client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound && name == manifest.ReleaseName {
		return nil, errors.New("no uas release is published yet")
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("GET %s: %s", name, resp.Status)
	}
	b, err := io.ReadAll(io.LimitReader(resp.Body, limit))
	if err != nil {
		return nil, err
	}
	if int64(len(b)) >= limit {
		return nil, fmt.Errorf("GET %s: too large", name)
	}
	return b, nil
}

func report(progress func(string), msg string) {
	if progress != nil {
		progress(msg)
	}
}
