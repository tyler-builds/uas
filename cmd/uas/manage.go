package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/tyler-builds/uas/internal/catalog"
	"github.com/tyler-builds/uas/internal/client/backend"
	"github.com/tyler-builds/uas/internal/client/backend/copr"
	"github.com/tyler-builds/uas/internal/client/backend/flatpak"
	"github.com/tyler-builds/uas/internal/client/backend/github"
	"github.com/tyler-builds/uas/internal/client/backend/homebrew"
	"github.com/tyler-builds/uas/internal/client/backend/rpmostree"
	"github.com/tyler-builds/uas/internal/client/paths"
	"github.com/tyler-builds/uas/internal/client/platform"
	"github.com/tyler-builds/uas/internal/client/recommend"
	"github.com/tyler-builds/uas/internal/client/run"
	"github.com/tyler-builds/uas/internal/client/settings"
	"github.com/tyler-builds/uas/internal/client/state"
	"github.com/tyler-builds/uas/internal/client/telemetry"
	"golang.org/x/term"
)

// env is what install, remove, update and list need: settings, the detected system and its backends.
type env struct {
	settingsPath string
	settings     settings.Settings
	platform     platform.Info
	backends     map[catalog.SourceID]backend.Backend
	telemetry    telemetry.Sink
}

// loadEnv reads settings and detects the system. scope, if set, overrides the Flatpak scope setting.
// catalogPath is used to look up newer releases when updating GitHub apps.
func loadEnv(ctx context.Context, scope, catalogPath string) (*env, error) {
	path, err := settings.Path()
	if err != nil {
		return nil, err
	}
	s, err := settings.Load(path)
	if err != nil {
		return nil, err
	}
	if scope != "" {
		s.FlatpakScope = settings.FlatpakScope(scope)
		if !s.FlatpakScope.Valid() {
			return nil, fmt.Errorf("unknown scope %q: use user or system", scope)
		}
	}
	e := &env{
		settingsPath: path,
		settings:     s,
		platform:     platform.Detect(platform.System()),
		backends:     map[catalog.SourceID]backend.Backend{},
		telemetry:    telemetry.ForSettings(s.Telemetry),
	}
	if e.platform.Flatpak != "" {
		e.backends[catalog.SourceFlathub] = &flatpak.Backend{Runner: run.Exec{}, Path: e.platform.Flatpak,
			Scope: s.FlatpakScope}
	}
	if e.platform.Brew != "" {
		e.backends[catalog.SourceHomebrew] = &homebrew.Backend{
			Runner: run.Exec{Env: []string{"HOMEBREW_NO_ENV_HINTS=1"}}, Path: e.platform.Brew}
	}
	if gh, err := githubBackend(catalogPath); err == nil {
		e.backends[catalog.SourceGitHub] = gh
	}
	// Layering needs an image-based system. Package-based Fedora (dnf) is outside the Bazzite slice.
	if e.platform.Atomic && e.platform.RPMOSTree != "" && e.platform.FedoraRelease() > 0 {
		ostree := rpmostree.New(ctx, run.Exec{}, e.platform.RPMOSTree, "rpm", e.platform.FedoraRelease())
		e.backends[catalog.SourceFedora] = ostree
		statePath, err := state.DefaultPath()
		if err == nil && e.platform.Pkexec != "" {
			store := state.Store{Path: statePath}
			e.backends[catalog.SourceCOPR] = &copr.Backend{Ostree: ostree, Runner: run.Exec{},
				Pkexec: e.platform.Pkexec, RepoDir: "/etc/yum.repos.d", State: store}
			// Packages layered from COPR show up under COPR, not Fedora.
			if st, err := store.Load(); err == nil && len(st.COPR) > 0 {
				ostree.Exclude = func(pkg string) bool { _, ok := st.FindCOPR(pkg); return ok }
			}
		}
	}
	return e, nil
}

// available reports whether this system can install l.
func (e *env) available(l catalog.Listing) bool {
	b := e.backends[l.Source]
	return b != nil && b.Supports(l)
}

