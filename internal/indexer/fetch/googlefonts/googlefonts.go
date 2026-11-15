// Package googlefonts lists the font families of the github.com/google/fonts repo, which is where Google
// Fonts' files come from, with whole TTF files and licences that allow redistribution.
//
// It reads the repo at the commit main points at: the recursive tree (one GitHub API call), then each
// family's METADATA.pb (a text protocol buffer) and description from raw.githubusercontent.com, kept on disk
// by their git blob hash so only changed ones are read again. Every font file is downloaded once to record
// its SHA-256 in the signed catalog (kept by blob hash too) and checked as a font. About 2.5 GB of fonts is
// too much for one run, so a run downloads at most PinBytes of new files, and a family is listed once all its
// files are pinned. Links point at the commit, so a file never changes under its checksum.
//
// The Google Fonts Developer API is not used: it needs a key, and its terms forbid embedding one in an open
// source project. Google Fonts' own site list (fonts.google.com/metadata/fonts, no key) gives each family's
// popularity rank and last change; it is optional, and the fetch works without it.
package googlefonts

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"html"
	"io"
	"log/slog"
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

	"github.com/tyler-builds/uas/internal/appearance"
	"github.com/tyler-builds/uas/internal/catalog"
	"github.com/tyler-builds/uas/internal/indexer/fetch"
)

// The places read.
const (
	API      = "https://api.github.com"
	Raw      = "https://raw.githubusercontent.com"
	Metadata = "https://fonts.google.com/metadata/fonts"
	Repo     = "google/fonts"
	Specimen = "https://fonts.google.com/specimen/"
)

// licenceDirs are the repo's folders of families, by licence.
var licenceDirs = map[string]string{"ofl": "OFL-1.1", "apache": "Apache-2.0", "ufl": "UFL-1.0"}

// PinVersion changes when what is recorded per file changes.
const PinVersion = 1

// Fetcher lists Google's font families.
type Fetcher struct {
	Client   *http.Client
	API      string // API, or a test server
	Raw      string // Raw, or a test server
	Metadata string // Metadata, or a test server; "-" skips it
	Token    string // a GitHub token, optional
	CacheDir string
	// PinBytes caps the bytes of new font files downloaded in one run (0: no cap).
	PinBytes int64
	// MaxFamilies caps the families read (0: no cap), the most popular first, for development.
	MaxFamilies int
	Every       time.Duration // the pause between raw file requests; 100 ms when zero
	Log         *slog.Logger

	mu       sync.Mutex
	next     time.Time
	newBytes int64
}

// Source implements fetch.Fetcher.
func (f *Fetcher) Source() catalog.SourceID { return catalog.SourceGoogleFonts }

type treeEntry struct {
	Path string `json:"path"`
	Type string `json:"type"`
	SHA  string `json:"sha"`
	Size int64  `json:"size"`
}

// family is one family folder of the tree.
type family struct {
	dir     string               // "ofl/roboto"
	licence string               // SPDX
	meta    treeEntry            // METADATA.pb
	desc    *treeEntry           // DESCRIPTION.en_us.html, if any
	files   map[string]treeEntry // by file name
}

// siteFamily is what fonts.google.com says about a family.
type siteFamily struct {
	Family       string `json:"family"`
	Popularity   int    `json:"popularity"`
	LastModified string `json:"lastModified"`
}

