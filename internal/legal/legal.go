// Package legal holds the licenses and notices that a release of uas carries: uas's own license, and those
// of the Go modules, the Go runtime, the font and the logos built into it. packaging/linux/build-release.sh
// gathers them into bundle/ and builds with the uas_release tag, which embeds them; builds from source
// have none.
package legal

import (
	"io/fs"
	"sort"
)

// Files returns the bundled licenses by path, such as "LICENSE" or "go/LICENSE", or nil in a build from
// source.
func Files() map[string][]byte {
	if bundle == nil {
		return nil
	}
	out := map[string][]byte{}
	fs.WalkDir(bundle, "bundle", func(p string, d fs.DirEntry, err error) error {
		if err == nil && !d.IsDir() {
			if b, err := fs.ReadFile(bundle, p); err == nil {
				out[p[len("bundle/"):]] = b
			}
		}
		return nil
	})
	return out
}

// Paths returns the bundled files' paths in order, uas's own LICENSE first.
func Paths(files map[string][]byte) []string {
	var paths []string
	for p := range files {
		paths = append(paths, p)
	}
	sort.Slice(paths, func(i, j int) bool {
		if (paths[i] == "LICENSE") != (paths[j] == "LICENSE") {
			return paths[i] == "LICENSE"
		}
		return paths[i] < paths[j]
	})
	return paths
}
