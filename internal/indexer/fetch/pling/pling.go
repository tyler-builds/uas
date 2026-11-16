// Package pling lists icon, cursor and GTK themes from the Pling store family (gnome-look.org,
// store.kde.org, pling.com), through OpenDesktop's OCS API.
//
// The API lists a category's items most downloaded first, 100 a page, with each item's files. Only items with
// at least MinDownloads are kept, a lower floor for cursor themes, which are downloaded less: in November
// 2026, 10,000 downloads kept about 300 icon themes, 3,000 about 300 cursor themes, and 10,000 about 270
// GTK themes. Each file is downloaded once, through the list's own download link, checked against the MD5 Pling
// publishes, and read with internal/appearance: its SHA-256 and the themes inside are pinned in the catalog,
// and a file uas wouldn't install (a program inside, no theme, a layout it can't tell apart) is left out, with
// the item if none of its files is left. Scripts and the like inside a file are left out of it, not the file
// (see appearance), and counted on its asset. What was found is kept on disk by item, file name and MD5, so a
// file is downloaded again only when its author replaces it. Download links expire and can't be reused much, so
// the catalog holds none; the client asks Pling for a fresh one at install time.
//
// Pling publishes no rate limit; requests (pages and downloads alike) are spaced a second apart.
package pling

import (
	"context"
	"crypto/md5"
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
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/tyler-builds/uas/internal/appearance"
	"github.com/tyler-builds/uas/internal/catalog"
	"github.com/tyler-builds/uas/internal/indexer/fetch"
	"github.com/tyler-builds/uas/internal/indexer/trust"
)

// API is OpenDesktop's OCS API. The same paths under www.gnome-look.org/ocs/v1 answer 410 Gone.
const API = "https://api.opendesktop.org/ocs/v1"

// Store is where items' own pages are.
const Store = "https://www.pling.com/p/"

// Category is a Pling category and the kind of its items.
type Category struct {
	ID   int
	Kind catalog.Kind
}

// Categories are the categories indexed: full icon themes, X11 mouse themes, and GTK3/4 themes (gnome-look's
// "GTK3/4 Themes", whose items usually carry a gnome-shell, gtk-2.0, xfwm4 or cinnamon folder too). GTK 2
// themes (136), GNOME Shell themes (134) and Cinnamon themes (133) are categories of their own, and not
// indexed. GTK themes come last, so the run's budget reaches icon and cursor themes as before.
var Categories = []Category{{132, catalog.KindIconTheme}, {107, catalog.KindCursorTheme}, {135, catalog.KindGTKTheme}}

// Defaults.
const (
	DefaultMinDownloads       = 10_000
	DefaultMinCursorDownloads = 3_000
	DefaultMinGTKDownloads    = 10_000
	DefaultMaxFiles           = 30
	DefaultMaxFileBytes       = 200 << 20
	// PinVersion changes when what is recorded per file changes, so files are read again.
	PinVersion = 1
)

// every spaces requests.
const every = time.Second

// Fetcher lists Pling themes.
type Fetcher struct {
	Client *http.Client
	API    string // API, or a test server
	// CacheDir keeps what was found per file (pins/).
	CacheDir string
	// MinDownloads leaves out icon themes downloaded fewer times, MinCursorDownloads cursor themes and
	// MinGTKDownloads GTK themes; DefaultMinDownloads, DefaultMinCursorDownloads and DefaultMinGTKDownloads
	// when zero.
	MinDownloads       int
	MinCursorDownloads int
	MinGTKDownloads    int
	// MaxItems caps the items read per category (0: no cap), for development.
	MaxItems int
	// MaxFiles caps the files read per item; DefaultMaxFiles when zero. Some items carry a file per color.
	MaxFiles int
	// MaxFileBytes leaves out larger files; DefaultMaxFileBytes when zero.
	MaxFileBytes int64
	// PinCount and PinBytes cap the files downloaded in one run (0: no cap). An item with a file not yet
	// downloaded is left out until a later run has read them all.
	//
	// Each category gets a fair share first, so that icon themes, which come first and are much larger, don't
	// spend it all before any cursor or GTK theme is read: of what is left, a category may spend its part (a
	// third for icon themes, half of what is left for cursor themes, all that is left for GTK themes, the
	// last). What the shares leave then goes to the items still waiting, in the same order.
	PinCount int
	PinBytes int64
	Every    time.Duration // the pause between requests; every when zero
	Limits   appearance.Limits
	Log      *slog.Logger
	Now      func() time.Time

	mu        sync.Mutex
	next      time.Time
	pins      int
	pinBytes  int64
	capPins   int   // what f.pins may reach in this part of the run (0: no cap)
	capBytes  int64 // and f.pinBytes
	refusals  map[string]int
	postponed int
}