// Fetch implements fetch.Fetcher.
func (f *Fetcher) Fetch(ctx context.Context) ([]catalog.Listing, error) {
	f.newBytes = 0
	api := or(f.API, API)
	var head struct {
		SHA string `json:"sha"`
	}
	if err := f.getJSON(ctx, api+"/repos/"+Repo+"/commits/main", true, &head); err != nil {
		return nil, fmt.Errorf("google fonts: main: %w", err)
	}
	if !regexp.MustCompile(`^[0-9a-f]{40}$`).MatchString(head.SHA) {
		return nil, fmt.Errorf("google fonts: odd commit %q", head.SHA)
	}
	var tree struct {
		Tree      []treeEntry `json:"tree"`
		Truncated bool        `json:"truncated"`
	}
	if err := f.getJSON(ctx, api+"/repos/"+Repo+"/git/trees/"+head.SHA+"?recursive=1", true, &tree); err != nil {
		return nil, fmt.Errorf("google fonts: tree: %w", err)
	}
	if tree.Truncated {
		return nil, fmt.Errorf("google fonts: GitHub cut the tree short")
	}
	families := map[string]*family{}
	for _, e := range tree.Tree {
		parts := strings.Split(e.Path, "/")
		if e.Type != "blob" || len(parts) != 3 || licenceDirs[parts[0]] == "" {
			continue
		}
		dir := parts[0] + "/" + parts[1]
		fam := families[dir]
		if fam == nil {
			fam = &family{dir: dir, licence: licenceDirs[parts[0]], files: map[string]treeEntry{}}
			families[dir] = fam
		}
		switch parts[2] {
		case "METADATA.pb":
			fam.meta = e
		case "DESCRIPTION.en_us.html":
			e := e
			fam.desc = &e
		default:
			fam.files[parts[2]] = e
		}
	}

	site := f.site(ctx)
	// One family per folder name; ofl first should two licences share one.
	byRef := map[string]*family{}
	for _, dir := range sortedKeys(families) {
		fam := families[dir]
		ref := path.Base(dir)
		if fam.meta.SHA == "" {
			continue
		}
		if prev := byRef[ref]; prev != nil && strings.HasPrefix(prev.dir, "ofl/") {
			continue
		}
		byRef[ref] = fam
	}
	type todo struct {
		ref string
		fam *family
	}
	var list []todo
	for ref, fam := range byRef {
		list = append(list, todo{ref: ref, fam: fam})
	}
	// The most popular first, so a run's budget pins them first and MaxFamilies keeps them.
	rankOf := func(name string) int {
		if s, ok := site[strings.ToLower(name)]; ok && s.Popularity > 0 {
			return s.Popularity
		}
		return 1 << 30
	}
	// Rank by the folder name until METADATA gives the real name; they nearly always agree.
	sort.Slice(list, func(i, j int) bool {
		ri, rj := rankOf(guessName(list[i].ref, site)), rankOf(guessName(list[j].ref, site))
		if ri != rj {
			return ri < rj
		}
		return list[i].ref < list[j].ref
	})
	if f.MaxFamilies > 0 && len(list) > f.MaxFamilies {
		list = list[:f.MaxFamilies]
	}

	commitRaw := or(f.Raw, Raw) + "/" + Repo + "/" + head.SHA + "/"
	var out []catalog.Listing
	postponed, dropped := 0, 0
	for _, td := range list {
		raw, err := f.blob(ctx, commitRaw+td.fam.meta.Path, td.fam.meta.SHA, "metadata")
		if err != nil {
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
			dropped++
			f.debug("family left out", "dir", td.fam.dir, "why", err)
			continue
		}
		meta, err := parseTextProto(string(raw))
		if err != nil {
			dropped++
			f.debug("family left out", "dir", td.fam.dir, "why", err)
			continue
		}
		l, ok, wait := f.listing(ctx, td.ref, td.fam, meta, site, commitRaw)
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		switch {
		case wait:
			postponed++
		case ok:
			out = append(out, l)
		default:
			dropped++
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Ref < out[j].Ref })
	if f.Log != nil {
		f.Log.Info("google fonts listed", "families", len(out), "postponed", postponed, "left_out", dropped,
			"new_bytes", f.newBytes, "commit", head.SHA, "ranked", len(site) > 0)
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("google fonts: no family kept")
	}
	return out, nil
}

// guessName turns a folder name into the family name the site uses, when the site lists one that matches.
func guessName(ref string, site map[string]siteFamily) string {
	if _, ok := site[ref]; ok {
		return ref
	}
	if s, ok := site["#"+ref]; ok {
		return s.Family
	}
	return ref
}

