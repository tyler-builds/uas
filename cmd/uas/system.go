package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"text/tabwriter"

	"github.com/tyler-builds/uas/internal/catalog"
	"github.com/tyler-builds/uas/internal/client/backend/copr"
	"github.com/tyler-builds/uas/internal/client/backend/rpmostree"
	"github.com/tyler-builds/uas/internal/client/paths"
	"github.com/tyler-builds/uas/internal/client/session"
	"github.com/tyler-builds/uas/internal/client/settings"
	"github.com/tyler-builds/uas/internal/client/updatecheck"
)

func runSystem(ctx context.Context, w io.Writer, args []string) error {
	fs := flag.NewFlagSet("system", flag.ContinueOnError)
	asJSON := fs.Bool("json", false, "print as JSON")
	if rest, err := parseArgs(fs, args); err != nil {
		return err
	} else if len(rest) > 0 {
		return errors.New("system: takes no arguments")
	}
	catalogPath, _ := paths.Catalog()
	e, err := session.Load(ctx, "", catalogPath)
	if err != nil {
		return err
	}
	p := e.Platform
	if *asJSON {
		enc := json.NewEncoder(w)
		enc.SetIndent("", "  ")
		return enc.Encode(p)
	}

	yesNo := func(b bool) string {
		if b {
			return "yes"
		}
		return "no"
	}
	orMissing := func(s string) string {
		if s == "" {
			return "not found"
		}
		return s
	}
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	rows := [][2]string{
		{"System", p.PrettyName},
		{"Image", p.ImageID},
		{"Architecture", p.Arch},
		{"Image-based (atomic)", yesNo(p.Atomic)},
		{"Desktop", p.Desktop + " (" + p.SessionType + ")"},
		{"flatpak", orMissing(p.Flatpak)},
		{"brew", orMissing(p.Brew)},
		{"rpm-ostree", orMissing(p.RPMOSTree)},
		{"pkexec", orMissing(p.Pkexec)},
		{"FUSE 2 (AppImages)", yesNo(p.LibFUSE2)},
	}
	if r := p.FedoraRelease(); r > 0 {
		rows = append(rows[:2], append([][2]string{{"Fedora release", fmt.Sprint(r)}}, rows[2:]...)...)
	}
	for _, r := range rows {
		fmt.Fprintf(tw, "%s:\t%s\n", r[0], r[1])
	}
	tw.Flush()

	fmt.Fprintln(w, "\nSources uas can install from:")
	for _, s := range catalog.SourcePrecedence {
		state := "not supported yet"
		switch b := e.Backends[s].(type) {
		case nil:
		case *rpmostree.Backend:
			state = fmt.Sprintf("ready (layering onto Fedora %d)", b.Release())
		case *copr.Backend:
			state = fmt.Sprintf("ready (layering onto Fedora %d; enabling a repo asks for your password)",
				b.Ostree.Release())
		default:
			state = "ready"
		}
		fmt.Fprintf(w, "  %-9s %s\n", s, state)
	}
	return nil
}

func runSettings(ctx context.Context, w io.Writer, args []string) error {
	path, err := settings.Path()
	if err != nil {
		return err
	}
	s, err := settings.Load(path)
	if err != nil {
		return err
	}
	switch len(args) {
	case 0:
		timer, err := updatecheck.DefaultTimer()
		if err != nil {
			return err
		}
		fmt.Fprintf(w, "flatpak_scope  %s\ntelemetry      %s\nupdate_checks  %s\n", s.FlatpakScope,
			onOff(s.Telemetry), onOff(timer.Enabled()))
		fmt.Fprintf(w, "(%s)\n", path)
		return nil
	case 2:
	default:
		return errors.New("settings: use \"uas settings\" or \"uas settings <key> <value>\"")
	}
	switch key, val := args[0], args[1]; key {
	case "flatpak_scope":
		s.FlatpakScope = settings.FlatpakScope(val)
		if !s.FlatpakScope.Valid() {
			return fmt.Errorf("flatpak_scope must be user or system, not %q", val)
		}
	case "update_checks":
		// Not in the settings file: the systemd timer is the setting.
		timer, err := updatecheck.DefaultTimer()
		if err != nil {
			return err
		}
		switch val {
		case "on":
			exe, err := os.Executable()
			if err != nil {
				return err
			}
			err = timer.Enable(ctx, []string{exe, "check-updates", "-notify"})
			if err != nil {
				return err
			}
			fmt.Fprintln(w, "update_checks set to on: uas checks once a day and notifies you of updates.")
		case "off":
			if err := timer.Disable(ctx); err != nil {
				return err
			}
			fmt.Fprintln(w, "update_checks set to off")
		default:
			return fmt.Errorf("update_checks must be on or off, not %q", val)
		}
		return nil
	case "telemetry":
		switch val {
		case "on":
			s.Telemetry = true
		case "off":
			s.Telemetry = false
		default:
			return fmt.Errorf("telemetry must be on or off, not %q", val)
		}
	default:
		return fmt.Errorf("unknown setting %q (known: flatpak_scope, telemetry, update_checks)", key)
	}
	if err := settings.Save(path, s); err != nil {
		return err
	}
	fmt.Fprintf(w, "%s set to %s\n", args[0], args[1])
	return nil
}

func onOff(b bool) string {
	if b {
		return "on"
	}
	return "off"
}