// outcome is what became of an item.
type outcome int

const (
	listed    outcome = iota
	leftOut           // no file of it can be installed
	postponed         // a file of it wasn't downloaded yet for the run's budget
)

// Source implements fetch.Fetcher.
func (f *Fetcher) Source() catalog.SourceID { return catalog.SourcePling }

// Fetch implements fetch.Fetcher.
func (f *Fetcher) Fetch(ctx context.Context) ([]catalog.Listing, error) {
	f.pins, f.pinBytes, f.postponed, f.refusals = 0, 0, 0, map[string]int{}
	type waiting struct {
		it   item
		kind catalog.Kind
	}
	var out []catalog.Listing
	var later []waiting
	for i, cat := range Categories {
		items, err := f.list(ctx, cat)
		if err != nil {
			return nil, fmt.Errorf("pling: category %d: %w", cat.ID, err)
		}
		f.share(len(Categories) - i)
		for _, it := range items {
			l, res := f.listing(ctx, it, cat.Kind)
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
			switch res {
			case listed:
				out = append(out, l)
			case postponed:
				later = append(later, waiting{it, cat.Kind})
			}
		}
	}
	// What the shares left goes to the items still waiting, most downloaded first within each category.
	f.capPins, f.capBytes = f.PinCount, f.PinBytes
	for _, w := range later {
		l, res := f.listing(ctx, w.it, w.kind)
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		switch res {
		case listed:
			out = append(out, l)
		case postponed:
			f.postponed++
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Ref < out[j].Ref })
	if f.Log != nil {
		f.Log.Info("pling themes listed", "listings", len(out), "new_files", f.pins, "new_bytes", f.pinBytes,
			"postponed_items", f.postponed, "refused_files", f.refusals)
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("pling: no themes kept")
	}
	return out, nil
}

// share sets the caps for the next category, of left categories still to read: an equal part of what the
// run's budget has left.
func (f *Fetcher) share(left int) {
	f.capPins, f.capBytes = 0, 0
	if f.PinCount > 0 {
		f.capPins = f.pins + ceilDiv(max(f.PinCount-f.pins, 0), left)
	}
	if f.PinBytes > 0 {
		f.capBytes = f.pinBytes + ceilDiv(max(f.PinBytes-f.pinBytes, 0), int64(left))
	}
}

func ceilDiv[T int | int64](a, b T) T { return (a + b - 1) / b }

// item is one entry of a list: its fixed fields, and its numbered download fields by name.
type item struct {
	ID          string
	Name        string
	Version     string
	Person      string
	Created     time.Time
	Changed     time.Time
	Downloads   int
	Score       int
	Summary     string
	Description string
	Tags        []string
	Excluded    bool
	Previews    []catalog.Screenshot
	Files       []file
	Category    int
	DetailPage  string
	raw         map[string]json.RawMessage
}

// file is one of an item's downloads.
type file struct {
	Slot     int
	Name     string
	Link     string
	MD5      string
	SizeKiB  int64
	Mimetype string
	Way      int
	Version  string
}

type listPage struct {
	Status     string                       `json:"status"`
	Message    string                       `json:"message"`
	TotalItems int                          `json:"totalitems"`
	Data       []map[string]json.RawMessage `json:"data"`
}

