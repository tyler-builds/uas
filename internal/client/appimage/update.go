package appimage

import (
	"debug/elf"
	"errors"
	"fmt"
	"io"
	"regexp"
	"strings"
)

// Update is where an AppImage says its updates come from: the update information that appimagetool embeds
// in the runtime's .upd_info section, as AppImageUpdate reads it. See
// https://github.com/AppImage/AppImageSpec/blob/master/draft.md#update-information.
type Update struct {
	// Kind is "gh-releases-zsync", "gh-releases-direct" or "zsync".
	Kind string
	// For the GitHub kinds: the repo, the release ("latest", "latest-pre", "latest-all" or a tag) and a
	// pattern of the file in it, with * wildcards (the .zsync file, or the AppImage itself for direct).
	Owner, Repo, Release, Pattern string
	// For zsync: the URL of the .zsync file.
	URL string
}

// The update kinds uas follows.
const (
	UpdateGitHubZsync  = "gh-releases-zsync"
	UpdateGitHubDirect = "gh-releases-direct"
	UpdateZsync        = "zsync"
)

var ghName = regexp.MustCompile(`^[A-Za-z0-9_.-]+$`)

// String returns the update information as the AppImage holds it.
func (u Update) String() string {
	if u.Kind == UpdateZsync {
		return u.Kind + "|" + u.URL
	}
	return strings.Join([]string{u.Kind, u.Owner, u.Repo, u.Release, u.Pattern}, "|")
}

// GitHubRepo returns "owner/repo" for the GitHub kinds, or "".
func (u Update) GitHubRepo() string {
	if u.Owner == "" {
		return ""
	}
	return u.Owner + "/" + u.Repo
}

// ParseUpdate reads update information. Kinds uas does not follow (such as pling-v1-zsync) are an error.
func ParseUpdate(s string) (Update, error) {
	parts := strings.Split(strings.TrimSpace(s), "|")
	switch parts[0] {
	case UpdateGitHubZsync, UpdateGitHubDirect:
		if len(parts) != 5 || !ghName.MatchString(parts[1]) || !ghName.MatchString(parts[2]) ||
			strings.Contains(parts[1]+parts[2], "..") || parts[3] == "" || parts[4] == "" ||
			strings.ContainsAny(parts[3]+parts[4], "/\\") {
			return Update{}, fmt.Errorf("appimage: bad update information %q", s)
		}
		return Update{Kind: parts[0], Owner: parts[1], Repo: parts[2], Release: parts[3], Pattern: parts[4]}, nil
	case UpdateZsync:
		if len(parts) != 2 || !strings.HasPrefix(parts[1], "https://") {
			return Update{}, fmt.Errorf("appimage: bad update information %q (uas follows only https URLs)", s)
		}
		return Update{Kind: UpdateZsync, URL: parts[1]}, nil
	case "":
		return Update{}, errors.New("appimage: no update information")
	}
	return Update{}, fmt.Errorf("appimage: uas cannot follow %s updates", parts[0])
}

const maxUpdateInfo = 4 << 10

// UpdateInfo returns the update information embedded in an AppImage, or "" if it has none.
func UpdateInfo(r io.ReaderAt) (string, error) {
	f, err := elf.NewFile(r)
	if err != nil {
		return "", fmt.Errorf("appimage: %w", err)
	}
	s := f.Section(".upd_info")
	if s == nil {
		return "", nil
	}
	b, err := io.ReadAll(io.LimitReader(s.Open(), maxUpdateInfo))
	if err != nil {
		return "", fmt.Errorf("appimage: read update information: %w", err)
	}
	if i := strings.IndexByte(string(b), 0); i >= 0 {
		b = b[:i]
	}
	return strings.TrimSpace(string(b)), nil
}

var metainfoID = regexp.MustCompile(`<id(?:\s[^>]*)?>\s*([^<\s]+)\s*</id>`)

// AppID returns the app's AppStream ID, from its metainfo, else from its desktop entry's file name.
func (i *Info) AppID() string {
	if m := metainfoID.FindSubmatch(i.Metainfo); m != nil {
		return strings.TrimSuffix(string(m[1]), ".desktop")
	}
	return strings.TrimSuffix(i.DesktopName, ".desktop")
}
