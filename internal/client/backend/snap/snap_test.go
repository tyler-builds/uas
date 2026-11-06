package snap

import (
	"context"
	"strings"
	"testing"

	"github.com/tyler-builds/uas/internal/catalog"
)

type fakeRunner struct {
	calls   []string
	outputs map[string]string
}

func (f *fakeRunner) Run(_ context.Context, name string, args []string, _ func(string)) (string, error) {
	cmd := name + " " + strings.Join(args, " ")
	f.calls = append(f.calls, cmd)
	return f.outputs[cmd], nil
}

func TestSnap(t *testing.T) {
	r := &fakeRunner{outputs: map[string]string{
		"snap list --unicode=never --color=never": "Name     Version  Rev   Tracking       Publisher   Notes\n" +
			"core22   2024     1612  latest/stable  canonical** base\n" +
			"vlc      3.0.21   3777  latest/stable  videolan**  -\n",
		"snap refresh --list --unicode=never --color=never": "Name  Version  Rev   Size   Publisher   Notes\n" +
			"vlc   3.0.22   3800  300MB  videolan**  -\n",
	}}
	b := &Backend{Runner: r, Path: "snap", Known: func(context.Context) (map[string]bool, error) {
		return map[string]bool{"vlc": true, "code": true}, nil
	}}
	apps, err := b.Installed(context.Background())
	if err != nil || len(apps) != 1 || apps[0].Ref != "vlc" || apps[0].Version != "3.0.21" {
		t.Fatalf("installed = %+v, %v", apps, err)
	}
	ups, _ := b.Updates(context.Background())
	if len(ups) != 1 || ups[0].Version != "3.0.22" {
		t.Errorf("updates = %+v", ups)
	}
	b.Install(context.Background(), catalog.Listing{Source: catalog.SourceSnap, Ref: "code", Classic: true}, nil)
	if last := r.calls[len(r.calls)-1]; last != "snap install code --classic" {
		t.Errorf("install = %q", last)
	}
	if b.Supports(catalog.Listing{Source: catalog.SourceSnap, Ref: "--dangerous"}) {
		t.Error("an option as a snap name should be refused")
	}
}