// list reads a category's items, most downloaded first, down to MinDownloads.
func (f *Fetcher) list(ctx context.Context, cat Category) ([]item, error) {
	api, minDL := f.API, f.MinDownloads
	switch cat.Kind {
	case catalog.KindCursorTheme:
		minDL = f.MinCursorDownloads
	case catalog.KindGTKTheme:
		minDL = f.MinGTKDownloads
	}
	if api == "" {
		api = API
	}
	switch {
	case minDL > 0:
	case cat.Kind == catalog.KindCursorTheme:
		minDL = DefaultMinCursorDownloads
	case cat.Kind == catalog.KindGTKTheme:
		minDL = DefaultMinGTKDownloads
	default:
		minDL = DefaultMinDownloads
	}
	var out []item
	for page := 0; ; page++ {
		q := url.Values{"categories": {strconv.Itoa(cat.ID)}, "sortmode": {"down"}, "pagesize": {"100"},
			"page": {strconv.Itoa(page)}, "format": {"json"}}
		var p listPage
		if err := f.getJSON(ctx, api+"/content/data?"+q.Encode(), &p); err != nil {
			return nil, fmt.Errorf("page %d: %w", page, err)
		}
		if p.Status != "" && p.Status != "ok" {
			return nil, fmt.Errorf("page %d: %s %s", page, p.Status, p.Message)
		}
		for _, raw := range p.Data {
			it := parseItem(raw)
			if it.Downloads < minDL {
				return out, nil
			}
			if it.Excluded || it.ID == "" {
				continue
			}
			out = append(out, it)
			if f.MaxItems > 0 && len(out) >= f.MaxItems {
				return out, nil
			}
		}
		if len(p.Data) < 100 {
			return out, nil
		}
	}
}

// parseItem reads one item of a list. The API writes numbers as strings or numbers, as it pleases.
func parseItem(raw map[string]json.RawMessage) item {
	it := item{raw: raw, ID: str(raw, "id"), Name: strings.TrimSpace(html.UnescapeString(str(raw, "name"))),
		Version: strings.TrimSpace(str(raw, "version")), Person: str(raw, "personid"),
		Downloads: num(raw, "downloads"), Score: num(raw, "score"), Summary: str(raw, "summary"),
		Description: str(raw, "description"), Excluded: num(raw, "ghns_excluded") != 0,
		Category: num(raw, "typeid"), DetailPage: str(raw, "detailpage")}
	it.Created, _ = time.Parse(time.RFC3339, str(raw, "created"))
	it.Changed, _ = time.Parse(time.RFC3339, str(raw, "changed"))
	for _, t := range strings.Split(str(raw, "tags"), ",") {
		if t = strings.TrimSpace(t); t != "" {
			it.Tags = append(it.Tags, t)
		}
	}
	for n := 1; ; n++ {
		big, small := str(raw, fmt.Sprintf("previewpic%d", n)), str(raw, fmt.Sprintf("smallpreviewpic%d", n))
		if _, ok := raw[fmt.Sprintf("previewpic%d", n)]; !ok {
			break
		}
		if strings.HasPrefix(big, "https://images.pling.com/") {
			s := catalog.Screenshot{URL: big}
			if strings.HasPrefix(small, "https://images.pling.com/") && small != big {
				s.ThumbURL = small
			}
			it.Previews = append(it.Previews, s)
		}
	}
	for n := 1; ; n++ {
		k := func(f string) string { return fmt.Sprintf("%s%d", f, n) }
		if _, ok := raw[k("downloadname")]; !ok {
			break
		}
		fl := file{Slot: n, Name: str(raw, k("downloadname")), Link: str(raw, k("downloadlink")),
			MD5: strings.ToLower(str(raw, k("downloadmd5sum"))), SizeKiB: int64(num(raw, k("downloadsize"))),
			Way: num(raw, k("downloadway")), Version: str(raw, k("download_version"))}
		for _, tag := range strings.Split(str(raw, k("downloadtags")), ",") {
			if m, ok := strings.CutPrefix(tag, "data##mimetype="); ok {
				fl.Mimetype = m
			}
		}
		it.Files = append(it.Files, fl)
	}
	return it
}

func str(raw map[string]json.RawMessage, key string) string {
	v, ok := raw[key]
	if !ok {
		return ""
	}
	var s string
	if json.Unmarshal(v, &s) == nil {
		return s
	}
	var n json.Number
	if json.Unmarshal(v, &n) == nil {
		return n.String()
	}
	return ""
}

func num(raw map[string]json.RawMessage, key string) int {
	n, _ := strconv.Atoi(strings.TrimSpace(str(raw, key)))
	return n
}

