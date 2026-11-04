package appimage

import (
	"errors"
	"fmt"
	"strings"
)

// Entry describes the launcher uas writes for an installed AppImage.
type Entry struct {
	Name     string
	Comment  string
	AppImage string // absolute path of the AppImage
	Icon     string // absolute path of the icon, or a theme icon name
	Source   string // for example "github:owner/repo", recorded as X-UAS-Source
}

// DesktopFile returns a desktop entry for an installed AppImage.
//
// Only descriptive keys are copied from the AppImage's own entry (from). Exec is always rebuilt to run the
// AppImage itself, keeping only a file or URL placeholder from the original, so a crafted entry cannot make
// the launcher run some other command.
func DesktopFile(e Entry, from map[string]string) (string, error) {
	if strings.ContainsAny(e.AppImage, "\"`$\\\n\r") || !strings.HasPrefix(e.AppImage, "/") {
		return "", errors.New("appimage: install path has characters a desktop entry cannot hold")
	}
	name := clean(from["Name"])
	if name == "" {
		name = clean(e.Name)
	}
	if name == "" {
		return "", errors.New("appimage: no name for the desktop entry")
	}
	comment := clean(from["Comment"])
	if comment == "" {
		comment = clean(e.Comment)
	}

	exec := `"` + e.AppImage + `"`
	for _, tok := range strings.Fields(from["Exec"]) {
		if tok == "%f" || tok == "%F" || tok == "%u" || tok == "%U" {
			exec += " " + tok
			break
		}
	}

	var b strings.Builder
	line := func(k, v string) {
		if v != "" {
			fmt.Fprintf(&b, "%s=%s\n", k, v)
		}
	}
	b.WriteString("[Desktop Entry]\n")
	line("Type", "Application")
	line("Name", name)
	line("Comment", comment)
	line("Exec", exec)
	line("TryExec", e.AppImage)
	line("Icon", clean(e.Icon))
	if clean(from["Terminal"]) == "true" {
		line("Terminal", "true")
	} else {
		line("Terminal", "false")
	}
	for _, k := range []string{"GenericName", "Categories", "Keywords", "MimeType", "StartupWMClass"} {
		line(k, clean(from[k]))
	}
	line("X-UAS-Source", clean(e.Source))
	return b.String(), nil
}

// clean makes a value safe for one line of a desktop entry.
func clean(s string) string {
	return strings.TrimSpace(strings.NewReplacer("\n", " ", "\r", " ").Replace(s))
}
