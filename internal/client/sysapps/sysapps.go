// Package sysapps finds the apps that came with the system: those whose AppStream metadata is installed in
// /usr/share/metainfo, as Steam, Lutris and Waydroid are on Bazzite's image. No uas backend installed them, so
// without this an app page would offer to install, from Flathub, an app the system already has.
package sysapps

import (
	"encoding/xml"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// App is an app installed with the system.
type App struct {
	ID      string // AppStream ID, such as "com.valvesoftware.Steam"
	Name    string
	Version string // the newest release its metadata lists, or ""
	Desktop string // its launcher: the desktop file's path
}

// Dirs are where the system keeps AppStream metadata and launchers.
var (
	MetainfoDirs = []string{"/usr/share/metainfo", "/usr/share/appdata"}
	AppDirs      = []string{"/usr/share/applications"}
)

type component struct {
	Type   string `xml:"type,attr"`
	ID     string `xml:"id"`
	Names  []text `xml:"name"`
	Launch []struct {
		Type  string `xml:"type,attr"`
		Value string `xml:",chardata"`
	} `xml:"launchable"`
	Releases []struct {
		Version string `xml:"version,attr"`
	} `xml:"releases>release"`
}

type text struct {
	Lang  string `xml:"http://www.w3.org/XML/1998/namespace lang,attr"`
	Value string `xml:",chardata"`
}

// Scan reads the desktop apps in metainfoDirs whose launcher is in appDirs, by AppStream ID. Fonts, drivers and
// other components that aren't apps are left out, as are apps without a launcher to show.
func Scan(metainfoDirs, appDirs []string) map[string]App {
	out := map[string]App{}
	for _, dir := range metainfoDirs {
		files, _ := filepath.Glob(filepath.Join(dir, "*.xml"))
		for _, f := range files {
			c, ok := read(f)
			if !ok || (c.Type != "desktop-application" && c.Type != "desktop") {
				continue
			}
			id := strings.TrimSpace(c.ID)
			if id == "" {
				continue
			}
			desktop := launcher(c, id, appDirs)
			if desktop == "" {
				continue
			}
			a := App{ID: strings.TrimSuffix(id, ".desktop"), Desktop: desktop}
			for _, n := range c.Names {
				if n.Lang == "" {
					a.Name = strings.TrimSpace(n.Value)
					break
				}
			}
			if len(c.Releases) > 0 {
				a.Version = strings.TrimSpace(c.Releases[0].Version)
			}
			if _, seen := out[a.ID]; !seen {
				out[a.ID] = a
			}
		}
	}
	return out
}

func read(path string) (component, bool) {
	f, err := os.Open(path)
	if err != nil {
		return component{}, false
	}
	defer f.Close()
	var c component
	if err := xml.NewDecoder(io.LimitReader(f, 4<<20)).Decode(&c); err != nil {
		return component{}, false
	}
	return c, true
}

// launcher finds a component's desktop file: the one it names, or else the one named after its ID.
func launcher(c component, id string, appDirs []string) string {
	var names []string
	for _, l := range c.Launch {
		if l.Type == "desktop-id" {
			names = append(names, strings.TrimSpace(l.Value))
		}
	}
	names = append(names, strings.TrimSuffix(id, ".desktop")+".desktop")
	for _, dir := range appDirs {
		for _, n := range names {
			if n == "" || strings.ContainsAny(n, "/\\") {
				continue
			}
			p := filepath.Join(dir, n)
			if shown(p) {
				return p
			}
		}
	}
	return ""
}

// shown reports whether a desktop file exists and is meant to be shown in menus.
func shown(path string) bool {
	b, err := os.ReadFile(path)
	if err != nil {
		return false
	}
	for _, line := range strings.Split(string(b), "\n") {
		line = strings.TrimSpace(line)
		if line == "NoDisplay=true" || line == "Hidden=true" {
			return false
		}
	}
	return true
}
