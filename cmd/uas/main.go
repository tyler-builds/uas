// Command uas finds and installs apps from every source your system supports.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/tyler-builds/uas/internal/catalog"
	"github.com/tyler-builds/uas/internal/client/paths"
	"github.com/tyler-builds/uas/internal/client/refresh"
)

const usage = `usage: uas <command> [flags] [args]

commands:
  refresh              download the latest catalog
  search <words>       find apps
  info <app-id>        show one app and where it is available
  install <app-id>     install an app
  remove <app-id>      remove an installed app
  update <app-id>      update an installed app
  list                 list installed apps
  system               show what uas detected about this system
  settings [key value] show or change settings

Run "uas <command> -h" for a command's flags.
`

func main() {
	if len(os.Args) < 2 {
		fmt.Fprint(os.Stderr, usage)
		os.Exit(2)
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	var err error
	switch cmd, args := os.Args[1], os.Args[2:]; cmd {
	case "refresh":
		err = runRefresh(ctx, os.Stdout, args)
	case "search":
		err = runSearch(ctx, os.Stdout, args)
	case "info":
		err = runInfo(ctx, os.Stdout, args)
	case "install":
		err = runInstall(ctx, args)
	case "remove", "uninstall":
		err = runRemove(ctx, args)
	case "update":
		err = runUpdate(ctx, args)
	case "list":
		err = runList(ctx, os.Stdout, args)
	case "system":
		err = runSystem(ctx, os.Stdout, args)
	case "settings":
		err = runSettings(os.Stdout, args)
	case "help", "-h", "-help", "--help":
		fmt.Print(usage)
		return
	default:
		fmt.Fprintf(os.Stderr, "uas: unknown command %q\n\n%s", cmd, usage)
		os.Exit(2)
	}
	if errors.Is(err, flag.ErrHelp) {
		return
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "uas:", err)
		os.Exit(1)
	}
}

// parseArgs parses flags that may appear before, between or after positional arguments.
func parseArgs(fs *flag.FlagSet, args []string) ([]string, error) {
	var positional []string
	for {
		if err := fs.Parse(args); err != nil {
			return nil, err
		}
		args = fs.Args()
		if len(args) == 0 {
			return positional, nil
		}
		positional = append(positional, args[0])
		args = args[1:]
	}
}

func catalogFlag(fs *flag.FlagSet) *string {
	def, _ := paths.Catalog()
	return fs.String("catalog", def, "catalog database (or set "+paths.CatalogEnv+")")
}

// openCatalog opens the local catalog, downloading it first if there is none yet.
func openCatalog(ctx context.Context, path string) (*catalog.DB, error) {
	if _, err := os.Stat(path); errors.Is(err, os.ErrNotExist) {
		fmt.Fprintln(os.Stderr, "Downloading the app catalog...")
		if _, err := refresh.Refresh(ctx, refreshOptions(path)); err != nil {
			return nil, fmt.Errorf("download catalog: %w", err)
		}
	}
	return catalog.Open(path)
}

func refreshOptions(catalogPath string) refresh.Options {
	base := os.Getenv(refresh.BaseURLEnv)
	if base == "" {
		base = refresh.DefaultBaseURL
	}
	return refresh.Options{
		BaseURL:     base,
		TrustedKeys: refresh.TrustedKeys(),
		CatalogPath: catalogPath,
		Client:      &http.Client{Timeout: 5 * time.Minute},
		UserAgent:   "uas",
	}
}

func runRefresh(ctx context.Context, w io.Writer, args []string) error {
	fs := flag.NewFlagSet("refresh", flag.ContinueOnError)
	catalogPath := catalogFlag(fs)
	if rest, err := parseArgs(fs, args); err != nil {
		return err
	} else if len(rest) > 0 {
		return errors.New("refresh: takes no arguments")
	}
	res, err := refresh.Refresh(ctx, refreshOptions(*catalogPath))
	if err != nil {
		return err
	}
	m := res.Manifest
	state := "Already up to date"
	if res.Updated {
		state = "Updated"
	}
	fmt.Fprintf(w, "%s: %d apps, built %s.\n", state, m.Apps, m.BuiltAt.Local().Format("2006-01-02 15:04"))
	for _, s := range m.Sources {
		if s.Error != "" {
			fmt.Fprintf(w, "  %s was unavailable at build time: %s\n", s.Source, s.Error)
		}
	}
	return nil
}

