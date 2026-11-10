package session

import (
	"context"
	"errors"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/godbus/dbus/v5"

	"github.com/tyler-builds/uas/internal/catalog"
	"github.com/tyler-builds/uas/internal/client/backend"
	"github.com/tyler-builds/uas/internal/client/settings"
	"github.com/tyler-builds/uas/internal/client/shellext"
	"github.com/tyler-builds/uas/internal/client/telemetry"
)

// fakeShell is GNOME Shell's extension service, in memory.
type fakeShell struct {
	version    string
	extensions map[string]map[string]any
	answer     string // the install dialog's answer
	calls      []string
}

func (f *fakeShell) Call(_ context.Context, method string, args []any, out ...any) error {
	uuid := ""
	if len(args) > 0 {
		uuid, _ = args[0].(string)
	}
	if method != "ListExtensions" {
		f.calls = append(f.calls, method+" "+uuid)
	}
	switch method {
	case "ListExtensions":
		list := map[string]map[string]dbus.Variant{}
		for id, e := range f.extensions {
			list[id] = map[string]dbus.Variant{}
			for k, v := range e {
				list[id][k] = dbus.MakeVariant(v)
			}
		}
		*out[0].(*map[string]map[string]dbus.Variant) = list
	case "InstallRemoteExtension":
		if f.answer == "successful" {
			f.extensions[uuid] = ext(uuid, uuid, false, true)
		}
		*out[0].(*string) = f.answer
	case "EnableExtension", "DisableExtension":
		e, ok := f.extensions[uuid]
		if ok {
			e["enabled"] = method == "EnableExtension"
		}
		*out[0].(*bool) = ok
	default:
		return errors.New("unknown method " + method)
	}
	return nil
}

func (f *fakeShell) Get(_ context.Context, property string) (any, error) {
	if property == "ShellVersion" {
		return f.version, nil
	}
	return true, nil
}

func (f *fakeShell) Set(context.Context, string, any) error { return nil }

func ext(uuid, name string, system, on bool) map[string]any {
	kind := 2.0
	if system {
		kind = 1.0
	}
	return map[string]any{"uuid": uuid, "name": name, "type": kind, "state": 1.0, "enabled": on, "canChange": true}
}

// extSession is a GNOME 51 session whose catalog has three extensions: dock and clock for GNOME 51, and old
// for GNOME 45 only.
func extSession(t *testing.T, shell *fakeShell) *Session {
	t.Helper()
	path := filepath.Join(t.TempDir(), "catalog.db")
	app := func(uuid, name string, shells ...string) catalog.App {
		l := catalog.Listing{Source: catalog.SourceGNOMEExtensions, Ref: uuid, Kind: catalog.KindShellExtension,
			Name: name, Shells: shells}
		return catalog.App{ID: catalog.ExtensionAppID(uuid), Kind: catalog.KindShellExtension, Name: name,
			Listings: []catalog.Listing{l}}
	}
	info := catalog.BuildInfo{BuiltAt: time.Now(),
		Extensions: &catalog.SourceStatus{Source: catalog.SourceGNOMEExtensions, Listings: 3}}
	if err := catalog.Write(context.Background(), path, []catalog.App{app("dock@example.com", "Dock", "50", "51"),
		app("clock@example.com", "Clock", "51"), app("old@example.com", "Old", "45")}, info); err != nil {
		t.Fatal(err)
	}
	s := &Session{Settings: settings.Default(), Backends: map[catalog.SourceID]backend.Backend{},
		Telemetry: telemetry.Nop{}, CatalogPath: path}
	if shell != nil {
		s.Extensions = &shellext.Manager{Bus: shell, UpdatesDir: t.TempDir()}
	}
	return s
}

