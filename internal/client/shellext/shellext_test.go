package shellext

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/godbus/dbus/v5"
)

// fakeBus answers like the Shell's extension service.
type fakeBus struct {
	extensions map[string]map[string]dbus.Variant
	install    string // InstallRemoteExtension's answer
	refuse     bool   // answer false to enable, disable and uninstall
	props      map[string]any
	calls      []string
}

func (f *fakeBus) Call(_ context.Context, method string, args []any, out ...any) error {
	call := method
	for _, a := range args {
		if s, ok := a.(string); ok {
			call += " " + s
		}
	}
	f.calls = append(f.calls, call)
	switch method {
	case "ListExtensions":
		*out[0].(*map[string]map[string]dbus.Variant) = f.extensions
	case "InstallRemoteExtension":
		*out[0].(*string) = f.install
	case "UninstallExtension", "EnableExtension", "DisableExtension":
		*out[0].(*bool) = !f.refuse
	case "OpenExtensionPrefs", "CheckForUpdates":
	default:
		return errors.New("unknown method " + method)
	}
	return nil
}

func (f *fakeBus) Get(_ context.Context, property string) (any, error) { return f.props[property], nil }

func (f *fakeBus) Set(_ context.Context, property string, value any) error {
	f.props[property] = value
	return nil
}

func variants(m map[string]any) map[string]dbus.Variant {
	out := map[string]dbus.Variant{}
	for k, v := range m {
		out[k] = dbus.MakeVariant(v)
	}
	return out
}