// needsRestart reports whether changes from source take effect only after a restart.
func (e *env) needsRestart(s catalog.SourceID) bool {
	r, ok := e.backends[s].(backend.Restarter)
	return ok && r.NeedsRestart()
}

// installed lists installed apps from every backend, sorted by name.
func (e *env) installed(ctx context.Context) ([]backend.InstalledApp, error) {
	var all []backend.InstalledApp
	for _, src := range catalog.SourcePrecedence {
		b := e.backends[src]
		if b == nil {
			continue
		}
		apps, err := b.Installed(ctx)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", src, err)
		}
		all = append(all, apps...)
	}
	sort.SliceStable(all, func(i, j int) bool { return strings.ToLower(all[i].Name) < strings.ToLower(all[j].Name) })
	return all, nil
}

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
	e, err := loadEnv(ctx, *scope, *catalogPath)
	if err != nil {
		return err
	}
	db, err := openCatalog(ctx, *catalogPath)
	if err != nil {
		return err
	}
	defer db.Close()
	app, err := db.Resolve(ctx, ids[0])
	if err != nil {
		return err
	}
	choice, err := recommend.Choose(app, e.available, catalog.SourceID(*source))
	if err != nil {
		return err
	}
	l := choice.Listing

	installed, err := e.installed(ctx)
	if err != nil {
		return err
	}
	for _, a := range installed {
		if a.Matches(l) {
			fmt.Printf("%s is already installed (%s).\n", app.Name, a.Scope)
			return nil
		}
	}

	fmt.Printf("%s %s from %s", app.Name, l.Version, l.Source)
	if l.Verified {
		fmt.Print(", verified publisher")
		if l.VerifiedAs != "" {
			fmt.Printf(" (%s)", l.VerifiedAs)
		}
	}
	fmt.Printf("\nWhy this source: %s\n", choice.Reason)
	if l.Source == catalog.SourceFlathub {
		if e.settings.FlatpakScope == settings.ScopeUser {
			fmt.Println("Installs for your user account only.")
		} else {
			fmt.Println("Installs for all users. Flatpak will ask for your password.")
		}
	}
	if l.Source == catalog.SourceGitHub {
		a, _ := l.Asset(catalog.AssetAppImage)
		fmt.Printf("Downloads %s (%s) from github.com/%s. Not reviewed by any distribution.\n", a.Name,
			humanSize(a.Size), l.Ref)
		fmt.Printf("Trust score %d/100:\n", l.Trust.Score)
		printTrust(os.Stdout, l)
		if !e.platform.LibFUSE2 {
			fmt.Println("Note: FUSE 2 was not found; older AppImages may not start.")
		}
	}
	if l.Source == catalog.SourceHomebrew {
		fmt.Println("Installs with Homebrew for your user account. No restart needed.")
	}
	if l.Source == catalog.SourceCOPR {
		fmt.Printf("From the COPR project %s, built by its owner and not reviewed by Fedora.\n", l.Project)
		fmt.Printf("Trust score %d/100:\n", l.Trust.Score)
		printTrust(os.Stdout, l)
		name, _ := copr.RepoFileName(l.Project)
		if _, err := os.Stat(filepath.Join("/etc/yum.repos.d", name)); err != nil {
			fmt.Println("Enabling the project's repo needs your password.")
		}
	}
	if l.Source == catalog.SourceFedora || l.Source == catalog.SourceCOPR {
		fmt.Println("Adds the package to your system image (layering). It takes effect after a restart.")
		fmt.Println("Layered packages can hold up system updates if they conflict with a new image.")
	}
	if !*yes {
		ok, err := confirm("Install?")
		if err != nil || !ok {
			return err
		}
	}

	p := newProgressPrinter()
	err = e.backends[l.Source].Install(ctx, l, p.report)
	p.done()
	e.telemetry.Emit(telemetry.Event{Name: "install", Fields: map[string]string{
		"source": string(l.Source), "result": result(err)}})
	if err != nil {
		return err
	}
	fmt.Printf("Installed %s.\n", app.Name)
	if e.needsRestart(l.Source) {
		fmt.Println("Restart your computer to finish.")
	}
	return nil
}

