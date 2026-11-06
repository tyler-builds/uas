package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"text/tabwriter"

	"github.com/tyler-builds/uas/internal/client/session"
)

// runExport writes the installed apps to a file (or standard output), for "uas restore" elsewhere.
func runExport(ctx context.Context, w io.Writer, args []string) error {
	fs := flag.NewFlagSet("export", flag.ContinueOnError)
	catalogPath := catalogFlag(fs)
	out := fs.String("o", "", "write to this file instead of standard output")
	if _, err := parseArgs(fs, args); err != nil {
		return err
	}
	e, err := session.Load(ctx, "", *catalogPath)
	if err != nil {
		return err
	}
	ex, err := e.Export(ctx)
	if err != nil {
		return err
	}
	if *out == "" {
		return session.WriteExport(w, ex)
	}
	f, err := os.Create(*out)
	if err != nil {
		return err
	}
	if err := session.WriteExport(f, ex); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	fmt.Fprintf(os.Stderr, "Saved %d apps to %s. Install them elsewhere with \"uas restore %s\".\n",
		len(ex.Apps), *out, *out)
	return nil
}

// runRestore installs the apps of an export that aren't installed yet.
func runRestore(ctx context.Context, w io.Writer, args []string) error {
	fs := flag.NewFlagSet("restore", flag.ContinueOnError)
	catalogPath := catalogFlag(fs)
	yes := fs.Bool("y", false, "do not ask for confirmation")
	dry := fs.Bool("n", false, "only show what would be installed")
	files, err := parseArgs(fs, args)
	if err != nil {
		return err
	}
	if len(files) != 1 {
		return errors.New("restore: give one export file (from \"uas export\")")
	}
	f, err := os.Open(files[0])
	if err != nil {
		return err
	}
	ex, err := session.ReadExport(f)
	f.Close()
	if err != nil {
		return err
	}
	e, err := session.Load(ctx, "", *catalogPath)
	if err != nil {
		return err
	}
	plan, err := e.PlanRestore(ctx, ex)
	if err != nil {
		return err
	}
	tw := tabwriter.NewWriter(w, 0, 4, 2, ' ', 0)
	fmt.Fprintln(tw, "NAME\tSOURCE\tACTION\t")
	var todo []session.RestoreItem
	for _, it := range plan {
		src := it.App.Source.Name()
		if it.Action == session.RestoreInstall {
			src = it.Listing.Source.Name()
			todo = append(todo, it)
		}
		action := string(it.Action)
		if it.Note != "" {
			action += ": " + it.Note
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\t\n", it.App.Name, src, action)
	}
	tw.Flush()
	if *dry {
		return nil
	}
	if len(todo) == 0 && len(ex.FlatpakOverrides) == 0 {
		fmt.Fprintln(w, "Nothing to install.")
		return nil
	}
	if len(todo) > 0 && !*yes {
		ok, err := confirm(fmt.Sprintf("Install %d apps?", len(todo)))
		if err != nil || !ok {
			return err
		}
	}
	var errs []error
	restart := false
	for _, it := range todo {
		fmt.Fprintf(w, "Installing %s from %s\n", it.App.Name, it.Listing.Source.Name())
		p := newProgressPrinter()
		err := e.Install(ctx, it.Listing, p.report)
		p.done()
		if err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", it.App.Name, err))
			fmt.Fprintln(w, "  failed:", err)
			continue
		}
		restart = restart || e.NeedsRestart(it.Listing.Source)
	}
	if ids, err := session.RestoreOverrides(ex); err != nil {
		errs = append(errs, err)
	} else if len(ids) > 0 {
		n := fmt.Sprintf("%d apps", len(ids))
		if len(ids) == 1 {
			n = "1 app"
		}
		fmt.Fprintf(w, "Restored your Flatpak permission changes for %s.\n", n)
	}
	if restart {
		fmt.Fprintln(w, "Restart your computer to finish.")
	}
	return errors.Join(errs...)
}
