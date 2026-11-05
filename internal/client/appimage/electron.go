package appimage

import (
	"errors"
	"fmt"
	"net/url"
	"regexp"
	"strconv"
	"strings"
)

// UpdateElectron is update information uas makes for apps built with electron-builder: "electron-updater|"
// and the https URL of the feed electron-updater reads, such as ".../latest-linux.yml".
const UpdateElectron = "electron-updater"

var electronGitHubFeed = regexp.MustCompile(`^https://github\.com/([A-Za-z0-9_.-]+)/([A-Za-z0-9_.-]+)/releases/`)

// ElectronFeed returns the update information for the feed that an electron-builder app's
// resources/app-update.yml names, for arch ("x86_64" or "aarch64"). It knows the generic, github and s3
// providers.
func (i *Info) ElectronFeed(arch string) (Update, error) {
	c := i.ElectronUpdate
	if c == nil {
		return Update{}, errors.New("appimage: no electron-builder update settings")
	}
	channel := c["channel"]
	if channel == "" {
		channel = "latest"
	}
	file := channel + "-linux.yml"
	if arch == "aarch64" {
		file = channel + "-linux-arm64.yml"
	}
	var base string
	switch c["provider"] {
	case "generic":
		base = c["url"]
	case "github":
		if !ghName.MatchString(c["owner"]) || !ghName.MatchString(c["repo"]) {
			return Update{}, errors.New("appimage: electron-builder github settings name no repo")
		}
		base = "https://github.com/" + c["owner"] + "/" + c["repo"] + "/releases/latest/download"
	case "s3":
		if c["bucket"] == "" {
			return Update{}, errors.New("appimage: electron-builder s3 settings name no bucket")
		}
		host := c["bucket"] + ".s3.amazonaws.com"
		if c["region"] != "" {
			host = c["bucket"] + ".s3." + c["region"] + ".amazonaws.com"
		}
		base = "https://" + host + "/" + strings.Trim(c["path"], "/")
	default:
		return Update{}, fmt.Errorf("appimage: uas cannot follow electron-builder's %q provider", c["provider"])
	}
	return ParseUpdate(UpdateElectron + "|" + strings.TrimSuffix(base, "/") + "/" + file)
}

// parseElectron reads "electron-updater|<feed URL>".
func parseElectron(feed string) (Update, error) {
	u, err := url.Parse(feed)
	if err != nil || u.Scheme != "https" || u.Host == "" {
		return Update{}, fmt.Errorf("appimage: bad electron-updater feed %q (uas follows only https URLs)", feed)
	}
	up := Update{Kind: UpdateElectron, URL: feed}
	if m := electronGitHubFeed.FindStringSubmatch(feed); m != nil {
		up.Owner, up.Repo = m[1], m[2]
	}
	return up, nil
}

// ElectronRelease is the AppImage an electron-updater feed offers.
type ElectronRelease struct {
	Version string
	URL     string // as the feed gives it, often relative to the feed
	SHA512  string // base64
	Size    int64
}

// ParseElectronFeed reads an electron-updater feed (latest-linux.yml) and returns its AppImage.
func ParseElectronFeed(s string) (ElectronRelease, error) {
	var r ElectronRelease
	var cur *ElectronRelease
	var files []ElectronRelease
	top := ElectronRelease{}
	inFiles := false
	for _, line := range strings.Split(s, "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}
		indented := line[0] == ' ' || line[0] == '\t'
		if !indented {
			inFiles = false
		}
		item := strings.HasPrefix(trimmed, "- ")
		if item {
			trimmed = strings.TrimSpace(trimmed[2:])
		}
		k, v, ok := strings.Cut(trimmed, ":")
		if !ok {
			continue
		}
		k, v = strings.TrimSpace(k), unquoteYAML(strings.TrimSpace(v))
		switch {
		case !indented && k == "files":
			inFiles = true
		case !indented:
			setRelease(&top, k, v)
			if k == "version" {
				r.Version = v
			}
		case inFiles && item:
			files = append(files, ElectronRelease{})
			cur = &files[len(files)-1]
			setRelease(cur, k, v)
		case inFiles && cur != nil:
			setRelease(cur, k, v)
		}
	}
	for _, f := range files {
		if strings.HasSuffix(strings.ToLower(f.URL), ".appimage") && f.SHA512 != "" {
			f.Version = r.Version
			return f, nil
		}
	}
	if strings.HasSuffix(strings.ToLower(top.URL), ".appimage") && top.SHA512 != "" {
		top.Version = r.Version
		return top, nil
	}
	return ElectronRelease{}, errors.New("appimage: the update feed offers no AppImage with a checksum")
}

func setRelease(r *ElectronRelease, k, v string) {
	switch k {
	case "url", "path":
		if r.URL == "" || k == "url" {
			r.URL = v
		}
	case "sha512":
		r.SHA512 = v
	case "size":
		r.Size, _ = strconv.ParseInt(v, 10, 64)
	}
}

func unquoteYAML(v string) string {
	if len(v) >= 2 && (v[0] == '\'' || v[0] == '"') && v[len(v)-1] == v[0] {
		return v[1 : len(v)-1]
	}
	return v
}

// parseYAMLKeys reads the top-level "key: value" lines of a flat YAML file such as app-update.yml.
func parseYAMLKeys(s string) map[string]string {
	vals := map[string]string{}
	for _, line := range strings.Split(s, "\n") {
		if line == "" || line[0] == ' ' || line[0] == '\t' || line[0] == '#' {
			continue
		}
		if k, v, ok := strings.Cut(line, ":"); ok {
			vals[strings.TrimSpace(k)] = unquoteYAML(strings.TrimSpace(v))
		}
	}
	return vals
}
