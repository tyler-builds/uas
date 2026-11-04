package homebrew

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const formulaJSON = `[
 {"name": "htop", "full_name": "htop", "tap": "homebrew/core", "desc": "Improved top", "license": "GPL-2.0-or-later",
  "homepage": "https://htop.dev/", "versions": {"stable": "3.5.3"},
  "urls": {"head": {"url": "https://github.com/htop-dev/htop.git"}},
  "bottle": {"stable": {"files": {"x86_64_linux": {}, "arm64_sonoma": {}}}}, "keg_only": false},
 {"name": "macos-only", "tap": "homebrew/core", "versions": {"stable": "1"},
  "bottle": {"stable": {"files": {"arm64_sonoma": {}}}}},
 {"name": "unpopular", "tap": "homebrew/core", "versions": {"stable": "1"},
  "bottle": {"stable": {"files": {"x86_64_linux": {}}}}},
 {"name": "zlib", "tap": "homebrew/core", "versions": {"stable": "1"}, "keg_only": true,
  "bottle": {"stable": {"files": {"x86_64_linux": {}}}}},
 {"name": "scripty", "tap": "homebrew/core", "desc": "Pure script", "versions": {"stable": "2.0"},
  "bottle": {"stable": {"files": {"all": {}}}}}
]`

func TestParse(t *testing.T) {
	installs := map[string]int{"htop": 50000, "macos-only": 9000, "unpopular": 3, "zlib": 90000, "scripty": 2000}
	ls, err := Parse(strings.NewReader(formulaJSON), installs, "x86_64_linux", 1000)
	if err != nil {
		t.Fatal(err)
	}
	if len(ls) != 2 || ls[0].Name != "htop" || ls[1].Name != "scripty" {
		t.Fatalf("listings = %+v", ls)
	}
	h := ls[0]
	if h.Ref != "htop" || h.Package != "htop" || h.Version != "3.5.3" || h.Installs != 50000 ||
		h.VCSURL != "https://github.com/htop-dev/htop" || h.Homepage != "https://htop.dev/" {
		t.Errorf("htop = %+v", h)
	}
}

func TestReadAnalytics(t *testing.T) {
	path := filepath.Join(t.TempDir(), "a.json")
	os.WriteFile(path, []byte(`{"items": [{"formula": "gh", "count": "3,090,775"},
		{"formula": "htop", "count": "10"}, {"formula": "htop --HEAD", "count": "5"}]}`), 0o644)
	got, err := readAnalytics(path)
	if err != nil {
		t.Fatal(err)
	}
	if got["gh"] != 3090775 || got["htop"] != 15 {
		t.Errorf("installs = %v", got)
	}
}
