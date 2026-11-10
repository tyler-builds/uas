// Package shellext manages GNOME Shell extensions through the Shell's own D-Bus service,
// org.gnome.Shell.Extensions, which the Extensions app and the browser connector use too. The Shell does the
// work: it lists every extension it knows (the user's and the system's, whoever installed them), downloads
// and installs one from extensions.gnome.org after asking the user in its own dialog, and turns them on and
// off. uas never unpacks an extension itself.
package shellext

import (
	"context"
	"errors"
	"fmt"
	"os"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"

	"github.com/godbus/dbus/v5"
)

const (
	service = "org.gnome.Shell.Extensions"
	path    = "/org/gnome/Shell/Extensions"
	iface   = "org.gnome.Shell.Extensions"
)

// State is what the Shell is doing with an extension.
type State int

// The Shell's extension states (ExtensionState in its extensionUtils.js).
const (
	StateActive       State = 1 // loaded and running
	StateInactive     State = 2 // off
	StateError        State = 3 // failed to load; see Extension.Error
	StateOutOfDate    State = 4 // not made for this Shell release
	StateDownloading  State = 5
	StateInitialized  State = 6 // found but not turned on
	StateDeactivating State = 7
	StateActivating   State = 8
	StateUninstalled  State = 99
)

// Extension is one extension the Shell knows.
type Extension struct {
	UUID        string
	Name        string
	Description string
	URL         string
	// Version is the author's version name when the extension has one, else its version number on
	// extensions.gnome.org.
	Version string
	State   State
	// Enabled is whether the user has it switched on. An enabled extension that failed to load or is out of
	// date is not running; State says so.
	Enabled bool
	// System is true for an extension that came with the system (under /usr), which can be switched but not
	// removed.
	System bool
	// HasPrefs is whether it has a settings window.
	HasPrefs bool
	// HasUpdate is whether the Shell has downloaded a newer version, which it installs at the next login.
	HasUpdate bool
	// CanChange is false when the system's settings lock which extensions are on.
	CanChange bool
	// Error is why it failed to load, with StateError.
	Error string
}

// Problem says why an enabled extension isn't running, or "".
func (e Extension) Problem() string {
	switch e.State {
	case StateError:
		if e.Error != "" {
			return "Failed to load: " + e.Error
		}
		return "Failed to load"
	case StateOutOfDate:
		return "Not made for this version of GNOME"
	}
	return ""
}

// ErrCancelled is returned by Install when the user says no in the Shell's dialog.
var ErrCancelled = errors.New("cancelled in the GNOME dialog")

// ErrUnavailable is returned when the Shell's extension service can't be reached.
var ErrUnavailable = errors.New("GNOME Shell's extension service is not available")

// Bus is the Shell's extension service. Tests use a fake.
type Bus interface {
	// Call calls a method of the service and stores its results in out.
	Call(ctx context.Context, method string, args []any, out ...any) error
	// Get reads a property of the service.
	Get(ctx context.Context, property string) (any, error)
	// Set writes a property of the service.
	Set(ctx context.Context, property string, value any) error
}

// Manager manages the extensions of the running GNOME Shell.
type Manager struct {
	Bus Bus

	mu      sync.Mutex
	version string
}

// Detect returns a manager when this session runs GNOME Shell, and nil otherwise. It doesn't connect until
// the manager is used, and never starts the service on another desktop.
func Detect() *Manager {
	if !slicesContainsFold(strings.Split(os.Getenv("XDG_CURRENT_DESKTOP"), ":"), "GNOME") {
		return nil
	}
	return &Manager{Bus: &sessionBus{}}
}

func slicesContainsFold(list []string, want string) bool {
	for _, s := range list {
		if strings.EqualFold(strings.TrimSpace(s), want) {
			return true
		}
	}
	return false
}

// uuidPattern is what an extension UUID may be made of.
var uuidPattern = regexp.MustCompile(`^[-a-zA-Z0-9@._]{1,200}$`)

func checkUUID(uuid string) error {
	if !uuidPattern.MatchString(uuid) {
		return fmt.Errorf("not an extension UUID: %q", uuid)
	}
	return nil
}

// ShellVersion returns the Shell's version, such as "51.1". It is read once.
func (m *Manager) ShellVersion(ctx context.Context) (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.version != "" {
		return m.version, nil
	}
	v, err := m.Bus.Get(ctx, "ShellVersion")
	if err != nil {
		return "", err
	}
	s, _ := v.(string)
	if s == "" {
		return "", fmt.Errorf("%w: it reports no version", ErrUnavailable)
	}
	m.version = s
	return s, nil
}

