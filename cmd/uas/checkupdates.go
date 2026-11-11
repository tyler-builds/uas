package main

import (
	"context"
	"flag"
	"fmt"
	"io"
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
	open, err := guiCommand()
	if err != nil {
		return err
	}
	return updatecheck.Notify(ctx, r, time.Hour, func() error { return updatecheck.OpenUpdates(open) })
}
