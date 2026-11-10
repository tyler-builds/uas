package appimage

import (
	"errors"
	"fmt"
	"regexp"
	"strings"
)

// Entry describes the launcher uas writes for an installed AppImage.
type Entry struct {
	Name     string
	Comment  string
	AppImage string // absolute path of the AppImage
	Icon     string // absolute path of the icon, or a theme icon name
	Source   string // for example "github:owner/repo", recorded as X-UAS-Source
	// WMClass is the StartupWMClass to write when the AppImage's own entry has none: the name of that entry
	// without ".desktop", which is what most apps use as their window's app ID. The launcher has a name of
	// uas's own, so without it the desktop can't tell which launcher a window belongs to and shows a
	// generic icon in the dock.
	WMClass string
	// Args are the launch arguments to use when the AppImage's own entry has no Exec to take them from, as
	// for an AppImage uas can't read: the ones a launcher uas took over started it with.
	Args string
}

// LauncherVersion is written into each launcher as X-UAS-Launcher. Launchers from older versions are
// rewritten (see the github backend's RepairLaunchers). Version 3 keeps the app's own launch arguments.
const LauncherVersion = 3

// safeArg matches a launch argument uas will write into a launcher: plain characters only, so it needs no
// quoting and can't be read as anything but one argument.
var safeArg = regexp.MustCompile(`^[A-Za-z0-9_=.,:/+@-]+$`)

// LaunchArgs returns the arguments of a desktop entry's Exec that uas keeps in the launcher it writes: the
// ones the app starts itself with, such as "--no-sandbox" or "--gtk-single-instance=true", and one file or
// URL placeholder. Apps set these for a reason (Electron apps packed without a sandbox helper don't start
// without --no-sandbox), and they come from inside the AppImage the launcher runs anyway.
//
// The command itself is never kept: the launcher always runs the AppImage. Arguments are kept only when
// every one of them is plain (letters, digits and a few signs: no quotes, spaces, "$" or the like), so
// nothing needs quoting and nothing can be read as more than an argument. Otherwise only the placeholder
// is kept, as before uas kept arguments at all.
func LaunchArgs(exec string) string {
	toks := strings.Fields(exec)
	// The command, after any "env NAME=value" in front of it.
	i := 0
	for i < len(toks) && (toks[i] == "env" || (strings.Contains(toks[i], "=") && !strings.HasPrefix(toks[i], "-"))) {
		i++
	}
	if i < len(toks) {
		i++
	}
	return SafeArgs(strings.Join(toks[i:], " "))
}

// SafeArgs returns launch arguments (everything after the command) as uas writes them: see LaunchArgs.
func SafeArgs(args string) string {
	var out []string
	placeholder := ""
	plain := true
	for _, tok := range strings.Fields(args) {
		switch {
		case tok == "%f" || tok == "%F" || tok == "%u" || tok == "%U":
			if placeholder == "" {
				placeholder = tok
				out = append(out, tok)
			}
		case strings.HasPrefix(tok, "%") && len(tok) == 2:
			// Other field codes (%i, %c, %k) describe the launcher the app shipped, not this one.
		case safeArg.MatchString(tok):
			out = append(out, tok)
		default:
			plain = false
		}
	}
	if !plain {
		return placeholder
	}
	return strings.Join(out, " ")
}

// DesktopFile returns a desktop entry for an installed AppImage.
//
// Only descriptive keys are copied from the AppImage's own entry (from). Exec is always rebuilt to run the
// AppImage itself, with the launch arguments of the original (see LaunchArgs), so a crafted entry cannot
// make the launcher run some other command.
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
	args := LaunchArgs(from["Exec"])
	if strings.TrimSpace(from["Exec"]) == "" {
		args = SafeArgs(e.Args)
	}
	if args != "" {
		exec += " " + args
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
	for _, k := range []string{"GenericName", "Categories", "Keywords", "MimeType"} {
		line(k, clean(from[k]))
	}
	wmclass := clean(from["StartupWMClass"])
	if wmclass == "" {
		wmclass = clean(e.WMClass)
	}
	line("StartupWMClass", wmclass)
	line("X-UAS-Source", clean(e.Source))
	line("X-UAS-Launcher", fmt.Sprint(LauncherVersion))
	return b.String(), nil
}

// clean makes a value safe for one line of a desktop entry.
func clean(s string) string {
	return strings.TrimSpace(strings.NewReplacer("\n", " ", "\r", " ").Replace(s))
}

// ParseDesktop reads the unlocalized keys of a desktop entry's [Desktop Entry] group.
func ParseDesktop(s string) map[string]string { return parseDesktop(s) }

// IconExt returns ".png" or ".svg" for icon data, or "" when it is neither.
func IconExt(b []byte) string { return iconExt(b) }
