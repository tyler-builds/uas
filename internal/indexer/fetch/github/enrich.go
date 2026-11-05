package github

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"image"
	_ "image/gif"
	_ "image/jpeg"
	"image/png"
	"io"
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"golang.org/x/image/draw"
	_ "golang.org/x/image/webp"

	"github.com/tyler-builds/uas/internal/catalog"
	"github.com/tyler-builds/uas/internal/client/appimage"
	"github.com/tyler-builds/uas/internal/indexer/appstream"
	"github.com/tyler-builds/uas/internal/indexer/fetch"
	"github.com/tyler-builds/uas/internal/indexer/fetch/rangeread"
)

// A GitHub repo says little about the app it ships: no icon, often no description. The AppImage itself
// carries the app's name, a one-line description, categories and icon (its desktop entry), and sometimes
// AppStream metainfo with screenshots. Reading those takes a few range requests (about 400 KB) per AppImage.
// Screenshots otherwise come from the images in the README, and failing that from picture files in the repo
// named like screenshots. An app with no AppImage icon gets one from the repo's files (see repoIcon).
//
// What was found is kept per release file, so an app is read again only when it ships a new version.

// facts is what enrich found for one release file.
type facts struct {
	Name        string               `json:"name,omitempty"`
	Summary     string               `json:"summary,omitempty"`
	Description string               `json:"description,omitempty"`
	Categories  []string             `json:"categories,omitempty"`
	Keywords    []string             `json:"keywords,omitempty"`
	Icon        string               `json:"icon,omitempty"` // file name in the icon directory
	Screenshots []catalog.Screenshot `json:"screenshots,omitempty"`
	Problem     string               `json:"problem,omitempty"` // why the AppImage couldn't be read
}

const (
	enrichWorkers  = 8
	appImageBudget = 24 << 20 // the most fetched from one AppImage
	maxIconSide    = 256      // larger PNG icons are scaled down to this
	maxSVGIcon     = 256 << 10
	maxScreenshots = 4
)

// MediaVersion changes whenever what enrich reads changes, so cached facts and cached GitHub results made
// the old way are not reused. 2: the right desktop entry when an image has several. 3: only the app's own
// metainfo, not a bundled library's. 4: screenshots from repo files, and a size check on every screenshot.
// 5: icons from repo files.
const MediaVersion = 5

// IconDir is where enrich keeps icons, under MediaDir. The publisher uploads them from here.
func IconDir(mediaDir string) string { return filepath.Join(mediaDir, "icons") }

// enrich fills in listings from their AppImages and READMEs. Failures leave a listing as it was.
func (f *Fetcher) enrich(ctx context.Context, listings []catalog.Listing) {
	work := make(chan int)
	var wg sync.WaitGroup
	var mu sync.Mutex
	read, reused, failed := 0, 0, 0
	for range enrichWorkers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := range work {
				fc, fresh := f.factsFor(ctx, listings[i])
				mu.Lock()
				switch {
				case !fresh:
					reused++
				case fc.Problem != "":
					failed++
					read++
				default:
					read++
				}
				mu.Unlock()
				f.apply(&listings[i], fc)
			}
		}()
	}
	for i := range listings {
		work <- i
	}
	close(work)
	wg.Wait()
	if f.Log != nil {
		f.Log.Info("github apps described", "read", read, "reused", reused, "appimage_unreadable", failed)
	}
}

// factsFor returns the facts for a listing, from the cache when they are there. fresh is true when they
// were read just now.
func (f *Fetcher) factsFor(ctx context.Context, l catalog.Listing) (fc facts, fresh bool) {
	key := l.Ref
	if len(l.Assets) > 0 {
		key = l.Assets[0].URL
	}
	sum := sha256.Sum256([]byte(fmt.Sprintf("v%d|%s", MediaVersion, key)))
	cache := filepath.Join(f.MediaDir, "facts", hex.EncodeToString(sum[:])+".json")
	now := time.Now()
	if raw, err := os.ReadFile(cache); err == nil && json.Unmarshal(raw, &fc) == nil {
		os.Chtimes(cache, now, now) // in use: keep it from cache pruning
		if fc.Icon != "" {
			os.Chtimes(filepath.Join(IconDir(f.MediaDir), fc.Icon), now, now)
		}
		return fc, false
	}

	fc = f.read(ctx, l)
	if ctx.Err() != nil {
		return fc, true // cut short: don't remember a partial read
	}
	if raw, err := json.Marshal(fc); err == nil && os.MkdirAll(filepath.Dir(cache), 0o755) == nil {
		os.WriteFile(cache, raw, 0o644)
	}
	return fc, true
}