func TestExportExtensions(t *testing.T) {
	shell := &fakeShell{version: "51.1", extensions: map[string]map[string]any{
		"dock@example.com":     ext("dock@example.com", "Dock", false, true),
		"clock@example.com":    ext("clock@example.com", "Clock", false, false),
		"blur@example.com":     ext("blur@example.com", "Blur", true, true),
		"caffeine@example.com": ext("caffeine@example.com", "Caffeine", true, false),
	}}
	ex, err := extSession(t, shell).Export(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	// The user's, on or off, and the system's that are on. A system extension that is off is as shipped.
	want := []ExportedExtension{
		{UUID: "blur@example.com", Name: "Blur", Enabled: true, System: true},
		{UUID: "clock@example.com", Name: "Clock"},
		{UUID: "dock@example.com", Name: "Dock", Enabled: true},
	}
	if !reflect.DeepEqual(ex.Extensions, want) {
		t.Errorf("Extensions = %+v, want %+v", ex.Extensions, want)
	}
	// Without GNOME there are none, and no error.
	ex, err = extSession(t, nil).Export(context.Background())
	if err != nil || ex.Extensions != nil {
		t.Errorf("without GNOME: %+v, %v", ex.Extensions, err)
	}
}

func TestPlanAndRestoreExtensions(t *testing.T) {
	ctx := context.Background()
	ex := Export{Version: ExportVersion, System: "Bazzite", Extensions: []ExportedExtension{
		{UUID: "dock@example.com", Name: "Dock", Enabled: true},                 // not here: install
		{UUID: "clock@example.com", Name: "Clock"},                              // not here, was off: install, switch off
		{UUID: "old@example.com", Name: "Old", Enabled: true},                   // no version for GNOME 51
		{UUID: "gone@example.com", Name: "Gone", Enabled: true},                 // not on extensions.gnome.org
		{UUID: "blur@example.com", Name: "Blur", Enabled: true, System: true},   // here but off: switch on
		{UUID: "kept@example.com", Name: "Kept", Enabled: true},                 // here and on
		{UUID: "quiet@example.com", Name: "Quiet"},                              // here and on; restoring leaves it on
		{UUID: "extra@example.com", Name: "Extra", Enabled: true, System: true}, // came with the other system only
		{UUID: "dock@example.com", Name: "Dock again", Enabled: true},           // a duplicate
	}}
	shell := &fakeShell{version: "51.1", answer: "successful", extensions: map[string]map[string]any{
		"blur@example.com":  ext("blur@example.com", "Blur", true, false),
		"kept@example.com":  ext("kept@example.com", "Kept", false, true),
		"quiet@example.com": ext("quiet@example.com", "Quiet", false, true),
	}}
	s := extSession(t, shell)
	plan, err := s.PlanExtensions(ctx, ex)
	if err != nil {
		t.Fatal(err)
	}
	type row struct {
		uuid   string
		action RestoreAction
		note   string
	}
	var got []row
	for _, it := range plan {
		got = append(got, row{it.Ext.UUID, it.Action, it.Note})
	}
	want := []row{
		{"dock@example.com", RestoreInstall, ""},
		{"clock@example.com", RestoreInstall, "switched off, as it was"},
		{"old@example.com", RestoreUnavailable, "no version for GNOME 51"},
		{"gone@example.com", RestoreUnavailable, "not on extensions.gnome.org"},
		{"blur@example.com", RestoreEnable, ""},
		{"kept@example.com", RestoreInstalled, ""},
		{"quiet@example.com", RestoreInstalled, ""},
		{"extra@example.com", RestoreUnavailable, "came with Bazzite; not on extensions.gnome.org"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("plan =\n%+v\nwant\n%+v", got, want)
	}

	for _, it := range plan {
		if err := s.RestoreExtension(ctx, it); err != nil {
			t.Errorf("%s: %v", it.Ext.UUID, err)
		}
	}
	wantCalls := []string{"InstallRemoteExtension dock@example.com", "InstallRemoteExtension clock@example.com",
		"DisableExtension clock@example.com", "EnableExtension blur@example.com"}
	if !reflect.DeepEqual(shell.calls, wantCalls) {
		t.Errorf("calls = %q, want %q", shell.calls, wantCalls)
	}
	if shell.extensions["dock@example.com"]["enabled"] != true || shell.extensions["clock@example.com"]["enabled"] != false ||
		shell.extensions["blur@example.com"]["enabled"] != true || shell.extensions["quiet@example.com"]["enabled"] != true {
		t.Errorf("extensions after the restore: %+v", shell.extensions)
	}
	// A second restore has nothing left to do.
	plan, err = s.PlanExtensions(ctx, ex)
	if err != nil {
		t.Fatal(err)
	}
	for _, it := range plan {
		if it.Action == RestoreInstall || it.Action == RestoreEnable {
			t.Errorf("still to do after a restore: %+v", it)
		}
	}

	// A no in GNOME's dialog is passed on, and nothing is switched.
	shell.answer, shell.calls = "cancelled", nil
	err = s.RestoreExtension(ctx, ExtensionRestore{Ext: ExportedExtension{UUID: "new@example.com"}, Action: RestoreInstall})
	if !errors.Is(err, shellext.ErrCancelled) || len(shell.calls) != 1 {
		t.Errorf("cancelled install: %v, calls %q", err, shell.calls)
	}

	// Without GNOME nothing can be restored, and the plan says why.
	plan, err = extSession(t, nil).PlanExtensions(ctx, ex)
	if err != nil || len(plan) != len(ex.Extensions) || plan[0].Action != RestoreUnavailable ||
		plan[0].Note != "needs the GNOME desktop" {
		t.Errorf("without GNOME: %+v, %v", plan, err)
	}
}

func TestExtensionUpdates(t *testing.T) {
	ctx := context.Background()
	dock := ext("dock@example.com", "Dock", false, true)
	dock["hasUpdate"] = true
	shell := &fakeShell{version: "51.1", extensions: map[string]map[string]any{
		"dock@example.com": dock, "clock@example.com": ext("clock@example.com", "Clock", false, true)}}
	ups, err := extSession(t, shell).ExtensionUpdates(ctx)
	if err != nil || len(ups) != 1 || ups[0].UUID != "dock@example.com" {
		t.Errorf("ExtensionUpdates = %+v, %v", ups, err)
	}
	if ups, err := extSession(t, nil).ExtensionUpdates(ctx); err != nil || ups != nil {
		t.Errorf("without GNOME: %+v, %v", ups, err)
	}
}
