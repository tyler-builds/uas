// Command uas-gui is the uas desktop app.
//
//	uas-gui                    open the app
//	uas-gui --page updates     open it on a page: updates, installed, extensions or settings
//	uas-gui --check-updates    check for updates and notify, without opening a window (run by the
//	                           background check's systemd timer)
package main

import (
	"context"
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

func main() {
	page := flag.String("page", "", "open on this page: updates, installed, extensions or settings")
	check := flag.Bool("check-updates", false, "check for updates, notify, and exit without opening a window")
	flag.Parse()
	catalogPath, err := paths.Catalog()
	if err != nil {
		fmt.Fprintln(os.Stderr, "uas-gui:", err)
		os.Exit(1)
	}
	if *check {
		if err := checkUpdates(catalogPath); err != nil {
			fmt.Fprintln(os.Stderr, "uas-gui:", err)
			os.Exit(1)
		}
		return
	}
	s, err := session.Load(context.Background(), "", catalogPath)
	if err != nil {
		fmt.Fprintln(os.Stderr, "uas-gui:", err)
		os.Exit(1)
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
		err := syscall.Exec(exe, []string{exe, "--page", "settings"}, os.Environ())
		fmt.Fprintln(os.Stderr, "uas-gui: could not restart:", err) // Exec only returns when it fails
		os.Exit(1)
	}
}

// checkUpdates runs one background check: a notification for new updates, which opens this app on its
// Updates page when clicked.
func checkUpdates(catalogPath string) error {
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
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	return updatecheck.Notify(ctx, r, time.Hour, func() error { return updatecheck.OpenUpdates(exe) })
}
