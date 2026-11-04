package flatpak

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/tyler-builds/uas/internal/catalog"
	"github.com/tyler-builds/uas/internal/client/backend"
	"github.com/tyler-builds/uas/internal/client/settings"
)

// fakeRunner records commands and answers them from a table keyed by the joined arguments.
type fakeRunner struct {
	calls   []string
	outputs map[string]string
	lines   map[string][]string
	fail    map[string]error
}

func (f *fakeRunner) Run(_ context.Context, name string, args []string, onLine func(string)) (string, error) {
	key := strings.Join(args, " ")
	f.calls = append(f.calls, name+" "+key)
	if err := f.fail[key]; err != nil {
		return "", err
	}
	for _, l := range f.lines[key] {
		if onLine != nil {
			onLine(l)
		}
	}
	return f.outputs[key], nil
}

func listing(ref string) catalog.Listing {
	return catalog.Listing{Source: catalog.SourceFlathub, Ref: ref, AppStreamID: "org.example.App"}
}

func TestInstalled(t *testing.T) {
	r := &fakeRunner{outputs: map[string]string{
		"list --app --columns=application,ref,name,version,origin,installation": "" +
			"org.example.App\torg.example.App/x86_64/stable\tExample\t1.2\tflathub\tsystem\n" +
			"org.example.Other\torg.example.Other/x86_64/beta\tOther\t\tflathub-beta\tuser\n",
	}}
	b := &Backend{Runner: r, Path: "flatpak", Scope: settings.ScopeUser}
	apps, err := b.Installed(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	want := []backend.InstalledApp{
		{Source: catalog.SourceFlathub, Ref: "app/org.example.App/x86_64/stable", AppID: "org.example.App",
			Name: "Example", Version: "1.2", Origin: "flathub", Scope: "system"},
		{Source: catalog.SourceFlathub, Ref: "app/org.example.Other/x86_64/beta", AppID: "org.example.Other",
			Name: "Other", Origin: "flathub-beta", Scope: "user"},
	}
	if !reflect.DeepEqual(apps, want) {
		t.Errorf("apps = %+v\nwant %+v", apps, want)
	}
}

func TestInstallUserAddsFlathubRemote(t *testing.T) {
	r := &fakeRunner{
		outputs: map[string]string{"remotes --user --columns=name": ""},
		lines: map[string][]string{
			"install --user --noninteractive flathub app/org.example.App/x86_64/stable": {"Installing 1/1… 50%"},
		},
	}
	b := &Backend{Runner: r, Path: "flatpak", Scope: settings.ScopeUser}
	var msgs []string
	err := b.Install(context.Background(), listing("app/org.example.App/x86_64/stable"),
		func(p backend.Progress) { msgs = append(msgs, p.Message) })
	if err != nil {
		t.Fatal(err)
	}
	wantCalls := []string{
		"flatpak remotes --user --columns=name",
		"flatpak remote-add --user --if-not-exists flathub https://dl.flathub.org/repo/flathub.flatpakrepo",
		"flatpak install --user --noninteractive flathub app/org.example.App/x86_64/stable",
	}
	if !reflect.DeepEqual(r.calls, wantCalls) {
		t.Errorf("calls = %q\nwant %q", r.calls, wantCalls)
	}
	if len(msgs) == 0 || msgs[len(msgs)-1] != "Installing 1/1… 50%" {
		t.Errorf("progress = %q", msgs)
	}
}

func TestInstallSystemWithExistingRemote(t *testing.T) {
	r := &fakeRunner{outputs: map[string]string{"remotes --system --columns=name": "fedora\nflathub\n"}}
	b := &Backend{Runner: r, Path: "flatpak", Scope: settings.ScopeSystem}
	if err := b.Install(context.Background(), listing("app/org.example.App/x86_64/stable"), nil); err != nil {
		t.Fatal(err)
	}
	if len(r.calls) != 2 || r.calls[1] != "flatpak install --system --noninteractive flathub app/org.example.App/x86_64/stable" {
		t.Errorf("calls = %q", r.calls)
	}
}

func TestInstallRejectsBadInput(t *testing.T) {
	b := &Backend{Runner: &fakeRunner{}, Path: "flatpak", Scope: settings.ScopeUser}
	ctx := context.Background()
	for _, ref := range []string{"--from=evil", "app/org.example.App/x86_64", "runtime/org.example.Platform/x86_64/1",
		"app/org.example.App/x86_64/stable --system"} {
		if err := b.Install(ctx, listing(ref), nil); err == nil {
			t.Errorf("Install(%q) succeeded", ref)
		}
	}
	other := listing("app/org.example.App/x86_64/stable")
	other.Source = catalog.SourceFedora
	if err := b.Install(ctx, other, nil); !errors.Is(err, backend.ErrWrongSource) {
		t.Errorf("wrong source err = %v", err)
	}
}

func TestRemoveAndUpdateUseAppScope(t *testing.T) {
	r := &fakeRunner{}
	b := &Backend{Runner: r, Path: "flatpak", Scope: settings.ScopeUser}
	a := backend.InstalledApp{Source: catalog.SourceFlathub, Ref: "app/org.example.App/x86_64/stable", Scope: "system"}
	ctx := context.Background()
	if err := b.Update(ctx, a, nil); err != nil {
		t.Fatal(err)
	}
	if err := b.Remove(ctx, a, nil); err != nil {
		t.Fatal(err)
	}
	a.Scope = "steamdeck-sd"
	if err := b.Remove(ctx, a, nil); err != nil {
		t.Fatal(err)
	}
	want := []string{
		"flatpak update --system --noninteractive app/org.example.App/x86_64/stable",
		"flatpak uninstall --system --noninteractive app/org.example.App/x86_64/stable",
		"flatpak uninstall --installation=steamdeck-sd --noninteractive app/org.example.App/x86_64/stable",
	}
	if !reflect.DeepEqual(r.calls, want) {
		t.Errorf("calls = %q\nwant %q", r.calls, want)
	}
	a.Scope = "--evil"
	if err := b.Remove(ctx, a, nil); err == nil {
		t.Error("Remove with a malformed scope succeeded")
	}
}