// Major returns the Shell's major version, such as "51", which is how extensions name the releases they
// support.
func (m *Manager) Major(ctx context.Context) (string, error) {
	v, err := m.ShellVersion(ctx)
	if err != nil {
		return "", err
	}
	return MajorOf(v), nil
}

// MajorOf returns the major version of a Shell version: "51" for "51.1" and "51.beta".
func MajorOf(version string) string {
	major, _, _ := strings.Cut(version, ".")
	return major
}

// List returns every extension the Shell knows, by name.
func (m *Manager) List(ctx context.Context) ([]Extension, error) {
	var raw map[string]map[string]dbus.Variant
	if err := m.Bus.Call(ctx, "ListExtensions", nil, &raw); err != nil {
		return nil, err
	}
	out := make([]Extension, 0, len(raw))
	for uuid, info := range raw {
		e := parse(info)
		if e.UUID == "" {
			e.UUID = uuid
		}
		if e.State == StateUninstalled || checkUUID(e.UUID) != nil {
			continue
		}
		if e.Name == "" {
			e.Name = e.UUID
		}
		out = append(out, e)
	}
	sort.Slice(out, func(i, j int) bool {
		a, b := strings.ToLower(out[i].Name), strings.ToLower(out[j].Name)
		if a != b {
			return a < b
		}
		return out[i].UUID < out[j].UUID
	})
	return out, nil
}

// parse reads the Shell's description of an extension: its metadata.json plus the Shell's own fields. The
// Shell is written in JavaScript, so every number arrives as a double.
func parse(info map[string]dbus.Variant) Extension {
	str := func(key string) string {
		s, _ := info[key].Value().(string)
		return s
	}
	flag := func(key string) bool {
		b, _ := info[key].Value().(bool)
		return b
	}
	state := State(number(info["state"]))
	e := Extension{
		UUID:        str("uuid"),
		Name:        strings.TrimSpace(str("name")),
		Description: strings.TrimSpace(str("description")),
		URL:         str("url"),
		State:       state,
		System:      number(info["type"]) == 1,
		HasPrefs:    flag("hasPrefs"),
		HasUpdate:   flag("hasUpdate"),
		CanChange:   flag("canChange"),
		Error:       strings.TrimSpace(str("error")),
	}
	// Shells before 45 report no "enabled"; there the state tells.
	if v, ok := info["enabled"]; ok {
		e.Enabled, _ = v.Value().(bool)
	} else {
		e.Enabled = state == StateActive || state == StateActivating || state == StateError ||
			state == StateOutOfDate
	}
	e.Version = strings.TrimSpace(str("version-name"))
	if e.Version == "" {
		if v, ok := info["version"]; ok {
			if s, isString := v.Value().(string); isString {
				e.Version = strings.TrimSpace(s)
			} else if n := number(v); n > 0 {
				e.Version = strconv.FormatFloat(n, 'f', -1, 64)
			}
		}
	}
	return e
}

func number(v dbus.Variant) float64 {
	switch n := v.Value().(type) {
	case float64:
		return n
	case int32:
		return float64(n)
	case uint32:
		return float64(n)
	case int64:
		return float64(n)
	case uint64:
		return float64(n)
	}
	return 0
}

// Install asks the Shell to download and install an extension from extensions.gnome.org. The Shell asks the
// user first, in its own dialog, and the call waits for the answer: ErrCancelled when it is no. The Shell
// turns a newly installed extension on.
func (m *Manager) Install(ctx context.Context, uuid string) error {
	if err := checkUUID(uuid); err != nil {
		return err
	}
	var result string
	if err := m.Bus.Call(ctx, "InstallRemoteExtension", []any{uuid}, &result); err != nil {
		return err
	}
	switch result {
	case "successful":
		return nil
	case "cancelled":
		return ErrCancelled
	}
	return fmt.Errorf("GNOME Shell could not install %s: %s", uuid, result)
}

// Remove uninstalls one of the user's extensions. The system's can't be removed.
func (m *Manager) Remove(ctx context.Context, uuid string) error {
	if err := checkUUID(uuid); err != nil {
		return err
	}
	var ok bool
	if err := m.Bus.Call(ctx, "UninstallExtension", []any{uuid}, &ok); err != nil {
		return err
	}
	if !ok {
		return fmt.Errorf("GNOME Shell could not remove %s: it isn't installed, or it came with the system", uuid)
	}
	return nil
}

