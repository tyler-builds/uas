// Package packaging holds uas's launcher and icon. The uas program carries them and installs them itself
// (see internal/client/selfinstall), so a release is the program alone.
package packaging

import _ "embed"

// Desktop is the launcher. Its Exec line names uas-gui, which an install replaces with the full path.
//
//go:embed io.github.tyler_builds.uas.desktop
var Desktop []byte

// Icon is the app icon, installed in the hicolor theme as scalable/apps/io.github.tyler_builds.uas.svg.
//
//go:embed icons/hicolor/scalable/apps/io.github.tyler_builds.uas.svg
var Icon []byte
