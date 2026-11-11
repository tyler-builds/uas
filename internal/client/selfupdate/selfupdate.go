// Package selfupdate updates uas itself from its GitHub releases.
//
// Checking asks GitHub's API for the latest release (no account needed) and compares its tag with the running
// version. If it is newer, uas downloads the release's signed description (manifest.ReleaseAsset), checks it
// against the keys the catalog is checked against, and takes the program's size and SHA-256 from it, so a
// release changed on GitHub by anyone without the signing key is refused. Installing downloads the program for
// this architecture, checks it, and puts it in place of the running program, if that folder is the user's.
// Builds from source ("dev") never update themselves.
package selfupdate

import (
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"time"

	"github.com/tyler-builds/uas/internal/client/selfinstall"
	"github.com/tyler-builds/uas/internal/manifest"
	"github.com/tyler-builds/uas/internal/version"
)

// LatestURL is GitHub's API for uas's latest release. GitHub allows 60 requests an hour from an address
// without an account; a check makes one, and the downloads aren't counted.
const LatestURL = "https://api.github.com/repos/tyler-builds/uas/releases/latest"

// Updater checks for and installs releases.
type Updater struct {
	// LatestURL is the latest-release API; the package's LatestURL when empty.
	LatestURL   string
	TrustedKeys []ed25519.PublicKey
	Client      *http.Client
	Arch        string // "x86_64" or "aarch64"
	// Current is the running version; version.Version when empty.
	Current string
	// Dir is where the programs are installed; the running program's folder when empty.
	Dir string
	// Now is the time, for messages about GitHub's rate limit; time.Now when nil.
	Now func() time.Time
}

// ErrDevBuild is returned when asked to update a build from source.
var ErrDevBuild = errors.New("this uas was built from source; update it the way you built it")

// RateLimitError is returned when GitHub refuses a check because this address has used up its requests.
type RateLimitError struct {
	Reset time.Time // when GitHub allows requests again; zero if it didn't say
	now   time.Time
}

func (e *RateLimitError) Error() string {
	msg := "GitHub is limiting update checks from this network"
	if e.Reset.IsZero() {
		return msg + "; try again later"
	}
	wait := e.Reset.Sub(e.now).Round(time.Minute)
	if wait < time.Minute {
		return msg + "; try again in a minute"
	}
	return fmt.Sprintf("%s; try again in %d minutes", msg, int(wait.Minutes()))
}

// Available is a newer release, if there is one.
type Available struct {
	Release manifest.Release
	File    manifest.ReleaseFile
	URL     string // where to download File
}

// githubRelease is the part of GitHub's release object uas reads.
type githubRelease struct {
	TagName string        `json:"tag_name"`
	Assets  []githubAsset `json:"assets"`
}

type githubAsset struct {
	Name string `json:"name"`
	URL  string `json:"browser_download_url"`
	Size int64  `json:"size"`
}

func (r *githubRelease) asset(name string) (githubAsset, bool) {
	for _, a := range r.Assets {
		if a.Name == name {
			return a, true
		}
	}
	return githubAsset{}, false
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
	api := u.LatestURL
	if api == "" {
		api = LatestURL
	}
	raw, err := u.get(ctx, api, 4<<20, true)
	if err != nil {
		return nil, err
	}
	var gh githubRelease
	if err := json.Unmarshal(raw, &gh); err != nil {
		return nil, fmt.Errorf("read GitHub's answer: %w", err)
	}
	tagged, err := manifest.ParseVersion(gh.TagName)
	if err != nil {
		return nil, fmt.Errorf("the latest release's tag %q is not a version", gh.TagName)
	}
	if !tagged.Newer(have) {
		return nil, nil
	}

	// Newer by its tag: trust it only as far as its signature says.
	desc, ok := gh.asset(manifest.ReleaseAsset)
	if !ok {
		return nil, fmt.Errorf("uas %s has no %s", gh.TagName, manifest.ReleaseAsset)
	}
	signed, err := u.get(ctx, desc.URL, 1<<20, false)
	if err != nil {
		return nil, err
	}
	rel, err := manifest.VerifyRelease(signed, u.TrustedKeys)
	if err != nil {
		return nil, err
	}
	latest, _ := manifest.ParseVersion(rel.Version) // validated by VerifyRelease
	if latest != tagged {
		return nil, fmt.Errorf("release %s is signed as uas %s", gh.TagName, rel.Version)
	}
	f, ok := rel.File(u.Arch)
	if !ok {
		return nil, fmt.Errorf("uas %s has no build for %s", rel.Version, u.Arch)
	}
	prog, ok := gh.asset(f.Name)
	if !ok || prog.Size != f.Size {
		return nil, fmt.Errorf("uas %s's %s is missing or isn't the signed one", rel.Version, f.Name)
	}
	return &Available{Release: rel, File: f, URL: prog.URL}, nil
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
	b, err := u.get(ctx, a.URL, a.File.Size+1, false)
	if err != nil {
		return err
	}
	sum := sha256.Sum256(b)
	if int64(len(b)) != a.File.Size || hex.EncodeToString(sum[:]) != a.File.SHA256 {
		return errors.New("the downloaded release doesn't match its signed checksum")
	}
	report(progress, "Checksum verified")

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

// get downloads url, refusing more than limit bytes. api marks a request to GitHub's API, whose errors
// get their own messages.
func (u *Updater) get(ctx context.Context, url string, limit int64, api bool) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "uas/"+version.Version)
	if api {
		req.Header.Set("Accept", "application/vnd.github+json")
		req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	}
	resp, err := u.Client.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, fmt.Errorf("can't reach GitHub: %w", err)
	}
	defer resp.Body.Close()
	if api {
		if err := u.apiError(resp); err != nil {
			return nil, err
		}
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("download from GitHub: %s", resp.Status)
	}
	b, err := io.ReadAll(io.LimitReader(resp.Body, limit))
	if err != nil {
		return nil, fmt.Errorf("download from GitHub: %w", err)
	}
	if int64(len(b)) >= limit {
		return nil, errors.New("download from GitHub: larger than expected")
	}
	return b, nil
}

// apiError turns GitHub's API errors into messages for people: no release yet, and rate limits, which
// GitHub answers with 403 or 429 and either X-RateLimit-Remaining: 0 or Retry-After.
func (u *Updater) apiError(resp *http.Response) error {
	switch resp.StatusCode {
	case http.StatusOK:
		return nil
	case http.StatusNotFound:
		return errors.New("no uas release is published yet")
	case http.StatusForbidden, http.StatusTooManyRequests:
		now := time.Now
		if u.Now != nil {
			now = u.Now
		}
		e := &RateLimitError{now: now()}
		if s, err := strconv.Atoi(resp.Header.Get("Retry-After")); err == nil {
			e.Reset = e.now.Add(time.Duration(s) * time.Second)
			return e
		}
		if resp.Header.Get("X-RateLimit-Remaining") == "0" {
			if s, err := strconv.ParseInt(resp.Header.Get("X-RateLimit-Reset"), 10, 64); err == nil {
				e.Reset = time.Unix(s, 0)
			}
			return e
		}
		if resp.StatusCode == http.StatusTooManyRequests {
			return e
		}
	}
	return fmt.Errorf("GitHub answered %s", resp.Status)
}

func report(progress func(string), msg string) {
	if progress != nil {
		progress(msg)
	}
}