// read looks inside a listing's AppImage, and at its README for screenshots.
func (f *Fetcher) read(ctx context.Context, l catalog.Listing) facts {
	var fc facts
	for _, a := range l.Assets {
		if a.Kind != catalog.AssetAppImage {
			continue
		}
		c, cancel := context.WithTimeout(ctx, 2*time.Minute)
		info, err := appimage.InspectAt(rangeread.New(c, f.Client, a.URL, a.Size, appImageBudget))
		cancel()
		if err != nil {
			fc.Problem = err.Error()
			break
		}
		fc.Name = info.Desktop["Name"]
		fc.Summary = firstSentence(info.Desktop["Comment"])
		fc.Categories = splitList(info.Desktop["Categories"])
		fc.Keywords = splitList(info.Desktop["Keywords"])
		if len(info.Metainfo) > 0 {
			f.fromMetainfo(&fc, info.Metainfo)
		}
		if name, err := f.saveIcon(info.Icon, info.IconExt); err == nil {
			fc.Icon = name
		} else if len(info.Icon) > 0 && f.Log != nil {
			f.Log.Debug("github icon skipped", "repo", l.Ref, "err", err)
		}
		break
	}
	if len(fc.Screenshots) == 0 {
		fc.Screenshots = f.readmeScreenshots(ctx, l.Ref)
	}
	// The repo's files are listed only when needed, and at most once.
	var files []string
	listed := false
	list := func() []string {
		if !listed {
			files, _ = f.repoFiles(ctx, l.Ref)
			listed = true
		}
		return files
	}
	if len(fc.Screenshots) == 0 {
		fc.Screenshots = f.repoScreenshots(ctx, l.Ref, list())
	}
	if fc.Icon == "" {
		fc.Icon = f.repoIcon(ctx, l.Ref, list())
	}
	return fc
}

// fromMetainfo fills in what the app's AppStream metainfo says.
func (f *Fetcher) fromMetainfo(fc *facts, raw []byte) {
	appstream.Parse(bytes.NewReader(raw), func(c *appstream.Component) error {
		if c.Name != "" {
			fc.Name = c.Name
		}
		if c.Summary != "" {
			fc.Summary = c.Summary
		}
		fc.Description = c.Description
		if len(c.Categories) > 0 {
			fc.Categories = c.Categories
		}
		fc.Keywords = append(fc.Keywords, c.Keywords...)
		for _, s := range c.Screenshots {
			if strings.HasPrefix(s.Source, "https://") && len(fc.Screenshots) < maxScreenshots {
				fc.Screenshots = append(fc.Screenshots, catalog.Screenshot{URL: s.Source, Caption: s.Caption})
			}
		}
		return io.EOF // the first component is the app
	})
}

// saveIcon writes an icon into the icon directory under the hash of its contents, scaling large PNGs down,
// and returns its file name.
func (f *Fetcher) saveIcon(b []byte, ext string) (string, error) {
	switch ext {
	case ".png":
		cfg, err := png.DecodeConfig(bytes.NewReader(b))
		if err != nil {
			return "", err
		}
		if cfg.Width > maxIconSide || cfg.Height > maxIconSide {
			img, err := png.Decode(bytes.NewReader(b))
			if err != nil {
				return "", err
			}
			scale := float64(maxIconSide) / float64(max(cfg.Width, cfg.Height))
			dst := image.NewNRGBA(image.Rect(0, 0, max(1, int(float64(cfg.Width)*scale)), max(1, int(float64(cfg.Height)*scale))))
			draw.CatmullRom.Scale(dst, dst.Bounds(), img, img.Bounds(), draw.Src, nil)
			var buf bytes.Buffer
			if err := png.Encode(&buf, dst); err != nil {
				return "", err
			}
			b = buf.Bytes()
		}
	case ".svg":
		if len(b) > maxSVGIcon {
			return "", fmt.Errorf("svg icon of %d bytes is too large", len(b))
		}
	default:
		return "", fmt.Errorf("no icon")
	}
	sum := sha256.Sum256(b)
	name := hex.EncodeToString(sum[:]) + ext
	dir := IconDir(f.MediaDir)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	return name, os.WriteFile(filepath.Join(dir, name), b, 0o644)
}

