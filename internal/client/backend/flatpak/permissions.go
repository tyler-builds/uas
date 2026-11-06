package flatpak

import (
	"context"

	"errors"
	"fmt"
	"github.com/tyler-builds/uas/internal/client/backend"
	"regexp"
	"slices"
	"strings"
)

// Permissions: what a Flatpak app may reach outside its sandbox, and the user's changes to it. Changes are
// user overrides (flatpak override --user), which apply to the app in every installation and need no
// password; flatpak keeps them in ~/.local/share/flatpak/overrides. Flatseal edits the same files.

// Permission is one thing an app may or may not reach.
type Permission = backend.Permission

// permission describes how one Permission maps onto flatpak's metadata and override flags.
type permission struct {
	key, label, detail string
	group              string   // the [Context] key: shared, sockets, devices, features, filesystems
	values             []string // any of these grants it
	allow, deny        string   // flatpak override flags
}

// permissions are the ones uas shows, in order: the ones people most often want to change, as in Flatseal.
var permissions = []permission{
	{key: "network", label: "Network", detail: "Connect to the internet and the local network",
		group: "shared", values: []string{"network"}, allow: "--share=network", deny: "--unshare=network"},
	{key: "host", label: "All files", detail: "Read and change every file you can, including system files",
		group: "filesystems", values: []string{"host", "host-os", "host-etc"},
		allow: "--filesystem=host", deny: "--nofilesystem=host"},
	{key: "home", label: "Home folder", detail: "Read and change the files in your home folder",
		group: "filesystems", values: []string{"home", "~"}, allow: "--filesystem=home", deny: "--nofilesystem=home"},
	{key: "downloads", label: "Downloads folder", detail: "Read and change the files in Downloads",
		group: "filesystems", values: []string{"xdg-download"},
		allow: "--filesystem=xdg-download", deny: "--nofilesystem=xdg-download"},
	{key: "devices", label: "All devices", detail: "Webcams, game controllers, USB devices and the like",
		group: "devices", values: []string{"all"}, allow: "--device=all", deny: "--nodevice=all"},
	{key: "audio", label: "Sound", detail: "Play sound and use microphones",
		group: "sockets", values: []string{"pulseaudio"}, allow: "--socket=pulseaudio", deny: "--nosocket=pulseaudio"},
	{key: "bluetooth", label: "Bluetooth", detail: "Use Bluetooth devices directly",
		group: "features", values: []string{"bluetooth"}, allow: "--allow=bluetooth", deny: "--disallow=bluetooth"},
	{key: "x11", label: "X11 windowing", detail: "Draw windows through X11, which lets an app see other X11 apps' windows and keystrokes",
		group: "sockets", values: []string{"x11"}, allow: "--socket=x11", deny: "--nosocket=x11"},
}

// appIDPattern matches Flatpak app IDs.
var appIDPattern = regexp.MustCompile(`^[A-Za-z0-9_-]+(\.[A-Za-z0-9_-]+)+$`)

// ErrUnknownPermission is returned for a permission key uas doesn't manage.
var ErrUnknownPermission = errors.New("unknown permission")

// Permissions returns what an installed app may reach, as its manifest and every override leave it, and which
// of those the user has changed.
func (b *Backend) Permissions(ctx context.Context, appID string) ([]Permission, error) {
	if !appIDPattern.MatchString(appID) {
		return nil, fmt.Errorf("flatpak: invalid app ID %q", appID)
	}
	out, err := b.Runner.Run(ctx, b.Path, []string{"info", "--show-permissions", appID}, nil)
	if err != nil {
		return nil, err
	}
	effective := parseContext(out)
	over, err := b.Runner.Run(ctx, b.Path, []string{"override", "--user", "--show", appID}, nil)
	if err != nil {
		return nil, err
	}
	overrides := parseContext(over)

	var ps []Permission
	for _, p := range permissions {
		ps = append(ps, Permission{Key: p.key, Label: p.label, Detail: p.detail,
			Allowed: grants(effective[p.group], p.values) == granted,
			Changed: grants(overrides[p.group], p.values) != unset})
	}
	// Access to all files includes the home folder and Downloads.
	if all := find(ps, "host"); all != nil && all.Allowed {
		for _, k := range []string{"home", "downloads"} {
			if p := find(ps, k); p != nil {
				if !p.Allowed {
					p.Allowed = true
				}
				p.Note = "Included in access to all files"
			}
		}
	}
	if home, dl := find(ps, "home"), find(ps, "downloads"); home != nil && dl != nil && home.Allowed && dl.Note == "" {
		dl.Allowed, dl.Note = true, "Included in access to the home folder"
	}
	return ps, nil
}

func find(ps []Permission, key string) *Permission {
	for i := range ps {
		if ps[i].Key == key {
			return &ps[i]
		}
	}
	return nil
}

// SetPermission allows or denies one permission for an app, as a user override.
func (b *Backend) SetPermission(ctx context.Context, appID, key string, allow bool) error {
	if !appIDPattern.MatchString(appID) {
		return fmt.Errorf("flatpak: invalid app ID %q", appID)
	}
	i := slices.IndexFunc(permissions, func(p permission) bool { return p.key == key })
	if i < 0 {
		return fmt.Errorf("flatpak: %w %q", ErrUnknownPermission, key)
	}
	flag := permissions[i].deny
	if allow {
		flag = permissions[i].allow
	}
	_, err := b.Runner.Run(ctx, b.Path, []string{"override", "--user", flag, appID}, nil)
	return err
}

// ResetPermissions removes the user's overrides for an app, back to what its manifest (and any system-wide
// overrides) say.
func (b *Backend) ResetPermissions(ctx context.Context, appID string) error {
	if !appIDPattern.MatchString(appID) {
		return fmt.Errorf("flatpak: invalid app ID %q", appID)
	}
	_, err := b.Runner.Run(ctx, b.Path, []string{"override", "--user", "--reset", appID}, nil)
	return err
}

// parseContext reads the [Context] group of flatpak metadata or an override file into lists of values.
func parseContext(s string) map[string][]string {
	out := map[string][]string{}
	group := ""
	for _, line := range strings.Split(s, "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "[") {
			group = line
			continue
		}
		if group != "[Context]" {
			continue
		}
		k, v, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		for _, item := range strings.Split(v, ";") {
			if item = strings.TrimSpace(item); item != "" {
				out[k] = append(out[k], item)
			}
		}
	}
	return out
}

type grant int

const (
	unset grant = iota
	granted
	denied
)

// grants says whether a list of values grants or denies any of values. A filesystem entry may carry an
// access mode ("home:ro"); a "!" denies. The last mention wins.
func grants(list, values []string) grant {
	g := unset
	for _, item := range list {
		neg := strings.HasPrefix(item, "!")
		item = strings.TrimPrefix(item, "!")
		item, _, _ = strings.Cut(item, ":")
		item = strings.TrimSuffix(item, "/")
		if !slices.Contains(values, item) {
			continue
		}
		if neg {
			g = denied
		} else {
			g = granted
		}
	}
	return g
}