// archiveName reports whether a file looks like an archive appearance can read, by name or mimetype. Links to
// web pages ("text/html") and Windows cursor packs in .7z are skipped without a download.
func (fl file) archive() bool {
	n := strings.ToLower(fl.Name)
	for _, ext := range []string{".tar.gz", ".tgz", ".tar.xz", ".txz", ".tar.bz2", ".tbz2", ".tar.zst", ".tar",
		".zip"} {
		if strings.HasSuffix(n, ext) {
			return true
		}
	}
	switch fl.Mimetype {
	case "application/gzip", "application/x-gzip", "application/x-xz", "application/x-bzip2",
		"application/zip", "application/x-tar", "application/zstd", "application/x-compressed-tar":
		return true
	}
	return false
}

// listing makes a listing of an item, pinning its files. It is listed when a file of it can be installed;
// it is postponed, with nothing recorded, when a file wasn't downloaded yet for the run's budget.
func (f *Fetcher) listing(ctx context.Context, it item, kind catalog.Kind) (catalog.Listing, outcome) {
	maxFiles := f.MaxFiles
	if maxFiles == 0 {
		maxFiles = DefaultMaxFiles
	}
	maxBytes := f.MaxFileBytes
	if maxBytes == 0 {
		maxBytes = DefaultMaxFileBytes
	}
	if it.Name == "" {
		return catalog.Listing{}, leftOut
	}
	var assets []catalog.Asset
	type refusal struct{ file, why string }
	var refusals []refusal
	seen := map[string]bool{}
	tried := 0
	for _, fl := range it.Files {
		if fl.Way != 1 || !fl.archive() || fl.MD5 == "" || seen[fl.MD5] {
			continue
		}
		if fl.SizeKiB*1024 > maxBytes {
			refusals = append(refusals, refusal{fl.Name, "larger than the size cap"})
			continue
		}
		if tried++; tried > maxFiles {
			break
		}
		seen[fl.MD5] = true
		p, ok := f.pin(ctx, it, fl)
		if !ok {
			return catalog.Listing{}, postponed // not read yet: a later pass or run lists it whole
		}
		if p.Problem != "" {
			refusals = append(refusals, refusal{fl.Name, p.Problem})
			continue
		}
		var themes []string
		for _, t := range p.Themes {
			if t.Is(kind) {
				themes = append(themes, t.Name)
			}
		}
		if len(themes) == 0 {
			refusals = append(refusals, refusal{fl.Name, "no " + kindWord(kind) + " inside"})
			continue
		}
		assets = append(assets, catalog.Asset{Kind: catalog.AssetTheme, Name: fl.Name, Size: p.Size,
			SHA256: p.SHA256, Slot: fl.Slot, MD5: fl.MD5, Themes: themes, LeftOut: p.LeftOut})
	}
	// Each refusal is logged with the item, so that a theme missing from the catalog can be traced from the
	// run's log alone.
	for _, r := range refusals {
		f.refused(r.why)
		if f.Log != nil {
			f.Log.Info("pling file refused", "item", it.ID, "name", it.Name, "kind", kind, "file", r.file,
				"why", r.why)
		}
	}
	if len(assets) == 0 {
		if f.Log != nil {
			why := "every file refused"
			if len(refusals) == 0 {
				why = "no file uas can read (links to web pages, other formats)"
			}
			f.Log.Info("pling item left out", "item", it.ID, "name", it.Name, "kind", kind,
				"downloads", it.Downloads, "why", why)
		}
		return catalog.Listing{}, leftOut
	}
	now := time.Now()
	if f.Now != nil {
		now = f.Now()
	}
	desc := plainText(it.Description)
	tr := trust.Pling(trust.PlingFacts{Created: it.Created, Changed: it.Changed, Downloads: it.Downloads,
		Score: it.Score}, now)
	l := catalog.Listing{
		Source:      catalog.SourcePling,
		Ref:         it.ID,
		Kind:        kind,
		Name:        it.Name,
		Summary:     summary(it.Summary, desc),
		Description: desc,
		Developer:   it.Person,
		License:     license(it.Tags),
		StoreURL:    Store + it.ID + "/",
		Screenshots: it.Previews,
		Version:     it.Version,
		Released:    it.Changed,
		Installs:    it.Downloads,
		Score:       it.Score,
		Assets:      assets,
		Trust:       &tr,
	}
	for _, t := range it.Tags {
		if !strings.Contains(t, "##") && licenses[t] == "" && t != "unix" && t != "linux" {
			l.Keywords = append(l.Keywords, t)
		}
	}
	return l, listed
}