// apply puts facts into a listing. The repo's own description wins over the app's, as it's what GitHub shows.
func (f *Fetcher) apply(l *catalog.Listing, fc facts) {
	if n := strings.TrimSpace(fc.Name); n != "" && len(n) <= 60 {
		l.Name = n
	}
	if l.Summary == "" {
		l.Summary = fc.Summary
	}
	if l.Description == "" {
		l.Description = fc.Description
	}
	if len(l.Categories) == 0 {
		l.Categories = fc.Categories
	}
	l.Keywords = append(l.Keywords, fc.Keywords...)
	if len(l.Screenshots) == 0 {
		l.Screenshots = fc.Screenshots
	}
	if fc.Icon != "" && f.IconBase != "" {
		l.IconURL = strings.TrimSuffix(f.IconBase, "/") + "/icons/" + fc.Icon
	}
}

// readmeImage matches Markdown and HTML images: ![alt](src "title") and <img ... src="src" ...>.
var readmeImage = regexp.MustCompile(`!\[([^\]]*)\]\(\s*<?([^)\s>]+)>?(?:\s+"[^"]*")?\s*\)|<img\s[^>]*?src\s*=\s*["']([^"']+)["'][^>]*>`)

// notScreenshot matches image addresses that are badges, logos and the like rather than screenshots.
var notScreenshot = regexp.MustCompile(`(?i)badge|shields\.io|badgen|logo|icon|banner|sponsor|donate|kofi|ko-fi|` +
	`buymeacoffee|patreon|opencollective|star-history|contrib\.rocks|codecov|actions/workflows|\.svg(\?|$)|` +
	`emoji|avatar|flathub\.org/(api|assets)|download-button|get-it|store`)

var hintsScreenshot = regexp.MustCompile(`(?i)screen|preview|demo|showcase`)

// readmeScreenshots returns up to maxScreenshots likely screenshots from a repo's README: images that look
// like screenshots by name first, then other pictures, never badges or logos.
func (f *Fetcher) readmeScreenshots(ctx context.Context, repo string) []catalog.Screenshot {
	md, base, err := f.readme(ctx, repo)
	if err != nil {
		return nil
	}
	var hinted, other []catalog.Screenshot
	seen := map[string]bool{}
	checked := 0
	for _, m := range readmeImage.FindAllStringSubmatch(md, -1) {
		alt, src := m[1], m[2]
		if src == "" {
			src = m[3]
		}
		u := resolveImage(src, base)
		if u == "" || seen[u] || notScreenshot.MatchString(u) || notScreenshot.MatchString(alt) {
			continue
		}
		seen[u] = true
		if checked++; checked > 3*maxScreenshots || !f.isPicture(ctx, u) {
			continue
		}
		shot := catalog.Screenshot{URL: u}
		if hintsScreenshot.MatchString(alt) || hintsScreenshot.MatchString(path.Base(u)) {
			hinted = append(hinted, shot)
		} else {
			other = append(other, shot)
		}
	}
	return append(hinted, other...)[:min(maxScreenshots, len(hinted)+len(other))]
}

// Screenshots smaller than this, or narrower than 1:3, are icons, buttons or strips, not screenshots.
// Pictures exactly the size of a social card (GitHub's 1280x640, the usual 1200x630) are logo cards.
const (
	minShotWidth  = 480
	minShotHeight = 270
	pictureHead   = 128 << 10 // enough of a picture to find its size
)

