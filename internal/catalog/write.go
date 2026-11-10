package catalog

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strings"
	"time"

	_ "modernc.org/sqlite" // registers the "sqlite" driver
)

const schema = `
CREATE TABLE meta (
	key   TEXT PRIMARY KEY,
	value TEXT NOT NULL
) WITHOUT ROWID;

CREATE TABLE apps (
	id          TEXT PRIMARY KEY,
	kind        TEXT NOT NULL,
	name        TEXT NOT NULL,
	summary     TEXT NOT NULL,
	description TEXT NOT NULL,
	developer   TEXT NOT NULL,
	license     TEXT NOT NULL,
	homepage    TEXT NOT NULL,
	vcs_url     TEXT NOT NULL,
	icon_url    TEXT NOT NULL,
	categories  TEXT NOT NULL, -- JSON array
	keywords    TEXT NOT NULL, -- JSON array
	screenshots TEXT NOT NULL, -- JSON array
	popularity  INTEGER NOT NULL,
	stars       INTEGER NOT NULL,
	updated     INTEGER NOT NULL, -- Unix seconds of the newest release, 0 if unknown
	-- boost multiplies the search score (bm25, where lower is better): 1 for unknown apps, up to about 2 for
	-- the most installed ones. Precomputed because SQLite's math functions may not be compiled in.
	boost       REAL NOT NULL
);

-- One row per app per source. data holds the full Listing as JSON; the other columns are for filtering.
CREATE TABLE listings (
	source       TEXT NOT NULL,
	ref          TEXT NOT NULL,
	app_id       TEXT NOT NULL REFERENCES apps(id),
	appstream_id TEXT NOT NULL,
	version      TEXT NOT NULL,
	released     INTEGER, -- Unix seconds
	verified     INTEGER NOT NULL,
	data         TEXT NOT NULL,
	PRIMARY KEY (source, ref)
);
CREATE INDEX listings_app ON listings(app_id);

CREATE TABLE app_categories (
	category TEXT NOT NULL,
	app_id   TEXT NOT NULL REFERENCES apps(id),
	PRIMARY KEY (category, app_id)
) WITHOUT ROWID;

CREATE VIRTUAL TABLE apps_fts USING fts5(
	app_id UNINDEXED,
	name,
	keywords,
	summary,
	developer,
	description,
	tokenize = 'porter unicode61 remove_diacritics 2'
);

-- Shell extensions (apps.kind = 'shell-extension') are indexed here instead of in apps_fts, so a search for
-- apps never returns them. That also keeps them from versions of uas from before extensions, which is why
-- adding this table did not change the schema version.
CREATE VIRTUAL TABLE extensions_fts USING fts5(
	app_id UNINDEXED,
	name,
	keywords,
	summary,
	developer,
	description,
	tokenize = 'porter unicode61 remove_diacritics 2'
);
`

// Write builds a catalog database at path from apps. It writes to a temporary file first and renames it into
// place, so a reader never sees a half-written catalog.
func Write(ctx context.Context, path string, apps []App, info BuildInfo) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.Remove(tmp); err != nil && !os.IsNotExist(err) {
		return err
	}
	if err := writeDB(ctx, tmp, apps, info); err != nil {
		os.Remove(tmp)
		return err
	}
	return os.Rename(tmp, path)
}

