package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"time"

	"github.com/tyler-builds/uas/internal/client/session"
	"github.com/tyler-builds/uas/internal/client/updatecheck"
)

// runCheckUpdates refreshes the catalog and lists the apps with updates. With -notify it shows a desktop
// notification for updates not notified before, as the background check does.
func runCheckUpdates(ctx context.Context, w io.Writer, args []string) error {
	fs := flag.NewFlagSet("check-updates", flag.ContinueOnError)
	catalogPath := catalogFlag(fs)
	notifyFlag := fs.Bool("notify", false, "show a desktop notification for new updates")
	if _, err := parseArgs(fs, args); err != nil {
		return err
	}
	e, err := session.Load(ctx, "", *catalogPath)
	if err != nil {
		return err
	}
	r, err := updatecheck.Check(ctx, e)
	if err != nil {
		return err
	}
	if len(r.Updates) == 0 {
		fmt.Fprintln(w, "No updates.")
	} else {
		title, body := updatecheck.Message(r)
		fmt.Fprintf(w, "%s: %s\n", title, body)
	}
	if !*notifyFlag {
		return nil
	}
	gui := desktopApp()
	return updatecheck.Notify(ctx, r, time.Hour, func() error {
		if gui == "" {
			return nil
		}
		return updatecheck.OpenUpdates(gui)
	})
}

// desktopApp finds uas-gui: beside this program, else on the PATH.
func desktopApp() string {
	if exe, err := os.Executable(); err == nil {
		p := filepath.Join(filepath.Dir(exe), "uas-gui")
		if _, err := os.Stat(p); err == nil {
			return p
		}
	}
	p, _ := exec.LookPath("uas-gui")
	return p
}
