package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/tyler-builds/uas/internal/client/notify"
	"github.com/tyler-builds/uas/internal/client/selfinstall"
	"github.com/tyler-builds/uas/internal/client/updatecheck"
	"github.com/tyler-builds/uas/internal/legal"
	"github.com/tyler-builds/uas/internal/manifest"
	"github.com/tyler-builds/uas/internal/version"
)

// A release of uas is this one program. Run with no arguments from wherever it was downloaded to, it
// installs itself (see selfinstall) and opens the app.

// downloadedCopy reports whether this program is a release that isn't installed: run from a folder that
// isn't the install's and isn't on the PATH (where something else, such as a package, would have put it).
func downloadedCopy() bool {
	if !version.Release() {
		return false
	}
	exe, err := executable()
	if err != nil {
		return false
	}
	if l, err := selfinstall.Installed(); err != nil || l.Runs(exe) {
		return false
	}
	dir, err := os.Stat(filepath.Dir(exe))
	if err != nil {
		return false
	}
	for _, p := range filepath.SplitList(os.Getenv("PATH")) {
		if pi, err := os.Stat(p); err == nil && os.SameFile(dir, pi) {
			return false
		}
	}
	return true
}

// runFirstInstall installs a downloaded copy and opens the app. Opened from a file manager there is no
// terminal to show a failure in, so it shows as a notification too.
func runFirstInstall(ctx context.Context, w io.Writer) error {
	err := runSelfInstall(ctx, w, []string{"-open"})
	if err != nil && !terminal() {
		if n, nerr := notify.Send(notify.Notification{AppName: "uas", Icon: "dialog-error",
			Title: "uas", Body: err.Error()}); nerr == nil {
			n.Close()
		}
	}
	return err
}

// runSelfInstall installs this program for this user, or refreshes the install it runs from.
func runSelfInstall(ctx context.Context, w io.Writer, args []string) error {
	fs := flag.NewFlagSet("self-install", flag.ContinueOnError)
	open := fs.Bool("open", false, "open the app once it is installed")
	if rest, err := parseArgs(fs, args); err != nil {
		return err
	} else if len(rest) > 0 {
		return errors.New("self-install: takes no arguments")
	}
	exe, err := executable()
	if err != nil {
		return err
	}
	l, err := selfinstall.Installed()
	if err != nil {
		return err
	}
	// Running an older download keeps the newer uas that is installed.
	have := ""
	if !l.Runs(exe) {
		have = installedVersion(ctx, l.ProgramPath())
	}
	if newer(have, version.Version) {
		fmt.Fprintf(w, "uas %s is installed already, which is newer than this one (%s).\n", have, version.Version)
	} else {
		if err := l.Install(exe); err != nil {
			return fmt.Errorf("install uas: %w", err)
		}
		fmt.Fprintf(w, "Installed uas %s in %s. Open it from your app menu, or run \"uas\" in a terminal.\n",
			version.Version, tilde(l.Bin))
		if !onPath(l.Bin) {
			fmt.Fprintf(w, "%s isn't on your PATH yet: add it, or open a new terminal, to run the uas command.\n",
				tilde(l.Bin))
		}
	}
	if *open {
		if err := selfinstall.Detach([]string{l.ProgramPath(), "gui"}); err != nil {
			return fmt.Errorf("uas is installed, but didn't open (%w); open it from your app menu", err)
		}
	}
	return nil
}

// runSelfUninstall removes uas: what self-install installed, and the background update check.
func runSelfUninstall(ctx context.Context, w io.Writer, args []string) error {
	fs := flag.NewFlagSet("self-uninstall", flag.ContinueOnError)
	if rest, err := parseArgs(fs, args); err != nil {
		return err
	} else if len(rest) > 0 {
		return errors.New("self-uninstall: takes no arguments")
	}
	if err := uninstallSelf(ctx); err != nil {
		return err
	}
	fmt.Fprintln(w, "uas is removed. The apps you installed with it stay, and so do its settings and catalog, in "+
		"~/.config/uas and ~/.cache/uas.")
	return nil
}

// uninstallSelf removes the installed programs, the launcher, the icon and the background update check.
func uninstallSelf(ctx context.Context) error {
	l, err := selfinstall.Installed()
	if err != nil {
		return err
	}
	var errs []error
	if timer, err := updatecheck.DefaultTimer(); err != nil {
		errs = append(errs, err)
	} else if err := timer.Disable(ctx); err != nil {
		errs = append(errs, fmt.Errorf("turn off the background update check: %w", err))
	}
	if err := l.Remove(); err != nil {
		errs = append(errs, err)
	}
	return errors.Join(errs...)
}

// refreshInstall brings the install this program runs from up to date with it, after an update (see
// selfinstall.Layout.Refresh). A failure leaves the install as it was, which still works.
func refreshInstall() {
	if !version.Release() {
		return
	}
	exe, err := executable()
	if err != nil {
		return
	}
	l, err := selfinstall.Installed()
	if err != nil {
		return
	}
	if err := l.Refresh(exe); err != nil {
		fmt.Fprintln(os.Stderr, "uas: could not update its launcher:", err)
	}
}

// executable is the path of the running program, with links followed.
func executable() (string, error) {
	exe, err := os.Executable()
	if err != nil {
		return "", err
	}
	return filepath.EvalSymlinks(exe)
}

// installedVersion asks the program at path for its version: "" when there is none or it is a build from
// source.
func installedVersion(ctx context.Context, path string) string {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, path, "version").Output()
	if err != nil {
		return ""
	}
	v, _ := strings.CutPrefix(strings.TrimSpace(string(out)), "uas ")
	if _, err := manifest.ParseVersion(v); err != nil {
		return ""
	}
	return v
}

// newer reports whether release a is newer than release b; false if either is a build from source.
func newer(a, b string) bool {
	av, aerr := manifest.ParseVersion(a)
	bv, berr := manifest.ParseVersion(b)
	return aerr == nil && berr == nil && av.Newer(bv)
}

func onPath(dir string) bool {
	for _, p := range filepath.SplitList(os.Getenv("PATH")) {
		if filepath.Clean(p) == filepath.Clean(dir) {
			return true
		}
	}
	return false
}

// tilde shortens a path in the home folder to ~/….
func tilde(p string) string {
	if home, err := os.UserHomeDir(); err == nil {
		if rel, err := filepath.Rel(home, p); err == nil && !strings.HasPrefix(rel, "..") {
			return filepath.Join("~", rel)
		}
	}
	return p
}

// terminal reports whether stderr is a terminal.
func terminal() bool {
	fi, err := os.Stderr.Stat()
	return err == nil && fi.Mode()&os.ModeCharDevice != 0
}

// runLicenses prints the licenses a release carries.
func runLicenses(w io.Writer, args []string) error {
	if len(args) > 0 {
		return errors.New("licenses: takes no arguments")
	}
	files := legal.Files()
	if files == nil {
		fmt.Fprintln(w, "uas is licensed under the Apache License 2.0 (see LICENSE in its source). Builds from "+
			"source don't carry the licenses of what they are built from; releases do.")
		return nil
	}
	for i, p := range legal.Paths(files) {
		if i > 0 {
			fmt.Fprintln(w)
		}
		fmt.Fprintf(w, "==> %s <==\n%s", p, files[p])
		if b := files[p]; len(b) > 0 && b[len(b)-1] != '\n' {
			fmt.Fprintln(w)
		}
	}
	return nil
}