// isPicture checks that an address serves a picture (not a video or a page) of a sensible size, from the
// start of the file.
func (f *Fetcher) isPicture(ctx context.Context, u string) bool {
	c, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(c, http.MethodGet, u, nil)
	if err != nil {
		return false
	}
	req.Header.Set("User-Agent", fetch.UserAgent)
	req.Header.Set("Range", fmt.Sprintf("bytes=0-%d", pictureHead-1))
	resp, err := f.Client.Do(req)
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	ct := resp.Header.Get("Content-Type")
	if (resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusPartialContent) ||
		!strings.HasPrefix(ct, "image/") || strings.Contains(ct, "svg") || pictureSize(resp) >= 15<<20 {
		return false
	}
	cfg, _, err := image.DecodeConfig(io.LimitReader(resp.Body, pictureHead))
	if err != nil {
		return false
	}
	w, h := cfg.Width, cfg.Height
	if (w == 1280 && h == 640) || (w == 1200 && h == 630) {
		return false
	}
	return w >= minShotWidth && h >= minShotHeight && w <= 3*h && h <= 3*w
}

// pictureSize is a response's whole file size: the total in Content-Range for a partial response.
func pictureSize(resp *http.Response) int64 {
	if _, total, ok := strings.Cut(resp.Header.Get("Content-Range"), "/"); ok {
		if n, err := strconv.ParseInt(total, 10, 64); err == nil {
			return n
		}
	}
	return resp.ContentLength
}

// resolveImage makes an image address absolute, and returns "" for anything that isn't a picture on the
// web: data URLs, plain http, and files that aren't images.
func resolveImage(src, base string) string {
	u, err := url.Parse(strings.TrimSpace(src))
	if err != nil {
		return ""
	}
	if !u.IsAbs() {
		b, err := url.Parse(base)
		if err != nil {
			return ""
		}
		u = b.ResolveReference(u)
	}
	if u.Scheme != "https" {
		return ""
	}
	// github.com/<owner>/<repo>/blob/<ref>/<path> is a page; the picture is at raw.githubusercontent.com.
	if u.Host == "github.com" {
		if parts := strings.SplitN(strings.TrimPrefix(u.Path, "/"), "/", 4); len(parts) == 4 &&
			(strings.HasPrefix(parts[2], "blob") || strings.HasPrefix(parts[2], "raw")) {
			ref, rest, _ := strings.Cut(parts[3], "/")
			u = &url.URL{Scheme: "https", Host: "raw.githubusercontent.com",
				Path: "/" + parts[0] + "/" + parts[1] + "/" + ref + "/" + rest}
		}
	}
	// GitHub's attachment links have no file extension, and are almost always screenshots or recordings.
	if u.Host == "github.com" && strings.HasPrefix(u.Path, "/user-attachments/assets/") {
		return u.String()
	}
	switch strings.ToLower(path.Ext(u.Path)) {
	case ".png", ".jpg", ".jpeg", ".gif", ".webp":
		return u.String()
	}
	return ""
}

// Repo files that are screenshots: by name, then a demo picture. ("preview" is mostly a social card.) notRepoShot leaves out
// pictures in vendored code, tests, docs of other things, and icons.
var (
	repoShotName = regexp.MustCompile(`(?i)screen.?shots?|(^|/)screens?(/|[-_.])|(^|/)demo\.(png|jpe?g|webp|gif)$`)
	repoPicture  = regexp.MustCompile(`(?i)\.(png|jpe?g|webp|gif)$`)
	notRepoShot  = regexp.MustCompile(`(?i)(^|/)(node_modules|vendor|3rdparty|third_party|thirdparty|external|extern|deps|` +
		`\.repos|\.yarn|tests?|testdata|fixtures?|__tests__|__screenshots__|spec|e2e|examples?|samples?|blog|contribute|` +
		`bindings|plugins|evidence|rfcs?|case-studies|covers?|themes?|locales?|i18n|build|dist|out|Pods)/|` +
		`icon|logo|favicon|badge|avatar|@[23]x\.`)
)

const maxRepoShotDepth = 4 // folders deep