// site reads fonts.google.com's family list, by lower-case name, and by "#"+name without spaces for
// matching folder names. A failure returns an empty map.
func (f *Fetcher) site(ctx context.Context) map[string]siteFamily {
	out := map[string]siteFamily{}
	u := or(f.Metadata, Metadata)
	if u == "-" {
		return out
	}
	var doc struct {
		Families []siteFamily `json:"familyMetadataList"`
	}
	if err := f.getJSON(ctx, u, false, &doc); err != nil {
		if f.Log != nil {
			f.Log.Warn("google fonts: no popularity ranks", "err", err)
		}
		return out
	}
	for _, s := range doc.Families {
		out[strings.ToLower(s.Family)] = s
		out["#"+strings.ToLower(strings.ReplaceAll(s.Family, " ", ""))] = s
	}
	return out
}

// fontEntry is one font of a family's METADATA.
type fontEntry struct {
	file   string
	style  string // "normal" or "italic"
	weight int
}

// listing makes a family's listing, pinning its files. wait is true when the run's budget left a file
// unread.
func (f *Fetcher) listing(ctx context.Context, ref string, fam *family, meta []field,
	site map[string]siteFamily, commitRaw string) (l catalog.Listing, ok, wait bool) {
	name := strings.TrimSpace(get(meta, "name"))
	if name == "" {
		return l, false, false
	}
	var fonts []fontEntry
	for _, fe := range all(meta, "fonts") {
		w, _ := strconv.Atoi(get(fe.Msg, "weight"))
		fonts = append(fonts, fontEntry{file: get(fe.Msg, "filename"), style: get(fe.Msg, "style"), weight: w})
	}
	sort.SliceStable(fonts, func(i, j int) bool { return fontOrder(fonts[i]) < fontOrder(fonts[j]) })
	var assets []catalog.Asset
	seen := map[string]bool{}
	for _, fe := range fonts {
		te, ok := fam.files[fe.file]
		if !ok || seen[fe.file] || !appearance.FontFileName.MatchString(fe.file) {
			continue
		}
		seen[fe.file] = true
		u := commitRaw + fam.dir + "/" + escape(fe.file)
		p, ok := f.pin(ctx, u, te)
		if !ok {
			return l, false, true
		}
		if p.Problem != "" {
			f.debug("font file left out", "dir", fam.dir, "file", fe.file, "why", p.Problem)
			continue
		}
		assets = append(assets, catalog.Asset{Kind: catalog.AssetFont, Name: fe.file,
			URL: u, Size: te.Size, SHA256: p.SHA256, Style: styleName(fe)})
	}
	if len(assets) == 0 {
		return l, false, false
	}
	cat := categoryName(get(meta, "category"))
	l = catalog.Listing{
		Source:    catalog.SourceGoogleFonts,
		Ref:       ref,
		Kind:      catalog.KindFont,
		Name:      name,
		Developer: strings.TrimSpace(get(meta, "designer")),
		License:   fam.licence,
		StoreURL:  Specimen + strings.ReplaceAll(url.PathEscape(name), "%20", "+"),
		Assets:    assets,
	}
	if cat != "" {
		l.Categories = []string{cat}
	}
	variable := strings.Contains(assets[0].Name, "[")
	parts := []string{}
	if cat != "" {
		parts = append(parts, cat)
	}
	if variable {
		parts = append(parts, "variable")
	} else {
		parts = append(parts, plural(len(assets), "style"))
	}
	if d := l.Developer; d != "" && len(d) < 60 {
		parts = append(parts, "by "+d)
	}
	l.Summary = strings.Join(parts, " · ")
	for _, s := range all(meta, "subsets") {
		if s.Value != "menu" && s.Value != "" {
			l.Keywords = append(l.Keywords, s.Value)
		}
	}
	for _, src := range all(meta, "source") {
		if r := get(src.Msg, "repository_url"); strings.HasPrefix(r, "https://") {
			l.Homepage = r
		}
	}
	if fam.desc != nil {
		if raw, err := f.blob(ctx, commitRaw+fam.desc.Path, fam.desc.SHA, "descriptions"); err == nil {
			l.Description = plainText(string(raw))
		}
	}
	added, _ := time.Parse("2006-01-02", get(meta, "date_added"))
	l.Released = added
	sum := fam.meta.SHA
	l.Version = sum[:min(len(sum), 10)]
	if s, ok := site[strings.ToLower(name)]; ok {
		l.Rank = s.Popularity
		if t, err := time.Parse("2006-01-02", s.LastModified); err == nil {
			l.Version, l.Released = s.LastModified, t
		}
	}
	return l, true, false
}

