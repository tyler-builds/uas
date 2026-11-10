package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"slices"
	"strings"
	"text/tabwriter"

	"github.com/tyler-builds/uas/internal/catalog"
	"github.com/tyler-builds/uas/internal/client/paths"
	"github.com/tyler-builds/uas/internal/client/session"
	"github.com/tyler-builds/uas/internal/client/shellext"
)

const extensionsUsage = `usage: uas extensions [command]

GNOME Shell extensions, from extensions.gnome.org. GNOME Shell installs and runs them.

commands:
  list                 list the extensions on this system (the default)
  search <words>       find extensions for this version of GNOME
  install <uuid>       install one; GNOME asks you to confirm in its own dialog
  remove <uuid>        remove one of yours
  enable <uuid>        turn one on
  disable <uuid>       turn one off
  settings <uuid>      open one's settings window
`

// runExtensions lists and manages GNOME Shell extensions.
func runExtensions(ctx context.Context, w io.Writer, args []string) error {
	cmd := "list"
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		cmd, args = args[0], args[1:]
	}
	fs := flag.NewFlagSet("extensions "+cmd, flag.ContinueOnError)
	fs.Usage = func() { fmt.Fprint(fs.Output(), extensionsUsage) }
	limit := 20
	if cmd == "search" {
		fs.IntVar(&limit, "limit", limit, "maximum number of results")
	}
	rest, err := parseArgs(fs, args)
	if err != nil {
		return err
	}
	catalogPath, _ := paths.Catalog()
	e, err := session.Load(ctx, "", catalogPath)
	if err != nil {
		return err
	}
	m := e.Extensions
	if m == nil {
		return errors.New("extensions: this isn't a GNOME session, so there is no GNOME Shell to ask")
	}
	one := func() (string, error) {
		if len(rest) != 1 {
			return "", fmt.Errorf("extensions %s: give exactly one extension UUID (see \"uas extensions search\")", cmd)
		}
		return rest[0], nil
	}

	switch cmd {
	case "list":
		if len(rest) > 0 {
			return errors.New("extensions list: takes no arguments")
		}
		return listExtensions(ctx, w, m)
	case "search":
		if len(rest) == 0 {
			return errors.New("extensions search: give one or more words to search for")
		}
		return searchExtensions(ctx, w, e, strings.Join(rest, " "), limit)
	case "install":
		uuid, err := one()
		if err != nil {
			return err
		}
		fmt.Fprintf(w, "Asking GNOME Shell to install %s. Answer its dialog to continue.\n", uuid)
		if err := m.Install(ctx, uuid); errors.Is(err, shellext.ErrCancelled) {
			fmt.Fprintln(w, "Cancelled; nothing was installed.")
			return nil
		} else if err != nil {
			return err
		}
		fmt.Fprintf(w, "%s is installed and turned on.\n", uuid)
	case "remove", "uninstall":
		uuid, err := one()
		if err != nil {
			return err
		}
		if err := m.Remove(ctx, uuid); err != nil {
			return err
		}
		fmt.Fprintf(w, "%s is removed.\n", uuid)
	case "enable", "disable":
		uuid, err := one()
		if err != nil {
			return err
		}
		if err := m.SetEnabled(ctx, uuid, cmd == "enable"); err != nil {
			return err
		}
		fmt.Fprintf(w, "%s is turned %s.\n", uuid, map[string]string{"enable": "on", "disable": "off"}[cmd])
	case "settings", "prefs":
		uuid, err := one()
		if err != nil {
			return err
		}
		return m.OpenPrefs(ctx, uuid)
	default:
		return fmt.Errorf("extensions: unknown command %q\n\n%s", cmd, extensionsUsage)
	}
	return nil
}

func listExtensions(ctx context.Context, w io.Writer, m *shellext.Manager) error {
	list, err := m.List(ctx)
	if err != nil {
		return err
	}
	if len(list) == 0 {
		fmt.Fprintln(w, "No extensions installed.")
		return nil
	}
	if on, err := m.UserExtensionsEnabled(ctx); err == nil && !on {
		fmt.Fprintln(w, "GNOME's main switch for extensions is off, so none of these run.")
	}
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "NAME\tUUID\tVERSION\tFROM\tSTATE")
	for _, x := range list {
		from := "you"
		if x.System {
			from = "system"
		}
		state := "off"
		switch {
		case x.Enabled && x.Problem() != "":
			state = "on, not running: " + x.Problem()
		case x.Enabled:
			state = "on"
		}
		if x.HasUpdate {
			state += " (update at next login)"
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\n", truncate(x.Name, 30), x.UUID, x.Version, from, state)
	}
	return tw.Flush()
}

func searchExtensions(ctx context.Context, w io.Writer, e *session.Session, query string, limit int) error {
	db, err := openCatalog(ctx, e)
	if err != nil {
		return err
	}
	defer db.Close()
	if db.Info().Extensions == nil {
		return errors.New("the catalog on this computer has no extensions yet; run \"uas refresh\"")
	}
	major, err := e.Extensions.Major(ctx)
	if err != nil {
		return err
	}
	results, err := db.Search(ctx, query, catalog.SearchOptions{Limit: limit, Extensions: true, Shell: major})
	if err != nil {
		return err
	}
	if len(results) == 0 {
		fmt.Fprintf(w, "No extensions for GNOME %s found.\n", major)
		return nil
	}
	installed, err := e.Extensions.List(ctx)
	if err != nil {
		return err
	}
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "NAME\tUUID\tDOWNLOADS\tSUMMARY")
	for _, r := range results {
		uuid := ""
		for _, ref := range r.Refs {
			if ref.Source == catalog.SourceGNOMEExtensions {
				uuid = ref.Ref
			}
		}
		name := truncate(r.Name, 30)
		if slices.ContainsFunc(installed, func(x shellext.Extension) bool { return x.UUID == uuid }) {
			name += " (installed)"
		}
		fmt.Fprintf(tw, "%s\t%s\t%d\t%s\n", name, uuid, r.Popularity, truncate(r.Summary, 60))
	}
	return tw.Flush()
}
