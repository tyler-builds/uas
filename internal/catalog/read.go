package catalog

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
	"unicode"
)

// ErrNotFound is returned when an app is not in the catalog.
var ErrNotFound = errors.New("not found")

// ErrSchema is returned by Open for a catalog in a format this build doesn't read.
var ErrSchema = errors.New("catalog format not supported")

// DB is an open, read-only catalog.
type DB struct {
	db   *sql.DB
	info BuildInfo
}

// Open opens the catalog at path read-only and checks its schema version.
func Open(path string) (*DB, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return nil, err
	}
	// Stat first: SQLite would otherwise report a missing file as a vague "unable to open" error.
	if _, err := os.Stat(abs); err != nil {
		return nil, err
	}
	dsn := "file:" + (&url.URL{Path: abs}).EscapedPath() + "?mode=ro"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	d := &DB{db: db}
	if err := d.loadMeta(); err != nil {
		db.Close()
		return nil, fmt.Errorf("open catalog %s: %w", path, err)
	}
	return d, nil
}

// Close closes the catalog.
func (d *DB) Close() error { return d.db.Close() }

// Info returns how and when the catalog was built.
func (d *DB) Info() BuildInfo { return d.info }

func (d *DB) loadMeta() error {
	var version, build string
	if err := d.db.QueryRow(`SELECT value FROM meta WHERE key = 'schema_version'`).Scan(&version); err != nil {
		return fmt.Errorf("read schema version: %w", err)
	}
	if v, err := strconv.Atoi(version); err != nil || v != SchemaVersion {
		return fmt.Errorf("%w: schema version %s, this build of uas needs %d", ErrSchema, version, SchemaVersion)
	}
	if err := d.db.QueryRow(`SELECT value FROM meta WHERE key = 'build'`).Scan(&build); err != nil {
		return fmt.Errorf("read build info: %w", err)
	}
	return json.Unmarshal([]byte(build), &d.info)
}

// Order is how lists of apps are sorted.
type Order string

// The orders.
const (
	// OrderRelevance sorts search results by how well they match, weighted by popularity. For browsing,
	// where there is no query, it is OrderPopular.
	OrderRelevance Order = "relevance"
	// OrderPopular sorts by installs, then GitHub stars.
	OrderPopular Order = "popular"
	// OrderUpdated sorts by the newest release of any listing.
	OrderUpdated Order = "updated"
	// OrderName sorts by name.
	OrderName Order = "name"
)

// orderBy returns the ORDER BY terms for an order, for queries over apps a.
func orderBy(o Order) string {
	switch o {
	case OrderPopular:
		return "a.popularity DESC, a.stars DESC, lower(a.name)"
	case OrderUpdated:
		return "a.updated DESC, lower(a.name)"
	case OrderName:
		return "lower(a.name), a.id"
	}
	return ""
}

const resultColumns = `a.id, a.name, a.summary, a.developer, a.kind, a.icon_url,
	(SELECT group_concat(DISTINCT source) FROM listings l WHERE l.app_id = a.id),
	(SELECT max(verified) FROM listings l WHERE l.app_id = a.id),
	a.popularity, a.stars, a.updated,
	(SELECT group_concat(source || char(31) || ref || char(31) || coalesce(json_extract(data, '$.package'), ''),
	        char(30)) FROM listings l WHERE l.app_id = a.id)`

// SearchOptions narrow and sort a search.
type SearchOptions struct {
	Order Order
	// Source, if set, keeps only apps with a listing from that source.
	Source SourceID
	Limit  int
}