// SetEnabled switches an extension on or off.
func (m *Manager) SetEnabled(ctx context.Context, uuid string, on bool) error {
	if err := checkUUID(uuid); err != nil {
		return err
	}
	method, word := "DisableExtension", "off"
	if on {
		method, word = "EnableExtension", "on"
	}
	var ok bool
	if err := m.Bus.Call(ctx, method, []any{uuid}, &ok); err != nil {
		return err
	}
	if !ok {
		return fmt.Errorf("GNOME Shell could not turn %s %s", uuid, word)
	}
	return nil
}

// OpenPrefs opens an extension's settings window.
func (m *Manager) OpenPrefs(ctx context.Context, uuid string) error {
	if err := checkUUID(uuid); err != nil {
		return err
	}
	return m.Bus.Call(ctx, "OpenExtensionPrefs", []any{uuid, "", map[string]dbus.Variant{}})
}

// CheckUpdates asks the Shell to look for newer versions of the user's extensions now. It downloads what it
// finds and installs it at the next login; Extension.HasUpdate then says which.
func (m *Manager) CheckUpdates(ctx context.Context) error {
	return m.Bus.Call(ctx, "CheckForUpdates", nil)
}

// UserExtensionsEnabled reports whether extensions are allowed to run at all: the main switch of the
// Extensions app. With it off, no extension runs, whatever its own switch says.
func (m *Manager) UserExtensionsEnabled(ctx context.Context) (bool, error) {
	v, err := m.Bus.Get(ctx, "UserExtensionsEnabled")
	if err != nil {
		return false, err
	}
	on, ok := v.(bool)
	if !ok {
		return false, fmt.Errorf("%w: unexpected UserExtensionsEnabled", ErrUnavailable)
	}
	return on, nil
}

// SetUserExtensionsEnabled turns the main switch on or off.
func (m *Manager) SetUserExtensionsEnabled(ctx context.Context, on bool) error {
	return m.Bus.Set(ctx, "UserExtensionsEnabled", on)
}

// sessionBus talks to the service on the session bus, connecting on first use.
type sessionBus struct {
	mu   sync.Mutex
	conn *dbus.Conn
}

func (b *sessionBus) object() (dbus.BusObject, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.conn == nil || !b.conn.Connected() {
		// A private connection, so closing or losing it never touches the shared one other packages use.
		conn, err := dbus.ConnectSessionBus()
		if err != nil {
			return nil, errors.Join(ErrUnavailable, err)
		}
		b.conn = conn
	}
	return b.conn.Object(service, path), nil
}

func (b *sessionBus) Call(ctx context.Context, method string, args []any, out ...any) error {
	obj, err := b.object()
	if err != nil {
		return err
	}
	call := obj.CallWithContext(ctx, iface+"."+method, 0, args...)
	if call.Err != nil {
		return busError(call.Err)
	}
	if len(out) == 0 {
		return nil
	}
	return call.Store(out...)
}

func (b *sessionBus) Get(ctx context.Context, property string) (any, error) {
	obj, err := b.object()
	if err != nil {
		return nil, err
	}
	var v dbus.Variant
	call := obj.CallWithContext(ctx, "org.freedesktop.DBus.Properties.Get", 0, iface, property)
	if call.Err != nil {
		return nil, busError(call.Err)
	}
	if err := call.Store(&v); err != nil {
		return nil, err
	}
	return v.Value(), nil
}

func (b *sessionBus) Set(ctx context.Context, property string, value any) error {
	obj, err := b.object()
	if err != nil {
		return err
	}
	call := obj.CallWithContext(ctx, "org.freedesktop.DBus.Properties.Set", 0, iface, property,
		dbus.MakeVariant(value))
	return busError(call.Err)
}

// busError marks the errors that mean there is no Shell to talk to.
func busError(err error) error {
	var de dbus.Error
	if errors.As(err, &de) {
		switch de.Name {
		case "org.freedesktop.DBus.Error.ServiceUnknown", "org.freedesktop.DBus.Error.NameHasNoOwner",
			"org.freedesktop.DBus.Error.Spawn.ChildExited", "org.freedesktop.DBus.Error.UnknownMethod":
			return errors.Join(ErrUnavailable, err)
		}
	}
	return err
}
