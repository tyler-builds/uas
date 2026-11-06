package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"

	"github.com/tyler-builds/uas/internal/client/selfupdate"
	"github.com/tyler-builds/uas/internal/client/session"
	"github.com/tyler-builds/uas/internal/version"
)

// runSelfUpdate updates uas itself to the latest release.
func runSelfUpdate(ctx context.Context, w io.Writer, args []string) error {
	fs := flag.NewFlagSet("self-update", flag.ContinueOnError)
	check := fs.Bool("check", false, "only say whether a newer uas is available")
	if _, err := parseArgs(fs, args); err != nil {
		return err
	}
	e, err := session.Load(ctx, "", "")
	if err != nil {
		return err
	}
	u := e.SelfUpdater()
	a, err := u.Check(ctx)
	if errors.Is(err, selfupdate.ErrDevBuild) {
		return err
	}
	if err != nil {
		return fmt.Errorf("check for a newer uas: %w", err)
	}
	if a == nil {
		fmt.Fprintf(w, "uas %s is the latest version.\n", version.Version)
		return nil
	}
	if *check {
		fmt.Fprintf(w, "uas %s is available (this is %s). Run \"uas self-update\" to install it.\n",
			a.Release.Version, version.Version)
		return nil
	}
	return u.Install(ctx, a, func(msg string) { fmt.Fprintln(w, msg) })
}