// Search finds apps matching query. All words must match; if that finds nothing, any word may match. Each
// word also matches as a prefix, so "firef" finds Firefox. With OrderRelevance, the best match comes first:
// see search.
func (d *DB) Search(ctx context.Context, query string, opt SearchOptions) ([]SearchResult, error) {
	words := queryWords(query)
	if len(words) == 0 {
		return nil, nil
	}
	if len(words) == 1 && len([]rune(words[0])) <= 2 && synonyms[words[0]] == nil && orderBy(opt.Order) == "" {
		return d.searchShort(ctx, words[0], opt)
	}
	// A search of several words, or a word with synonyms ("office", "browser"), asks for a kind of app
	// rather than one by name.
	generic := len(words) > 1 || synonyms[words[0]] != nil
	for _, op := range []string{"AND", "OR"} {
		r := ranking{exactName: strings.Join(words, " "), byName: !generic || len(words) > 1}
		if generic {
			r.wholeWords = wholeWordQuery(words, op)
		}
		results, err := d.search(ctx, ftsQuery(words, op), r, opt)
		if err != nil || len(results) > 0 || len(words) == 1 {
			return results, err
		}
	}
	return nil, nil
}

// ranking is how search orders matches by relevance.
type ranking struct {
	exactName string // an app with this name comes first
	// byName puts apps whose names start with exactName as whole words next ("Godot Engine" for "godot",
	// before "Godots", which the stemmer also matches). A word with synonyms names a kind of app, so an app
	// named "Office Runner" is no better a match for "office" than any other.
	byName bool
	// wholeWords, when set, is an FTS query for apps whose name, summary, keywords or categories have the
	// words whole. These come next, the most installed first, so "password manager" puts Bitwarden and
	// KeePassXC ahead of rarely used apps; apps that only mention the words in their description, or only
	// start words with them ("codec" for "code"), follow by text relevance.
	wholeWords string
}

// wholeWordQuery is an FTS query for words whole (no prefix matches) in the name, keywords and summary.
func wholeWordQuery(words []string, op string) string {
	parts := make([]string, len(words))
	for i, w := range words {
		alts := []string{`"` + w + `"`}
		for _, s := range synonyms[w] {
			alts = append(alts, `"`+s+`"`)
		}
		parts[i] = "(" + strings.Join(alts, " OR ") + ")"
	}
	return "{name keywords summary} : (" + strings.Join(parts, " "+op+" ") + ")"
}

// searchShort searches for one or two letters, as the first keystrokes of a search are. Nearly every app
// matches them somewhere, so ranking by text relevance means scoring the whole catalog (30 to 50 ms) for
// little use. Instead it matches names that start with them and puts the most popular first (about 3 ms).
func (d *DB) searchShort(ctx context.Context, word string, opt SearchOptions) ([]SearchResult, error) {
	rows, err := d.db.QueryContext(ctx, `
		SELECT `+resultColumns+`
		FROM apps_fts
		JOIN apps a ON a.id = apps_fts.app_id
		WHERE apps_fts MATCH ?1
		  AND (?4 = '' OR a.id IN (SELECT app_id FROM listings WHERE source = ?4))
		ORDER BY lower(a.name) = ?2 DESC, a.popularity DESC, a.stars DESC, lower(a.name)
		LIMIT ?3`, `name : "`+word+`"*`, word, limitOr(opt.Limit), string(opt.Source))
	if err != nil {
		return nil, fmt.Errorf("search: %w", err)
	}
	return scanResults(rows)
}

func (d *DB) search(ctx context.Context, match string, r ranking, opt SearchOptions) ([]SearchResult, error) {
	// bm25 weights, one per FTS column: app_id, name, keywords, summary, developer, description.
	sort := orderBy(opt.Order)
	if sort == "" {
		sort = "lower(a.name) = ?2 DESC, "
		if r.byName {
			sort += "lower(a.name) LIKE ?2 || ' %' DESC, " // query words are letters and digits only
		}
		if r.wholeWords != "" {
			sort += "a.id IN whole DESC, CASE WHEN a.id IN whole THEN a.popularity END DESC, " +
				"CASE WHEN a.id IN whole THEN a.stars END DESC, "
		}
		sort += "bm25(apps_fts, 0, 10.0, 5.0, 2.0, 1.0, 0.5) * a.boost"
	}
	rows, err := d.db.QueryContext(ctx, `
		WITH whole AS (SELECT app_id FROM apps_fts WHERE ?5 != '' AND apps_fts MATCH ?5)
		SELECT `+resultColumns+`
		FROM apps_fts
		JOIN apps a ON a.id = apps_fts.app_id
		WHERE apps_fts MATCH ?1
		  AND (?4 = '' OR a.id IN (SELECT app_id FROM listings WHERE source = ?4))
		ORDER BY `+sort+`
		LIMIT ?3`, match, r.exactName, limitOr(opt.Limit), string(opt.Source), r.wholeWords)
	if err != nil {
		return nil, fmt.Errorf("search: %w", err)
	}
	return scanResults(rows)
}