func kindWord(k catalog.Kind) string {
	switch k {
	case catalog.KindCursorTheme:
		return "cursor theme"
	case catalog.KindGTKTheme:
		return "GTK theme"
	}
	return "icon theme"
}

func (f *Fetcher) refused(why string) {
	f.mu.Lock()
	f.refusals[reasonKind(why)]++
	f.mu.Unlock()
}

// reasonKinds tally refusals by kind, without the file names the reasons carry. The first match wins.
var reasonKinds = []struct{ part, kind string }{
	{"isn't in the archive", "a link to a file missing from the archive"},
	{"outside the archive", "a path or link outside the archive"},
	{"another archive", "an archive inside the archive"},
	{"program", "a program or script inside"},
	{"script", "a program or script inside"},
	{"library", "a program or script inside"},
	{"another theme inside", "a theme inside another theme"},
	{"two of its themes", "two themes of one name"},
	{"top of the archive", "a theme with no folder"},
	{"no icon or cursor theme", "no theme inside"}, // as uas before GTK themes said it
	{"holds no theme", "no theme inside"},
	{"inside", "no theme of the item's kind inside"},
	{"take over", "a reserved theme name"},
	{"won't use", "an unusable theme name"},
	{"larger than", "too large"},
	{"unpacks to more", "too large"},
	{"more than", "too many files"},
	{"7-Zip", "an archive format uas doesn't open"},
	{"RAR", "an archive format uas doesn't open"},
	{"isn't an archive", "an archive format uas doesn't open"},
	{"MD5", "didn't match Pling's MD5"},
	{"download", "download failed"},
	{"damaged", "a damaged archive"},
}

func reasonKind(why string) string {
	for _, r := range reasonKinds {
		if strings.Contains(why, r.part) {
			return r.kind
		}
	}
	return "other"
}

// pinned is what is kept per file.
type pinned struct {
	SHA256 string             `json:"sha256,omitempty"`
	Size   int64              `json:"size,omitempty"`
	Themes []appearance.Theme `json:"themes,omitempty"`
	// LeftOut counts the files inside that could be run, which uas leaves out (appearance.Contents.LeftOut).
	LeftOut int `json:"left_out,omitempty"`
	// Problem is why the file is left out: a refusal, which stands until the file changes, or a download
	// that failed, tried again after a day.
	Problem string `json:"problem,omitempty"`
	Refused bool   `json:"refused,omitempty"`
	// Rules is the appearance.RulesVersion a refusal was made under; a refusal under older rules is checked
	// again.
	Rules int       `json:"rules,omitempty"`
	At    time.Time `json:"at"`
}

const retryProblemsAfter = 24 * time.Hour

// pin returns what is known of a file, downloading it when nothing is; ok is false when the run's budget is
// spent and it wasn't downloaded.
func (f *Fetcher) pin(ctx context.Context, it item, fl file) (pinned, bool) {
	sum := sha256.Sum256([]byte(fmt.Sprintf("v%d|%s|%s|%s", PinVersion, it.ID, fl.Name, fl.MD5)))
	cache := filepath.Join(f.CacheDir, "pins", hex.EncodeToString(sum[:])+".json")
	now := time.Now()
	var p pinned
	if raw, err := os.ReadFile(cache); err == nil && json.Unmarshal(raw, &p) == nil &&
		(p.Problem == "" || (p.Refused && p.Rules >= appearance.RulesVersion) ||
			(!p.Refused && now.Sub(p.At) < retryProblemsAfter)) {
		os.Chtimes(cache, now, now) // in use: keep it from cache pruning
		return p, true
	}
	if (f.capPins > 0 && f.pins >= f.capPins) || (f.capBytes > 0 && f.pinBytes+fl.SizeKiB*1024 > f.capBytes) {
		return pinned{}, false
	}
	f.pins++
	f.pinBytes += fl.SizeKiB * 1024
	p, left := f.download(ctx, fl)
	if ctx.Err() != nil {
		return p, true
	}
	if len(left) > 0 && f.Log != nil {
		f.Log.Info("pling file read, files that could run left out", "item", it.ID, "name", it.Name,
			"file", fl.Name, "left_out", len(left), "first", left[:min(len(left), 5)])
	}
	p.At, p.Rules = now, appearance.RulesVersion
	if raw, err := json.Marshal(p); err == nil && os.MkdirAll(filepath.Dir(cache), 0o755) == nil {
		os.WriteFile(cache, raw, 0o644)
	}
	return p, true
}

