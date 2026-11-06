package pkgmgr

import (
	"context"
	"slices"
	"strings"
	"testing"

	"github.com/tyler-builds/uas/internal/catalog"
	"github.com/tyler-builds/uas/internal/client/backend"
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

func known(pkgs ...string) func(context.Context) (map[string]bool, error) {
	return func(context.Context) (map[string]bool, error) {
		m := map[string]bool{}
		for _, p := range pkgs {
			m[p] = true
		}
		return m, nil
	}
}

func TestApt(t *testing.T) {
	r := &fakeRunner{outputs: map[string]string{
		"dpkg-query -W -f=${Package}\t${Version}\t${db:Status-Status}\n": "gitg\t44-1\tinstalled\nlibc6\t2.39\tinstalled\n" +
			"htop\t3.4\tconfig-files\n",
		"apt list --upgradable": "Listing...\ngitg/noble-updates 44-1ubuntu0.1 amd64 [upgradable from: 44-1]\n" +
			"libc6/noble-updates 2.39-1 amd64 [upgradable from: 2.39]\n",
	}}
	b := &Backend{Kind: Apt, From: catalog.SourceUbuntu, Distro: "ubuntu-24.04", Runner: r,
		Root: []string{"/usr/bin/pkexec"}, Known: known("gitg", "htop")}
	apps, err := b.Installed(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(apps) != 1 || apps[0].Ref != "ubuntu-24.04/gitg" || apps[0].Package != "gitg" || apps[0].Version != "44-1" {
		t.Errorf("installed = %+v", apps)
	}
	ups, _ := b.Updates(context.Background())
	if len(ups) != 1 || ups[0].Ref != "ubuntu-24.04/gitg" || ups[0].Version != "44-1ubuntu0.1" {
		t.Errorf("updates = %+v", ups)
	}
	l := catalog.Listing{Source: catalog.SourceUbuntu, Ref: "ubuntu-24.04/gitg", Distro: "ubuntu-24.04", Package: "gitg"}
	if !b.Supports(l) {
		t.Error("should support its own release's listing")
	}
	other := l
	other.Distro = "ubuntu-22.04"
	if b.Supports(other) {
		t.Error("should not support another release's listing")
	}
	ctx := context.Background()
	if err := b.Install(ctx, l, nil); err != nil {
		t.Fatal(err)
	}
	if err := b.Update(ctx, apps[0], nil); err != nil {
		t.Fatal(err)
	}
	if err := b.Remove(ctx, apps[0], nil); err != nil {
		t.Fatal(err)
	}
	want := []string{
		"/usr/bin/pkexec env DEBIAN_FRONTEND=noninteractive apt-get install -y -- gitg",
		"/usr/bin/pkexec env DEBIAN_FRONTEND=noninteractive apt-get install -y --only-upgrade -- gitg",
		"/usr/bin/pkexec env DEBIAN_FRONTEND=noninteractive apt-get remove -y -- gitg",
	}
	if got := r.calls[len(r.calls)-3:]; !slices.Equal(got, want) {
		t.Errorf("calls = %q", got)
	}
	bad := backend.InstalledApp{Source: catalog.SourceUbuntu, Package: "-oAPT::x=y"}
	if err := b.Remove(ctx, bad, nil); err == nil {
		t.Error("an option as a package name should be refused")
	}
}

func TestDNFAndPacman(t *testing.T) {
	r := &fakeRunner{outputs: map[string]string{
		"rpm -qa --qf %{NAME}\t%{VERSION}-%{RELEASE}\tinstalled\n": "gitg\t44-2.fc45\tinstalled\nglibc\t2.40-1\tinstalled\n",
		"dnf list --upgrades --quiet":                              "Available upgrades\ngitg.x86_64  44-3.fc45  updates\n",
		"pacman -Q":                                                "gitg 44-2\nglibc 2.40-1\n",
	}}
	dnf := &Backend{Kind: DNF, From: catalog.SourceFedora, Distro: "fedora-45", Runner: r, Known: known("gitg")}
	apps, _ := dnf.Installed(context.Background())
	ups, _ := dnf.Updates(context.Background())
	if len(apps) != 1 || apps[0].Version != "44-2.fc45" || len(ups) != 1 || ups[0].Version != "44-3.fc45" {
		t.Errorf("dnf installed %+v, updates %+v", apps, ups)
	}
	pac := &Backend{Kind: Pacman, From: catalog.SourceArch, Distro: "arch", Runner: r, Known: known("gitg")}
	apps, _ = pac.Installed(context.Background())
	if len(apps) != 1 || apps[0].Ref != "arch/gitg" || apps[0].Version != "44-2" {
		t.Errorf("pacman installed %+v", apps)
	}
	if err := pac.Update(context.Background(), apps[0], nil); err == nil || !strings.Contains(err.Error(), "pacman -Syu") {
		t.Errorf("pacman update: %v", err)
	}
	if err := pac.Install(context.Background(), catalog.Listing{Source: catalog.SourceArch, Distro: "arch",
		Package: "gitg"}, nil); err != nil {
		t.Fatal(err)
	}
	if last := r.calls[len(r.calls)-1]; last != "pacman -S --needed --noconfirm -- gitg" {
		t.Errorf("pacman install as root = %q", last)
	}
}