// BrowseQuery says which apps to list.
type BrowseQuery struct {
	Category string   // an AppStream category, or "" for all
	Source   SourceID // only apps with a listing from this source, or "" for all
	Order    Order    // OrderRelevance means OrderPopular here
	// MinPopularity leaves out apps with fewer yearly installs, such as for a "new and updated" list that
	// should not fill up with apps nobody uses.
	MinPopularity int
	// AppsOnly keeps desktop apps with an icon, for browsing pages; source pages list everything.
	AppsOnly bool
	Limit    int
}

// Browse lists apps without a search.
func (d *DB) Browse(ctx context.Context, q BrowseQuery) ([]SearchResult, error) {
	sort := orderBy(q.Order)
	if sort == "" {
		sort = orderBy(OrderPopular)
	}
	rows, err := d.db.QueryContext(ctx, `
		SELECT `+resultColumns+`
		FROM apps a
		WHERE (?1 = '' OR a.id IN (SELECT app_id FROM app_categories WHERE category = ?1))
		  AND (?2 = '' OR a.id IN (SELECT app_id FROM listings WHERE source = ?2))
		  AND a.popularity >= ?3
		  AND (?4 = 0 OR (a.kind = 'desktop-application' AND a.icon_url != ''))
		ORDER BY `+sort+`
		LIMIT ?5`, q.Category, string(q.Source), q.MinPopularity, q.AppsOnly, limitOr(q.Limit))
	if err != nil {
		return nil, fmt.Errorf("browse: %w", err)
	}
	return scanResults(rows)
}

// SourceCounts returns how many apps each source offers.
func (d *DB) SourceCounts(ctx context.Context) (map[SourceID]int, error) {
	rows, err := d.db.QueryContext(ctx, `SELECT source, count(DISTINCT app_id) FROM listings GROUP BY source`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	counts := map[SourceID]int{}
	for rows.Next() {
		var s string
		var n int
		if err := rows.Scan(&s, &n); err != nil {
			return nil, err
		}
		counts[SourceID(s)] = n
	}
	return counts, rows.Err()
}

func limitOr(n int) int {
	if n <= 0 {
		return 200
	}
	return n
}

func scanResults(rows *sql.Rows) ([]SearchResult, error) {
	defer rows.Close()
	var results []SearchResult
	for rows.Next() {
		var r SearchResult
		var kind, sources string
		var updated int64
		var refs sql.NullString
		if err := rows.Scan(&r.ID, &r.Name, &r.Summary, &r.Developer, &kind, &r.IconURL, &sources, &r.Verified,
			&r.Popularity, &r.Stars, &updated, &refs); err != nil {
			return nil, err
		}
		for _, l := range strings.Split(refs.String, "\x1e") {
			if f := strings.Split(l, "\x1f"); len(f) == 3 {
				r.Refs = append(r.Refs, ListingRef{Source: SourceID(f[0]), Ref: f[1], Package: f[2]})
			}
		}
		r.Kind = Kind(kind)
		for _, s := range strings.Split(sources, ",") {
			r.Sources = append(r.Sources, SourceID(s))
		}
		if updated > 0 {
			r.Updated = time.Unix(updated, 0).UTC()
		}
		results = append(results, r)
	}
	return results, rows.Err()
}

// queryWords splits a search query into lowercase words, dropping punctuation so that FTS5 query syntax in
// the input is never interpreted.
func queryWords(q string) []string {
	return strings.FieldsFunc(strings.ToLower(q), func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsNumber(r)
	})
}