// download fetches a file into a fresh temporary folder, checks its MD5, hashes it and reads it. It also
// returns the files inside that are left out, sorted.
func (f *Fetcher) download(ctx context.Context, fl file) (pinned, []string) {
	maxBytes := f.MaxFileBytes
	if maxBytes == 0 {
		maxBytes = DefaultMaxFileBytes
	}
	if !strings.HasPrefix(fl.Link, "https://") && !strings.HasPrefix(fl.Link, "http://") {
		return pinned{Problem: "no download link"}, nil
	}
	dir, err := os.MkdirTemp("", "uas-pling-*")
	if err != nil {
		return pinned{Problem: err.Error()}, nil
	}
	defer os.RemoveAll(dir)
	tmp := filepath.Join(dir, "download")
	out, err := os.Create(tmp)
	if err != nil {
		return pinned{Problem: err.Error()}, nil
	}
	if err := f.wait(ctx); err != nil {
		out.Close()
		return pinned{Problem: err.Error()}, nil
	}
	// Pling's links aren't URL-encoded: a file name may hold a raw space.
	link := strings.ReplaceAll(fl.Link, " ", "%20")
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, link, nil)
	if err != nil {
		out.Close()
		return pinned{Problem: err.Error()}, nil
	}
	req.Header.Set("User-Agent", fetch.UserAgent)
	resp, err := f.Client.Do(req)
	if err != nil {
		out.Close()
		return pinned{Problem: err.Error()}, nil
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		out.Close()
		return pinned{Problem: "download: " + resp.Status}, nil
	}
	sh, mh := sha256.New(), md5.New()
	n, err := io.Copy(io.MultiWriter(out, sh, mh), io.LimitReader(resp.Body, maxBytes+1))
	if cerr := out.Close(); err == nil {
		err = cerr
	}
	switch {
	case err != nil:
		return pinned{Problem: "download: " + err.Error()}, nil
	case n > maxBytes:
		return pinned{Problem: "larger than the size cap", Refused: true}, nil
	case hex.EncodeToString(mh.Sum(nil)) != fl.MD5:
		return pinned{Problem: "download: the file doesn't match Pling's MD5"}, nil
	}
	p := pinned{SHA256: hex.EncodeToString(sh.Sum(nil)), Size: n}
	lim := f.Limits
	if lim.MaxEntries == 0 {
		lim = appearance.DefaultLimits
	}
	c, err := appearance.Inspect(tmp, lim)
	if err != nil {
		return pinned{Problem: err.Error(), Refused: appearance.IsRefused(err)}, nil
	}
	p.Themes, p.LeftOut = c.Themes, len(c.LeftOut)
	left := make([]string, 0, len(c.LeftOut))
	for name := range c.LeftOut {
		left = append(left, name)
	}
	sort.Strings(left)
	return p, left
}

