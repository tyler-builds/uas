package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"strings"
	"text/tabwriter"

	"github.com/tyler-builds/uas/internal/client/session"
)

// runMigrate finds AppImages that other tools or the user set up, and hands them over to uas.
func runMigrate(ctx context.Context, w io.Writer, args []string) error {
	fs := flag.NewFlagSet("migrate", flag.ContinueOnError)
	catalogPath := catalogFlag(fs)
	dry := fs.Bool("n", false, "only show what would be migrated")
	yes := fs.Bool("y", false, "do not ask for confirmation")
	all := fs.Bool("all", false, "also migrate AppImages that merely sit in Downloads, on the desktop and the like")
	keep := fs.Bool("keep-leftovers", false, "leave Gear Lever's records of AppImages that are gone as they are")
	if rest, err := parseArgs(fs, args); err != nil {
		return err
	} else if len(rest) > 0 {
		return errors.New("migrate: takes no arguments")
	}
	e, err := session.Load(ctx, "", *catalogPath)
	if err != nil {
		return err
	}
	scan, err := e.ScanMigrations(ctx)
	if err != nil {
		return err
	}
	if *keep {
		scan.Leftovers = nil
	}
	if len(scan.Apps) == 0 && len(scan.Leftovers) == 0 {
		fmt.Fprintln(w, "Nothing to migrate: uas found no AppImages that it doesn't manage already.")
		return nil
	}
	var todo []session.Migration
	if len(scan.Apps) > 0 {
		tw := tabwriter.NewWriter(w, 0, 4, 2, ' ', 0)
		fmt.Fprintln(tw, "NAME\tVERSION\tFROM\tIN\tACTION\t")
		for _, m := range scan.Apps {
			from := m.From
			if from == "" {
				from = "a file"
			}
			var action string
			switch {
			case m.Blocked != "":
				action = "skip: " + m.Blocked
			case !m.Suggested && !*all:
				action = "skip: only a file in " + m.Folder + " (use -all to migrate it)"
			default:
				todo = append(todo, m)
				action = "migrate; " + updatesText(m)
			}
			fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t\n", truncate(m.Name, 30), m.Version, from, m.Folder, action)
		}
		tw.Flush()
	}
	if n := len(scan.Leftovers); n > 0 {
		names := make([]string, n)
		for i, l := range scan.Leftovers {
			names[i] = l.Name
		}
		fmt.Fprintf(w, "Gear Lever still has %d AppImages on record that are gone (%s). Migrating clears them.\n",
			n, strings.Join(names, ", "))
	}
	if *dry {
		return nil
	}
	if len(todo) == 0 && len(scan.Leftovers) == 0 {
		fmt.Fprintln(w, "Nothing to migrate.")
		return nil
	}
	if !*yes {
		q := fmt.Sprintf("Migrate %d apps to uas?", len(todo))
		if len(todo) == 0 {
			q = "Clear Gear Lever's leftover records?"
		}
		ok, err := confirm(q)
		if err != nil || !ok {
			return err
		}
	}
	var errs []error
	for _, m := range todo {
		fmt.Fprintf(w, "Migrating %s\n", m.Name)
		p := newProgressPrinter()
		_, err := e.Migrate(ctx, m, p.report)
		p.done()
		if err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", m.Name, err))
			fmt.Fprintln(w, "  failed:", err)
		}
	}
	if len(scan.Leftovers) > 0 {
		if err := e.ClearLeftovers(ctx, scan.Leftovers); err != nil {
			errs = append(errs, fmt.Errorf("Gear Lever's leftovers: %w", err))
		} else {
			fmt.Fprintf(w, "Cleared %d leftover Gear Lever records.\n", len(scan.Leftovers))
		}
	}
	return errors.Join(errs...)
}

// updatesText says how a migrated app will update.
func updatesText(m session.Migration) string {
	switch {
	case m.Catalog:
		return "updates from the catalog (" + m.Updates + ")"
	case m.Updates != "":
		return "updates from " + m.Updates
	}
	return "won't update: it doesn't say where its updates come from"
}
