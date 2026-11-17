package appearance

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	looks "github.com/tyler-builds/uas/internal/appearance"
	"github.com/tyler-builds/uas/internal/catalog"
	"github.com/tyler-builds/uas/internal/client/shellext"
)

// GNOME Shell themes. A Shell theme is a theme folder with a gnome-shell/gnome-shell.css, which most GTK
// themes on Pling carry, so a GTK theme uas installs is often a Shell theme too. GNOME loads one only through
// its User Themes extension, whose setting (org.gnome.shell.extensions.user-theme name) holds the theme's
// folder name and is KeyShellTheme here. uas never turns the extension on, or installs it, without being
// asked: Use refuses a Shell theme while it isn't running, with a *NeedsUserThemesError, and
// EnableUserThemes does what the user agrees to.

// UserThemesUUID is the User Themes extension's UUID.
const UserThemesUUID = "user-theme@gnome-shell-extensions.gcampax.github.com"

// ShellNote is the one line said where a Shell theme is chosen.
const ShellNote = "Shell themes are made for particular GNOME versions; one made for an older GNOME may not look " +
	"right."

// shellRoots are the folders User Themes finds Shell themes in, in its order: ~/.themes,
// $XDG_DATA_HOME/themes, then each of the system's. A theme it finds first hides one of the same name.
func (in *Installer) shellRoots() []themeRoot {
	var roots []themeRoot
	if in.Home != "" {
		roots = append(roots, themeRoot{filepath.Join(in.Home, ".themes"), true})
	}
	roots = append(roots, themeRoot{in.ThemesDir(), true})
	for _, d := range in.SystemData {
		roots = append(roots, themeRoot{filepath.Join(d, "themes"), false})
	}
	return roots
}

// themeRoot is a folder themes are found in.
type themeRoot struct {
	dir  string
	user bool
}

// hasShellCSS reports whether a theme folder has a GNOME Shell theme: gnome-shell/gnome-shell.css.
func hasShellCSS(dir string) bool {
	fi, err := os.Stat(filepath.Join(dir, "gnome-shell", "gnome-shell.css"))
	return err == nil && fi.Mode().IsRegular()
}

// ShellThemes lists the GNOME Shell themes User Themes can find, in its order (see shellRoots), where a theme
// it would find first hides one of the same name. By is the GTK theme install of uas's a theme came with.
// They are sorted by name.
func (in *Installer) ShellThemes() ([]Theme, error) {
	st, err := in.State.Load()
	if err != nil {
		return nil, err
	}
	by := map[string]string{}
	for _, rec := range st.Appearance {
		if catalog.Kind(rec.Kind) == catalog.KindGTKTheme {
			for _, t := range rec.Themes {
				by[t] = rec.ID
			}
		}
	}
	seen := map[string]bool{}
	var out []Theme
	for _, r := range in.shellRoots() {
		entries, err := os.ReadDir(r.dir)
		if err != nil {
			continue
		}
		for _, e := range entries {
			name := e.Name()
			if seen[name] || strings.HasPrefix(name, ".") {
				continue
			}
			dir := filepath.Join(r.dir, name)
			if fi, err := os.Stat(dir); err != nil || !fi.IsDir() || !hasShellCSS(dir) {
				continue
			}
			t := Theme{Name: name, Dir: dir, User: r.user, Shell: true}
			if r.user && filepath.Dir(dir) == in.ThemesDir() {
				t.By = by[name]
			}
			seen[name] = true
			out = append(out, t)
		}
	}
	sort.Slice(out, func(i, j int) bool { return strings.ToLower(out[i].Name) < strings.ToLower(out[j].Name) })
	return out, nil
}

// HasShellTheme reports whether a Shell theme of a name is where User Themes looks.
func (in *Installer) HasShellTheme(name string) bool {
	if !looks.ThemeName.MatchString(name) {
		return false
	}
	for _, r := range in.shellRoots() {
		if hasShellCSS(filepath.Join(r.dir, name)) {
			return true
		}
	}
	return false
}

// UserThemes is the state of GNOME's User Themes extension, as GNOME Shell reports it.
type UserThemes struct {
	// Known is false when GNOME Shell couldn't be asked (outside a GNOME session); nothing else is known
	// then, and uas doesn't stop a change for it.
	Known bool
	// Installed is whether the Shell has the extension, System whether it came with the system.
	Installed, System bool
	// Enabled is its own switch; ExtensionsOn the main switch, without which no extension runs.
	Enabled, ExtensionsOn bool
	// Problem is why it isn't running though it is on: it failed to load, or isn't made for this GNOME.
	Problem string
}