// wait spaces requests.
func (f *Fetcher) wait(ctx context.Context) error {
	gap := f.Every
	if gap == 0 {
		gap = every
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

// getJSON fetches a JSON document, paced, retrying server errors and rate limits.
func (f *Fetcher) getJSON(ctx context.Context, u string, v any) error {
	var lastErr error
	for attempt := 0; attempt < 4; attempt++ {
		if err := f.wait(ctx); err != nil {
			return err
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
		if err != nil {
			return err
		}
		req.Header.Set("User-Agent", fetch.UserAgent)
		req.Header.Set("Accept", "application/json")
		resp, err := f.Client.Do(req)
		if err != nil {
			lastErr = err
			continue
		}
		raw, err := io.ReadAll(io.LimitReader(resp.Body, 64<<20))
		resp.Body.Close()
		switch {
		case err != nil:
			lastErr = err
		case resp.StatusCode == http.StatusOK:
			return json.Unmarshal(raw, v)
		case resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode >= 500:
			lastErr = fmt.Errorf("%s", resp.Status)
			wait := time.Duration(attempt+1) * 10 * time.Second
			if s, err := strconv.Atoi(resp.Header.Get("Retry-After")); err == nil && s > 0 && s <= 300 {
				wait = time.Duration(s) * time.Second
			}
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(wait):
			}
		default:
			return fmt.Errorf("%s: %s", resp.Status, strings.TrimSpace(string(raw[:min(len(raw), 200)])))
		}
	}
	return lastErr
}

// licenses maps Pling's licence tags to SPDX IDs.
var licenses = map[string]string{
	"gplv3": "GPL-3.0-only", "gplv3-later": "GPL-3.0-or-later", "gplv2": "GPL-2.0-only",
	"gplv2-later": "GPL-2.0-or-later", "lgplv3": "LGPL-3.0-only", "lgplv2.1": "LGPL-2.1-only",
	"lgplv2": "LGPL-2.0-only", "mit": "MIT", "mit-x11": "MIT", "bsd": "BSD-3-Clause", "apache2": "Apache-2.0",
	"apache-2.0": "Apache-2.0", "cc-by": "CC-BY-4.0", "cc-by-sa": "CC-BY-SA-4.0", "cc-by-sa-40": "CC-BY-SA-4.0",
	"cc-by-nc-sa": "CC-BY-NC-SA-4.0", "cc-by-nc": "CC-BY-NC-4.0", "cc0": "CC0-1.0", "mpl": "MPL-2.0",
	"artistic-2.0": "Artistic-2.0", "agplv3": "AGPL-3.0-only",
}

func license(tags []string) string {
	for _, t := range tags {
		if l := licenses[strings.ToLower(t)]; l != "" {
			return l
		}
	}
	return ""
}

var (
	breakTags = regexp.MustCompile(`(?i)<\s*(br|/p|/div|/h[1-6]|/ul|/ol|hr)\b[^>]*>`)
	itemTags  = regexp.MustCompile(`(?i)<\s*li\b[^>]*>`)
	anyTag    = regexp.MustCompile(`<[^>]*>`)
	blankRuns = regexp.MustCompile(`\n{3,}`)
)

// plainText turns an item's HTML description into plain text with line breaks.
func plainText(s string) string {
	s = strings.ReplaceAll(s, "\r\n", "\n")
	s = strings.ReplaceAll(s, "\n", " ")
	s = itemTags.ReplaceAllString(s, "\n• ")
	s = breakTags.ReplaceAllString(s, "\n")
	s = anyTag.ReplaceAllString(s, "")
	s = html.UnescapeString(s)
	lines := strings.Split(s, "\n")
	for i, l := range lines {
		lines[i] = strings.Join(strings.Fields(strings.ReplaceAll(l, " ", " ")), " ")
	}
	s = strings.Join(lines, "\n")
	s = blankRuns.ReplaceAllString(s, "\n\n")
	return strings.TrimSpace(s)
}

// plea matches a line asking for money rather than saying what the item is.
var plea = regexp.MustCompile(`(?i)support|donat|patreon|ko-?fi|buy me|paypal|sponsor|♥`)

// summary is the one line shown in lists: the item's own summary unless it is a plea for support, else the
// description's first sentence that isn't, cut at a word when long.
func summary(own, desc string) string {
	own = strings.Join(strings.Fields(html.UnescapeString(own)), " ")
	line := ""
	if len(own) >= 12 && !plea.MatchString(own) {
		line = own
	} else {
		for _, l := range strings.Split(desc, "\n") {
			l = strings.TrimSpace(strings.TrimPrefix(l, "•"))
			if len(l) >= 12 && !plea.MatchString(l) && !strings.HasPrefix(l, "http") {
				line = l
				break
			}
		}
	}
	const least = 15
	if len(line) > least {
		if i := strings.Index(line[least:], ". "); i >= 0 {
			line = line[:least+i+1]
		}
	}
	const most = 140
	if utf8.RuneCountInString(line) <= most {
		return line
	}
	r := []rune(line)[:most]
	cut := string(r)
	if i := strings.LastIndexByte(cut, ' '); i > most/2 {
		cut = cut[:i]
	}
	return strings.TrimRight(cut, " ,;:.") + "…"
}
