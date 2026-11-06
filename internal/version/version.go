// Package version is the version of this build of uas, set by the release build:
//
//	go build -ldflags "-X github.com/tyler-builds/uas/internal/version.Version=0.2.0"
package version

// Version is "dev" for builds from source.
var Version = "dev"

// Release reports whether this is a released build, which can update itself.
func Release() bool { return Version != "dev" }
