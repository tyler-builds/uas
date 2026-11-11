// Package selfupdate updates uas itself from its signed releases, published beside the catalog.
//
// A release is a signed description (releases/latest.json, checked against the keys the catalog is checked
// against) and the uas program for each architecture, which is both the CLI and the app. Updating downloads
// it, checks its size and SHA-256, and puts it in place of the running program, if that folder is the user's.
// Builds from source ("dev") never update themselves.
//
// Releases also carry archives for copies of uas that predate the single program (see manifest.Release).
// Those copies updated uas and uas-gui from them, each to a copy of the new program; the new program then
// makes uas-gui a link to itself (selfinstall.Layout.Refresh).
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

	"github.com/tyler-builds/uas/internal/client/selfinstall"
	"github.com/tyler-builds/uas/internal/manifest"
	"github.com/tyler-builds/uas/internal/version"
)

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

// Install downloads a release and puts its program in place of the running one.
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
	b, err := u.get(ctx, a.File.Path, a.File.Size+1)
	if err != nil {
		return err
	}
	sum := sha256.Sum256(b)
	if int64(len(b)) != a.File.Size || hex.EncodeToString(sum[:]) != a.File.SHA256 {
		return errors.New("the downloaded release doesn't match its signed checksum")
	}
	report(progress, "Checksum verified")
	if a.File.Archive() {
		if b, err = extract(b, selfinstall.Program); err != nil {
			return err
		}
	}

	// Written beside the old program and swapped in whole, so a failure leaves the old one.
	tmp := filepath.Join(dir, "."+selfinstall.Program+".update")
	defer os.Remove(tmp)
	if err := os.WriteFile(tmp, b, 0o755); err != nil {
		return err
	}
	if err := os.Rename(tmp, filepath.Join(dir, selfinstall.Program)); err != nil {
		return err
	}
	// uas-gui, where there is one, opens the new program too.
	l := selfinstall.Layout{Bin: dir}
	if _, err := os.Lstat(l.GUIPath()); err == nil {
		if err := l.LinkGUI(); err != nil {
			return err
		}
	}
	report(progress, "Updated to uas "+a.Release.Version)
	return nil
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

// extract reads a program out of a release archive. Archive entries are "uas-<version>-linux-<arch>/<file>".
func extract(archive []byte, name string) ([]byte, error) {
	gz, err := gzip.NewReader(bytes.NewReader(archive))
	if err != nil {
		return nil, err
	}
	defer gz.Close()
	tr := tar.NewReader(gz)
	for {
		h, err := tr.Next()
		if err == io.EOF {
			return nil, fmt.Errorf("the release has no %s", name)
		}
		if err != nil {
			return nil, err
		}
		if parts := strings.Split(path.Clean(h.Name), "/"); h.Typeflag == tar.TypeReg && len(parts) == 2 &&
			parts[1] == name {
			return io.ReadAll(io.LimitReader(tr, 512<<20))
		}
	}
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
