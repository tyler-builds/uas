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
	"unicode"
)

// ErrNotFound is returned when an app is not in the catalog.
var ErrNotFound = errors.New("not found")

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
		return fmt.Errorf("catalog schema version %s, this build of uas needs %d", version, SchemaVersion)
	}
	if err := d.db.QueryRow(`SELECT value FROM meta WHERE key = 'build'`).Scan(&build); err != nil {
		return fmt.Errorf("read build info: %w", err)
	}
	return json.Unmarshal([]byte(build), &d.info)
}

// Search finds apps matching query, best match first. All words must match; if that finds nothing, any word
// may match. Each word also matches as a prefix, so "firef" finds Firefox.
func (d *DB) Search(ctx context.Context, query string, limit int) ([]SearchResult, error) {
	words := queryWords(query)
	if len(words) == 0 {
		return nil, nil
	}
	results, err := d.search(ctx, ftsQuery(words, "AND"), strings.Join(words, " "), limit)
	if err != nil || len(results) > 0 || len(words) == 1 {
		return results, err
	}
	return d.search(ctx, ftsQuery(words, "OR"), strings.Join(words, " "), limit)
}

func (d *DB) search(ctx context.Context, match, exactName string, limit int) ([]SearchResult, error) {
	// bm25 weights, one per FTS column: app_id, name, keywords, summary, developer, description.
	// An exact name match always comes first.
	rows, err := d.db.QueryContext(ctx, `
		SELECT a.id, a.name, a.summary, a.kind, a.icon_url,
		       (SELECT group_concat(DISTINCT source) FROM listings l WHERE l.app_id = a.id),
		       (SELECT max(verified) FROM listings l WHERE l.app_id = a.id)
		FROM apps_fts
		JOIN apps a ON a.id = apps_fts.app_id
		WHERE apps_fts MATCH ?
		ORDER BY lower(a.name) = ? DESC, bm25(apps_fts, 0, 10.0, 5.0, 2.0, 1.0, 0.5)
		LIMIT ?`, match, exactName, limit)
	if err != nil {
		return nil, fmt.Errorf("search: %w", err)
	}
	defer rows.Close()

	var results []SearchResult
	for rows.Next() {
		var r SearchResult
		var kind, sources string
		if err := rows.Scan(&r.ID, &r.Name, &r.Summary, &kind, &r.IconURL, &sources, &r.Verified); err != nil {
			return nil, err
		}
		r.Kind = Kind(kind)
		for _, s := range strings.Split(sources, ",") {
			r.Sources = append(r.Sources, SourceID(s))
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

func ftsQuery(words []string, op string) string {
	parts := make([]string, len(words))
	for i, w := range words {
		parts[i] = `"` + w + `"*`
	}
	return strings.Join(parts, " "+op+" ")
}

// App returns one app with all its listings.
func (d *DB) App(ctx context.Context, id string) (*App, error) {
	a := App{ID: id}
	var kind, categories, keywords, screenshots string
	err := d.db.QueryRowContext(ctx, `
		SELECT kind, name, summary, description, developer, license, homepage, vcs_url, icon_url,
		       categories, keywords, screenshots
		FROM apps WHERE id = ?`, id).
		Scan(&kind, &a.Name, &a.Summary, &a.Description, &a.Developer, &a.License, &a.Homepage, &a.VCSURL,
			&a.IconURL, &categories, &keywords, &screenshots)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("app %q: %w", id, ErrNotFound)
	}
	if err != nil {
		return nil, err
	}
	a.Kind = Kind(kind)
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
