package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"text/tabwriter"

	"github.com/tyler-builds/uas/internal/client/session"
)

// runPermissions shows or changes what an installed Flatpak app may reach:
//
//	uas permissions <app-id>               list them
//	uas permissions <app-id> <key> on|off  allow or deny one
//	uas permissions <app-id> reset         undo your changes
func runPermissions(ctx context.Context, w io.Writer, args []string) error {
	fs := flag.NewFlagSet("permissions", flag.ContinueOnError)
	fs.Usage = func() {
		fmt.Fprintln(fs.Output(), "usage: uas permissions <flatpak-app-id> [<permission> on|off | reset]")
	}
	pos, err := parseArgs(fs, args)
	if err != nil {
		return err
	}
	if len(pos) == 0 || len(pos) > 3 {
		fs.Usage()
		return flag.ErrHelp
	}
	e, err := session.Load(ctx, "", "")
	if err != nil {
		return err
	}
	app := pos[0]
	switch {
	case len(pos) == 2 && pos[1] == "reset":
		if err := e.ResetPermissions(ctx, app); err != nil {
			return err
		}
		fmt.Fprintln(w, "Permissions of "+app+" are back to what the app asks for.")
	case len(pos) == 3 && (pos[2] == "on" || pos[2] == "off"):
		if err := e.SetPermission(ctx, app, pos[1], pos[2] == "on"); err != nil {
			return err
		}
		fmt.Fprintf(w, "%s: %s %s. Restart the app for this to take effect.\n", app, pos[1], pos[2])
	case len(pos) != 1:
		fs.Usage()
		return flag.ErrHelp
	}
	ps, err := e.Permissions(ctx, app)
	if err != nil {
		return err
	}
	tw := tabwriter.NewWriter(w, 0, 4, 2, ' ', 0)
	fmt.Fprintln(tw, "PERMISSION\tKEY\tALLOWED\t")
	for _, p := range ps {
		state := onOff(p.Allowed)
		if p.Changed {
			state += " (changed by you)"
		}
		if p.Note != "" {
			state += " (" + p.Note + ")"
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\t\n", p.Label, p.Key, state)
	}
	return tw.Flush()
}
