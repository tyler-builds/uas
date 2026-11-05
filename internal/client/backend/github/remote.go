package github

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"path"
	"regexp"
	"strconv"
	"strings"

	"github.com/tyler-builds/uas/internal/client/appimage"
)

// remoteFile is the file an AppImage's update information points to now.
type remoteFile struct {
	URL     string
	Name    string
	Size    int64  // 0 when unknown
	SHA256  string // hex, when GitHub publishes the file's digest
	SHA1    string // hex, from the .zsync file
	Version string // the release's tag, for GitHub
}

// sameAs reports whether the remote file is the one with these checksums. ok is false when the remote file
// has no checksum to compare.
func (r remoteFile) sameAs(sha256, sha1 string) (same, ok bool) {
	switch {
	case r.SHA256 != "":
		return r.SHA256 == sha256, true
	case r.SHA1 != "":
		return r.SHA1 == sha1, true
	}
	return false, false
}

// DefaultAPI is GitHub's REST API.
const DefaultAPI = "https://api.github.com"

type ghRelease struct {
	TagName    string `json:"tag_name"`
	Draft      bool   `json:"draft"`
	Prerelease bool   `json:"prerelease"`
	Assets     []struct {
		Name   string `json:"name"`
		URL    string `json:"browser_download_url"`
		Size   int64  `json:"size"`
		Digest string `json:"digest"` // "sha256:<hex>"
	} `json:"assets"`
}

var hexSHA256 = regexp.MustCompile(`^[0-9a-f]{64}$`)
var hexSHA1 = regexp.MustCompile(`^[0-9a-f]{40}$`)

// latestFile finds the file that u points to now.
func (b *Backend) latestFile(ctx context.Context, api string, u appimage.Update) (remoteFile, error) {
	if u.Kind == appimage.UpdateZsync {
		return b.zsyncFile(ctx, u.URL)
	}
	rel, err := b.release(ctx, api, u)
	if err != nil {
		return remoteFile{}, err
	}
	prefix := "https://github.com/" + u.GitHubRepo() + "/releases/download/"
	var file remoteFile
	for _, a := range rel.Assets {
		if ok, _ := path.Match(u.Pattern, a.Name); !ok || !strings.HasPrefix(a.URL, prefix) {
			continue
		}
		if u.Kind == appimage.UpdateGitHubZsync {
			if file, err = b.zsyncFile(ctx, a.URL); err != nil {
				return remoteFile{}, err
			}
		} else {
			file = remoteFile{URL: a.URL, Name: a.Name}
		}
		break
	}
	if file.URL == "" {
		return remoteFile{}, fmt.Errorf("release %s of %s has no file matching %s", rel.TagName, u.GitHubRepo(),
			u.Pattern)
	}
	// GitHub publishes a SHA-256 of each file; check it too when the zsync file names one of the release's.
	for _, a := range rel.Assets {
		if a.URL == file.URL {
			file.Size = a.Size
			if d, ok := strings.CutPrefix(a.Digest, "sha256:"); ok && hexSHA256.MatchString(d) {
				file.SHA256 = d
			}
		}
	}
	if !strings.HasPrefix(file.URL, prefix) {
		return remoteFile{}, fmt.Errorf("%s's update file is not in its own releases", u.GitHubRepo())
	}
	file.Version = strings.TrimPrefix(rel.TagName, "v")
	return file, nil
}

// release finds the release that u names.
func (b *Backend) release(ctx context.Context, api string, u appimage.Update) (ghRelease, error) {
	base := strings.TrimSuffix(api, "/") + "/repos/" + u.GitHubRepo() + "/releases"
	switch u.Release {
	case "latest":
		var r ghRelease
		return r, b.getJSON(ctx, base+"/latest", &r)
	case "latest-pre", "latest-all":
		var rs []ghRelease
		if err := b.getJSON(ctx, base+"?per_page=20", &rs); err != nil {
			return ghRelease{}, err
		}
		for _, r := range rs {
			if !r.Draft && (u.Release == "latest-all" || r.Prerelease) {
				return r, nil
			}
		}
		return ghRelease{}, fmt.Errorf("%s has no %s release", u.GitHubRepo(), u.Release)
	}
	var r ghRelease
	return r, b.getJSON(ctx, base+"/tags/"+url.PathEscape(u.Release), &r)
}

func (b *Backend) getJSON(ctx context.Context, u string, v any) error {
	resp, err := b.get(ctx, u)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	return json.NewDecoder(io.LimitReader(resp.Body, 8<<20)).Decode(v)
}

func (b *Backend) get(ctx context.Context, u string) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "uas")
	resp, err := b.Client.Do(req)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		resp.Body.Close()
		if resp.StatusCode == http.StatusForbidden || resp.StatusCode == http.StatusTooManyRequests {
			return nil, fmt.Errorf("GET %s: %s (GitHub's limit for checks may be used up; try again in an hour)",
				u, resp.Status)
		}
		return nil, fmt.Errorf("GET %s: %s", u, resp.Status)
	}
	return resp, nil
}

// maxZsyncHeader caps the text header read from a .zsync file; its block checksums follow it.
const maxZsyncHeader = 64 << 10

// zsyncFile reads the header of a .zsync file: the file's URL (relative to the .zsync file's), length and
// SHA-1.
func (b *Backend) zsyncFile(ctx context.Context, zsyncURL string) (remoteFile, error) {
	base, err := url.Parse(zsyncURL)
	if err != nil || base.Scheme != "https" {
		return remoteFile{}, fmt.Errorf("zsync file %s is not an https URL", zsyncURL)
	}
	resp, err := b.get(ctx, zsyncURL)
	if err != nil {
		return remoteFile{}, err
	}
	defer resp.Body.Close()
	h := map[string]string{}
	sc := bufio.NewScanner(io.LimitReader(resp.Body, maxZsyncHeader))
	for sc.Scan() {
		line := sc.Text()
		if line == "" {
			break
		}
		if k, v, ok := strings.Cut(line, ":"); ok {
			h[strings.TrimSpace(k)] = strings.TrimSpace(v)
		}
	}
	ref, err := url.Parse(h["URL"])
	if h["URL"] == "" || err != nil {
		return remoteFile{}, errors.New("zsync file has no URL")
	}
	file := base.ResolveReference(ref)
	if file.Scheme != "https" {
		return remoteFile{}, fmt.Errorf("zsync file points to %s, which is not https", file)
	}
	sha1 := strings.ToLower(h["SHA-1"])
	if !hexSHA1.MatchString(sha1) {
		return remoteFile{}, errors.New("zsync file has no SHA-1")
	}
	size, _ := strconv.ParseInt(h["Length"], 10, 64)
	name := h["Filename"]
	if name == "" {
		name = path.Base(file.Path)
	}
	return remoteFile{URL: file.String(), Name: name, Size: size, SHA1: sha1}, nil
}
