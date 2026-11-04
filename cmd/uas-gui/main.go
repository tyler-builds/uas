// Command uas-gui is the uas desktop app.
package main

import (
	"context"
	"fmt"
	"os"

	"github.com/tyler-builds/jamb/app"

	"github.com/tyler-builds/uas/internal/client/paths"
	"github.com/tyler-builds/uas/internal/client/session"
	"github.com/tyler-builds/uas/internal/gui"
)

func main() {
	catalogPath, err := paths.Catalog()
	if err != nil {
		fmt.Fprintln(os.Stderr, "uas-gui:", err)
		os.Exit(1)
	}
	s, err := session.Load(context.Background(), "", catalogPath)
	if err != nil {
		fmt.Fprintln(os.Stderr, "uas-gui:", err)
		os.Exit(1)
	}
	g := gui.New(s)
	light, dark := gui.Themes()
	app.New().Run(app.Window{
		Title:      "uas",
		Width:      1180,
		Height:     780,
		MinWidth:   760,
		MinHeight:  520,
		LightTheme: light,
		DarkTheme:  dark,
		Root:       g.Root,
	})
}