func runSearch(ctx context.Context, w io.Writer, args []string) error {
	fs := flag.NewFlagSet("search", flag.ContinueOnError)
	catalogPath := catalogFlag(fs)
	limit := fs.Int("limit", 20, "maximum number of results")
	asJSON := fs.Bool("json", false, "print results as JSON")
	words, err := parseArgs(fs, args)
	if err != nil {
		return err
	}
	if len(words) == 0 {
		return errors.New("search: give one or more words to search for")
	}
	db, err := openCatalog(ctx, *catalogPath)
	if err != nil {
		return err
	}
	defer db.Close()

	results, err := db.Search(ctx, strings.Join(words, " "), *limit)
	if err != nil {
		return err
	}
	if *asJSON {
		if results == nil {
			results = []catalog.SearchResult{}
		}
		enc := json.NewEncoder(w)
		enc.SetIndent("", "  ")
		return enc.Encode(results)
	}
	if len(results) == 0 {
		fmt.Fprintln(w, "No apps found.")
		return nil
	}
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "NAME\tID\tSOURCES\tSUMMARY")
	for _, r := range results {
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\n", truncate(r.Name, 30), r.ID, sourceList(r), truncate(r.Summary, 60))
	}
	return tw.Flush()
}

func sourceList(r catalog.SearchResult) string {
	s := make([]string, len(r.Sources))
	for i, src := range r.Sources {
		s[i] = string(src)
	}
	out := strings.Join(s, ",")
	if r.Verified {
		out += " (verified)"
	}
	return out
}

func runInfo(ctx context.Context, w io.Writer, args []string) error {
	fs := flag.NewFlagSet("info", flag.ContinueOnError)
	catalogPath := catalogFlag(fs)
	asJSON := fs.Bool("json", false, "print the app as JSON")
	ids, err := parseArgs(fs, args)
	if err != nil {
		return err
	}
	if len(ids) != 1 {
		return errors.New("info: give exactly one app ID (see \"uas search\")")
	}
	db, err := openCatalog(ctx, *catalogPath)
	if err != nil {
		return err
	}
	defer db.Close()

	a, err := db.App(ctx, ids[0])
	if err != nil {
		return err
	}
	if *asJSON {
		enc := json.NewEncoder(w)
		enc.SetIndent("", "  ")
		return enc.Encode(a)
	}

	fmt.Fprintf(w, "%s (%s)\n", a.Name, a.ID)
	if a.Summary != "" {
		fmt.Fprintln(w, a.Summary)
	}
	fmt.Fprintln(w)
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	for _, row := range [][2]string{
		{"Developer", a.Developer},
		{"License", a.License},
		{"Homepage", a.Homepage},
		{"Source code", a.VCSURL},
		{"Categories", strings.Join(a.Categories, ", ")},
	} {
		if row[1] != "" {
			fmt.Fprintf(tw, "%s:\t%s\n", row[0], row[1])
		}
	}
	tw.Flush()

	fmt.Fprintln(w, "\nAvailable from:")
	tw = tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	for _, l := range a.Listings {
		released := ""
		if !l.Released.IsZero() {
			released = l.Released.Format(time.DateOnly)
		}
		verified := ""
		if l.Verified {
			verified = "verified"
			if l.VerifiedAs != "" {
				verified += " (" + l.VerifiedAs + ")"
			}
		}
		fmt.Fprintf(tw, "  %s\t%s\t%s\t%s\t%s\n", l.Source, l.Version, released, verified, l.Ref)
	}
	tw.Flush()

	if a.Description != "" {
		fmt.Fprintf(w, "\n%s\n", a.Description)
	}
	return nil
}

func truncate(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n-1]) + "…"
}