func writeDB(ctx context.Context, path string, apps []App, info BuildInfo) error {
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return err
	}
	defer db.Close()
	// One connection, so the pragmas below apply to every statement.
	db.SetMaxOpenConns(1)

	for _, stmt := range []string{"PRAGMA journal_mode = OFF", "PRAGMA synchronous = OFF", schema} {
		if _, err := db.ExecContext(ctx, stmt); err != nil {
			return fmt.Errorf("create schema: %w", err)
		}
	}

	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	info.SchemaVersion = SchemaVersion
	infoJSON, err := json.Marshal(info)
	if err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO meta (key, value) VALUES ('schema_version', ?), ('build', ?)`,
		fmt.Sprint(SchemaVersion), string(infoJSON)); err != nil {
		return err
	}

	insApp, err := tx.PrepareContext(ctx, `INSERT INTO apps
		(id, kind, name, summary, description, developer, license, homepage, vcs_url, icon_url,
		 categories, keywords, screenshots, popularity, stars, updated, boost)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`)
	if err != nil {
		return err
	}
	insListing, err := tx.PrepareContext(ctx, `INSERT INTO listings
		(source, ref, app_id, appstream_id, version, released, verified, data)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)`)
	if err != nil {
		return err
	}
	insCategory, err := tx.PrepareContext(ctx, `INSERT OR IGNORE INTO app_categories (category, app_id) VALUES (?, ?)`)
	if err != nil {
		return err
	}
	insFTS, err := tx.PrepareContext(ctx, `INSERT INTO apps_fts
		(app_id, name, keywords, summary, developer, description) VALUES (?, ?, ?, ?, ?, ?)`)
	if err != nil {
		return err
	}
	insExtFTS, err := tx.PrepareContext(ctx, `INSERT INTO extensions_fts
		(app_id, name, keywords, summary, developer, description) VALUES (?, ?, ?, ?, ?, ?)`)
	if err != nil {
		return err
	}

	for i := range apps {
		a := &apps[i]
		if _, err := insApp.ExecContext(ctx, a.ID, string(a.Kind), a.Name, a.Summary, a.Description, a.Developer,
			a.License, a.Homepage, a.VCSURL, a.IconURL,
			jsonArray(a.Categories), jsonArray(a.Keywords), jsonArray(a.Screenshots), a.Popularity, a.Stars,
			unixOrZero(a.Updated), 1+math.Log10(float64(a.Popularity)+1)/6); err != nil {
			return fmt.Errorf("insert app %s: %w", a.ID, err)
		}
		for _, l := range a.Listings {
			data, err := json.Marshal(l)
			if err != nil {
				return err
			}
			var released any
			if !l.Released.IsZero() {
				released = l.Released.Unix()
			}
			if _, err := insListing.ExecContext(ctx, string(l.Source), l.Ref, a.ID, l.AppStreamID, l.Version,
				released, l.Verified, string(data)); err != nil {
				return fmt.Errorf("insert listing %s %s: %w", l.Source, l.Ref, err)
			}
		}
		for _, c := range a.Categories {
			if _, err := insCategory.ExecContext(ctx, c, a.ID); err != nil {
				return err
			}
		}
		// The ID and categories are searchable too, through the keywords column, so "org.mozilla" finds
		// Firefox and "web browser" finds apps in the WebBrowser category.
		keywords := strings.Join(append(append(append([]string{}, a.Keywords...), a.ID),
			categoryWords(a.Categories)...), " ")
		fts := insFTS
		if a.Kind == KindShellExtension {
			fts = insExtFTS
		}
		if _, err := fts.ExecContext(ctx, a.ID, a.Name, keywords, a.Summary, a.Developer,
			a.Description); err != nil {
			return err
		}
	}
	if err := tx.Commit(); err != nil {
		return err
	}

	for _, stmt := range []string{`INSERT INTO apps_fts (apps_fts) VALUES ('optimize')`,
		`INSERT INTO extensions_fts (extensions_fts) VALUES ('optimize')`, "VACUUM"} {
		if _, err := db.ExecContext(ctx, stmt); err != nil {
			return err
		}
	}
	return db.Close()
}

func unixOrZero(t time.Time) int64 {
	if t.IsZero() {
		return 0
	}
	return t.Unix()
}

// jsonArray encodes v as JSON, writing an empty array instead of null.
func jsonArray[T any](v []T) string {
	if len(v) == 0 {
		return "[]"
	}
	b, err := json.Marshal(v)
	if err != nil {
		// Slices of strings and plain structs always marshal.
		panic(err)
	}
	return string(b)
}