func runRemove(ctx context.Context, args []string) error {
	return runOnInstalled(ctx, "remove", "Remove", args, func(ctx context.Context, b backend.Backend,
		a backend.InstalledApp, progress func(backend.Progress)) error {
		return b.Remove(ctx, a, progress)
	})
}

func runUpdate(ctx context.Context, args []string) error {
	return runOnInstalled(ctx, "update", "Update", args, func(ctx context.Context, b backend.Backend,
		a backend.InstalledApp, progress func(backend.Progress)) error {
		return b.Update(ctx, a, progress)
	})
}

// runOnInstalled finds one installed app by ID and runs an action on it.
func runOnInstalled(ctx context.Context, cmd, verb string, args []string,
	action func(context.Context, backend.Backend, backend.InstalledApp, func(backend.Progress)) error) error {
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
	e, err := loadEnv(ctx, "", *catalogPath)
	if err != nil {
		return err
	}
	installed, err := e.installed(ctx)
	if err != nil {
		return err
	}
	names := installNames(ctx, *catalogPath, ids[0])
	var matches []backend.InstalledApp
	for _, a := range installed {
		if (names[a.AppID] || names[a.Ref] || (a.Package != "" && names[a.Package])) &&
			(*scope == "" || a.Scope == *scope) {
			matches = append(matches, a)
		}
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
	err = action(ctx, e.backends[a.Source], a, p.report)
	p.done()
	e.telemetry.Emit(telemetry.Event{Name: cmd, Fields: map[string]string{
		"source": string(a.Source), "result": result(err)}})
	if err != nil {
		return err
	}
	fmt.Printf("%s: done.\n", verb)
	if e.needsRestart(a.Source) {
		fmt.Println("Restart your computer to finish.")
	}
	return nil
}

// installNames returns every name an installed copy of the app may go by: the ID given, plus the refs and
// package names of its catalog listings. The catalog is optional here; without it only the ID is used.
func installNames(ctx context.Context, catalogPath, id string) map[string]bool {
	names := map[string]bool{id: true}
	db, err := catalog.Open(catalogPath)
	if err != nil {
		return names
	}
	defer db.Close()
	app, err := db.Resolve(ctx, id)
	if err != nil {
		return names
	}
	names[app.ID] = true
	for _, l := range app.Listings {
		names[l.Ref] = true
		if l.Package != "" {
			names[l.Package] = true
		}
	}
	return names
}

// githubBackend sets up AppImage installs, looking up updates in the catalog at catalogPath.
func githubBackend(catalogPath string) (*github.Backend, error) {
	dirs, err := github.DefaultDirs()
	if err != nil {
		return nil, err
	}
	statePath, err := state.DefaultPath()
	if err != nil {
		return nil, err
	}
	return &github.Backend{
		State:  state.Store{Path: statePath},
		Dirs:   dirs,
		Client: &http.Client{Timeout: 30 * time.Minute},
		Runner: run.Exec{},
		Latest: func(ctx context.Context, repo string) (catalog.Listing, error) {
			db, err := catalog.Open(catalogPath)
			if err != nil {
				return catalog.Listing{}, err
			}
			defer db.Close()
			app, err := db.Resolve(ctx, repo)
			if err != nil {
				return catalog.Listing{}, err
			}
			for _, l := range app.Listings {
				if l.Source == catalog.SourceGitHub && l.Ref == repo {
					return l, nil
				}
			}
			return catalog.Listing{}, fmt.Errorf("%s is no longer in the catalog", repo)
		},
	}, nil
}

func humanSize(n int64) string {
	switch {
	case n >= 1<<30:
		return fmt.Sprintf("%.1f GB", float64(n)/(1<<30))
	case n >= 1<<20:
		return fmt.Sprintf("%.1f MB", float64(n)/(1<<20))
	case n >= 1<<10:
		return fmt.Sprintf("%.0f KB", float64(n)/(1<<10))
	}
	return fmt.Sprintf("%d bytes", n)
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
	e, err := loadEnv(ctx, "", catalogPath)
	if err != nil {
		return err
	}
	apps, err := e.installed(ctx)
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

func result(err error) string {
	if err != nil {
		return "error"
	}
	return "ok"
}
