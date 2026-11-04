package homebrew

import (
	"context"
	"reflect"
	"strings"
	"testing"

	"github.com/tyler-builds/uas/internal/catalog"
	"github.com/tyler-builds/uas/internal/client/backend"
)

type fakeRunner struct {
	calls  []string
	output string
}

func (f *fakeRunner) Run(_ context.Context, name string, args []string, _ func(string)) (string, error) {
	f.calls = append(f.calls, name+" "+strings.Join(args, " "))
	return f.output, nil
}

func TestInstalled(t *testing.T) {
	r := &fakeRunner{output: `{"formulae": [
		{"name": "gh", "installed": [{"version": "2.103.3", "installed_on_request": true}]},
		{"name": "openssl@3", "installed": [{"version": "3.6.0", "installed_on_request": false}]}
	], "casks": []}`}
	b := &Backend{Runner: r, Path: "brew"}
	apps, err := b.Installed(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	want := []backend.InstalledApp{{Source: catalog.SourceHomebrew, Ref: "gh", AppID: "gh", Name: "gh",
		Package: "gh", Version: "2.103.3", Scope: "user", Origin: "homebrew"}}
	if !reflect.DeepEqual(apps, want) {
		t.Errorf("apps = %+v", apps)
	}
}

func TestCommands(t *testing.T) {
	r := &fakeRunner{}
	b := &Backend{Runner: r, Path: "brew"}
	ctx := context.Background()
	l := catalog.Listing{Source: catalog.SourceHomebrew, Ref: "tree", Package: "tree"}
	a := backend.InstalledApp{Source: catalog.SourceHomebrew, Ref: "tree", Package: "tree"}
	if err := b.Install(ctx, l, nil); err != nil {
		t.Fatal(err)
	}
	if err := b.Update(ctx, a, nil); err != nil {
		t.Fatal(err)
	}
	if err := b.Remove(ctx, a, nil); err != nil {
		t.Fatal(err)
	}
	want := []string{"brew install --formula tree", "brew upgrade --formula tree", "brew uninstall --formula tree"}
	if !reflect.DeepEqual(r.calls, want) {
		t.Errorf("calls = %q", r.calls)
	}
	for _, bad := range []string{"--force", "Tree", "user/tap/tree", ""} {
		l.Package = bad
		if err := b.Install(ctx, l, nil); err == nil {
			t.Errorf("Install(%q) succeeded", bad)
		}
	}
}