// Ready reports whether a Shell theme can be used: User Themes runs, or GNOME Shell couldn't be asked.
func (u UserThemes) Ready() bool {
	return !u.Known || (u.Installed && u.Enabled && u.ExtensionsOn && u.Problem == "")
}

// Why says why Shell themes can't be used now, or "".
func (u UserThemes) Why() string {
	const need = "Shell themes need GNOME's User Themes extension"
	switch {
	case u.Ready():
		return ""
	case !u.Installed:
		return need + ", which isn't installed."
	case !u.ExtensionsOn:
		return need + ", and extensions are turned off."
	case !u.Enabled:
		return need + ", which is off."
	}
	return need + ", which isn't working: " + u.Problem + "."
}

// Fix names what EnableUserThemes would do ("Turn on User Themes"), or "" when it can't help: the
// extension is on but broken, or all is well.
func (u UserThemes) Fix() string {
	switch {
	case u.Ready():
		return ""
	case !u.Installed:
		return "Install User Themes"
	case !u.ExtensionsOn && !u.Enabled:
		return "Turn on extensions and User Themes"
	case !u.ExtensionsOn:
		return "Turn on extensions"
	case !u.Enabled:
		return "Turn on User Themes"
	}
	return ""
}

// NeedsUserThemesError is returned when a Shell theme is used while User Themes isn't running. Nothing was
// changed.
type NeedsUserThemesError struct{ UserThemes UserThemes }

func (e *NeedsUserThemesError) Error() string { return e.UserThemes.Why() }

// UserThemes asks GNOME Shell about the User Themes extension.
func (in *Installer) UserThemes(ctx context.Context) UserThemes {
	if in.Shell == nil {
		return UserThemes{}
	}
	list, err := in.Shell.List(ctx)
	if err != nil {
		return UserThemes{}
	}
	on, err := in.Shell.UserExtensionsEnabled(ctx)
	if err != nil {
		return UserThemes{}
	}
	u := UserThemes{Known: true, ExtensionsOn: on}
	for _, e := range list {
		if e.UUID != UserThemesUUID {
			continue
		}
		u.Installed, u.System, u.Enabled = true, e.System, e.Enabled
		if e.Enabled && on {
			u.Problem = e.Problem()
		}
	}
	return u
}

// EnableUserThemes gets the User Themes extension running, as the user asked: GNOME Shell installs it from
// extensions.gnome.org when it is missing (asking the user in its own dialog), then the main switch and the
// extension's are turned on as needed. It never turns anything off.
func (in *Installer) EnableUserThemes(ctx context.Context) error {
	u := in.UserThemes(ctx)
	if !u.Known {
		return errors.New("GNOME Shell isn't answering, so its extensions can't be changed")
	}
	if u.Ready() {
		return nil
	}
	if !u.Installed {
		if err := in.Shell.Install(ctx, UserThemesUUID); err != nil {
			return err
		}
		if u = in.UserThemes(ctx); u.Ready() {
			return nil
		}
	}
	if !u.ExtensionsOn {
		if err := in.Shell.SetUserExtensionsEnabled(ctx, true); err != nil {
			return fmt.Errorf("turning extensions on: %w", err)
		}
	}
	if !u.Enabled {
		if err := in.Shell.SetEnabled(ctx, UserThemesUUID, true); err != nil {
			return err
		}
	}
	if u = in.UserThemes(ctx); !u.Ready() {
		return errors.New(u.Why())
	}
	return nil
}

// checkShell refuses a Shell theme that isn't on the system, or while User Themes isn't running. The
// Shell's own look ("") needs neither.
func (in *Installer) checkShell(ctx context.Context, value string) error {
	if value == "" {
		return nil
	}
	if !in.HasShellTheme(value) {
		return fmt.Errorf("there is no GNOME Shell theme called %s on this system", value)
	}
	if u := in.UserThemes(ctx); !u.Ready() {
		return &NeedsUserThemesError{UserThemes: u}
	}
	return nil
}

// ShellManager is GNOME Shell's extension service, which Installer.Shell holds.
type ShellManager = shellext.Manager
