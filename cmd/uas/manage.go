package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
	"text/tabwriter"

	"github.com/tyler-builds/uas/internal/catalog"
	"github.com/tyler-builds/uas/internal/client/backend"
	"github.com/tyler-builds/uas/internal/client/paths"
	"github.com/tyler-builds/uas/internal/client/session"
	"golang.org/x/term"
)

func runInstall(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("install", flag.ContinueOnError)
	catalogPath := catalogFlag(fs)
	source := fs.String("source", "", "install from this source instead of the recommended one")
	scope := fs.String("scope", "", "Flatpak scope: user or system (default from settings)")
	yes := fs.Bool("y", false, "do not ask for confirmation")
	ids, err := parseArgs(fs, args)
	if err != nil {
		return err
	}
	if len(ids) != 1 {
		return errors.New("install: give exactly one app ID (see \"uas search\")")
	}
	e, err := session.Load(ctx, *scope, *catalogPath)
	if err != nil {
		return err
	}
	db, err := openCatalog(ctx, e)
	if err != nil {
		return err
	}
	defer db.Close()
	app, err := db.Resolve(ctx, ids[0])
	if err != nil {
		return err
	}
	choice, err := e.Choose(app, catalog.SourceID(*source))
	if err != nil {
		return err
	}
	l := choice.Listing

	if a, ok, err := e.InstalledCopy(ctx, l); err != nil {
		return err
	} else if ok {
		fmt.Printf("%s is already installed (%s).\n", app.Name, a.Scope)
		return nil
	}

	fmt.Printf("%s %s from %s", app.Name, l.Version, l.Source)
	if l.Verified {
		fmt.Print(", verified publisher")
		if l.VerifiedAs != "" {
			fmt.Printf(" (%s)", l.VerifiedAs)
		}
	}
	fmt.Printf("\nWhy this source: %s\n", choice.Reason)
	if l.Trust != nil {
		fmt.Printf("Trust score %d/100:\n", l.Trust.Score)
		printTrust(os.Stdout, l)
	}
	for _, n := range e.InstallNotes(l) {
		fmt.Println(n)
	}
	if !*yes {
		ok, err := confirm("Install?")
		if err != nil || !ok {
			return err
		}
	}

	p := newProgressPrinter()
	err = e.Install(ctx, l, p.report)
	p.done()
	if err != nil {
		return err
	}
	fmt.Printf("Installed %s.\n", app.Name)
	if e.NeedsRestart(l.Source) {
		fmt.Println("Restart your computer to finish.")
	}
	return nil
}

func runRemove(ctx context.Context, args []string) error {
	return runOnInstalled(ctx, "remove", "Remove", args, (*session.Session).Remove)
}

func runUpdate(ctx context.Context, args []string) error {
	return runOnInstalled(ctx, "update", "Update", args, (*session.Session).Update)
}

// runOnInstalled finds one installed app by ID and runs an action on it.
func runOnInstalled(ctx context.Context, cmd, verb string, args []string,
	action func(*session.Session, context.Context, backend.InstalledApp, func(backend.Progress)) error) error {
	fs := flag.NewFlagSet(cmd, flag.ContinueOnError)
	catalogPath := catalogFlag(fs)
	scope := fs.String("scope", "", "pick the copy in this scope when an app is installed more than once")
	yes := fs.Bool("y", false, "do not ask for confirmation")
	ids, err := parseArgs(fs, args)
	if err != nil {
		return err
	}
	if len(ids) != 1 {
		return fmt.Errorf("%s: give exactly one app ID (see \"uas list\")", cmd)
	}
	e, err := session.Load(ctx, "", *catalogPath)
	if err != nil {
		return err
	}
	matches, err := e.FindInstalled(ctx, ids[0], *scope)
	if err != nil {
		return err
	}
	switch len(matches) {
	case 0:
		return fmt.Errorf("%s is not installed", ids[0])
	case 1:
	default:
		var scopes []string
		for _, m := range matches {
			scopes = append(scopes, m.Scope)
		}
		return fmt.Errorf("%s is installed more than once (%s); pick one with -scope",
			ids[0], strings.Join(scopes, ", "))
	}
	a := matches[0]

	if !*yes {
		ok, err := confirm(fmt.Sprintf("%s %s %s (%s, %s)?", verb, a.Name, a.Version, a.Source, a.Scope))
		if err != nil || !ok {
			return err
		}
	}
	p := newProgressPrinter()
	err = action(e, ctx, a, p.report)
	p.done()
	if err != nil {
		return err
	}
	fmt.Printf("%s: done.\n", verb)
	if e.NeedsRestart(a.Source) {
		fmt.Println("Restart your computer to finish.")
	}
	return nil
}

