// Package paths says where the uas client keeps its files.
package paths

import (
	"os"
	"path/filepath"
)

// CatalogEnv overrides the catalog location.
const CatalogEnv = "UAS_CATALOG"

// Catalog returns the path of the local catalog database: $UAS_CATALOG if set, otherwise
// $XDG_CACHE_HOME/uas/catalog.db (usually ~/.cache/uas/catalog.db).
func Catalog() (string, error) {
	if p := os.Getenv(CatalogEnv); p != "" {
		return p, nil
	}
	dir, err := os.UserCacheDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "uas", "catalog.db"), nil
}