// fontOrder sorts a family's files: the upright regular (or upright variable) file first, as the one
// previews are drawn from, then the uprights, lightest first, then the italics.
func fontOrder(fe fontEntry) int {
	n := fe.weight
	if fe.weight == 400 {
		n = 0
	}
	if fe.style == "italic" {
		n += 10000
	}
	return n
}

var weightNames = map[int]string{100: "Thin", 200: "ExtraLight", 300: "Light", 400: "Regular", 500: "Medium",
	600: "SemiBold", 700: "Bold", 800: "ExtraBold", 900: "Black"}

func styleName(fe fontEntry) string {
	italic := fe.style == "italic"
	if strings.Contains(fe.file, "[") {
		if italic {
			return "Variable Italic"
		}
		return "Variable"
	}
	w := weightNames[fe.weight]
	if w == "" {
		w = strconv.Itoa(fe.weight)
	}
	switch {
	case italic && w == "Regular":
		return "Italic"
	case italic:
		return w + " Italic"
	}
	return w
}

func categoryName(c string) string {
	switch strings.ToUpper(c) {
	case "SANS_SERIF":
		return "Sans serif"
	case "SERIF":
		return "Serif"
	case "DISPLAY":
		return "Display"
	case "HANDWRITING":
		return "Handwriting"
	case "MONOSPACE":
		return "Monospace"
	}
	return ""
}

func plural(n int, w string) string {
	if n == 1 {
		return "1 " + w
	}
	return strconv.Itoa(n) + " " + w + "s"
}

// escape escapes a file name for a URL path: variable fonts have brackets and commas in theirs.
func escape(name string) string { return url.PathEscape(name) }

// pinned is what is kept per font file, by its blob hash.
type pinned struct {
	SHA256   string    `json:"sha256,omitempty"`
	Families []string  `json:"families,omitempty"`
	Problem  string    `json:"problem,omitempty"`
	Refused  bool      `json:"refused,omitempty"`
	At       time.Time `json:"at"`
}

// pin returns a font file's SHA-256, downloading it when it isn't known; ok is false when the run's budget
// is spent.
func (f *Fetcher) pin(ctx context.Context, u string, te treeEntry) (pinned, bool) {
	cache := filepath.Join(f.CacheDir, "pins", fmt.Sprintf("v%d-%s.json", PinVersion, te.SHA))
	now := time.Now()
	var p pinned
	if raw, err := os.ReadFile(cache); err == nil && json.Unmarshal(raw, &p) == nil &&
		(p.Problem == "" || p.Refused || now.Sub(p.At) < 24*time.Hour) {
		os.Chtimes(cache, now, now)
		return p, true
	}
	if f.PinBytes > 0 && f.newBytes+te.Size > f.PinBytes {
		return pinned{}, false
	}
	f.newBytes += te.Size
	data, err := f.get(ctx, u, te.Size+1)
	switch {
	case ctx.Err() != nil:
		return pinned{Problem: "cancelled"}, true
	case err != nil:
		p = pinned{Problem: err.Error()}
	case int64(len(data)) != te.Size:
		p = pinned{Problem: fmt.Sprintf("got %d bytes, the tree says %d", len(data), te.Size)}
	default:
		sum := sha256.Sum256(data)
		fams, err := appearance.FontFamilies(data)
		if err != nil {
			p = pinned{Problem: err.Error(), Refused: true}
		} else {
			p = pinned{SHA256: hex.EncodeToString(sum[:]), Families: fams}
		}
	}
	p.At = now
	if raw, err := json.Marshal(p); err == nil && os.MkdirAll(filepath.Dir(cache), 0o755) == nil {
		os.WriteFile(cache, raw, 0o644)
	}
	return p, true
}