// repoScreenshots returns up to maxScreenshots picture files in a repo named like screenshots, such as
// screenshots/main.png or docs/screenshot-dark.png: shallowest first, and "screenshot" before "demo".
func (f *Fetcher) repoScreenshots(ctx context.Context, repo string, paths []string) []catalog.Screenshot {
	type cand struct {
		path        string
		rank, depth int
	}
	var cands []cand
	for _, p := range paths {
		depth := strings.Count(p, "/")
		if depth > maxRepoShotDepth || !repoPicture.MatchString(p) || !repoShotName.MatchString(p) ||
			notRepoShot.MatchString(p) {
			continue
		}
		rank := 2
		if m := strings.ToLower(repoShotName.FindString(p)); strings.Contains(m, "shot") {
			rank = 0
		} else if strings.Contains(m, "screen") {
			rank = 1
		}
		cands = append(cands, cand{p, rank, depth})
	}
	sort.SliceStable(cands, func(i, j int) bool {
		a, b := cands[i], cands[j]
		if a.rank != b.rank {
			return a.rank < b.rank
		}
		if a.depth != b.depth {
			return a.depth < b.depth
		}
		return a.path < b.path
	})
	var shots []catalog.Screenshot
	for i, c := range cands {
		if i >= 3*maxScreenshots || len(shots) == maxScreenshots {
			break
		}
		u := f.rawURL(repo, c.path)
		if f.isPicture(ctx, u) {
			shots = append(shots, catalog.Screenshot{URL: u})
		}
	}
	return shots
}

// rawURL is the address of a file in a repo's default branch.
func (f *Fetcher) rawURL(repo, file string) string {
	raw := f.RawEndpoint
	if raw == "" {
		raw = "https://raw.githubusercontent.com"
	}
	segs := strings.Split(file, "/")
	for k := range segs {
		segs[k] = url.PathEscape(segs[k])
	}
	return raw + "/" + repo + "/HEAD/" + strings.Join(segs, "/")
}

// repoFiles lists the files in a repo's default branch (GitHub stops at about 100,000).
func (f *Fetcher) repoFiles(ctx context.Context, repo string) ([]string, error) {
	var body struct {
		Tree []struct {
			Path string `json:"path"`
			Type string `json:"type"`
		} `json:"tree"`
	}
	if err := f.rest(ctx, "/repos/"+repo+"/git/trees/HEAD?recursive=1", 32<<20, &body); err != nil {
		return nil, err
	}
	var out []string
	for _, e := range body.Tree {
		if e.Type == "blob" {
			out = append(out, e.Path)
		}
	}
	return out, nil
}

// rest gets a GitHub REST API path into v, reading at most limit bytes.
func (f *Fetcher) rest(ctx context.Context, path string, limit int64, v any) error {
	api := f.RESTEndpoint
	if api == "" {
		api = "https://api.github.com"
	}
	c, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(c, http.MethodGet, api+path, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("Authorization", "Bearer "+f.Token)
	req.Header.Set("User-Agent", fetch.UserAgent)
	resp, err := f.Client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("%s: %s", path, resp.Status)
	}
	return json.NewDecoder(io.LimitReader(resp.Body, limit)).Decode(v)
}

// readme returns a repo's README as text, and the address relative images in it are resolved against.
func (f *Fetcher) readme(ctx context.Context, repo string) (string, string, error) {
	var body struct {
		Content     string `json:"content"`
		Encoding    string `json:"encoding"`
		DownloadURL string `json:"download_url"`
	}
	if err := f.rest(ctx, "/repos/"+repo+"/readme", 4<<20, &body); err != nil {
		return "", "", err
	}
	if body.Encoding != "base64" {
		return "", "", fmt.Errorf("readme: encoding %q", body.Encoding)
	}
	text, err := decodeBase64Lines(body.Content)
	if err != nil {
		return "", "", err
	}
	return text, body.DownloadURL, nil
}

// decodeBase64Lines decodes base64 that GitHub wraps at 60 characters.
func decodeBase64Lines(s string) (string, error) {
	b, err := base64.StdEncoding.DecodeString(strings.ReplaceAll(s, "\n", ""))
	return string(b), err
}

// splitList splits a desktop entry list ("Office;Utility;").
func splitList(s string) []string {
	var out []string
	for _, v := range strings.Split(s, ";") {
		if v = strings.TrimSpace(v); v != "" {
			out = append(out, v)
		}
	}
	return out
}

// firstSentence shortens a desktop entry comment to its first sentence, for a one-line summary.
func firstSentence(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.Index(s, ". "); i > 0 {
		s = s[:i+1]
	}
	if len(s) > 140 {
		s = strings.TrimSpace(s[:137]) + "…"
	}
	return s
}