func TestList(t *testing.T) {
	bus := &fakeBus{extensions: map[string]map[string]dbus.Variant{
		// As GNOME 51 reports them: numbers are doubles.
		"dash-to-dock@micxgx.gmail.com": variants(map[string]any{
			"uuid": "dash-to-dock@micxgx.gmail.com", "name": "Dash to Dock", "description": "A dock",
			"url": "https://micheleg.github.io/dash-to-dock/", "version": 110.0, "type": 2.0, "state": 1.0,
			"enabled": true, "hasPrefs": true, "hasUpdate": true, "canChange": true, "error": ""}),
		"caffeine@patapon.info": variants(map[string]any{
			"uuid": "caffeine@patapon.info", "name": "Caffeine", "version": 61.0, "version-name": "61.1",
			"type": 1.0, "state": 6.0, "enabled": false, "hasPrefs": true, "canChange": true}),
		"broken@example.com": variants(map[string]any{
			"uuid": "broken@example.com", "name": "broken", "type": 2.0, "state": 3.0, "enabled": true,
			"error": "SyntaxError: bad"}),
		// An older Shell: no "enabled", and it is out of date.
		"old@example.com":  variants(map[string]any{"uuid": "old@example.com", "name": "Old", "type": 2.0, "state": 4.0}),
		"gone@example.com": variants(map[string]any{"uuid": "gone@example.com", "name": "Gone", "state": 99.0}),
		"bad uuid":         variants(map[string]any{"name": "Bad"}),
	}}
	// The update the Shell downloaded for Dash to Dock, as it unpacks it.
	updates := t.TempDir()
	if err := os.MkdirAll(filepath.Join(updates, "dash-to-dock@micxgx.gmail.com"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(updates, "dash-to-dock@micxgx.gmail.com", "metadata.json"),
		[]byte(`{"uuid": "dash-to-dock@micxgx.gmail.com", "version": 111}`), 0o644); err != nil {
		t.Fatal(err)
	}
	m := &Manager{Bus: bus, UpdatesDir: updates}
	got, err := m.List(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	want := []Extension{
		{UUID: "broken@example.com", Name: "broken", State: StateError, Enabled: true, Error: "SyntaxError: bad"},
		{UUID: "caffeine@patapon.info", Name: "Caffeine", Version: "61.1", State: StateInitialized, System: true,
			HasPrefs: true, CanChange: true},
		{UUID: "dash-to-dock@micxgx.gmail.com", Name: "Dash to Dock", Description: "A dock",
			URL: "https://micheleg.github.io/dash-to-dock/", Version: "110", State: StateActive, Enabled: true,
			HasPrefs: true, HasUpdate: true, UpdateVersion: "111", CanChange: true},
		{UUID: "old@example.com", Name: "Old", State: StateOutOfDate, Enabled: true},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("List =\n%+v\nwant\n%+v", got, want)
	}
	if p := got[0].Problem(); p != "Failed to load: SyntaxError: bad" {
		t.Errorf("Problem = %q", p)
	}
	if p := got[3].Problem(); p != "Not made for this version of GNOME" {
		t.Errorf("Problem = %q", p)
	}
	ups, err := m.Updates(context.Background())
	if err != nil || len(ups) != 1 || ups[0].UUID != "dash-to-dock@micxgx.gmail.com" || ups[0].UpdateVersion != "111" {
		t.Errorf("Updates = %+v, %v", ups, err)
	}
	// A version name wins over the number, and a download that can't be read gives no version.
	os.WriteFile(filepath.Join(updates, "dash-to-dock@micxgx.gmail.com", "metadata.json"),
		[]byte(`{"version": 111, "version-name": "111.2"}`), 0o644)
	if v := m.downloadedVersion("dash-to-dock@micxgx.gmail.com"); v != "111.2" {
		t.Errorf("version name = %q", v)
	}
	if v := m.downloadedVersion("caffeine@patapon.info"); v != "" {
		t.Errorf("version of no download = %q", v)
	}
	if v := m.downloadedVersion("../" + filepath.Base(updates)); v != "" {
		t.Errorf("a path was read as a UUID: %q", v)
	}
}

func TestInstallAndManage(t *testing.T) {
	ctx := context.Background()
	bus := &fakeBus{install: "successful", props: map[string]any{"ShellVersion": "51.1", "UserExtensionsEnabled": false}}
	m := &Manager{Bus: bus}
	const uuid = "caffeine@patapon.info"

	if err := m.Install(ctx, uuid); err != nil {
		t.Fatal(err)
	}
	bus.install = "cancelled"
	if err := m.Install(ctx, uuid); !errors.Is(err, ErrCancelled) {
		t.Errorf("cancelled install: %v", err)
	}
	bus.install = "error"
	if err := m.Install(ctx, uuid); err == nil || errors.Is(err, ErrCancelled) {
		t.Errorf("failed install: %v", err)
	}
	// Nothing that isn't a UUID reaches the bus.
	before := len(bus.calls)
	for _, bad := range []string{"", "a b", "x;rm", "../etc"} {
		if m.Install(ctx, bad) == nil || m.Remove(ctx, bad) == nil || m.SetEnabled(ctx, bad, true) == nil ||
			m.OpenPrefs(ctx, bad) == nil {
			t.Errorf("%q was accepted", bad)
		}
	}
	if len(bus.calls) != before {
		t.Errorf("bad UUIDs reached the bus: %v", bus.calls[before:])
	}

	for _, err := range []error{m.SetEnabled(ctx, uuid, true), m.SetEnabled(ctx, uuid, false), m.OpenPrefs(ctx, uuid),
		m.Remove(ctx, uuid), m.CheckUpdates(ctx)} {
		if err != nil {
			t.Error(err)
		}
	}
	if want := []string{"EnableExtension " + uuid, "DisableExtension " + uuid, "OpenExtensionPrefs " + uuid + " ",
		"UninstallExtension " + uuid, "CheckForUpdates"}; !reflect.DeepEqual(bus.calls[before:], want) {
		t.Errorf("calls = %q, want %q", bus.calls[before:], want)
	}
	bus.refuse = true
	if m.Remove(ctx, uuid) == nil || m.SetEnabled(ctx, uuid, true) == nil {
		t.Error("a refusal from the Shell was not an error")
	}

	if major, err := m.Major(ctx); err != nil || major != "51" {
		t.Errorf("Major = %q, %v", major, err)
	}
	if on, err := m.UserExtensionsEnabled(ctx); err != nil || on {
		t.Errorf("UserExtensionsEnabled = %v, %v", on, err)
	}
	if err := m.SetUserExtensionsEnabled(ctx, true); err != nil {
		t.Fatal(err)
	}
	if on, _ := m.UserExtensionsEnabled(ctx); !on {
		t.Error("the main switch did not turn on")
	}
}

func TestMajorOf(t *testing.T) {
	for in, want := range map[string]string{"51.1": "51", "48": "48", "52.beta": "52", "": ""} {
		if got := MajorOf(in); got != want {
			t.Errorf("MajorOf(%q) = %q, want %q", in, got, want)
		}
	}
}

// TestLive reads the running Shell's extensions without changing anything. Run it with UAS_LIVE=1 on GNOME.
func TestLive(t *testing.T) {
	if os.Getenv("UAS_LIVE") == "" {
		t.Skip("set UAS_LIVE=1")
	}
	m := Detect()
	if m == nil {
		t.Skip("not a GNOME session")
	}
	ctx := context.Background()
	v, err := m.ShellVersion(ctx)
	if err != nil {
		t.Fatal(err)
	}
	on, err := m.UserExtensionsEnabled(ctx)
	if err != nil {
		t.Fatal(err)
	}
	list, err := m.List(ctx)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("GNOME Shell %s, extensions allowed: %v, %d extensions", v, on, len(list))
	for _, e := range list {
		t.Logf("%-55s %-8s system=%-5v enabled=%-5v state=%d prefs=%v update=%v %s", e.UUID, e.Version, e.System,
			e.Enabled, e.State, e.HasPrefs, e.HasUpdate, e.Problem())
	}
}
