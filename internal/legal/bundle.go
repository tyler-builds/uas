//go:build uas_release

package legal

import (
	"embed"
	"io/fs"
)

//go:embed all:bundle
var embedded embed.FS

var bundle fs.FS = embedded