// ftsQuery joins words with op. Each word matches as a prefix, except a word with synonyms, which matches
// whole (so "ide" doesn't find "identify") or as any of its synonyms.
func ftsQuery(words []string, op string) string {
	parts := make([]string, len(words))
	for i, w := range words {
		syns, ok := synonyms[w]
		if !ok {
			parts[i] = `"` + w + `"*`
			continue
		}
		alts := []string{`"` + w + `"`}
		for _, s := range syns {
			alts = append(alts, `"`+s+`"`)
		}
		parts[i] = "(" + strings.Join(alts, " OR ") + ")"
	}
	return strings.Join(parts, " "+op+" ")
}

// AmbiguousError is returned by Resolve when a name fits more than one app.
type AmbiguousError struct {
	Name string
	IDs  []string
}

func (e *AmbiguousError) Error() string {
	return fmt.Sprintf("%q matches several apps: %s", e.Name, strings.Join(e.IDs, ", "))
}

// Resolve finds an app by its ID or by a name a source uses for it: a package name (such as a Homebrew
// formula), an AppStream ID or a listing ref.
func (d *DB) Resolve(ctx context.Context, name string) (*App, error) {
	a, err := d.App(ctx, name)
	if err == nil || !errors.Is(err, ErrNotFound) {
		return a, err
	}
	rows, err := d.db.QueryContext(ctx, `
		SELECT DISTINCT app_id FROM listings
		WHERE ref = ?1 OR appstream_id = ?1 OR json_extract(data, '$.package') = ?1
		ORDER BY app_id`, name)
	if err != nil {
		return nil, err
	}
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return nil, err
		}
		ids = append(ids, id)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	switch len(ids) {
	case 0:
		return nil, fmt.Errorf("app %q: %w", name, ErrNotFound)
	case 1:
		return d.App(ctx, ids[0])
	}
	return nil, &AmbiguousError{Name: name, IDs: ids}
}

// App returns one app with all its listings.
func (d *DB) App(ctx context.Context, id string) (*App, error) {
	a := App{ID: id}
	var kind, categories, keywords, screenshots string
	var updated int64
	err := d.db.QueryRowContext(ctx, `
		SELECT kind, name, summary, description, developer, license, homepage, vcs_url, icon_url,
		       categories, keywords, screenshots, popularity, stars, updated
		FROM apps WHERE id = ?`, id).
		Scan(&kind, &a.Name, &a.Summary, &a.Description, &a.Developer, &a.License, &a.Homepage, &a.VCSURL,
			&a.IconURL, &categories, &keywords, &screenshots, &a.Popularity, &a.Stars, &updated)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("app %q: %w", id, ErrNotFound)
	}
	if err != nil {
		return nil, err
	}
	a.Kind = Kind(kind)
	if updated > 0 {
		a.Updated = time.Unix(updated, 0).UTC()
	}
	for _, f := range []struct {
		raw string
		dst any
	}{{categories, &a.Categories}, {keywords, &a.Keywords}, {screenshots, &a.Screenshots}} {
		if err := json.Unmarshal([]byte(f.raw), f.dst); err != nil {
			return nil, fmt.Errorf("app %q: %w", id, err)
		}
	}

	rows, err := d.db.QueryContext(ctx, `SELECT data FROM listings WHERE app_id = ? ORDER BY rowid`, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var data string
		if err := rows.Scan(&data); err != nil {
			return nil, err
		}
		var l Listing
		if err := json.Unmarshal([]byte(data), &l); err != nil {
			return nil, fmt.Errorf("app %q: listing: %w", id, err)
		}
		a.Listings = append(a.Listings, l)
	}
	return &a, rows.Err()
}
