package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/tyler-builds/jamb/app"

	"github.com/tyler-builds/uas/internal/client/paths"
	"github.com/tyler-builds/uas/internal/client/session"
	"github.com/tyler-builds/uas/internal/client/updatecheck"
	"github.com/tyler-builds/uas/internal/gui"
)

// runGUI is the desktop app, which "uas gui" and the uas-gui link open:
//
//	uas gui                    open the app
//	uas gui --page updates     open it on a page: updates, installed, extensions or settings
//	uas gui --check-updates    check for updates and notify, without opening a window (run by the
//	                           background check's systemd timer)
func runGUI(args []string) error {
	fs := flag.NewFlagSet("gui", flag.ContinueOnError)
	page := fs.String("page", "", "open on this page: updates, installed, extensions or settings")
	check := fs.Bool("check-updates", false, "check for updates, notify, and exit without opening a window")
	if err := fs.Parse(args); err != nil {
		return err
	}
	catalogPath, err := paths.Catalog()
	if err != nil {
		return err
	}
	if *check {
		return checkUpdatesInBackground(catalogPath)
	}
	s, err := session.Load(context.Background(), "", catalogPath)
	if err != nil {
		return err
	}
	g := gui.New(s)
	g.StartOn(*page)
	// After an update of uas itself, Settings offers to restart: the window closes, and once the app has shut
	// down this process becomes the new program, opened on Settings again. The path is read now, while the
	// program at it is still the one running.
	exe, exeErr := os.Executable()
	var restart atomic.Bool
	if exeErr == nil {
		g.Restart = func() {
			restart.Store(true)
			app.Quit()
		}
	}
	g.Uninstall = uninstallSelf
	g.Quit = app.Quit
	light, dark := gui.Themes()
	// The app ID is the Wayland app ID, which the desktop matches to packaging/linux's .desktop file for the
	// name and icon in the dock.
	a := app.New().AppID("io.github.tyler_builds.uas")
	for _, f := range gui.Fonts() {
		a = a.Font("", f.Weight, f.Data) // "" replaces the UI font
	}
	a.Run(app.Window{
		Title:      "uas",
		Width:      1180,
		Height:     780,
		MinWidth:   760,
		MinHeight:  520,
		LightTheme: light,
		DarkTheme:  dark,
		Root:       g.Root,
		Chrome:     app.ChromeCustom, // the app's title bar carries the window's buttons (gui's titleBar)
		Corners:    app.CornersRound,
	})
	if restart.Load() {
		err := syscall.Exec(exe, []string{exe, "gui", "--page", "settings"}, os.Environ())
		return errors.New("could not restart: " + err.Error()) // Exec only returns when it fails
	}
	return nil
}

// guiCommand is the command that opens the app: this program, with "gui".
func guiCommand() ([]string, error) {
	exe, err := os.Executable()
	if err != nil {
		return nil, err
	}
	return []string{exe, "gui"}, nil
}

// checkUpdatesInBackground runs one background check: a notification for new updates, which opens this app
// on its Updates page when clicked.
func checkUpdatesInBackground(catalogPath string) error {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	s, err := session.Load(ctx, "", catalogPath)
	if err != nil {
		return err
	}
	r, err := updatecheck.Check(ctx, s)
	if err != nil {
		return err
	}
	open, err := guiCommand()
	if err != nil {
		return fmt.Errorf("find uas: %w", err)
	}
	return updatecheck.Notify(ctx, r, time.Hour, func() error { return updatecheck.OpenUpdates(open) })
}