// blob returns a small file of the repo by its blob hash, from the cache or raw.githubusercontent.com.
func (f *Fetcher) blob(ctx context.Context, u, sha, kind string) ([]byte, error) {
	cache := filepath.Join(f.CacheDir, kind, sha)
	if b, err := os.ReadFile(cache); err == nil {
		now := time.Now()
		os.Chtimes(cache, now, now)
		return b, nil
	}
	b, err := f.get(ctx, u, 4<<20)
	if err != nil {
		return nil, err
	}
	if os.MkdirAll(filepath.Dir(cache), 0o755) == nil {
		os.WriteFile(cache, b, 0o644)
	}
	return b, nil
}

// get downloads a file from raw.githubusercontent.com, paced, with a retry for server errors.
func (f *Fetcher) get(ctx context.Context, u string, limit int64) ([]byte, error) {
	var lastErr error
	for attempt := 0; attempt < 3; attempt++ {
		if err := f.wait(ctx); err != nil {
			return nil, err
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
		if err != nil {
			return nil, err
		}
		req.Header.Set("User-Agent", fetch.UserAgent)
		resp, err := f.Client.Do(req)
		if err != nil {
			lastErr = err
			continue
		}
		b, err := io.ReadAll(io.LimitReader(resp.Body, limit))
		resp.Body.Close()
		switch {
		case err != nil:
			lastErr = err
		case resp.StatusCode == http.StatusOK:
			return b, nil
		case resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode >= 500:
			lastErr = fmt.Errorf("GET %s: %s", u, resp.Status)
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-time.After(time.Duration(attempt+1) * 5 * time.Second):
			}
		default:
			return nil, fmt.Errorf("GET %s: %s", u, resp.Status)
		}
	}
	return nil, lastErr
}

func (f *Fetcher) wait(ctx context.Context) error {
	gap := f.Every
	if gap == 0 {
		gap = 100 * time.Millisecond
	}
	f.mu.Lock()
	now := time.Now()
	at := f.next
	if at.Before(now) {
		at = now
	}
	f.next = at.Add(gap)
	f.mu.Unlock()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-time.After(time.Until(at)):
		return nil
	}
}

// getJSON reads a JSON document; github adds the GitHub API's headers and token.
func (f *Fetcher) getJSON(ctx context.Context, u string, github bool, v any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return err
	}
	req.Header.Set("User-Agent", fetch.UserAgent)
	if github {
		req.Header.Set("Accept", "application/vnd.github+json")
		if f.Token != "" {
			req.Header.Set("Authorization", "Bearer "+f.Token)
		}
	}
	resp, err := f.Client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(io.LimitReader(resp.Body, 64<<20))
	if err != nil {
		return err
	}
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("GET %s: %s", u, resp.Status)
	}
	// fonts.google.com prefixes its JSON with ")]}'" against hijacking, at times.
	b = []byte(strings.TrimPrefix(string(b), ")]}'"))
	return json.Unmarshal(b, v)
}

func (f *Fetcher) debug(msg string, args ...any) {
	if f.Log != nil {
		f.Log.Debug("google fonts: "+msg, args...)
	}
}

func or(s, def string) string {
	if s == "" {
		return def
	}
	return s
}

func sortedKeys[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

var (
	breakTags = regexp.MustCompile(`(?i)<\s*(br|/p|/div|/h[1-6]|/ul|/ol|hr)\b[^>]*>`)
	itemTags  = regexp.MustCompile(`(?i)<\s*li\b[^>]*>`)
	anyTag    = regexp.MustCompile(`<[^>]*>`)
	blankRuns = regexp.MustCompile(`\n{3,}`)
)

// plainText turns a family's HTML description into plain text, a paragraph a line.
func plainText(s string) string {
	s = strings.ReplaceAll(s, "\r\n", "\n")
	s = strings.ReplaceAll(s, "\n", " ")
	s = itemTags.ReplaceAllString(s, "\n• ")
	s = breakTags.ReplaceAllString(s, "\n\n")
	s = anyTag.ReplaceAllString(s, "")
	s = html.UnescapeString(s)
	lines := strings.Split(s, "\n")
	for i, l := range lines {
		lines[i] = strings.Join(strings.Fields(strings.ReplaceAll(l, " ", " ")), " ")
	}
	s = blankRuns.ReplaceAllString(strings.Join(lines, "\n"), "\n\n")
	return strings.TrimSpace(s)
}
