package rpmostree

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/tyler-builds/uas/internal/catalog"
	"github.com/tyler-builds/uas/internal/client/backend"
)

// A trimmed rpm-ostree status: a staged Fedora 45 deployment (from a container image, so the kernel is in
// the manifest annotations) on top of a booted Fedora 44 one.
const statusJSON = `{"deployments": [
  {"booted": false, "staged": true, "version": "45.20261104",
   "requested-packages": ["htop", "nvtop"], "requested-local-packages": ["vendor-tool-1.0-1.fc44.x86_64"],
   "base-commit-meta": {"ostree.manifest": "{\"annotations\":{\"ostree.linux\":\"7.2.14-300.fc45.x86_64\"}}"}},
  {"booted": true, "staged": false, "version": "44.20261026",
   "requested-packages": ["nvtop"], "requested-local-packages": ["vendor-tool-1.0-1.fc44.x86_64"],
   "base-commit-meta": {"ostree.linux": "7.1.9-200.fc44.x86_64"}}
]}`

type fakeRunner struct {
	calls   []string
	outputs map[string]string
	fail    map[string]error
}

func (f *fakeRunner) Run(_ context.Context, name string, args []string, _ func(string)) (string, error) {
	call := name + " " + strings.Join(args, " ")
	f.calls = append(f.calls, call)
	if err := f.fail[call]; err != nil {
		return "", err
	}
	return f.outputs[call], nil
}

func newBackend(r *fakeRunner) *Backend {
	if r.outputs == nil {
		r.outputs = map[string]string{}
	}
	r.outputs["rpm-ostree status --json"] = statusJSON
	return New(context.Background(), r, "rpm-ostree", "rpm", 44)
}

func listing(distro, pkg string) catalog.Listing {
	return catalog.Listing{Source: catalog.SourceFedora, Ref: distro + "/" + pkg, Package: pkg, Distro: distro}
}

func TestTargetsStagedRelease(t *testing.T) {
	b := newBackend(&fakeRunner{})
	if b.Release != 45 {
		t.Fatalf("Release = %d, want 45 (the staged deployment)", b.Release)
	}
	if !b.Supports(listing("fedora-45", "htop")) || b.Supports(listing("fedora-44", "htop")) {
		t.Error("Supports should accept only fedora-45 listings")
	}
	if b.Supports(listing("fedora-45", "--evil")) {
		t.Error("Supports accepted a malformed package name")
	}
}

func TestFallbackRelease(t *testing.T) {
	r := &fakeRunner{fail: map[string]error{"rpm-ostree status --json": errors.New("no daemon")}}
	b := New(context.Background(), r, "rpm-ostree", "rpm", 44)
	if b.Release != 44 {
		t.Errorf("Release = %d, want the fallback 44", b.Release)
	}
}

func TestInstalled(t *testing.T) {
	b := newBackend(&fakeRunner{})
	apps, err := b.Installed(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	want := []backend.InstalledApp{
		{Source: catalog.SourceFedora, Ref: "htop", AppID: "htop", Name: "htop", Package: "htop", Scope: "system",
			Origin: "layered", Pending: true},
		{Source: catalog.SourceFedora, Ref: "nvtop", AppID: "nvtop", Name: "nvtop", Package: "nvtop",
			Scope: "system", Origin: "layered"},
		{Source: catalog.SourceFedora, Ref: "vendor-tool-1.0-1.fc44.x86_64", AppID: "vendor-tool-1.0-1.fc44.x86_64",
			Name: "vendor-tool-1.0-1.fc44.x86_64", Scope: "system", Origin: "local file"},
	}
	if !reflect.DeepEqual(apps, want) {
		t.Errorf("apps = %+v\nwant %+v", apps, want)
	}
	if !apps[0].Matches(listing("fedora-45", "htop")) || apps[0].Matches(listing("fedora-45", "btop")) {
		t.Error("Matches")
	}
}

func TestInstall(t *testing.T) {
	r := &fakeRunner{fail: map[string]error{"rpm -q --quiet btop": errors.New("not installed")}}
	b := newBackend(r)
	if err := b.Install(context.Background(), listing("fedora-45", "btop"), nil); err != nil {
		t.Fatal(err)
	}
	if last := r.calls[len(r.calls)-1]; last != "rpm-ostree install --idempotent btop" {
		t.Errorf("last call = %q", last)
	}

	// The running image already has steam.
	if err := b.Install(context.Background(), listing("fedora-45", "steam"), nil); !errors.Is(err, ErrInImage) {
		t.Errorf("err = %v, want ErrInImage", err)
	}
	if err := b.Install(context.Background(), listing("fedora-44", "btop"), nil); err == nil {
		t.Error("installed a listing for the wrong release")
	}
}

func TestRemoveAndUpdate(t *testing.T) {
	r := &fakeRunner{}
	b := newBackend(r)
	apps, _ := b.Installed(context.Background())
	if err := b.Remove(context.Background(), apps[0], nil); err != nil {
		t.Fatal(err)
	}
	if last := r.calls[len(r.calls)-1]; last != "rpm-ostree uninstall htop" {
		t.Errorf("last call = %q", last)
	}
	if err := b.Remove(context.Background(), apps[2], nil); err == nil {
		t.Error("removed a local-file package")
	}
	if err := b.Update(context.Background(), apps[0], nil); !errors.Is(err, ErrUpdatesWithSystem) {
		t.Errorf("Update err = %v", err)
	}
}