func runList(ctx context.Context, w io.Writer, args []string) error {
	fs := flag.NewFlagSet("list", flag.ContinueOnError)
	asJSON := fs.Bool("json", false, "print the list as JSON")
	if rest, err := parseArgs(fs, args); err != nil {
		return err
	} else if len(rest) > 0 {
		return errors.New("list: takes no arguments")
	}
	catalogPath, _ := paths.Catalog()
	e, err := session.Load(ctx, "", catalogPath)
	if err != nil {
		return err
	}
	apps, err := e.Installed(ctx)
	if err != nil {
		return err
	}
	if *asJSON {
		if apps == nil {
			apps = []backend.InstalledApp{}
		}
		enc := json.NewEncoder(w)
		enc.SetIndent("", "  ")
		return enc.Encode(apps)
	}
	if len(apps) == 0 {
		fmt.Fprintln(w, "No apps installed.")
		return nil
	}
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "NAME\tID\tVERSION\tSOURCE\tSCOPE")
	for _, a := range apps {
		scope := a.Scope
		if a.Pending {
			scope += " (after restart)"
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\n", truncate(a.Name, 30), a.AppID, a.Version, a.Origin, scope)
	}
	return tw.Flush()
}

// confirm asks a yes/no question on the terminal. Without a terminal it refuses, so scripts must pass -y.
func confirm(question string) (bool, error) {
	if !isTerminal(os.Stdin) {
		return false, errors.New("not a terminal; pass -y to confirm")
	}
	fmt.Printf("%s [y/N] ", question)
	line, err := bufio.NewReader(os.Stdin).ReadString('\n')
	if err != nil && !errors.Is(err, io.EOF) {
		return false, err
	}
	switch strings.ToLower(strings.TrimSpace(line)) {
	case "y", "yes":
		return true, nil
	}
	fmt.Println("Cancelled.")
	return false, nil
}

func isTerminal(f *os.File) bool { return term.IsTerminal(int(f.Fd())) }

// progressPrinter shows job progress on stderr: one updating line on a terminal, plain lines otherwise.
type progressPrinter struct {
	tty   bool
	dirty bool
}

func newProgressPrinter() *progressPrinter { return &progressPrinter{tty: isTerminal(os.Stderr)} }

func (p *progressPrinter) report(pr backend.Progress) {
	if p.tty {
		fmt.Fprintf(os.Stderr, "\r\033[K%s", truncate(pr.Message, 100))
		p.dirty = true
		return
	}
	fmt.Fprintln(os.Stderr, pr.Message)
}

func (p *progressPrinter) done() {
	if p.dirty {
		fmt.Fprint(os.Stderr, "\r\033[K")
		p.dirty = false
	}
}

func runUpdates(ctx context.Context, w io.Writer, args []string) error {
	fs := flag.NewFlagSet("updates", flag.ContinueOnError)
	if rest, err := parseArgs(fs, args); err != nil {
		return err
	} else if len(rest) > 0 {
		return errors.New("updates: takes no arguments")
	}
	catalogPath, _ := paths.Catalog()
	e, err := session.Load(ctx, "", catalogPath)
	if err != nil {
		return err
	}
	apps, err := e.Installed(ctx)
	if err != nil {
		return err
	}
	ups, checkErr := e.Updates(ctx)
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	n := 0
	for _, a := range apps {
		if v, ok := ups[a.Key()]; ok {
			if n == 0 {
				fmt.Fprintln(tw, "NAME\tID\tINSTALLED\tAVAILABLE\tSOURCE")
			}
			n++
			fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\n", truncate(a.Name, 30), a.AppID, a.Version, v, a.Source.Name())
		}
	}
	tw.Flush()
	if n == 0 {
		fmt.Fprintln(w, "Everything is up to date.")
	}
	fmt.Fprintln(w, "Packages layered onto the system image update with the system (ujust update).")
	if checkErr != nil {
		fmt.Fprintln(w, "Some sources could not be checked:", checkErr)
	}
	return nil
}
